//go:build (jpeg2000_openjpeg || codecfull) && !windows

package jpeg2000

import (
	"os/exec"
	"syscall"
	"time"
)

func configureExternalDecoderCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
}
