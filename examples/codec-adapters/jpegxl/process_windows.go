//go:build (jpegxl_djxl || codecfull) && windows

package jpegxladapter

import (
	"os/exec"
	"time"
)

func configureExternalDecoderCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.WaitDelay = 2 * time.Second
}
