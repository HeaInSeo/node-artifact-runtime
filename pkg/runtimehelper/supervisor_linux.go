package runtimehelper

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
)

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
