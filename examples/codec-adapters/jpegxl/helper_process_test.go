package jpegxladapter

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("JPEGXL_FAKE_HELPER"); mode != "" {
		os.Exit(runFakeHelper(mode))
	}
	os.Exit(m.Run())
}

func runFakeHelper(mode string) int {
	decode := func([]byte, pixeldata.Metadata) ([]byte, error) {
		switch mode {
		case "crash":
			os.Exit(2)
		case "hang":
			time.Sleep(24 * time.Hour)
		case "drop":
			_ = os.Stdin.Close()
			_ = os.Stdout.Close()
			return nil, io.EOF
		default:
			return []byte{1, 2}, nil
		}
		return []byte{1, 2}, nil
	}
	_ = ServeHelper(os.Stdin, os.Stdout, decode)
	return 0
}

func TestHelperProcessCrashDoesNotKillCaller(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: testBinaryDial(t, "crash"),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("error = %v, want ErrHelperCrashed", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("crash presented as malformed: %v", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after helper crash", len(frame))
	}
}

func TestHelperProcessHangTimesOutWithoutMalformed(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Timeout: 80 * time.Millisecond,
		Dial:    testBinaryDial(t, "hang"),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("hang timeout presented as malformed: %v", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after helper hang", len(frame))
	}
}

func TestHelperProcessCloseReapsChild(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: testBinaryDial(t, "ok"),
	})
	s := session
	s.mu.Lock()
	pid := s.conn.PID
	s.mu.Unlock()
	if pid <= 1 {
		t.Fatal("helper pid was not recorded")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	waitHelper(t, 2*time.Second, func() bool { return !helperPIDAlive(pid) })
}

func TestHelperProcessLossOfChannelIsTyped(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: testBinaryDial(t, "drop"),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("error = %v, want ErrHelperCrashed", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after channel loss", len(frame))
	}
}

func testBinaryDial(t *testing.T, mode string) DialFunc {
	t.Helper()
	return func() (HelperConn, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "JPEGXL_FAKE_HELPER="+mode)
		configureHelperCommand(cmd)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return HelperConn{}, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			_ = stdin.Close()
			return HelperConn{}, err
		}
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			_ = stdout.Close()
			return HelperConn{}, err
		}
		return HelperConn{
			Reader: stdout,
			Writer: stdin,
			Closer: helperProcessCloser{cmd: cmd, stdin: stdin},
			PID:    cmd.Process.Pid,
		}, nil
	}
}

func helperPIDAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		// os.FindProcess does not prove liveness on Windows.
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func TestReapHelperProcessReturnsWaitErrorBeforeKill(t *testing.T) {
	done := make(chan error, 1)
	done <- errors.New("exited")
	killed := false
	err := reapHelperProcessAfter(done, func() error {
		killed = true
		return nil
	}, time.Second)
	if err == nil || err.Error() != "exited" {
		t.Fatalf("err = %v, want wait error", err)
	}
	if killed {
		t.Fatal("killed after wait completed")
	}
}

func TestReapHelperProcessTimesOutWhenKillFails(t *testing.T) {
	done := make(chan error)
	killErr := errors.New("kill failed")
	start := time.Now()
	err := reapHelperProcessAfter(done, func() error { return killErr }, 20*time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("err = %v, want ErrHelperUnavailable", err)
	}
	if !errors.Is(err, killErr) {
		t.Fatalf("err = %v, want wrapped kill error", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("reap hung for %s after kill failure", elapsed)
	}
}

func TestReapHelperProcessTimesOutWhenWaitHangsAfterKill(t *testing.T) {
	done := make(chan error)
	err := reapHelperProcessAfter(done, func() error { return nil }, 20*time.Millisecond)
	if !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("err = %v, want shutdown timeout", err)
	}
}

func TestReapHelperProcessReturnsWaitErrorAfterKill(t *testing.T) {
	done := make(chan error, 1)
	waitErr := errors.New("killed")
	err := reapHelperProcessAfter(done, func() error {
		done <- waitErr
		return errors.New("kill ignored because wait finished")
	}, 20*time.Millisecond)
	if !errors.Is(err, waitErr) {
		t.Fatalf("err = %v, want wait error after kill", err)
	}
}
