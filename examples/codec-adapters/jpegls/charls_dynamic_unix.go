//go:build (jpegls_charls || codecfull) && (darwin || linux)

package jpegls

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/ebitengine/purego"
)

func nativeBytes(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: zero-length native buffer", ErrBackendInternal)
	}
	buf, err := syscall.Mmap(-1, 0, len(data), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("%w: mmap native buffer: %w", ErrBackendInternal, err)
	}
	copy(buf, data)
	return buf, nil
}

func releaseNativeBytes(buf []byte) {
	_ = syscall.Munmap(buf)
}

func openCharLSAPI() (*charlsAPI, error) {
	var errs []error
	for _, name := range charlsLibraryNames() {
		handle, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		api, err := bindCharLSAPI(handle)
		if err == nil {
			err = validateLoadedCharLSAPI(api)
		}
		if err != nil {
			_ = purego.Dlclose(handle)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		return api, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrDecoderUnavailable, errors.Join(errs...))
}

func charlsLibraryNames() []string {
	if override := os.Getenv("DICOM_GO_CHARLS_LIBRARY"); override != "" {
		return []string{override}
	}
	var bundled []string
	if executable, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executable)
		bundled = append(bundled,
			filepath.Join(executableDir, "libcharls.2.so"),
			filepath.Join(executableDir, "codec", "libcharls.2.so"),
			filepath.Clean(filepath.Join(executableDir, "..", "Resources", "codec", "libcharls.2.dylib")),
		)
	}
	switch runtime.GOOS {
	case "darwin":
		return append(bundled,
			"libcharls.2.dylib",
			"libcharls.dylib",
			"/opt/homebrew/lib/libcharls.2.dylib",
			"/usr/local/lib/libcharls.2.dylib",
		)
	case "linux":
		return append(bundled, "libcharls.so.2", "libcharls.so")
	default:
		return nil
	}
}
