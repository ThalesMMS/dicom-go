//go:build (jpeg2000_openjpeg || codecfull) && windows

package jpeg2000

import (
	"os/exec"
	"syscall"
	"time"
)

const decoderCreateNoWindow = 0x08000000

func configureExternalDecoderCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: decoderCreateNoWindow,
	}
	cmd.WaitDelay = 2 * time.Second
}
