package parser

import (
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
)

// BulkDataSource is a defined-length stream returned by BulkDataResolver.
// Size is the number of unpadded value bytes available from Reader.
type BulkDataSource struct {
	Reader io.ReadCloser
	Size   int64
}

// BulkDataResolver opens a stream for one BulkDataValue. When Reader is
// non-nil, Writer owns it and closes it exactly once, even when the resolver
// also returns an error.
type BulkDataResolver func(core.BulkDataValue) (BulkDataSource, error)

func (w *Writer) writeBulkDataValue(el core.Element, value core.BulkDataValue) (err error) {
	source, resolveErr := w.opts.BulkDataResolver(value)
	if source.Reader != nil {
		defer func() {
			if closeErr := source.Reader.Close(); closeErr != nil {
				wrapped := w.wrapWriteError(
					OpWriteValue,
					el.Tag(),
					el.VR(),
					el.Length(),
					fmt.Errorf("dicom: close Bulk Data source: %w", closeErr),
				)
				if err == nil {
					err = wrapped
				} else {
					err = errors.Join(err, wrapped)
				}
			}
		}()
	}
	if resolveErr != nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), bulkDataResolveError{err: resolveErr})
	}
	if source.Reader == nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: Bulk Data resolver returned a nil Reader"))
	}

	encodedLength, lengthErr := bulkDataEncodedLength(source.Size)
	if lengthErr != nil {
		return w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), el.Length(), lengthErr)
	}
	if el.Header.HasLength() && el.Header.Length != encodedLength {
		return w.wrapWriteError(
			OpWriteLength,
			el.Tag(),
			el.VR(),
			el.Header.Length,
			fmt.Errorf("dicom: Bulk Data source encoded length %d does not match element header length %d", encodedLength, el.Header.Length),
		)
	}
	if err := w.writeHeader(el.Tag(), el.VR(), encodedLength); err != nil {
		return err
	}

	copied, copyErr := io.CopyN(w.w, source.Reader, source.Size)
	if copyErr != nil {
		return w.wrapWriteError(
			OpWriteValue,
			el.Tag(),
			el.VR(),
			encodedLength,
			fmt.Errorf("dicom: Bulk Data source copied %d bytes, want %d: %w", copied, source.Size, copyErr),
		)
	}

	var extra [1]byte
	n, readErr := source.Reader.Read(extra[:])
	if n != 0 {
		return w.wrapWriteError(
			OpWriteValue,
			el.Tag(),
			el.VR(),
			encodedLength,
			fmt.Errorf("dicom: Bulk Data source exceeds declared size %d", source.Size),
		)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), encodedLength, fmt.Errorf("dicom: verify Bulk Data source size: %w", readErr))
	}
	if readErr == nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), encodedLength, fmt.Errorf("dicom: verify Bulk Data source size: %w", io.ErrNoProgress))
	}

	if source.Size%2 != 0 {
		if err := writeAll(w.w, []byte{el.VR().PaddingByte()}); err != nil {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), encodedLength, err)
		}
	}
	return nil
}

type bulkDataResolveError struct {
	err error
}

func (e bulkDataResolveError) Error() string {
	return "dicom: resolve Bulk Data source"
}

func (e bulkDataResolveError) Unwrap() error {
	return e.err
}

func bulkDataEncodedLength(size int64) (core.Length, error) {
	if size < 0 {
		return 0, fmt.Errorf("dicom: Bulk Data source size %d is negative", size)
	}
	encodedSize := size
	if encodedSize%2 != 0 {
		encodedSize++
	}
	if encodedSize >= int64(core.UndefinedLength) {
		return 0, fmt.Errorf("dicom: Bulk Data encoded length %d uses or exceeds the undefined-length sentinel", encodedSize)
	}
	return core.Length(encodedSize), nil
}
