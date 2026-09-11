package netstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func assertOnlyExisting(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "1.2.3.4.dcm" {
		t.Fatalf("unexpected files: %v %v", entries, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "1.2.3.4.dcm"))
	if err != nil || string(b) != "KEEP" {
		t.Fatalf("preexisting file changed: %q %v", b, err)
	}
}

func TestInstancePublicationFailuresCancelAndRedaction(t *testing.T) {
	for _, stage := range []string{"write", "sync", "close", "cancel-write", "cancel-close", "publish"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "1.2.3.4.dcm"), []byte("KEEP"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := errors.New("remote-secret-UID sensitive-path")
			ops := defaultSaveOperations()
			switch stage {
			case "write":
				ops.write = func(w io.Writer, _ *object.File) error {
					_, err := w.Write([]byte("partial"))
					return errors.Join(injected, err)
				}
			case "sync":
				ops.sync = func(*os.File) error { return injected }
			case "close":
				ops.close = func(f *os.File) error { return errors.Join(f.Close(), injected) }
			case "cancel-write":
				ops.write = func(w io.Writer, _ *object.File) error { cancel(); _, err := w.Write([]byte("partial")); return err }
			case "cancel-close":
				ops.close = func(f *os.File) error { cancel(); return f.Close() }
			case "publish":
				ops.publish = func(*os.File, string, string, os.FileInfo) (bool, error) { return false, injected }
			}
			path, err := savePart10(ctx, dir, validDataSet(), transfer.ExplicitVRLittleEndian, ops)
			if err == nil || path != "" {
				t.Fatalf("failed operation published: %q %v", path, err)
			}
			if !errors.Is(err, injected) && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cause: %v", err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), dir) {
				t.Fatalf("unredacted error: %v", err)
			}
			assertOnlyExisting(t, dir)
		})
	}
}

func TestInstancePublicationVisibilityAndPostCommitFailure(t *testing.T) {
	dir := t.TempDir()
	ops := defaultSaveOperations()
	write := ops.write
	ops.write = func(w io.Writer, f *object.File) error {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".partial") {
			t.Fatalf("instance visible before write: %v %v", entries, err)
		}
		return write(w, f)
	}
	publish := ops.publish
	injected := errors.New("postcommit close failure with sensitive path")
	ops.publish = func(parent *os.File, source, destination string, info os.FileInfo) (bool, error) {
		published, err := publish(parent, source, destination, info)
		if err != nil || !published {
			return published, err
		}
		return true, injected
	}
	path, err := savePart10(context.Background(), dir, validDataSet(), transfer.ExplicitVRLittleEndian, ops)
	var detail *Error
	if path == "" || !errors.As(err, &detail) || !detail.Published || !errors.Is(err, injected) {
		t.Fatalf("lost publication outcome: %q %v", path, err)
	}
	f, err := object.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if uid, ok := f.GetUID(dicomtags.SOPInstanceUID); !ok || uid != "1.2.3.4" {
		t.Fatal("published identity changed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary leak: %v %v", entries, err)
	}
}

func TestInstanceConcurrentPublicationNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1.2.3.4.dcm"), []byte("KEEP"), 0o600); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	var wg sync.WaitGroup
	paths := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := SavePart10(dir, validDataSet(), transfer.ExplicitVRLittleEndian)
			if err != nil {
				t.Error(err)
				return
			}
			paths <- path
		}()
	}
	wg.Wait()
	close(paths)
	seen := map[string]bool{}
	for path := range paths {
		if seen[path] {
			t.Fatal("duplicate published path")
		}
		seen[path] = true
		f, err := object.OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	if len(seen) != workers {
		t.Fatalf("published %d instances", len(seen))
	}
	b, err := os.ReadFile(filepath.Join(dir, "1.2.3.4.dcm"))
	if err != nil || string(b) != "KEEP" {
		t.Fatal("preexisting file overwritten")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != workers+1 {
		t.Fatalf("leaked/absent files: %d %v", len(entries), err)
	}
}

func TestInstanceRejectsRootSwapAndCleansOriginalDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "output")
	moved := filepath.Join(base, "original")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ops := defaultSaveOperations()
	closeFile := ops.close
	ops.close = func(f *os.File) error {
		if err := closeFile(f); err != nil {
			return err
		}
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		return os.Mkdir(dir, 0o700)
	}
	path, err := savePart10(context.Background(), dir, validDataSet(), transfer.ExplicitVRLittleEndian, ops)
	if path != "" || !errors.Is(err, ErrUnsafeDirectory) {
		t.Fatalf("root swap accepted: %q %v", path, err)
	}
	for _, p := range []string{dir, moved} {
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) != 0 {
			t.Fatalf("root swap left files: %v %v", entries, err)
		}
	}
}

func TestInstanceSymlinkTargetsAndAncestorsNeverFollowed(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	dir := filepath.Join(base, "output")
	for _, p := range []string{outside, dir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(outside, "KEEP")
	if err := os.WriteFile(target, []byte("KEEP"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "1.2.3.4.dcm")); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	path, err := SavePart10(dir, validDataSet(), transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "1.2.3.4.1.dcm" {
		t.Fatalf("collision path: %q", path)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "KEEP" {
		t.Fatal("symlink target changed")
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{alias, filepath.Join(alias, "child")} {
		if root != alias {
			if err := os.Mkdir(filepath.Join(outside, "child"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if path, err := SavePart10(root, validDataSet(), transfer.ExplicitVRLittleEndian); err == nil || path != "" {
			t.Fatalf("followed directory symlink: %q %v", path, err)
		}
	}
}

func TestInstanceUIDMultiplicityClassAndPaddingPolicy(t *testing.T) {
	for _, tag := range []core.Tag{dicomtags.SOPClassUID, dicomtags.SOPInstanceUID} {
		for _, values := range []core.StringValue{{"1.2.3", "1.2.4"}, {"1.02.3"}, {"1.2.3\\1.2.4"}} {
			ds := validDataSet()
			ds.Put(core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRUI}, Value: values})
			if _, err := SavePart10(t.TempDir(), ds, transfer.ExplicitVRLittleEndian); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("invalid identity accepted: %v", err)
			}
		}
	}
	ds := validDataSet()
	ds.Put(newUIElement(dicomtags.SOPInstanceUID, "1.2.3.4\x00"))
	path, err := SavePart10(t.TempDir(), ds, transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "1.2.3.4.dcm" {
		t.Fatal("UI padding changed canonical filename")
	}
	uid := "2." + strings.Repeat("1", 62)
	ds.Put(newUIElement(dicomtags.SOPInstanceUID, uid))
	if _, err := SavePart10(t.TempDir(), ds, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatalf("64-byte UID rejected: %v", err)
	}
}
