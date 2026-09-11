package testutil

import (
	"errors"
	"syscall"
	"testing"
)

func TestSymlinkCapabilityUnavailableOnlyAcceptsWindowsPrivilegeError(t *testing.T) {
	wrapped := errors.Join(errors.New("create link"), syscall.Errno(1314))
	tests := []struct {
		name string
		goos string
		err  error
		want bool
	}{
		{name: "windows privilege", goos: "windows", err: wrapped, want: true},
		{name: "linux same errno", goos: "linux", err: wrapped},
		{name: "windows access denied", goos: "windows", err: syscall.Errno(5)},
		{name: "windows other failure", goos: "windows", err: errors.New("bad fixture")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := symlinkCapabilityUnavailable(test.goos, test.err); got != test.want {
				t.Fatalf("symlinkCapabilityUnavailable(%q, %v) = %t, want %t", test.goos, test.err, got, test.want)
			}
		})
	}
}
