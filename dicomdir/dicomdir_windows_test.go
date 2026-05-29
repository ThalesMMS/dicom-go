//go:build windows

package dicomdir

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.dcm"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "LINK"), outside)

	ref := Reference{FileID: []string{"LINK", "outside.dcm"}}
	if got := Resolve(root, ref); got != "" {
		t.Fatalf("Resolve(%#v) = %q, want empty path", ref, got)
	}
}

func TestOpenReferencedFileRejectsComponentReplacedAfterResolution(t *testing.T) {
	root := t.TempDir()
	images := filepath.Join(root, "IMAGES")
	if err := os.Mkdir(images, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "IM1"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := Reference{FileID: []string{"IMAGES", "IM1"}}
	if resolved := Resolve(root, ref); resolved == "" {
		t.Fatal("Resolve() rejected the original regular-file reference")
	}

	original := filepath.Join(root, "ORIGINAL")
	if err := os.Rename(images, original); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "IM1"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	createJunction(t, images, outside)

	file, _, err := OpenReferencedFile(root, ref)
	if err == nil {
		_ = file.Close()
		t.Fatal("OpenReferencedFile() followed a replaced path component")
	}
}

func createJunction(t *testing.T, junction, target string) {
	t.Helper()
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction %q -> %q: %v\n%s", junction, target, err, output)
	}
}
