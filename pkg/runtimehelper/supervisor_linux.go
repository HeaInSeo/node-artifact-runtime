package runtimehelper

import (
	"errors"
	"fmt"
	"os"
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

// reapReparentedChildren reaps every exited child without blocking. It
// consumes the status of any child, so it must not run while another waiter
// still owns a child; during executeCommand only the childReaper waits.
func reapReparentedChildren() int {
	return reapChildren(nil)
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

// childReaper is the only wait4 caller while a user command runs. It reaps on
// SIGCHLD, on a slow ticker, and on request, routing the direct child's status
// to waitDirect and discarding the status of adopted descendants.
type childReaper struct {
	directPid int
	direct    chan childExit
	requests  chan chan struct{}
	stopCh    chan struct{}
	done      chan struct{}
	stopOnce  sync.Once

	// directReaped is only accessed by the run goroutine.
	directReaped bool
}

func startChildReaper(directPid int) *childReaper {
	r := &childReaper{
		directPid: directPid,
		direct:    make(chan childExit, 1),
		requests:  make(chan chan struct{}),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGCHLD)
	go r.run(sigCh)
	return r
}

func (r *childReaper) run(sigCh chan os.Signal) {
	defer close(r.done)
	defer signal.Stop(sigCh)
	ticker := time.NewTicker(orphanReapInterval)
	defer ticker.Stop()
	for {
		r.reap()
		select {
		case <-sigCh:
		case <-ticker.C:
		case ack := <-r.requests:
			r.reap()
			close(ack)
		case <-r.stopCh:
			r.reap()
			return
		}
	}
}

func (r *childReaper) reap() {
	reapChildren(func(pid int, status syscall.WaitStatus) {
		// Once the direct child is reaped its pid may be reused by a later
		// adopted descendant, so only the first match is the direct child.
		if pid == r.directPid && !r.directReaped {
			r.directReaped = true
			r.direct <- exitFromWaitStatus(status)
		}
	})
}

// waitDirect blocks until the direct child's status has been collected. It
// returns false if the reaper stopped without collecting it.
func (r *childReaper) waitDirect() (childExit, bool) {
	select {
	case exit := <-r.direct:
		return exit, true
	case <-r.done:
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
	case r.requests <- ack:
		<-ack
	case <-r.done:
	}
}

// stop runs a final reap pass and stops the reaper.
func (r *childReaper) stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	<-r.done
}

func exitFromWaitStatus(status syscall.WaitStatus) childExit {
	if status.Signaled() {
		return childExit{signaled: true, signal: status.Signal(), coreDumped: status.CoreDump()}
	}
	return childExit{code: status.ExitStatus()}
}
