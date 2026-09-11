package dicomweb

import (
	"context"
	"errors"
	"io"
	"os"
)

type jsonArraySpool struct {
	file      *os.File
	path      string
	maximum   int64
	size      int64
	count     int
	finalized bool
}

type responseSpool struct {
	file    *os.File
	path    string
	maximum int64
	size    int64
}

func newResponseSpool(directory string, maximum int64) (*responseSpool, error) {
	file, err := os.CreateTemp(directory, ".dicomweb-render-*")
	if err != nil {
		return nil, ErrBackend
	}
	return &responseSpool{file: file, path: file.Name(), maximum: maximum}, nil
}

func (s *responseSpool) Write(data []byte) (int, error) {
	if s == nil || s.file == nil {
		return 0, ErrBackend
	}
	if int64(len(data)) > s.maximum-s.size {
		return 0, ErrResourceLimit
	}
	n, err := s.file.Write(data)
	s.size += int64(n)
	return n, err
}

func (s *responseSpool) Rewind() error {
	if s == nil || s.file == nil {
		return ErrBackend
	}
	_, err := s.file.Seek(0, io.SeekStart)
	return err
}

func (s *responseSpool) Close() error {
	if s == nil {
		return nil
	}
	var closeErr, removeErr error
	if s.file != nil {
		closeErr = s.file.Close()
		s.file = nil
	}
	if s.path != "" {
		removeErr = os.Remove(s.path)
		s.path = ""
	}
	if closeErr != nil || (removeErr != nil && !errors.Is(removeErr, os.ErrNotExist)) {
		return ErrBackend
	}
	return nil
}

func newJSONArraySpool(directory string, maximum int64) (*jsonArraySpool, error) {
	file, err := os.CreateTemp(directory, ".dicomweb-json-*")
	if err != nil {
		return nil, ErrBackend
	}
	spool := &jsonArraySpool{file: file, path: file.Name(), maximum: maximum}
	if _, err := file.Write([]byte{'['}); err != nil {
		_ = spool.Close()
		return nil, ErrBackend
	}
	spool.size = 1
	return spool, nil
}

func (s *jsonArraySpool) Append(data []byte) error {
	if s == nil || s.file == nil || s.finalized {
		return ErrBackend
	}
	extra := int64(len(data))
	if s.count > 0 {
		extra++
	}
	if extra < 0 || extra > s.maximum-1 || s.size > s.maximum-1-extra {
		return ErrResourceLimit
	}
	if s.count > 0 {
		if _, err := s.file.Write([]byte{','}); err != nil {
			return ErrBackend
		}
		s.size++
	}
	if _, err := s.file.Write(data); err != nil {
		return ErrBackend
	}
	s.size += int64(len(data))
	s.count++
	return nil
}

func (s *jsonArraySpool) Finalize() error {
	if s == nil || s.file == nil || s.finalized {
		return ErrBackend
	}
	if s.size >= s.maximum {
		return ErrResourceLimit
	}
	if _, err := s.file.Write([]byte{']'}); err != nil {
		return ErrBackend
	}
	s.size++
	s.finalized = true
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return ErrBackend
	}
	return nil
}

func (s *jsonArraySpool) Close() error {
	if s == nil {
		return nil
	}
	var closeErr, removeErr error
	if s.file != nil {
		closeErr = s.file.Close()
		s.file = nil
	}
	if s.path != "" {
		removeErr = os.Remove(s.path)
		s.path = ""
	}
	if closeErr != nil || (removeErr != nil && !errors.Is(removeErr, os.ErrNotExist)) {
		return ErrBackend
	}
	return nil
}

func copyBounded(ctx context.Context, destination io.Writer, source io.Reader, maximum int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		remaining := maximum - total
		if remaining < 0 {
			return total, ErrResourceLimit
		}
		readSize := len(buffer)
		if int64(readSize) > remaining+1 {
			readSize = int(remaining + 1)
		}
		n, readErr := source.Read(buffer[:readSize])
		if n > 0 {
			if total+int64(n) > maximum {
				return total, ErrResourceLimit
			}
			written, writeErr := destination.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func closeReader(reader io.Reader) error {
	if closer, ok := reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func withReaderClosed(reader io.Reader, consume func() error) (err error) {
	defer func() {
		closeErr := safeCloseReader(reader)
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
		if err == nil && closeErr != nil {
			err = ErrBackend
		}
	}()
	return consume()
}

func safeCloseReader(reader io.Reader) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return closeReader(reader)
}
