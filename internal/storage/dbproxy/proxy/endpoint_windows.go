//go:build windows

package proxy

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// stillActive is GetExitCodeProcess's STILL_ACTIVE (STATUS_PENDING, 0x103),
// which x/sys/windows does not export under that name.
const stillActive = 0x103

// pidAlive reports whether a process with the given pid currently exists.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
