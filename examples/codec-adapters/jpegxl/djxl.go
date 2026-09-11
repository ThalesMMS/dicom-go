package jpegxladapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const (
	defaultDjxlTimeout   = 30 * time.Second
	QualifiedDjxlVersion = "0.11.2"
)

type djxlDecoder struct {
	executable string
	timeout    time.Duration
}

// DjxlOption configures the optional djxl-backed decoder.
type DjxlOption func(*djxlDecoder)

// DjxlExecutable sets the djxl executable path. When unset, DICOM_GO_DJXL is
// checked first, then PATH.
func DjxlExecutable(path string) DjxlOption {
	return func(decoder *djxlDecoder) {
		decoder.executable = path
	}
}

// DjxlTimeout sets the maximum time allowed for one djxl subprocess.
// Non-positive values use the package default.
func DjxlTimeout(timeout time.Duration) DjxlOption {
	return func(decoder *djxlDecoder) {
		decoder.timeout = timeout
	}
}

// NewDjxlDecoder returns an optional djxl-backed decoder. Without the
// jpegxl_djxl build tag, DecodeFrame returns ErrDjxlUnavailable.
func NewDjxlDecoder(options ...DjxlOption) Decoder {
	decoder := newDjxlDecoder(options...)
	return decoder
}

func newDjxlDecoder(options ...DjxlOption) djxlDecoder {
	decoder := djxlDecoder{
		executable: os.Getenv("DICOM_GO_DJXL"),
		timeout:    defaultDjxlTimeout,
	}
	for _, option := range options {
		if option != nil {
			option(&decoder)
		}
	}
	if decoder.timeout <= 0 {
		decoder.timeout = defaultDjxlTimeout
	}
	return decoder
}

func (decoder djxlDecoder) resolveExecutable() (string, error) {
	executable := decoder.executable
	if executable == "" {
		executable = os.Getenv("DICOM_GO_DJXL")
	}
	if executable != "" {
		if filepath.Base(executable) == executable {
			resolved, err := exec.LookPath(executable)
			if err != nil {
				return "", fmt.Errorf("%w: %s not found in PATH", ErrDjxlUnavailable, executable)
			}
			return resolved, nil
		}
		info, err := os.Stat(executable)
		if err != nil {
			return "", fmt.Errorf("%w: %s: %w", ErrDjxlUnavailable, executable, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: %s is not a regular executable file", ErrDjxlUnavailable, executable)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%w: %s is not executable", ErrDjxlUnavailable, executable)
		}
		return executable, nil
	}
	for _, bundled := range bundledDjxlCandidates() {
		info, err := os.Stat(bundled)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return bundled, nil
	}
	resolved, err := exec.LookPath("djxl")
	if err != nil {
		return "", fmt.Errorf("%w: djxl not found in PATH", ErrDjxlUnavailable)
	}
	return resolved, nil
}

func bundledDjxlCandidates() []string {
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	dir := filepath.Dir(executable)
	name := "djxl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return []string{
		filepath.Join(dir, name),
		filepath.Join(dir, "codec", name),
		filepath.Clean(filepath.Join(dir, "..", "Resources", "codec", name)),
	}
}

func boundDecoderContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	if deadline, ok := parent.Deadline(); ok {
		if remaining := time.Until(deadline); remaining <= timeout {
			return context.WithCancel(parent)
		}
	}
	return context.WithTimeout(parent, timeout)
}

func mapBoundDecoderError(parent, run context.Context) error {
	if parent != nil {
		if err := parent.Err(); err != nil {
			return err
		}
	}
	if run == nil {
		return nil
	}
	if errors.Is(run.Err(), context.DeadlineExceeded) {
		return ErrDecoderTimeout
	}
	return run.Err()
}
