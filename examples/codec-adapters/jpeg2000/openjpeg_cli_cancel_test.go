//go:build jpeg2000_openjpeg || codecfull

package jpeg2000

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestOpenJPEGDecoderCancelBeforeLaunchDoesNotStartProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	launched := filepath.Join(tmp, "launched")
	path := writeOpenJPEGCancelExecutable(t, "unrun-opj", `#!/bin/sh
echo launched > "`+launched+`"
exit 1
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newOpenJPEGDecoder(OpenJPEGExecutable(path)).DecodeFrameContext(ctx, []byte("encoded"), openJPEGCancelMetadata())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeFrameContext() error = %v, want context.Canceled", err)
	}
	if leftover := leftoverOpenJPEGDirs(t, tmp); leftover != 0 {
		t.Fatalf("temp directories left after cancel-before-launch = %d", leftover)
	}
	if _, err := os.Stat(launched); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decoder process launched before cancellation: %v", err)
	}
}

func TestOpenJPEGDecoderCancelDuringDecodeKillsProcessAndTemps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	started := filepath.Join(tmp, "started")
	childPID := filepath.Join(tmp, "child.pid")
	path := writeOpenJPEGCancelExecutable(t, "hang-opj", `#!/bin/sh
echo $$ > "`+started+`"
sleep 120 &
echo $! > "`+childPID+`"
while :; do sleep 0.05; done
`)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := newOpenJPEGDecoder(OpenJPEGExecutable(path), OpenJPEGTimeout(5*time.Second)).DecodeFrameContext(ctx, []byte("encoded"), openJPEGCancelMetadata())
		errCh <- err
	}()

	waitForOpenJPEGFile(t, started)
	waitForOpenJPEGFile(t, childPID)
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

	waitUntilOpenJPEG(t, 2*time.Second, func() bool {
		return leftoverOpenJPEGDirs(t, tmp) == 0
	})
	if openJPEGProcessAlive(readOpenJPEGPIDFile(t, started)) {
		t.Fatal("opj_decompress process still running after cancel")
	}
	if openJPEGProcessAlive(readOpenJPEGPIDFile(t, childPID)) {
		t.Fatal("opj_decompress descendant still running after cancel")
	}
}

func TestOpenJPEGDecoderIsolatesConcurrentCalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	path := writeOpenJPEGCancelExecutable(t, "ok-opj", `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift
done
printf 'P5\n2 1\n255\n\000\377' > "$out"
`)
	decoder := newOpenJPEGDecoder(OpenJPEGExecutable(path))
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frame, err := decoder.DecodeFrameContext(context.Background(), []byte("encoded"), openJPEGCancelMetadata())
			if err != nil {
				errCh <- err
				return
			}
			if len(frame) != 2 {
				errCh <- errors.New("unexpected isolated frame size")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func openJPEGCancelMetadata() pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows:            1,
		Columns:         2,
		SamplesPerPixel: 1,
		BitsAllocated:   8,
		BitsStored:      8,
	}
}

func leftoverOpenJPEGDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "dicom-go-openjpeg-") {
			count++
		}
	}
	return count
}

func writeOpenJPEGCancelExecutable(t testing.TB, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForOpenJPEGFile(t *testing.T, path string) {
	t.Helper()
	waitUntilOpenJPEG(t, 2*time.Second, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func waitUntilOpenJPEG(t *testing.T, timeout time.Duration, ready func() bool) {
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

func readOpenJPEGPIDFile(t *testing.T, path string) int {
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

func openJPEGProcessAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
