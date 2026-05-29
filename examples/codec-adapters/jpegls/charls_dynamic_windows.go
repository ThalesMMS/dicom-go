//go:build (jpegls_charls || codecfull) && windows

package jpegls

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	memCommitReserve = 0x3000
	memRelease       = 0x8000
	pageReadWrite    = 0x04
)

var (
	kernel32VirtualAlloc = syscall.NewLazyDLL("kernel32.dll").NewProc("VirtualAlloc")
	kernel32VirtualFree  = syscall.NewLazyDLL("kernel32.dll").NewProc("VirtualFree")
)

func nativeBytes(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: zero-length native buffer", ErrBackendInternal)
	}
	address, _, callErr := kernel32VirtualAlloc.Call(0, uintptr(len(data)), memCommitReserve, pageReadWrite)
	if address == 0 {
		return nil, fmt.Errorf("%w: VirtualAlloc native buffer: %w", ErrBackendInternal, callErr)
	}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(address)), len(data))
	copy(buf, data)
	return buf, nil
}

func releaseNativeBytes(buf []byte) {
	if len(buf) == 0 {
		return
	}
	_, _, _ = kernel32VirtualFree.Call(uintptr(unsafe.Pointer(&buf[0])), 0, memRelease)
}

func openCharLSAPI() (*charlsAPI, error) {
	var errs []error
	for _, name := range charlsLibraryNames() {
		handle, err := syscall.LoadLibrary(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		api, err := bindCharLSAPI(uintptr(handle))
		if err == nil {
			err = validateLoadedCharLSAPI(api)
		}
		if err != nil {
			_ = syscall.FreeLibrary(handle)
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
	switch runtime.GOARCH {
	case "amd64":
		return []string{"charls-2-x64.dll", "charls-2.dll", "charls.dll"}
	case "arm64":
		return []string{"charls-2-arm64.dll", "charls-2.dll", "charls.dll"}
	default:
		return []string{"charls-2.dll", "charls.dll"}
	}
}
