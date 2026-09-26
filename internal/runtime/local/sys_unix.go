//go:build unix

package local

import (
	"errors"
	"syscall"
)

// ownGroup starts a process in a process group of its own, so that it
// and whatever it starts can be signalled together.
func ownGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// ownSession starts the supervisor in a session of its own: it outlives
// the terminal pit ran in, and the Ctrl+C typed there.
func ownSession() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return errors.New("no process")
	}
	return syscall.Kill(-pid, sig)
}

func signalProcess(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return errors.New("no process")
	}
	return syscall.Kill(pid, sig)
}

// alive reports whether a process exists.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
