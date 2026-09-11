//go:build windows

package jpegxladapter

import (
	"os/exec"
	"syscall"
	"time"
)

const helperCreateNoWindow = 0x08000000

func configureHelperCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: helperCreateNoWindow,
	}
	cmd.WaitDelay = 2 * time.Second
}

func killHelperProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
