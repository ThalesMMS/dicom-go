package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestCGetPersistenceRejectsInvalidIdentityAndRedactsLogs(t *testing.T) {
	const class = "1.2.840.10008.5.1.4.1.1.2"
	for _, uid := range []string{"../escape", `..\escape`, "CON", "1.02.3", "1." + strings.Repeat("2", 63)} {
		t.Run(uid, func(t *testing.T) {
			remote, done := startGetSCP(t, class, []string{uid})
			dir := t.TempDir()
			var stdout, stderr bytes.Buffer
			code := run([]string{"-method", "get", "-remote", remote, "-calling-aet", "GETSCU", "-called-aet", "GETSCP", "-output", dir, "-level", "STUDY", "-study-uid", "1.2.3"}, &stdout, &stderr)
			serverErr := <-done
			multipleCommandValues := strings.Contains(uid, `\`)
			if serverErr != nil && !multipleCommandValues {
				t.Fatal(serverErr)
			}
			if multipleCommandValues && (code == 0 || serverErr == nil) {
				t.Fatalf("ambiguous command not aborted: code=%d error=%v", code, serverErr)
			}
			if (!multipleCommandValues && (code != 0 || !strings.Contains(stdout.String(), "failed=1"))) || strings.Contains(stdout.String(), "stored DICOM instance") {
				t.Fatalf("incorrect retrieve outcome: %d %s %s", code, &stdout, &stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid instance persisted: %v %v", entries, err)
			}
			logs := stdout.String() + stderr.String()
			for _, secret := range []string{uid, dir, class} {
				if strings.Contains(logs, secret) {
					t.Fatalf("unredacted C-GET logs: %s", logs)
				}
			}
		})
	}
}
