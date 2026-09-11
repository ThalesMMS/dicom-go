package codeccost

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func compileJXLHelper(ctx context.Context, src, out string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cflags, err := exec.CommandContext(ctx, "pkg-config", "--cflags", "--libs", "libjxl").Output()
	if err != nil {
		return fmt.Errorf("pkg-config libjxl: %w", err)
	}
	args := []string{"-O2", "-o", out, src}
	args = append(args, strings.Fields(string(cflags))...)
	cmd := exec.CommandContext(ctx, "cc", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cc: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func startHelperProcess(ctx context.Context, path string) (*HelperBackend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &HelperBackend{
		Name:   backendHelper,
		pid:    cmd.Process.Pid,
		writer: stdin,
		reader: stdout,
		closer: processCloser{cmd: cmd, stdin: stdin},
	}, nil
}

type processCloser struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func (c processCloser) Close() error {
	if c.stdin != nil {
		_ = writeHelperShutdown(c.stdin)
		_ = c.stdin.Close()
	}
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		return <-done
	}
}
