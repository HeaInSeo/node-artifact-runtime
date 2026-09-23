//go:build !linux

package runtimehelper

import (
	"errors"
	"syscall"
)

// The runtime shim supervises user commands only on Linux, where it relies on
// PR_SET_CHILD_SUBREAPER and process-group signalling. On other platforms the
// package builds, but executeCommand fails before starting the command.
var errSupervisorUnsupported = errors.New("command supervision is only supported on linux")

func commandSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func signalProcessGroup(int, syscall.Signal) error {
	return errSupervisorUnsupported
}

func processGroupExists(int) bool {
	return false
}

func enableChildSubreaper() error {
	return errSupervisorUnsupported
}

func reapReparentedChildren() int {
	return 0
}

type childReaper struct{}

func startChildReaper(int) *childReaper {
	return &childReaper{}
}

func (*childReaper) waitDirect() (childExit, bool) {
	return childExit{}, false
}

func (*childReaper) reapNow() {}

func (*childReaper) stop() {}
