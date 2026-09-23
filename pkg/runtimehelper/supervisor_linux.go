package runtimehelper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// orphanReapInterval bounds how long an adopted zombie can wait for the
// reaper if its SIGCHLD is coalesced or missed.
const orphanReapInterval = time.Second

func commandSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
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

func enableChildSubreaper() error {
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, uintptr(linuxPRSetChildSubreaper), 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// reapReparentedChildren reaps every exited child without blocking. A
// registered direct child's status is still routed to its waiter; every other
// status is discarded, so it must not run while an unrelated waiter (such as
// exec.Cmd.Wait) still owns a child.
func reapReparentedChildren() int {
	processReaper.mu.Lock()
	defer processReaper.mu.Unlock()
	return reapChildren(processReaper.deliverLocked)
}

func reapChildren(onReaped func(pid int, status syscall.WaitStatus)) int {
	reaped := 0
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if pid <= 0 || err != nil {
			return reaped
		}
		reaped++
		if onReaped != nil {
			onReaped(pid, status)
		}
	}
}

// processReaper is the process-wide wait4 owner while any user command runs.
// wait4(-1) consumes the status of whichever child exits, so overlapping
// executeCommand calls must share one reaper: it routes each registered
// direct child's status to that command's waiter and discards the status of
// adopted descendants. While a command runs, runtimehelper owns every wait4
// in the process; unrelated os/exec children waited concurrently elsewhere in
// the same process may lose their status.
var processReaper = &reaperRegistry{waiters: map[int]*childReaper{}}

type reaperRegistry struct {
	// mu is held for each reap pass and across starting and registering a
	// command, so no pass can consume a direct child's status before it has a
	// waiter.
	mu      sync.Mutex
	waiters map[int]*childReaper
	users   int
	loop    *reapLoop
}

type reapLoop struct {
	requests chan chan struct{}
	stopCh   chan struct{}
	done     chan struct{}
}

// childReaper is one command's handle on the process-wide reaper.
type childReaper struct {
	pid         int
	direct      chan childExit
	loop        *reapLoop
	released    chan struct{}
	releaseOnce sync.Once
}

// startReapedCommand starts cmd and registers its pid with the process-wide
// reaper, starting the reap loop if no other command is running.
func startReapedCommand(cmd *exec.Cmd) (*childReaper, error) {
	reg := processReaper
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if reg.loop == nil {
		reg.loop = startReapLoop(reg)
	}
	reg.users++
	r := &childReaper{
		pid:      cmd.Process.Pid,
		direct:   make(chan childExit, 1),
		loop:     reg.loop,
		released: make(chan struct{}),
	}
	reg.waiters[r.pid] = r
	return r, nil
}

func startReapLoop(reg *reaperRegistry) *reapLoop {
	l := &reapLoop{
		requests: make(chan chan struct{}),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGCHLD)
	go l.run(reg, sigCh)
	return l
}

func (l *reapLoop) run(reg *reaperRegistry, sigCh chan os.Signal) {
	defer close(l.done)
	defer signal.Stop(sigCh)
	ticker := time.NewTicker(orphanReapInterval)
	defer ticker.Stop()
	for {
		reg.reap()
		select {
		case <-sigCh:
		case <-ticker.C:
		case ack := <-l.requests:
			reg.reap()
			close(ack)
		case <-l.stopCh:
			reg.reap()
			return
		}
	}
}

func (reg *reaperRegistry) reap() {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reapChildren(reg.deliverLocked)
}

// deliverLocked routes a reaped status to its registered waiter. A pid is
// unregistered once reaped, so a later adopted descendant that reuses it is
// discarded.
func (reg *reaperRegistry) deliverLocked(pid int, status syscall.WaitStatus) {
	r, ok := reg.waiters[pid]
	if !ok {
		return
	}
	delete(reg.waiters, pid)
	r.direct <- exitFromWaitStatus(status)
}

// waitDirect blocks until the direct child's status has been collected. It
// returns false if the command was released without collecting it.
func (r *childReaper) waitDirect() (childExit, bool) {
	select {
	case exit := <-r.direct:
		return exit, true
	case <-r.released:
		select {
		case exit := <-r.direct:
			return exit, true
		default:
			return childExit{}, false
		}
	}
}

// reapNow runs one reap pass and waits for it to finish.
func (r *childReaper) reapNow() {
	ack := make(chan struct{})
	select {
	case r.loop.requests <- ack:
		<-ack
	case <-r.loop.done:
	}
}

// stop unregisters the command. The last running command stops the reap loop
// after a final reap pass.
func (r *childReaper) stop() {
	r.releaseOnce.Do(func() {
		reg := processReaper
		reg.mu.Lock()
		if reg.waiters[r.pid] == r {
			delete(reg.waiters, r.pid)
		}
		reg.users--
		var last *reapLoop
		if reg.users == 0 {
			last, reg.loop = reg.loop, nil
		}
		reg.mu.Unlock()
		close(r.released)
		if last != nil {
			close(last.stopCh)
			<-last.done
		}
	})
}

func exitFromWaitStatus(status syscall.WaitStatus) childExit {
	if status.Signaled() {
		return childExit{signaled: true, signal: status.Signal(), coreDumped: status.CoreDump()}
	}
	return childExit{code: status.ExitStatus()}
}
