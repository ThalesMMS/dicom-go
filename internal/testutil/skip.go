package testutil

import (
	"os"
	"testing"
)

// SkipIfIntegration skips a test unless integration tests are enabled.
//
// This repository keeps external peer interop tests opt-in so CI remains
// self-contained.
func SkipIfIntegration(t *testing.T) {
	if os.Getenv("DICOMGO_INTEGRATION") == "" {
		t.Skip("skipping integration test (set DICOMGO_INTEGRATION=1 to enable)")
	}
}
