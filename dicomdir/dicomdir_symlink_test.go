//go:build !windows

package dicomdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.dcm"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "LINK")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

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
	if err := os.Symlink(outside, images); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, _, err := OpenReferencedFile(root, ref)
	if err == nil {
		_ = file.Close()
		t.Fatal("OpenReferencedFile() followed a replaced path component")
	}
}
