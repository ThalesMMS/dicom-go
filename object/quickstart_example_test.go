package object_test

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

type contextReader struct {
	context.Context
	io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(buffer)
}

func ExampleOpenFileWithOptions_boundedDeferred() {
	file, err := object.OpenFileWithOptions("synthetic.dcm", object.ReadFileOptions{
		MaxTotalBytes:             1 << 30,
		MaxElementBytes:           64 << 20,
		MaxPixelDataBytes:         768 << 20,
		MaxSequenceDepth:          64,
		MaxElements:               250_000,
		MaxFragments:              100_000,
		InlineValueBytesThreshold: 1 << 20,
		DeferPixelData:            true,
	})
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Dataset.CopyValueTo(core.TagPixelData, io.Discard)
}

func ExampleReadFileWithOptions_boundedStream() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	source := contextReader{Context: ctx, Reader: bytes.NewReader(nil)}
	file, err := object.ReadFileWithOptions(source, object.ReadFileOptions{
		MaxTotalBytes: 256 << 20, MaxElementBytes: 32 << 20,
		MaxPixelDataBytes: 192 << 20, MaxSequenceDepth: 32,
		MaxElements: 100_000, MaxFragments: 20_000, SkipPixelData: true,
	})
	if err != nil {
		return
	}
	defer file.Close()
}
