package jpegxladapter

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const helperShutdownWait = 2 * time.Second

func startHelperProcess(path string) (HelperConn, error) {
	cmd := exec.Command(path)
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
	cmd.Env = append(os.Environ(), "JXL_NUM_THREADS=1")
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return HelperConn{}, fmt.Errorf("%w: start %s: %w", ErrHelperUnavailable, path, err)
	}
	return HelperConn{
		Reader: stdout,
		Writer: stdin,
		Closer: helperProcessCloser{cmd: cmd, stdin: stdin},
		PID:    cmd.Process.Pid,
	}, nil
}

type helperProcessCloser struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func (c helperProcessCloser) Close() error {
	if c.stdin != nil {
		_ = writeHelperRequest(c.stdin, helperRequest{Opcode: helperOpcodeShutdown})
		_ = c.stdin.Close()
	}
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	return reapHelperProcessAfter(done, func() error { return killHelperProcess(c.cmd) }, helperShutdownWait)
}

func reapHelperProcessAfter(done <-chan error, kill func() error, wait time.Duration) error {
	if wait <= 0 {
		wait = helperShutdownWait
	}
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		var killErr error
		if kill != nil {
			killErr = kill()
		}
		select {
		case err := <-done:
			if err != nil {
				return err
			}
			return killErr
		case <-time.After(wait):
			if killErr != nil {
				return fmt.Errorf("%w: helper shutdown timed out: %w", ErrHelperUnavailable, killErr)
			}
			return fmt.Errorf("%w: helper shutdown timed out", ErrHelperUnavailable)
		}
	}
}
