//go:build codecfull && !windows

package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestUnsupportedPlatformReportsExplicitError(t *testing.T) {
	err := validateProcessTreeMemoryMeasurement()
	if err == nil {
		t.Fatal("expected unsupported-platform error")
	}
	if message := err.Error(); !strings.Contains(message, runtime.GOOS) ||
		!strings.Contains(message, "not implemented") {
		t.Fatalf("error = %q, want explicit unsupported-platform detail", message)
	}
}
