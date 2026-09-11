//go:build jpegxl_djxl || codecfull

package jpegxladapter

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
)

func TestDjxlDecoderCancelBeforeLaunchDoesNotStartProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	launched := filepath.Join(tmp, "launched")
	path := writeExecutable(t, "unrun-djxl", `#!/bin/sh
echo launched > "`+launched+`"
exit 1
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newDjxlDecoder(DjxlExecutable(path)).DecodeFrameContext(ctx, []byte("encoded"), grayMetadata())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeFrameContext() error = %v, want context.Canceled", err)
	}
	if leftover := leftoverDjxlDirs(t, tmp); leftover != 0 {
		t.Fatalf("temp directories left after cancel-before-launch = %d", leftover)
	}
	if _, err := os.Stat(launched); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decoder process launched before cancellation: %v", err)
	}
}

func TestDjxlDecoderCancelDuringDecodeKillsProcessAndTemps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	started := filepath.Join(tmp, "started")
	childPID := filepath.Join(tmp, "child.pid")
	path := writeExecutable(t, "hang-djxl", `#!/bin/sh
echo $$ > "`+started+`"
sleep 120 &
echo $! > "`+childPID+`"
while :; do sleep 0.05; done
`)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := newDjxlDecoder(DjxlExecutable(path), DjxlTimeout(5*time.Second)).DecodeFrameContext(ctx, []byte("encoded"), grayMetadata())
		errCh <- err
	}()

	waitForFile(t, started)
	waitForFile(t, childPID)
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

	waitUntil(t, 2*time.Second, func() bool {
		return leftoverDjxlDirs(t, tmp) == 0
	})
	if processAlive(readPIDFile(t, started)) {
		t.Fatal("djxl process still running after cancel")
	}
	if processAlive(readPIDFile(t, childPID)) {
		t.Fatal("djxl descendant still running after cancel")
	}
}

func TestDjxlDecoderTimeoutIsDecoderTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script timeout probe is POSIX-specific")
	}
	path := writeExecutable(t, "slow-djxl", "#!/bin/sh\nwhile :; do :; done\n")

	_, err := NewDjxlDecoder(DjxlExecutable(path), DjxlTimeout(50*time.Millisecond)).DecodeFrame([]byte("encoded"), grayMetadata())
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("codec timeout wrapped context.DeadlineExceeded: %v", err)
	}
	if !errors.Is(err, ErrDecoderTimeout) {
		t.Fatalf("DecodeFrame() error = %v, want ErrDecoderTimeout", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("timeout presented as malformed codestream: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("DecodeFrame() error = %q, want timeout detail", err)
	}
}

func TestDjxlDecoderReportsLaunchError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable mode assertion does not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "missing-djxl")
	_, err := newDjxlDecoder(DjxlExecutable(path)).DecodeFrameContext(context.Background(), []byte("encoded"), grayMetadata())
	if !errors.Is(err, ErrDjxlUnavailable) {
		t.Fatalf("DecodeFrameContext() error = %v, want ErrDjxlUnavailable", err)
	}
}

func TestDjxlDecoderIsolatesConcurrentCalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	path := writeExecutable(t, "ok-djxl", `#!/bin/sh
out="${2}"
printf 'P5\n2 1\n255\n\000\377' > "$out"
`)
	decoder := newDjxlDecoder(DjxlExecutable(path))
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frame, err := decoder.DecodeFrameContext(context.Background(), []byte("encoded"), grayMetadata())
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

func leftoverDjxlDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "dicom-go-djxl-") {
			count++
		}
	}
	return count
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	waitUntil(t, 2*time.Second, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func waitUntil(t *testing.T, timeout time.Duration, ready func() bool) {
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

func readPIDFile(t *testing.T, path string) int {
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

func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
