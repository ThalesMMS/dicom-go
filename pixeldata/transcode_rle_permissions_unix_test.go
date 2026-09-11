//go:build !windows

package pixeldata_test

import (
	"os"
	"testing"
)

func assertTranscodeDestinationPermissions(t *testing.T, _ string, info os.FileInfo) {
	t.Helper()
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("destination mode = %o, want 600", info.Mode().Perm())
	}
}
