//go:build windows

package pixeldata

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenTranscodeFileAtReportsMissingEntry(t *testing.T) {
	parent, err := openTranscodeDirectoryNoFollow(t.TempDir())
	if err != nil {
		t.Fatalf("open destination directory: %v", err)
	}
	defer parent.Close()

	file, err := openTranscodeFileAt(parent, "missing.dcm")
	if file != nil {
		file.Close()
		t.Fatal("openTranscodeFileAt returned a handle for a missing entry")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing entry error = %T %v, want os.ErrNotExist", err, err)
	}
}

func TestReplaceTranscodedFileAtAtomicallyReplacesDestination(t *testing.T) {
	fixture := newTranscodeWindowsReplaceFixture(t)
	if err := replaceTranscodedFileAt(fixture.parent, fixture.temporary, fixture.temporaryName, fixture.destinationName, fixture.previous); err != nil {
		t.Fatalf("replaceTranscodedFileAt() error = %v", err)
	}
	assertTranscodeWindowsFileBytes(t, fixture.destinationPath, []byte("NEW"))
	if _, err := os.Stat(fixture.temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary stat error = %v, want not exist", err)
	}
}

func TestReplaceTranscodedWindowsFileAtFailurePreservesValidCopy(t *testing.T) {
	injected := errors.New("injected replace failure")
	tests := []struct {
		name      string
		context   string
		published bool
		wantErr   error
		mutate    func(*transcodeWindowsReplaceOperations)
	}{
		{
			name:    "inspect temporary",
			context: "inspect publish temporary",
			wantErr: injected,
			mutate: func(operations *transcodeWindowsReplaceOperations) {
				operations.inspectTemporary = func(*os.File) (os.FileInfo, error) { return nil, injected }
			},
		},
		{
			name:    "atomic rename",
			context: "atomically publish destination",
			wantErr: injected,
			mutate: func(operations *transcodeWindowsReplaceOperations) {
				operations.renameTemporary = func(*os.File, *os.File, string, bool) error { return injected }
			},
		},
		{
			name:      "verify destination",
			context:   "verify published destination",
			published: true,
			wantErr:   ErrTranscodeDestinationUnsafe,
			mutate: func(operations *transcodeWindowsReplaceOperations) {
				operations.destinationMatches = func(*os.File, string, os.FileInfo) bool { return false }
			},
		},
		{
			name:      "close destination",
			context:   "close published destination",
			published: true,
			wantErr:   injected,
			mutate: func(operations *transcodeWindowsReplaceOperations) {
				operations.closeTemporary = func(*os.File) error { return injected }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTranscodeWindowsReplaceFixture(t)
			operations := defaultTranscodeWindowsReplaceOperations()
			tt.mutate(&operations)

			err := replaceTranscodedWindowsFileAt(fixture.parent, fixture.temporary, fixture.destinationName, fixture.previous, operations)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("replaceTranscodedWindowsFileAt() error = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.context) {
				t.Fatalf("replace error = %q, want context %q", err, tt.context)
			}
			_ = fixture.temporary.Close()
			if tt.published {
				assertTranscodeWindowsFileBytes(t, fixture.destinationPath, []byte("NEW"))
				if _, statErr := os.Stat(fixture.temporaryPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("temporary stat error = %v, want not exist", statErr)
				}
				return
			}
			assertTranscodeWindowsFileBytes(t, fixture.destinationPath, []byte("OLD"))
			assertTranscodeWindowsFileBytes(t, fixture.temporaryPath, []byte("NEW"))
		})
	}
}

type transcodeWindowsReplaceFixture struct {
	parent          *os.File
	temporary       *os.File
	temporaryName   string
	temporaryPath   string
	destinationName string
	destinationPath string
	previous        transcodeFileSnapshot
}

func newTranscodeWindowsReplaceFixture(t *testing.T) transcodeWindowsReplaceFixture {
	t.Helper()
	dir := t.TempDir()
	parent, err := openTranscodeDirectoryNoFollow(dir)
	if err != nil {
		t.Fatalf("open destination directory: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	destinationName := "destination.dcm"
	destinationPath := filepath.Join(dir, destinationName)
	if err := os.WriteFile(destinationPath, []byte("OLD"), 0o600); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	destinationInfo, err := os.Stat(destinationPath)
	if err != nil {
		t.Fatalf("stat destination: %v", err)
	}
	temporaryName := ".dicom-transcode-test"
	temporary, err := createTranscodeFileAt(parent, temporaryName)
	if err != nil {
		t.Fatalf("create temporary: %v", err)
	}
	t.Cleanup(func() { _ = temporary.Close() })
	if _, err := temporary.Write([]byte("NEW")); err != nil {
		t.Fatalf("write temporary: %v", err)
	}
	if err := temporary.Sync(); err != nil {
		t.Fatalf("sync temporary: %v", err)
	}
	return transcodeWindowsReplaceFixture{
		parent:          parent,
		temporary:       temporary,
		temporaryName:   temporaryName,
		temporaryPath:   filepath.Join(dir, temporaryName),
		destinationName: destinationName,
		destinationPath: destinationPath,
		previous:        transcodeFileSnapshot{exists: true, info: destinationInfo},
	}
}

func assertTranscodeWindowsFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s bytes = %q, want %q", filepath.Base(path), got, want)
	}
}
