package codeccost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStartHelperProcessHonorsCanceledContext(t *testing.T) {
	hanging := writeHangingCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		helper, err := startHelperProcess(ctx, hanging)
		if helper != nil {
			_ = helper.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled start succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startHelperProcess ignored ctx; helper start can hang")
	}
}

func TestStartHelperProcessMissingBinaryReturnsError(t *testing.T) {
	helper, err := startHelperProcess(context.Background(), filepath.Join(t.TempDir(), "missing-helper"))
	if helper != nil {
		_ = helper.Close()
		t.Fatal("missing binary started")
	}
	if err == nil {
		t.Fatal("missing binary returned nil error")
	}
}

func TestStartHelperProcessStartsAndCloses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("success-path stub uses a POSIX cat")
	}
	path, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not found")
	}
	helper, err := startHelperProcess(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if helper == nil || helper.pid <= 0 {
		t.Fatal("started helper missing pid")
	}
	if err := helper.Close(); err != nil && !os.IsPermission(err) {
		t.Fatalf("Close() = %v", err)
	}
}
