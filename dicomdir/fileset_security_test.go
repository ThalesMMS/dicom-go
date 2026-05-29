package dicomdir_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomdir"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	testFileSetUID = "1.2.826.0.1.3680043.10.543.625.1"
	testSOPClass   = "1.2.840.10008.5.1.4.1.1.2"
	testPatientPHI = "PATIENT-JANE-SECRET"
)

func TestNewFileIDEnforcesPortableDICOMComponents(t *testing.T) {
	tests := []struct {
		name       string
		components []string
	}{
		{name: "empty"},
		{name: "more than eight", components: []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"}},
		{name: "component longer than eight", components: []string{"IMAGE0001"}},
		{name: "lowercase", components: []string{"image001"}},
		{name: "punctuation", components: []string{"IMAGE.01"}},
		{name: "space", components: []string{"IMAGE 01"}},
		{name: "reserved dicomdir", components: []string{"DICOMDIR"}},
		{name: "windows nul device", components: []string{"NUL"}},
		{name: "windows con device", components: []string{"CON"}},
		{name: "windows com device", components: []string{"COM1"}},
		{name: "windows lpt device", components: []string{"LPT9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dicomdir.NewFileID(tt.components...)
			if !errors.Is(err, dicomdir.ErrInvalidFileID) {
				t.Fatalf("NewFileID(%q) error = %v, want ErrInvalidFileID", tt.components, err)
			}
		})
	}

	components := []string{"A", "B", "C", "D", "E", "F", "G", "H"}
	got, err := dicomdir.NewFileID(components...)
	if err != nil {
		t.Fatalf("NewFileID(eight portable components) error = %v", err)
	}
	if fmt.Sprint([]string(got)) != fmt.Sprint(components) {
		t.Fatalf("NewFileID() = %q, want %q", got, components)
	}
}

func TestParseFileIDRejectsHostPathsAndTraversal(t *testing.T) {
	tests := []string{
		`/PATIENT`,
		`C:\PATIENT`,
		`C:PATIENT`,
		`\\SERVER\SHARE\PATIENT`,
		`\\?\C:\PATIENT`,
		`\\.\PhysicalDrive0`,
		`..\PATIENT`,
		`SAFE\..\PATIENT`,
		`SAFE/PATIENT`,
		`\PATIENT`,
		`DICOMDIR`,
	}
	for _, encoded := range tests {
		t.Run(encoded, func(t *testing.T) {
			_, err := dicomdir.ParseFileID(encoded)
			if !errors.Is(err, dicomdir.ErrInvalidFileID) {
				t.Fatalf("ParseFileID(%q) error = %v, want ErrInvalidFileID", encoded, err)
			}
			if strings.Contains(fmt.Sprint(err), encoded) {
				t.Fatalf("ParseFileID error discloses rejected value: %v", err)
			}
		})
	}

	got, err := dicomdir.ParseFileID(`DIR00001\IMAGE001`)
	if err != nil {
		t.Fatalf("ParseFileID(portable ID) error = %v", err)
	}
	if want := []string{"DIR00001", "IMAGE001"}; fmt.Sprint([]string(got)) != fmt.Sprint(want) {
		t.Fatalf("ParseFileID() = %q, want %q", got, want)
	}
}

func TestAddRevalidatesConstructedFileID(t *testing.T) {
	root := t.TempDir()
	fs := newTestFileSet(t, root, dicomdir.Options{})
	for _, id := range []dicomdir.FileID{{"..", "SECRET"}, {"DICOMDIR"}, {"image001"}} {
		err := fs.Add(context.Background(), id)
		if !errors.Is(err, dicomdir.ErrInvalidFileID) {
			t.Fatalf("Add(%q) error = %v, want ErrInvalidFileID", id, err)
		}
		assertRedacted(t, err, root, testPatientPHI, strings.Join([]string(id), `\`))
	}
}

func TestScanRejectsCaseCollidingNonPortableNames(t *testing.T) {
	root := t.TempDir()
	upper := filepath.Join(root, "IMAGE001")
	lower := filepath.Join(root, "image001")
	writeTestDICOM(t, upper, "1.2.826.0.1.3680043.10.543.625.101")
	writeTestDICOM(t, lower, "1.2.826.0.1.3680043.10.543.625.102")
	upperInfo, upperErr := os.Stat(upper)
	lowerInfo, lowerErr := os.Stat(lower)
	if upperErr != nil || lowerErr != nil || os.SameFile(upperInfo, lowerInfo) {
		t.Skip("filesystem is case-insensitive; distinct case-colliding entries cannot be created")
	}

	fs := newTestFileSet(t, root, dicomdir.Options{})
	_, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Policy: dicomdir.EntryReject})
	if !errors.Is(err, dicomdir.ErrInvalidFileID) {
		t.Fatalf("Scan(case-colliding names) error = %v, want ErrInvalidFileID", err)
	}
	assertRedacted(t, err, root, "image001", testPatientPHI)
}

func TestFileSetRejectsRootAndSourceSymlinks(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		realRoot := t.TempDir()
		alias := filepath.Join(t.TempDir(), "MEDIA")
		if err := os.Symlink(realRoot, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := dicomdir.NewFileSet(alias, testOptions(dicomdir.Options{}))
		if err == nil {
			t.Fatal("NewFileSet(symlink root) error = nil")
		}
		assertRedacted(t, err, alias, realRoot, testPatientPHI)
	})

	t.Run("source", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "TARGET01")
		writeTestDICOM(t, target, "1.2.826.0.1.3680043.10.543.625.103")
		link := filepath.Join(root, "LINK0001")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		fs := newTestFileSet(t, root, dicomdir.Options{})
		err := fs.Add(context.Background(), mustFileID(t, "LINK0001"))
		if err == nil {
			t.Fatal("Add(symlink source) error = nil")
		}
		assertRedacted(t, err, root, target, link, testPatientPHI)
	})
}

func TestFileSetDoesNotFollowRootReplacedAfterConstruction(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "MEDIA")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		fs := newTestFileSet(t, root, dicomdir.Options{})
		originalRoot := filepath.Join(parent, "ORIGINAL")
		if err := os.Rename(root, originalRoot); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		writeTestDICOM(t, filepath.Join(outside, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.105")
		if err := os.Symlink(outside, root); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		err := fs.Add(context.Background(), mustFileID(t, "IMAGE001"))
		if err == nil {
			t.Fatal("Add() followed a replacement symlink at the file-set root")
		}
		assertRedacted(t, err, root, originalRoot, outside, testPatientPHI)
	})

	t.Run("commit", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "MEDIA")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestDICOM(t, filepath.Join(root, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.106")
		fs := newTestFileSet(t, root, dicomdir.Options{})
		if err := fs.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		originalRoot := filepath.Join(parent, "ORIGINAL")
		if err := os.Rename(root, originalRoot); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, root); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		_, err := dicomdir.CommitDICOMDIR(context.Background(), fs, dicomdir.WriteOptions{})
		if !errors.Is(err, dicomdir.ErrSourceChanged) {
			t.Fatalf("CommitDICOMDIR(replaced root) error = %v, want ErrSourceChanged", err)
		}
		assertRedacted(t, err, root, originalRoot, outside, testPatientPHI)
		if _, statErr := os.Lstat(filepath.Join(outside, "DICOMDIR")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("CommitDICOMDIR published through replacement root: %v", statErr)
		}
	})
}

func TestAddRejectsHardLinkAlias(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "IMAGE001")
	second := filepath.Join(root, "IMAGE002")
	writeTestDICOM(t, first, "1.2.826.0.1.3680043.10.543.625.104")
	if err := os.Link(first, second); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	fs := newTestFileSet(t, root, dicomdir.Options{})
	if err := fs.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add(first hard link) error = %v", err)
	}
	// A metadata comparison alone must not be enough: changing the shared inode
	// gives the alias a different SOP Instance UID while it remains the same
	// physical file already registered under IMAGE001.
	rewriteTestDICOM(t, second, "1.2.826.0.1.3680043.10.543.625.107")
	err := fs.Add(context.Background(), mustFileID(t, "IMAGE002"))
	if !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("Add(second hard link) error = %v, want ErrInvalidRecord", err)
	}
	assertRedacted(t, err, root, first, second, testPatientPHI)
}

func TestScanLimitsAreExactAndFailOnPlusOne(t *testing.T) {
	t.Run("files", func(t *testing.T) {
		root := t.TempDir()
		writeTestDICOM(t, filepath.Join(root, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.201")
		writeTestDICOM(t, filepath.Join(root, "IMAGE002"), "1.2.826.0.1.3680043.10.543.625.202")
		fs := newTestFileSet(t, root, dicomdir.Options{})
		if _, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxFiles: 2}}); err != nil {
			t.Fatalf("Scan(MaxFiles exact) error = %v", err)
		}
		writeTestDICOM(t, filepath.Join(root, "IMAGE003"), "1.2.826.0.1.3680043.10.543.625.203")
		fs = newTestFileSet(t, root, dicomdir.Options{})
		_, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxFiles: 2}})
		assertLimitError(t, err, root)
	})

	t.Run("directories", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "DIR00001"), 0o700); err != nil {
			t.Fatal(err)
		}
		fs := newTestFileSet(t, root, dicomdir.Options{})
		if _, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxDirectories: 2}}); err != nil {
			t.Fatalf("Scan(MaxDirectories exact) error = %v", err)
		}
		if err := os.Mkdir(filepath.Join(root, "DIR00002"), 0o700); err != nil {
			t.Fatal(err)
		}
		fs = newTestFileSet(t, root, dicomdir.Options{})
		_, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxDirectories: 2}})
		assertLimitError(t, err, root)
	})

	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		levelOne := filepath.Join(root, "DIR00001")
		if err := os.Mkdir(levelOne, 0o700); err != nil {
			t.Fatal(err)
		}
		fs := newTestFileSet(t, root, dicomdir.Options{})
		if _, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxDepth: 1}}); err != nil {
			t.Fatalf("Scan(MaxDepth exact) error = %v", err)
		}
		if err := os.Mkdir(filepath.Join(levelOne, "DIR00002"), 0o700); err != nil {
			t.Fatal(err)
		}
		fs = newTestFileSet(t, root, dicomdir.Options{})
		_, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxDepth: 1}})
		assertLimitError(t, err, root)
	})

	t.Run("path bytes", func(t *testing.T) {
		root := t.TempDir()
		exactPath := filepath.Join(root, "IMAGE01")
		writeTestDICOM(t, exactPath, "1.2.826.0.1.3680043.10.543.625.204")
		exact := len([]byte("IMAGE01"))
		fs := newTestFileSet(t, root, dicomdir.Options{})
		if _, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxPathBytes: exact}}); err != nil {
			t.Fatalf("Scan(MaxPathBytes exact) error = %v", err)
		}
		fs = newTestFileSet(t, root, dicomdir.Options{})
		_, err := fs.Scan(context.Background(), dicomdir.ScanOptions{Limits: dicomdir.Limits{MaxPathBytes: exact - 1}})
		assertLimitError(t, err, root)
	})
}

func TestScanMaxDiagnosticsIsExactAndDoesNotSilentlyTruncate(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"BROKEN01", "BROKEN02"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(testPatientPHI), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fs := newTestFileSet(t, root, dicomdir.Options{})
	report, err := fs.Scan(context.Background(), dicomdir.ScanOptions{
		Policy: dicomdir.EntrySkip,
		Limits: dicomdir.Limits{MaxDiagnostics: 2},
	})
	if err != nil {
		t.Fatalf("Scan(MaxDiagnostics exact) error = %v", err)
	}
	if len(report.Diagnostics) != 2 {
		t.Fatalf("Scan(MaxDiagnostics exact) diagnostics = %d, want 2", len(report.Diagnostics))
	}
	for _, diagnostic := range report.Diagnostics {
		assertRedacted(t, errors.New(diagnostic.Message), root, testPatientPHI, "BROKEN01", "BROKEN02")
	}

	if err := os.WriteFile(filepath.Join(root, "BROKEN03"), []byte(testPatientPHI), 0o600); err != nil {
		t.Fatal(err)
	}
	fs = newTestFileSet(t, root, dicomdir.Options{})
	report, err = fs.Scan(context.Background(), dicomdir.ScanOptions{
		Policy: dicomdir.EntrySkip,
		Limits: dicomdir.Limits{MaxDiagnostics: 2},
	})
	assertLimitError(t, err, root, testPatientPHI, "BROKEN01", "BROKEN02", "BROKEN03")
	if len(report.Diagnostics) > 2 {
		t.Fatalf("Scan(MaxDiagnostics +1) retained %d diagnostics, want at most 2", len(report.Diagnostics))
	}
}

func TestCommitDICOMDIRDetectsSourceIdentityAndMetadataChanges(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "IMAGE001")
		original := writeTestDICOM(t, path, "1.2.826.0.1.3680043.10.543.625.301")
		fs := newTestFileSet(t, root, dicomdir.Options{})
		if err := fs.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		backup := filepath.Join(root, "BACKUP01")
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		oldInfo, oldErr := os.Stat(backup)
		newInfo, newErr := os.Stat(path)
		if oldErr != nil || newErr != nil || os.SameFile(oldInfo, newInfo) {
			t.Skip("filesystem did not create a distinct replacement identity")
		}

		_, err := dicomdir.CommitDICOMDIR(context.Background(), fs, dicomdir.WriteOptions{})
		if !errors.Is(err, dicomdir.ErrSourceChanged) {
			t.Fatalf("CommitDICOMDIR(replaced identity) error = %v, want ErrSourceChanged", err)
		}
		assertRedacted(t, err, root, path, testPatientPHI, "1.2.826.0.1.3680043.10.543.625.301")
		assertNoTemporaryFiles(t, root, "IMAGE001", "BACKUP01")
	})

	t.Run("metadata", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "IMAGE001")
		writeTestDICOM(t, path, "1.2.826.0.1.3680043.10.543.625.302")
		fs := newTestFileSet(t, root, dicomdir.Options{})
		if err := fs.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		rewriteTestDICOM(t, path, "1.2.826.0.1.3680043.10.543.625.303")
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Skip("filesystem replaced rather than rewrote the source identity")
		}

		_, err = dicomdir.CommitDICOMDIR(context.Background(), fs, dicomdir.WriteOptions{})
		if !errors.Is(err, dicomdir.ErrSourceChanged) {
			t.Fatalf("CommitDICOMDIR(changed metadata) error = %v, want ErrSourceChanged", err)
		}
		assertRedacted(t, err, root, path, testPatientPHI, "1.2.826.0.1.3680043.10.543.625.303")
		assertNoTemporaryFiles(t, root, "IMAGE001")
	})
}

func TestCommitDICOMDIRDoesNotFollowDestinationSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "IMAGE001")
	writeTestDICOM(t, source, "1.2.826.0.1.3680043.10.543.625.401")
	fs := newTestFileSet(t, root, dicomdir.Options{})
	if err := fs.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	outside := filepath.Join(t.TempDir(), "PATIENT-JANE-SECRET")
	wantOutside := []byte("do not overwrite")
	if err := os.WriteFile(outside, wantOutside, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "DICOMDIR")
	if err := os.Symlink(outside, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := dicomdir.CommitDICOMDIR(context.Background(), fs, dicomdir.WriteOptions{})
	if err == nil {
		t.Fatal("CommitDICOMDIR(destination symlink) error = nil")
	}
	assertRedacted(t, err, root, outside, destination, testPatientPHI)
	gotOutside, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(gotOutside, wantOutside) {
		t.Fatalf("destination symlink target changed: got %q, want %q", gotOutside, wantOutside)
	}
	info, lstatErr := os.Lstat(destination)
	if lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("destination symlink was replaced: info=%v err=%v", info, lstatErr)
	}
	assertNoTemporaryFiles(t, root, "IMAGE001", "DICOMDIR")
}

func TestCommitDICOMDIRNeverReopensReplacedSourceThroughSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "IMAGE001")
	writeTestDICOM(t, path, "1.2.826.0.1.3680043.10.543.625.402")
	fs := newTestFileSet(t, root, dicomdir.Options{})
	if err := fs.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := os.Rename(path, filepath.Join(root, "BACKUP01")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "SECRET01")
	wantOutside := []byte(testPatientPHI)
	if err := os.WriteFile(outside, wantOutside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := dicomdir.CommitDICOMDIR(context.Background(), fs, dicomdir.WriteOptions{})
	if !errors.Is(err, dicomdir.ErrSourceChanged) {
		t.Fatalf("CommitDICOMDIR(replaced source symlink) error = %v, want ErrSourceChanged", err)
	}
	assertRedacted(t, err, root, outside, path, testPatientPHI)
	gotOutside, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(gotOutside, wantOutside) {
		t.Fatalf("replaced source symlink target changed: got %q, want %q", gotOutside, wantOutside)
	}
	assertNoTemporaryFiles(t, root, "IMAGE001", "BACKUP01")
}

func TestCommitDICOMDIRPublishesAtomicallyWithoutMutatingSources(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "IMAGE001")
	wantSource := writeTestDICOM(t, path, "1.2.826.0.1.3680043.10.543.625.501")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fs := newTestFileSet(t, root, dicomdir.Options{})
	if err := fs.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := dicomdir.CommitDICOMDIR(context.Background(), fs, dicomdir.WriteOptions{}); err != nil {
		t.Fatalf("CommitDICOMDIR() error = %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("CommitDICOMDIR replaced the source file identity")
	}
	gotSource, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSource, wantSource) {
		t.Fatal("CommitDICOMDIR modified source bytes")
	}
	destinationInfo, err := os.Lstat(filepath.Join(root, "DICOMDIR"))
	if err != nil {
		t.Fatalf("DICOMDIR was not published: %v", err)
	}
	if !destinationInfo.Mode().IsRegular() || destinationInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("published DICOMDIR mode = %v, want regular file", destinationInfo.Mode())
	}
	if runtime.GOOS != "windows" && destinationInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("published DICOMDIR permissions = %o, want no group/world access", destinationInfo.Mode().Perm())
	}
	assertNoTemporaryFiles(t, root, "IMAGE001", "DICOMDIR")
}

func TestAddMalformedFileErrorIsPathAndPHIFree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "PATIENT-JANE-SECRET")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "SECRET01")
	if err := os.WriteFile(path, []byte(testPatientPHI), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := newTestFileSet(t, root, dicomdir.Options{})
	err := fs.Add(context.Background(), mustFileID(t, "SECRET01"))
	if err == nil {
		t.Fatal("Add(malformed file) error = nil")
	}
	assertRedacted(t, err, root, path, "SECRET01", testPatientPHI)
}

func newTestFileSet(t *testing.T, root string, overrides dicomdir.Options) *dicomdir.FileSet {
	t.Helper()
	fs, err := dicomdir.NewFileSet(root, testOptions(overrides))
	if err != nil {
		t.Fatalf("NewFileSet() error = %v", err)
	}
	return fs
}

func testOptions(options dicomdir.Options) dicomdir.Options {
	if options.FileSetUID == "" {
		options.FileSetUID = testFileSetUID
	}
	if options.FileSetID == "" {
		options.FileSetID = "SECURE625"
	}
	return options
}

func mustFileID(t *testing.T, components ...string) dicomdir.FileID {
	t.Helper()
	id, err := dicomdir.NewFileID(components...)
	if err != nil {
		t.Fatalf("NewFileID(%q) error = %v", components, err)
	}
	return id
}

func writeTestDICOM(t *testing.T, path, sopInstanceUID string) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, testDICOMFile(sopInstanceUID)); err != nil {
		t.Fatalf("encode test DICOM: %v", err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write test DICOM: %v", err)
	}
	return bytes.Clone(encoded.Bytes())
}

func rewriteTestDICOM(t *testing.T, path, sopInstanceUID string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := object.WriteFile(file, testDICOMFile(sopInstanceUID)); err != nil {
		_ = file.Close()
		t.Fatalf("rewrite test DICOM: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func testDICOMFile(sopInstanceUID string) *object.File {
	dataset := object.FromElements([]core.Element{
		testStringElement(core.NewTag(0x0008, 0x0016), core.VRUI, testSOPClass),
		testStringElement(core.NewTag(0x0008, 0x0018), core.VRUI, sopInstanceUID),
		testStringElement(core.NewTag(0x0008, 0x0020), core.VRDA, "20260808"),
		testStringElement(core.NewTag(0x0008, 0x0030), core.VRTM, "120000"),
		testStringElement(core.NewTag(0x0008, 0x0050), core.VRSH, "ACCESS1"),
		testStringElement(core.NewTag(0x0008, 0x0060), core.VRCS, "CT"),
		testStringElement(core.NewTag(0x0010, 0x0010), core.VRPN, testPatientPHI),
		testStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "PATIENT1"),
		testStringElement(core.NewTag(0x0020, 0x000D), core.VRUI, "1.2.826.0.1.3680043.10.543.625.10"),
		testStringElement(core.NewTag(0x0020, 0x000E), core.VRUI, "1.2.826.0.1.3680043.10.543.625.20"),
		testStringElement(core.NewTag(0x0020, 0x0010), core.VRSH, "STUDY1"),
		testStringElement(core.NewTag(0x0020, 0x0011), core.VRIS, "1"),
		testStringElement(core.NewTag(0x0020, 0x0013), core.VRIS, "1"),
	}, std.Dictionary)
	return &object.File{Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}
}

func testStringElement(tag core.Tag, vr core.VR, value string) core.Element {
	raw := []byte(value)
	if len(raw)%2 != 0 {
		padding := byte(' ')
		if vr == core.VRUI {
			padding = 0
		}
		raw = append(raw, padding)
	}
	return core.NewRawElement(tag, vr, raw)
}

func assertLimitError(t *testing.T, err error, canaries ...string) {
	t.Helper()
	if !errors.Is(err, dicomdir.ErrResourceLimit) {
		t.Fatalf("error = %v, want ErrResourceLimit", err)
	}
	assertRedacted(t, err, canaries...)
}

func assertRedacted(t *testing.T, err error, canaries ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want redacted error")
	}
	var inspect func(error, int)
	inspect = func(current error, depth int) {
		if current == nil {
			return
		}
		if depth > 32 {
			t.Fatal("error unwrap chain exceeds safety budget")
		}
		for _, message := range []string{current.Error(), fmt.Sprintf("%+v", current)} {
			for _, canary := range canaries {
				if canary != "" && strings.Contains(message, canary) {
					t.Fatalf("error %q contains sensitive value %q", message, canary)
				}
			}
		}
		switch unwrapped := current.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range unwrapped.Unwrap() {
				inspect(child, depth+1)
			}
		case interface{ Unwrap() error }:
			inspect(unwrapped.Unwrap(), depth+1)
		}
	}
	inspect(err, 0)
}

func assertNoTemporaryFiles(t *testing.T, root string, allowed ...string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	sort.Strings(allowed)
	if fmt.Sprint(got) != fmt.Sprint(allowed) {
		t.Fatalf("directory entries after commit = %q, want %q (no temporary files)", got, allowed)
	}
}
