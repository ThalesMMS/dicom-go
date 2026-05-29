package nifti

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestSourceFuncReopensAndForwardsOptions(t *testing.T) {
	var openCalls int
	var closeCalls int
	source := SourceFunc{
		Count: 1,
		OpenFile: func(_ context.Context, index int, opts object.ReadFileOptions) (OpenedFile, error) {
			openCalls++
			if index != 0 {
				t.Fatalf("index = %d, want 0", index)
			}
			if opts.MaxElements != 17 {
				t.Fatalf("MaxElements = %d, want 17", opts.MaxElements)
			}
			return OpenedFile{
				File: &object.File{},
				CloseFunc: func() error {
					closeCalls++
					return nil
				},
			}, nil
		},
	}
	var _ Source = source

	if got := source.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
	first, err := source.Open(context.Background(), 0, object.ReadFileOptions{MaxElements: 17})
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Open(context.Background(), 0, object.ReadFileOptions{MaxElements: 17})
	if err != nil {
		t.Fatal(err)
	}
	if first.File == second.File {
		t.Fatal("replayed Open returned the same file pointer")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if openCalls != 2 || closeCalls != 2 {
		t.Fatalf("open calls = %d, close calls = %d; want 2, 2", openCalls, closeCalls)
	}
}

func TestFilesSourceBorrowsFiles(t *testing.T) {
	path := writeSourceFixture(t)
	file, err := object.OpenFileWithOptions(path, object.ReadFileOptions{DeferPixelData: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	files := []*object.File{file}
	source := NewFilesSource(files)
	files[0] = nil
	if got := source.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
	opened, err := source.Open(context.Background(), 0, object.ReadFileOptions{MaxElements: 1})
	if err != nil {
		t.Fatal(err)
	}
	if opened.File != file {
		t.Fatal("Open returned a different file pointer")
	}
	if opened.CloseFunc != nil {
		t.Fatal("borrowed file has an owning CloseFunc")
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}

	var pixels bytes.Buffer
	written, err := file.Dataset.CopyValueTo(core.TagPixelData, &pixels)
	if err != nil {
		t.Fatalf("borrowed file was closed: %v", err)
	}
	if written != 64 || pixels.Len() != 64 {
		t.Fatalf("copied Pixel Data = %d/%d bytes, want 64/64", written, pixels.Len())
	}
}

func writeSourceFixture(t *testing.T) string {
	t.Helper()
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPathSourceReopensAndHonorsReadOptions(t *testing.T) {
	path := writeSourceFixture(t)
	paths := []string{path}
	source := NewPathSource(paths)
	paths[0] = filepath.Join(t.TempDir(), "replacement.dcm")
	if got := source.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}

	var frames []object.Frame
	streamed, err := source.Open(context.Background(), 0, object.ReadFileOptions{
		FrameSink: object.FrameSinkFunc(func(frame object.Frame) error {
			frames = append(frames, frame)
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || len(frames[0].Data) != 64 {
		t.Fatalf("streamed frames = %d with %d bytes, want 1 with 64 bytes", len(frames), frameBytes(frames))
	}
	if _, ok := streamed.File.Dataset.GetRaw(core.TagPixelData); ok {
		t.Fatal("FrameSink open materialized Pixel Data")
	}
	if err := streamed.Close(); err != nil {
		t.Fatal(err)
	}

	deferred, err := source.Open(context.Background(), 0, object.ReadFileOptions{DeferPixelData: true})
	if err != nil {
		t.Fatal(err)
	}
	if deferred.CloseFunc == nil {
		t.Fatal("path-backed file has no owning CloseFunc")
	}
	var pixels bytes.Buffer
	if written, err := deferred.File.Dataset.CopyValueTo(core.TagPixelData, &pixels); err != nil || written != 64 {
		t.Fatalf("CopyValueTo() = %d, %v; want 64, nil", written, err)
	}
	if err := deferred.Close(); err != nil {
		t.Fatal(err)
	}

	replayed, err := source.Open(context.Background(), 0, object.ReadFileOptions{DeferPixelData: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replayed.Close() }()
	pixels.Reset()
	if written, err := replayed.File.Dataset.CopyValueTo(core.TagPixelData, &pixels); err != nil || written != 64 {
		t.Fatalf("replayed CopyValueTo() = %d, %v; want 64, nil", written, err)
	}
}

func TestSourcesValidateRequests(t *testing.T) {
	path := writeSourceFixture(t)
	sources := map[string]Source{
		"files": NewFilesSource([]*object.File{{}}),
		"paths": NewPathSource([]string{path}),
	}
	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			for _, index := range []int{-1, 1} {
				if _, err := source.Open(context.Background(), index, object.ReadFileOptions{}); err == nil {
					t.Fatalf("Open(index=%d) error = nil", index)
				}
			}
			if _, err := source.Open(nil, 0, object.ReadFileOptions{}); err == nil {
				t.Fatal("Open(nil context) error = nil")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := source.Open(ctx, 0, object.ReadFileOptions{}); !errors.Is(err, context.Canceled) {
				t.Fatalf("Open(canceled context) error = %v, want context.Canceled", err)
			}
		})
	}

	if _, err := NewFilesSource([]*object.File{nil}).Open(context.Background(), 0, object.ReadFileOptions{}); err == nil {
		t.Fatal("Open(nil file) error = nil")
	}
	if _, err := NewPathSource([]string{""}).Open(context.Background(), 0, object.ReadFileOptions{}); err == nil {
		t.Fatal("Open(empty path) error = nil")
	}
}

func TestPathSourceRedactsOpenErrors(t *testing.T) {
	directory := t.TempDir()
	tests := []struct {
		name         string
		filename     string
		contents     []byte
		wantNotExist bool
	}{
		{name: "missing", filename: "SENSITIVE-SOURCE-12345.dcm", wantNotExist: true},
		{name: "malformed", filename: "SENSITIVE-SOURCE-67890.dcm", contents: []byte("not a Part 10 file")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secretPath := filepath.Join(directory, tt.filename)
			if tt.contents != nil {
				if err := os.WriteFile(secretPath, tt.contents, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := NewPathSource([]string{secretPath}).Open(context.Background(), 0, object.ReadFileOptions{})
			if err == nil {
				t.Fatal("Open(sensitive path) error = nil")
			}
			if tt.wantNotExist && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Open(missing path) error = %v, want fs.ErrNotExist classification", err)
			}
			if message := err.Error(); strings.Contains(message, secretPath) || strings.Contains(message, tt.filename) || strings.Contains(message, "SENSITIVE") {
				t.Fatalf("Open error leaked source path: %q", message)
			}
		})
	}
}

func TestOpenedFileCloseUsesExplicitOwnership(t *testing.T) {
	if err := (OpenedFile{File: &object.File{}}).Close(); err != nil {
		t.Fatalf("borrowed Close() error = %v", err)
	}
	want := errors.New("close failed")
	owned := OpenedFile{File: &object.File{}, CloseFunc: func() error { return want }}
	if err := owned.Close(); !errors.Is(err, want) {
		t.Fatalf("owned Close() error = %v, want %v", err, want)
	}
}

func frameBytes(frames []object.Frame) int {
	if len(frames) == 0 {
		return 0
	}
	return len(frames[0].Data)
}

func TestSourceFuncRejectsInvalidOpen(t *testing.T) {
	var calls int
	source := SourceFunc{
		Count: 1,
		OpenFile: func(context.Context, int, object.ReadFileOptions) (OpenedFile, error) {
			calls++
			return OpenedFile{File: &object.File{}}, nil
		},
	}

	for _, index := range []int{-1, 1} {
		if _, err := source.Open(context.Background(), index, object.ReadFileOptions{}); err == nil {
			t.Fatalf("Open(index=%d) error = nil", index)
		}
	}
	if _, err := source.Open(nil, 0, object.ReadFileOptions{}); err == nil {
		t.Fatal("Open(nil context) error = nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Open(ctx, 0, object.ReadFileOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open(canceled context) error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("opener called %d times for invalid requests", calls)
	}

	if _, err := (SourceFunc{Count: 1}).Open(context.Background(), 0, object.ReadFileOptions{}); err == nil {
		t.Fatal("Open with nil OpenFile error = nil")
	}

	var closed bool
	nilFileSource := SourceFunc{
		Count: 1,
		OpenFile: func(context.Context, int, object.ReadFileOptions) (OpenedFile, error) {
			return OpenedFile{CloseFunc: func() error {
				closed = true
				return nil
			}}, nil
		},
	}
	if _, err := nilFileSource.Open(context.Background(), 0, object.ReadFileOptions{}); err == nil {
		t.Fatal("Open returning nil file error = nil")
	}
	if !closed {
		t.Fatal("invalid opened file was not closed")
	}

	if got := (SourceFunc{Count: -1}).Len(); got != 0 {
		t.Fatalf("Len() for negative count = %d, want 0", got)
	}
}

func TestSourceFuncClosesFileWhenContextIsCanceledByOpener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var closed bool
	source := SourceFunc{
		Count: 1,
		OpenFile: func(context.Context, int, object.ReadFileOptions) (OpenedFile, error) {
			cancel()
			return OpenedFile{
				File: &object.File{},
				CloseFunc: func() error {
					closed = true
					return nil
				},
			}, nil
		},
	}
	if _, err := source.Open(ctx, 0, object.ReadFileOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context.Canceled", err)
	}
	if !closed {
		t.Fatal("file opened during cancellation was not closed")
	}
}
