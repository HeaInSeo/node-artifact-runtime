package runtimehelper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
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

func executeCommand(ctx context.Context, cfg Config, lifecycle *runLifecycle) commandResult {
	if err := enableChildSubreaper(); err != nil {
		return commandResult{
			Err:      fmt.Errorf("%w: %v", errSubreaperSetupFailed, err),
			ExitCode: ExitGenericError,
		}
	}

	// #nosec G204 -- runtimehelper intentionally executes the node command selected by the run spec.
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Stdout = stdoutOrDefault(cfg.Stdout)
	cmd.Stderr = stderrOrDefault(cfg.Stderr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmdEnv, err := cfg.commandEnv()
	if err != nil {
		return commandResult{Err: err, ExitCode: ExitGenericError}
	}
	cmd.Env = cmdEnv

	if err := cmd.Start(); err != nil {
		return commandResult{Err: err, ExitCode: ExitGenericError}
	}
	pgid := cmd.Process.Pid

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		_ = reapReparentedChildren()
		if processGroupExists(pgid) {
			killed := terminateRemainingProcessGroup(pgid, effectiveShutdownGracePeriod(cfg.ShutdownGracePeriod))
			_ = reapReparentedChildren()
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
			err, killed := terminateProcessGroupAndWait(pgid, sigv, effectiveShutdownGracePeriod(cfg.ShutdownGracePeriod), waitCh)
			_ = reapReparentedChildren()
			return commandResult{
				Err:         err,
				ExitCode:    signalExitCode(sigv),
				Interrupted: true,
				Killed:      killed,
				Signal:      sigv,
			}
		}
		err, killed := terminateProcessGroupAndWait(pgid, syscall.SIGTERM, effectiveShutdownGracePeriod(cfg.ShutdownGracePeriod), waitCh)
		_ = reapReparentedChildren()
		return commandResult{Err: err, ExitCode: ExitTimeout, TimedOut: true, Killed: killed}
	}
}

func signalProcessGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid process group id %d", pgid)
	}
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func processGroupExists(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminateRemainingProcessGroup(pgid int, grace time.Duration) bool {
	_ = signalProcessGroup(pgid, syscall.SIGTERM)
	if waitForProcessGroupExit(pgid, grace) {
		return false
	}
	_ = signalProcessGroup(pgid, syscall.SIGKILL)
	_ = waitForProcessGroupExit(pgid, processGroupKillWait)
	return true
}

func terminateProcessGroupAndWait(pgid int, sig syscall.Signal, grace time.Duration, waitCh <-chan error) (error, bool) {
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
		case err := <-waitCh:
			commandErr = err
			directExited = true
			_ = reapReparentedChildren()
		case <-ticker.C:
			_ = reapReparentedChildren()
		case <-deadline.C:
			_ = signalProcessGroup(pgid, syscall.SIGKILL)
			commandErr = waitForCommandExit(waitCh, commandErr, &directExited)
			_ = waitForProcessGroupExit(pgid, processGroupKillWait)
			return commandErr, true
		}
	}
}

func waitForCommandExit(waitCh <-chan error, commandErr error, directExited *bool) error {
	if directExited != nil && *directExited {
		return commandErr
	}
	select {
	case err := <-waitCh:
		if directExited != nil {
			*directExited = true
		}
		return err
	case <-time.After(processGroupKillWait):
		return commandErr
	}
}

func waitForProcessGroupExit(pgid int, limit time.Duration) bool {
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
			_ = reapReparentedChildren()
			if !processGroupExists(pgid) {
				return true
			}
		case <-deadline.C:
			_ = reapReparentedChildren()
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

func enableChildSubreaper() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, uintptr(linuxPRSetChildSubreaper), 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func reapReparentedChildren() int {
	if runtime.GOOS != "linux" {
		return 0
	}
	reaped := 0
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return reaped
		}
		reaped++
	}
}
