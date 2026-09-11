package jpegxladapter

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// HelperExecutableName is the packaged worker filename without OS suffix.
const HelperExecutableName = "jpegxl-helper"

// HelperResidentOverheadBytes is the conservative native RSS charged while a
// helper process is alive. It is not a Go heap number.
const HelperResidentOverheadBytes = helperResidentOverheadBytes

const helperCompileTimeout = 2 * time.Minute

func compileJPEGXLHelper(ctx context.Context, src, out string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cflags, err := exec.CommandContext(ctx, "pkg-config", "--cflags", "--libs", "libjxl").Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("pkg-config libjxl: %w", err)
	}
	args := []string{"-O2", "-o", out, src}
	args = append(args, strings.Fields(string(cflags))...)
	cmd := exec.CommandContext(ctx, "cc", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("cc: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
