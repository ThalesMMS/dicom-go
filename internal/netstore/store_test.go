package netstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestSafeFileBaseAvoidsHiddenNames(t *testing.T) {
	if got := SafeFileBase(".1.2.3"); got != "_1.2.3" {
		t.Fatalf("SafeFileBase() = %q, want %q", got, "_1.2.3")
	}
}

func TestSafeFileBaseFallback(t *testing.T) {
	if got := SafeFileBase(" \x00"); got != "instance" {
		t.Fatalf("SafeFileBase(blank) = %q, want instance", got)
	}
	if got := SafeFileBase("1.2.3 \x00"); got != "1.2.3" {
		t.Fatalf("SafeFileBase(padded UID) = %q, want 1.2.3", got)
	}
	if got := SafeFileBase("1.2\x003 \x00"); got != "1.2_3" {
		t.Fatalf("SafeFileBase(interior NUL) = %q, want 1.2_3", got)
	}
}

func TestValidateCStoreDataSetRejectsNilDataSet(t *testing.T) {
	err := ValidateCStoreDataSet("1.2.3", "1.2.3.4", ul.AcceptedContext{AbstractSyntaxUID: "1.2.3"}, nil)
	if err == nil || err.Error() != "missing dataset" {
		t.Fatalf("ValidateCStoreDataSet(nil) error = %v, want missing dataset", err)
	}
}

func TestValidateCStoreDataSetAcceptsValidDataset(t *testing.T) {
	dataset := validDataSet()
	err := ValidateCStoreDataSet("1.2.3", "1.2.3.4", ul.AcceptedContext{AbstractSyntaxUID: "1.2.3"}, dataset)
	if err != nil {
		t.Fatalf("ValidateCStoreDataSet() error = %v", err)
	}
}

func TestSavePart10RejectsNilDataSet(t *testing.T) {
	_, err := SavePart10(t.TempDir(), nil, transfer.ExplicitVRLittleEndian)
	if err == nil || err.Error() != "missing dataset" {
		t.Fatalf("SavePart10(nil) error = %v, want missing dataset", err)
	}
}

func TestSavePart10CreatesFile(t *testing.T) {
	path, err := SavePart10(t.TempDir(), validDataSet(), transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SavePart10() error = %v", err)
	}
	if path == "" {
		t.Fatal("SavePart10() returned empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("saved Part 10 file is empty")
	}
	if _, err := object.OpenFile(path); err != nil {
		t.Fatalf("OpenFile(saved) error = %v", err)
	}
}

func TestCreateInstanceFileUsesOwnerOnlyPermissions(t *testing.T) {
	path, f, err := CreateInstanceFile(t.TempDir(), "1.2.3")
	if err != nil {
		t.Fatalf("CreateInstanceFile() error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	private, err := isPrivateInstanceFile(path)
	if err != nil {
		t.Fatalf("inspect file protection: %v", err)
	}
	if !private {
		t.Fatal("instance file does not have effective owner-only protection")
	}
}

func TestCreateInstanceFileRemovesFileWhenProtectionFails(t *testing.T) {
	dir := t.TempDir()
	protectionErr := errors.New("injected protection failure")

	path, f, err := createInstanceFile(dir, "1.2.3", func(string) error {
		return protectionErr
	})
	if !errors.Is(err, protectionErr) {
		t.Fatalf("createInstanceFile() error = %v, want protection failure", err)
	}
	if path != "" || f != nil {
		t.Fatalf("createInstanceFile() = %q, %v; want empty path and nil file", path, f)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir() error = %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("protection failure left partial files: %v", entries)
	}
}

func TestCreateInstanceFileFinalErrorDoesNotExposeRawUID(t *testing.T) {
	dir := t.TempDir()
	uid := "1.2.840.10008.999.123456"
	base := SafeFileBase(uid)
	for i := 0; i < 1000; i++ {
		name := base + ".dcm"
		if i > 0 {
			name = fmt.Sprintf("%s.%d.dcm", base, i)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("collision"), 0o600); err != nil {
			t.Fatalf("WriteFile(collision %d) error = %v", i, err)
		}
	}
	_, _, err := CreateInstanceFile(dir, uid)
	if err == nil {
		t.Fatal("CreateInstanceFile() error = nil, want collision exhaustion")
	}
	if strings.Contains(err.Error(), uid) {
		t.Fatalf("CreateInstanceFile() error exposes raw UID: %v", err)
	}
}

func TestCreateInstanceFileStillHandlesCollisions(t *testing.T) {
	dir := t.TempDir()
	path, f, err := CreateInstanceFile(dir, "1.2.3")
	if err != nil {
		t.Fatalf("CreateInstanceFile(first) error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
	secondPath, second, err := CreateInstanceFile(dir, "1.2.3")
	if err != nil {
		t.Fatalf("CreateInstanceFile(second) error = %v", err)
	}
	_ = second.Close()
	if secondPath == path {
		t.Fatal("CreateInstanceFile(second) reused existing path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(first) error = %v", err)
	}
}

func validDataSet() *object.Object {
	return object.FromElements([]core.Element{
		newUIElement(dicomtags.SOPClassUID, "1.2.3"),
		newUIElement(dicomtags.SOPInstanceUID, "1.2.3.4"),
	}, std.Dictionary)
}
