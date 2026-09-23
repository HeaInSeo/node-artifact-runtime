package runtimehelper

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type commandResult struct {
	Err         error
	ExitCode    int
	TimedOut    bool
	Interrupted bool
	Killed      bool
	Signal      syscall.Signal
	NotClean    bool
}

type lifecycleCause int

const (
	lifecycleCauseNone lifecycleCause = iota
	lifecycleCauseTimeout
	lifecycleCauseSignal
)

type runLifecycle struct {
	ctx        context.Context
	cancel     context.CancelFunc
	stopSignal func()
	stopTimer  func()

	mu     sync.Mutex
	cause  lifecycleCause
	signal syscall.Signal
}

func newRunLifecycle(parent context.Context, cfg Config) *runLifecycle {
	ctx, cancel := context.WithCancel(parent)
	lifecycle := &runLifecycle{
		ctx:        ctx,
		cancel:     cancel,
		stopSignal: func() {},
		stopTimer:  func() {},
	}
	if cfg.RunTimeout > 0 {
		timer := time.AfterFunc(cfg.RunTimeout, func() {
			lifecycle.markTimeout()
			cancel()
		})
		lifecycle.stopTimer = func() {
			timer.Stop()
		}
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	lifecycle.stopSignal = func() {
		signal.Stop(sigCh)
	}
	go func() {
		select {
		case sig := <-sigCh:
			sigv, ok := sig.(syscall.Signal)
			if !ok {
				sigv = syscall.SIGTERM
			}
			lifecycle.markSignal(sigv)
			cancel()
		case <-ctx.Done():
		}
	}()
	return lifecycle
}

func (l *runLifecycle) stop() {
	if l == nil {
		return
	}
	l.stopTimer()
	l.stopSignal()
	l.cancel()
}

func (l *runLifecycle) markTimeout() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cause == lifecycleCauseNone {
		l.cause = lifecycleCauseTimeout
	}
}

func (l *runLifecycle) markSignal(sig syscall.Signal) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cause == lifecycleCauseNone {
		l.cause = lifecycleCauseSignal
		l.signal = sig
	}
}

