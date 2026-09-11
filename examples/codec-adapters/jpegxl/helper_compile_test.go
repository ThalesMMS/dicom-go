package jpegxladapter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCompileJPEGXLHelperHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := compileJPEGXLHelper(ctx, filepath.Join("helperc", "jpegxl_helper.c"), filepath.Join(t.TempDir(), HelperExecutableName))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("compileJPEGXLHelper() error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("compile ignored cancel for %s", elapsed)
	}
}
