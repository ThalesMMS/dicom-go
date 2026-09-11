package testutil

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
)

const windowsPrivilegeNotHeld syscall.Errno = 1314

// SymlinkOrSkip creates a symbolic link or skips only when Windows reports
// that the current token lacks the symlink privilege. Every other failure is
// fatal so invalid fixtures and filesystem regressions remain visible.
func SymlinkOrSkip(t testing.TB, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if symlinkCapabilityUnavailable(runtime.GOOS, err) {
			t.Skipf("symlink test requires Windows Developer Mode or elevation (%s): %v", runtime.GOOS, err)
		}
		t.Fatalf("create symlink: %v", err)
	}
}

// TrySymlink creates a symbolic link and returns false only when the Windows
// host lacks the required privilege. It is intended for optional sub-checks in
// tests that can still verify useful non-symlink behavior.
func TrySymlink(t testing.TB, oldname, newname string) bool {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if symlinkCapabilityUnavailable(runtime.GOOS, err) {
			t.Logf("symlink sub-check skipped on %s; Developer Mode or elevation is required: %v", runtime.GOOS, err)
			return false
		}
		t.Fatalf("create symlink: %v", err)
	}
	return true
}

func symlinkCapabilityUnavailable(goos string, err error) bool {
	return goos == "windows" && errors.Is(err, windowsPrivilegeNotHeld)
}
