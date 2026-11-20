//go:build windows

package videosrc

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func cmdSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
