//go:build codecfull

package jpeg2000

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestOpenJPHDecoderCancelBeforeLaunchDoesNotStartProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	launched := filepath.Join(tmp, "launched")
	path := writeOpenJPHCancelExecutable(t, "unrun-ojph", `#!/bin/sh
echo launched > "`+launched+`"
exit 1
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newOpenJPHDecoder(OpenJPHExecutable(path)).DecodeFrameContext(ctx, []byte("encoded"), openJPHCancelMetadata())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeFrameContext() error = %v, want context.Canceled", err)
	}
	if leftover := leftoverOpenJPHDirs(t, tmp); leftover != 0 {
		t.Fatalf("temp directories left after cancel-before-launch = %d", leftover)
	}
	if _, err := os.Stat(launched); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decoder process launched before cancellation: %v", err)
	}
}

func TestOpenJPHDecoderCancelDuringDecodeKillsProcessAndTemps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	started := filepath.Join(tmp, "started")
	childPID := filepath.Join(tmp, "child.pid")
	path := writeOpenJPHCancelExecutable(t, "hang-ojph", `#!/bin/sh
echo $$ > "`+started+`"
sleep 120 &
echo $! > "`+childPID+`"
while :; do sleep 0.05; done
`)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := newOpenJPHDecoder(OpenJPHExecutable(path), OpenJPHTimeout(5*time.Second)).DecodeFrameContext(ctx, []byte("encoded"), openJPHCancelMetadata())
		errCh <- err
	}()

	waitForOpenJPHFile(t, started)
	waitForOpenJPHFile(t, childPID)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DecodeFrameContext() error = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrMalformedCodestream) {
			t.Fatalf("cancel presented as malformed codestream: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DecodeFrameContext did not return after cancel")
	}

	waitUntilOpenJPH(t, 2*time.Second, func() bool {
		return leftoverOpenJPHDirs(t, tmp) == 0
	})
	if openJPHProcessAlive(readOpenJPHPIDFile(t, started)) {
		t.Fatal("ojph_expand process still running after cancel")
	}
	if openJPHProcessAlive(readOpenJPHPIDFile(t, childPID)) {
		t.Fatal("ojph_expand descendant still running after cancel")
	}
}

func openJPHCancelMetadata() pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows:                1,
		Columns:             2,
		SamplesPerPixel:     1,
		BitsAllocated:       8,
		BitsStored:          8,
		PixelRepresentation: 0,
	}
}

func leftoverOpenJPHDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "dicom-go-openjph-") {
			count++
		}
	}
	return count
}

func writeOpenJPHCancelExecutable(t testing.TB, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForOpenJPHFile(t *testing.T, path string) {
	t.Helper()
	waitUntilOpenJPH(t, 2*time.Second, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func waitUntilOpenJPH(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func readOpenJPHPIDFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

func openJPHProcessAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