func (l *runLifecycle) interruptedSignal() (syscall.Signal, bool) {
	if l == nil {
		return 0, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cause != lifecycleCauseSignal {
		return 0, false
	}
	return l.signal, true
}

// executeCommand runs the user command as the leader of its own process
// group. While it runs, the process-wide reaper owns every wait4 call in this
// process, including those for overlapping executeCommand calls: it hands the
// direct child's status to exitCh and reaps any descendants reparented to
// this subreaper. exec.Cmd.Wait is never called, so no second waiter can race
// the reaper for the direct child's status.
func executeCommand(ctx context.Context, cfg Config, lifecycle *runLifecycle) commandResult {
	if err := enableChildSubreaper(); err != nil {
		return commandResult{
			Err:      fmt.Errorf("%w: %v", errSubreaperSetupFailed, err),
			ExitCode: ExitGenericError,
		}
	}

	cmdEnv, err := cfg.commandEnv()
	if err != nil {
		return commandResult{Err: err, ExitCode: ExitGenericError}
	}
	stdoutW, stderrW := stdoutOrDefault(cfg.Stdout), stderrOrDefault(cfg.Stderr)
	stdout, err := newCommandOutput(stdoutW)
	if err != nil {
		return commandResult{Err: err, ExitCode: ExitGenericError}
	}
	outputs := []*commandOutput{stdout}
	stderr := stdout
	if !sameWriter(stdoutW, stderrW) {
		stderr, err = newCommandOutput(stderrW)
		if err != nil {
			stdout.closeParentEnd()
			return commandResult{Err: err, ExitCode: ExitGenericError}
		}
		outputs = append(outputs, stderr)
	}

	// #nosec G204 -- runtimehelper intentionally executes the node command selected by the run spec.
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Stdout = stdout.file
	cmd.Stderr = stderr.file
	cmd.SysProcAttr = commandSysProcAttr()
	cmd.Env = cmdEnv

	reaper, err := startReapedCommand(cmd)
	stdout.closeParentEnd()
	stderr.closeParentEnd()
	if err != nil {
		return commandResult{Err: err, ExitCode: ExitGenericError}
	}
	pgid := cmd.Process.Pid

	defer func() {
		reaper.stop()
		_ = cmd.Process.Release()
	}()

	// exitCh mirrors exec.Cmd.Wait: the direct child's status, delivered only
	// after any copied output has reached EOF.
	exitCh := make(chan error, 1)
	go func() {
		exit, ok := reaper.waitDirect()
		if !ok {
			return
		}
		err := exit.err()
		for _, out := range outputs {
			if copyErr := out.wait(); err == nil && copyErr != nil {
				err = copyErr
			}
		}
		exitCh <- err
	}()

	select {
	case err := <-exitCh:
		reaper.reapNow()
		if processGroupExists(pgid) {
			killed := terminateRemainingProcessGroup(pgid, effectiveShutdownGracePeriod(cfg.ShutdownGracePeriod), reaper)
			return commandResult{
				Err:      fmt.Errorf("%w: user command exited but process group %d still has live processes", errProcessGroupNotClean, pgid),
				ExitCode: ExitGenericError,
				Killed:   killed,
				NotClean: true,
			}
		}
		return commandResult{Err: err, ExitCode: exitCode(err)}
	case <-ctx.Done():
		if sigv, ok := lifecycle.interruptedSignal(); ok {
			err, killed := terminateProcessGroupAndWait(pgid, sigv, effectiveShutdownGracePeriod(cfg.ShutdownGracePeriod), exitCh, reaper)
			return commandResult{
				Err:         err,
				ExitCode:    signalExitCode(sigv),
				Interrupted: true,
				Killed:      killed,
				Signal:      sigv,
			}
		}
		err, killed := terminateProcessGroupAndWait(pgid, syscall.SIGTERM, effectiveShutdownGracePeriod(cfg.ShutdownGracePeriod), exitCh, reaper)
		return commandResult{Err: err, ExitCode: ExitTimeout, TimedOut: true, Killed: killed}
	}
}

func terminateRemainingProcessGroup(pgid int, grace time.Duration, reaper *childReaper) bool {
	_ = signalProcessGroup(pgid, syscall.SIGTERM)
	if waitForProcessGroupExit(pgid, grace, reaper) {
		return false
	}
	_ = signalProcessGroup(pgid, syscall.SIGKILL)
	_ = waitForProcessGroupExit(pgid, processGroupKillWait, reaper)
	return true
}

func terminateProcessGroupAndWait(pgid int, sig syscall.Signal, grace time.Duration, exitCh <-chan error, reaper *childReaper) (error, bool) {
	_ = signalProcessGroup(pgid, sig)
	var commandErr error
	directExited := false
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(processGroupPollInterval)
	defer ticker.Stop()
	for {
		if directExited && !processGroupExists(pgid) {
			return commandErr, false
		}
		select {
		case err := <-exitCh:
			commandErr = err
			directExited = true
			reaper.reapNow()
		case <-ticker.C:
			reaper.reapNow()
		case <-deadline.C:
			_ = signalProcessGroup(pgid, syscall.SIGKILL)
			commandErr = waitForCommandExit(exitCh, commandErr, &directExited)
			_ = waitForProcessGroupExit(pgid, processGroupKillWait, reaper)
			return commandErr, true
		}
	}
}

func waitForCommandExit(exitCh <-chan error, commandErr error, directExited *bool) error {
	if directExited != nil && *directExited {
		return commandErr
	}
	select {
	case err := <-exitCh:
		if directExited != nil {
			*directExited = true
		}
		return err
	case <-time.After(processGroupKillWait):
		return commandErr
	}
}

func waitForProcessGroupExit(pgid int, limit time.Duration, reaper *childReaper) bool {
	if !processGroupExists(pgid) {
		return true
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	ticker := time.NewTicker(processGroupPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			reaper.reapNow()
			if !processGroupExists(pgid) {
				return true
			}
		case <-deadline.C:
			reaper.reapNow()
			return !processGroupExists(pgid)
		}
	}
}

func effectiveShutdownGracePeriod(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return DefaultShutdownGracePeriod
}

func signalExitCode(sig syscall.Signal) int {
	return 128 + int(sig)
}

// childExit is the direct child's wait status as collected by the child
// reaper.
type childExit struct {
	code       int
	signaled   bool
	signal     syscall.Signal
	coreDumped bool
}

func (e childExit) err() error {
	if !e.signaled && e.code == 0 {
		return nil
	}
	return &commandExitError{exit: e}
}

// commandExitError reports an unsuccessful direct-child status. Its message
// and ExitCode match exec.ExitError for the same wait status.
type commandExitError struct {
	exit childExit
}

func (e *commandExitError) Error() string {
	msg := "exit status " + strconv.Itoa(e.exit.code)
	if e.exit.signaled {
		msg = "signal: " + e.exit.signal.String()
	}
	if e.exit.coreDumped {
		msg += " (core dumped)"
	}
	return msg
}

// ExitCode returns the exit code of a normally exited child, or -1 if the
// child was terminated by a signal.
func (e *commandExitError) ExitCode() int {
	if e.exit.signaled {
		return -1
	}
	return e.exit.code
}

// commandOutput is the file a child inherits for one output stream. A
// writer that is not already a file is fed through a pipe, and wait blocks
// until every holder of the pipe's write end has closed it, as
// exec.Cmd.Wait does for its own copy goroutines.
type commandOutput struct {
	file    *os.File
	pipeW   *os.File
	copyErr chan error
}

// sameWriter reports whether stdout and stderr go to one writer, in which
// case they share one pipe as they would with exec.Cmd.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

func newCommandOutput(w io.Writer) (*commandOutput, error) {
	if f, ok := w.(*os.File); ok {
		return &commandOutput{file: f}, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	out := &commandOutput{file: pw, pipeW: pw, copyErr: make(chan error, 1)}
	go func() {
		_, err := io.Copy(w, pr)
		_ = pr.Close()
		out.copyErr <- err
	}()
	return out, nil
}

// closeParentEnd closes this process's copy of the pipe write end once the
// child has inherited it or failed to start, so the copy sees EOF when the
// last child-side holder exits.
func (o *commandOutput) closeParentEnd() {
	if o.pipeW != nil {
		_ = o.pipeW.Close()
		o.pipeW = nil
	}
}

func (o *commandOutput) wait() error {
	if o.copyErr == nil {
		return nil
	}
	return <-o.copyErr
}
