//go:build unix

package proxy

import (
	"errors"
	"syscall"
)

func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// pidAlive reports whether a process with the given pid currently exists.
// EPERM means the process exists but belongs to another user — that still
// counts as alive.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
