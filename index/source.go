package index

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

// Source opens one replayable seekable byte source. Read always calls Close;
// adapters for borrowed readers install a no-op Close.
type Source interface {
	Open(context.Context) (OpenedSource, error)
}

type SourceFunc func(context.Context) (OpenedSource, error)

func (f SourceFunc) Open(ctx context.Context) (OpenedSource, error) {
	if f == nil {
		return OpenedSource{}, fmt.Errorf("%w: nil source", ErrInvalidOptions)
	}
	return f(ctx)
}

type OpenedSource struct {
	Reader io.ReadSeeker
	Info   SourceInfo
	Close  func() error
}

type pathSource string

func PathSource(path string) Source { return pathSource(path) }

func (p pathSource) Open(ctx context.Context) (OpenedSource, error) {
	if err := contextError(ctx); err != nil {
		return OpenedSource{}, err
	}
	file, err := os.Open(string(p))
	if err != nil {
		return OpenedSource{}, &sourceError{err: classifyExternalError(err, ErrRead)}
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if statErr == nil {
			statErr = errors.New("source is not a regular file")
		}
		return OpenedSource{}, &sourceError{err: classifyExternalError(statErr, ErrRead)}
	}
	return OpenedSource{
		Reader: file,
		Info:   SourceInfo{Origin: string(p), Size: info.Size()},
		Close: func() error {
			if err := file.Close(); err != nil {
				return classifyExternalError(err, ErrClose)
			}
			return nil
		},
	}, nil
}

func ReaderAtSource(origin string, reader io.ReaderAt, size int64) Source {
	return SourceFunc(func(ctx context.Context) (OpenedSource, error) {
		if err := contextError(ctx); err != nil {
			return OpenedSource{}, err
		}
		if reader == nil || size < 0 {
			return OpenedSource{}, fmt.Errorf("%w: invalid ReaderAt source", ErrInvalidOptions)
		}
		return OpenedSource{
			Reader: io.NewSectionReader(reader, 0, size),
			Info:   SourceInfo{Origin: origin, Size: size},
			Close:  func() error { return nil },
		}, nil
	})
}

func ReadSeekerSource(origin string, reader io.ReadSeeker, size int64) Source {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	source := &readSeekerSource{origin: origin, reader: reader, size: size, gate: gate}
	if reader != nil && size >= 0 {
		source.start, source.initErr = reader.Seek(0, io.SeekCurrent)
		source.initialized = source.initErr == nil
	}
	return source
}

type readSeekerSource struct {
	origin      string
	reader      io.ReadSeeker
	size        int64
	gate        chan struct{}
	initialized bool
	start       int64
	initErr     error
}

func (s *readSeekerSource) Open(ctx context.Context) (OpenedSource, error) {
	if s == nil || s.reader == nil || s.size < 0 {
		return OpenedSource{}, fmt.Errorf("%w: invalid ReadSeeker source", ErrInvalidOptions)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return OpenedSource{}, ctx.Err()
	case <-s.gate:
	}
	release := func() { s.gate <- struct{}{} }
	if s.initErr != nil {
		release()
		return OpenedSource{}, &sourceError{err: classifyExternalError(s.initErr, ErrRead)}
	}
	if !s.initialized {
		start, err := s.reader.Seek(0, io.SeekCurrent)
		if err != nil {
			release()
			return OpenedSource{}, &sourceError{err: classifyExternalError(err, ErrRead)}
		}
		s.start = start
		s.initialized = true
	}
	if _, err := s.reader.Seek(s.start, io.SeekStart); err != nil {
		release()
		return OpenedSource{}, &sourceError{err: classifyExternalError(err, ErrRead)}
	}
	var once sync.Once
	bounded := &boundedReadSeeker{source: s.reader, base: s.start, size: s.size}
	return OpenedSource{
		Reader: bounded,
		Info:   SourceInfo{Origin: s.origin, Size: s.size},
		Close: func() (err error) {
			once.Do(func() {
				_, err = s.reader.Seek(s.start, io.SeekStart)
				release()
			})
			if err != nil {
				return classifyExternalError(err, ErrClose)
			}
			return err
		},
	}, nil
}

type boundedReadSeeker struct {
	source io.ReadSeeker
	base   int64
	size   int64
	pos    int64
}

func (r *boundedReadSeeker) Read(p []byte) (int, error) {
	remaining := r.size - r.pos
	if remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.source.Read(p)
	r.pos += int64(n)
	return n, err
}

func (r *boundedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = r.pos + offset
	case io.SeekEnd:
		target = r.size + offset
	default:
		return 0, fmt.Errorf("%w: invalid seek mode", ErrInvalidOptions)
	}
	if target < 0 || target > r.size {
		return 0, io.ErrUnexpectedEOF
	}
	if _, err := r.source.Seek(r.base+target, io.SeekStart); err != nil {
		return 0, err
	}
	r.pos = target
	return target, nil
}

type sourceError struct{ err error }

func (e *sourceError) Error() string { return "dicom index source open failed" }
func (e *sourceError) Unwrap() error { return e.err }

func classifyExternalError(err, fallback error) error {
	for _, class := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		fs.ErrNotExist,
		fs.ErrPermission,
		fs.ErrExist,
		fs.ErrInvalid,
		io.ErrClosedPipe,
	} {
		if errors.Is(err, class) {
			return class
		}
	}
	return fallback
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
