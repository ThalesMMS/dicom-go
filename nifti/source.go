// Package nifti provides volume-aware NIfTI export from DICOM sources.
package nifti

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
)

var errInvalidSource = errors.New("nifti: invalid source")
var errSourceOpen = errors.New("nifti: source open failed")

// OpenedFile pairs a DICOM file with the operation that releases the source
// resources owned by the opener. A nil CloseFunc means the file is borrowed.
type OpenedFile struct {
	File      *object.File
	CloseFunc func() error
}

// Close releases source resources owned by the opener. It is a no-op for a
// borrowed file.
func (f OpenedFile) Close() error {
	if f.CloseFunc == nil {
		return nil
	}
	return f.CloseFunc()
}

// Source is a replayable collection of DICOM files. Open may be called more
// than once for the same index. Sources that parse or reopen a file must honor
// the supplied read options; adapters over already-parsed files must document
// that the options are ignored. The caller must close every successful result.
type Source interface {
	Len() int
	Open(context.Context, int, object.ReadFileOptions) (OpenedFile, error)
}

// SourceFunc adapts a caller-provided file opener to Source.
type SourceFunc struct {
	Count    int
	OpenFile func(context.Context, int, object.ReadFileOptions) (OpenedFile, error)
}

type filesSource struct {
	files []*object.File
}

type pathSource struct {
	paths []string
}

// NewFilesSource creates a source over caller-owned, already-open DICOM files.
// Open returns the original pointers and ignores read options because parsing has
// already completed. Closing an OpenedFile from this source is always a no-op.
func NewFilesSource(files []*object.File) Source {
	return &filesSource{files: append([]*object.File(nil), files...)}
}

func (s *filesSource) Len() int {
	if s == nil {
		return 0
	}
	return len(s.files)
}

func (s *filesSource) Open(ctx context.Context, index int, _ object.ReadFileOptions) (OpenedFile, error) {
	if err := validateOpenRequest(ctx, s.Len(), index); err != nil {
		return OpenedFile{}, err
	}
	file := s.files[index]
	if file == nil {
		return OpenedFile{}, fmt.Errorf("%w: file %d is nil", errInvalidSource, index)
	}
	return OpenedFile{File: file}, nil
}

// NewPathSource creates a replayable source over local Part 10 files. Paths are
// copied on construction and never included in errors returned by Open.
func NewPathSource(paths []string) Source {
	return &pathSource{paths: append([]string(nil), paths...)}
}

func (s *pathSource) Len() int {
	if s == nil {
		return 0
	}
	return len(s.paths)
}

func (s *pathSource) Open(ctx context.Context, index int, opts object.ReadFileOptions) (OpenedFile, error) {
	if err := validateOpenRequest(ctx, s.Len(), index); err != nil {
		return OpenedFile{}, err
	}
	path := s.paths[index]
	if strings.TrimSpace(path) == "" {
		return OpenedFile{}, fmt.Errorf("%w: empty path at index %d", errInvalidSource, index)
	}
	file, err := object.OpenFileWithOptions(path, opts)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return OpenedFile{}, ctxErr
		}
		return OpenedFile{}, redactedOpenError(index, err)
	}
	opened := OpenedFile{File: file, CloseFunc: file.Close}
	if err := ctx.Err(); err != nil {
		return OpenedFile{}, closeAfterError(opened, err)
	}
	return opened, nil
}

func redactedOpenError(index int, err error) error {
	cause := errSourceOpen
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cause = fs.ErrNotExist
	case errors.Is(err, fs.ErrPermission):
		cause = fs.ErrPermission
	case errors.Is(err, parser.ErrMaxTotalBytesExceeded):
		cause = ErrLimitExceeded
	}
	return fmt.Errorf("nifti: open source index %d: %w", index, cause)
}

// Len reports the number of source files.
func (s SourceFunc) Len() int {
	if s.Count < 0 {
		return 0
	}
	return s.Count
}

// Open reopens the indexed source file.
func (s SourceFunc) Open(ctx context.Context, index int, opts object.ReadFileOptions) (OpenedFile, error) {
	if err := validateOpenRequest(ctx, s.Len(), index); err != nil {
		return OpenedFile{}, err
	}
	if s.OpenFile == nil {
		return OpenedFile{}, fmt.Errorf("%w: nil opener", errInvalidSource)
	}
	opened, err := s.OpenFile(ctx, index, opts)
	if err != nil {
		return OpenedFile{}, closeAfterError(opened, err)
	}
	if err := ctx.Err(); err != nil {
		return OpenedFile{}, closeAfterError(opened, err)
	}
	if opened.File == nil {
		return OpenedFile{}, closeAfterError(opened, fmt.Errorf("%w: opener returned nil file", errInvalidSource))
	}
	return opened, nil
}

func validateOpenRequest(ctx context.Context, count, index int) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", errInvalidSource)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if index < 0 || index >= count {
		return fmt.Errorf("%w: index %d outside [0,%d)", errInvalidSource, index, count)
	}
	return nil
}

func closeAfterError(opened OpenedFile, err error) error {
	if closeErr := opened.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}
