package jpeg2000

import (
	"image"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func BenchmarkDecodeJPEG2000Profile(b *testing.B) {
	tests := []struct {
		name    string
		rows    int
		columns int
		syntax  transfer.Syntax
		opts    encodeOptions
	}{
		{name: "small/lossless", rows: 2, columns: 2, syntax: transfer.JPEG2000LosslessOnly, opts: encodeOptions{lossless: true}},
		{name: "small/lossy", rows: 2, columns: 2, syntax: transfer.JPEG2000, opts: encodeOptions{lossless: false}},
		{name: "small/htj2k", rows: 2, columns: 2, syntax: transfer.HTJ2KLossless, opts: encodeOptions{highThroughput: true, lossless: true}},
		{name: "medium/lossless", rows: 64, columns: 64, syntax: transfer.JPEG2000LosslessOnly, opts: encodeOptions{lossless: true}},
		{name: "medium/lossy", rows: 64, columns: 64, syntax: transfer.JPEG2000, opts: encodeOptions{lossless: false}},
		{name: "medium/htj2k", rows: 64, columns: 64, syntax: transfer.HTJ2KLossless, opts: encodeOptions{highThroughput: true, lossless: true}},
		{name: "large/lossless", rows: 256, columns: 256, syntax: transfer.JPEG2000LosslessOnly, opts: encodeOptions{lossless: true}},
		{name: "large/lossy", rows: 256, columns: 256, syntax: transfer.JPEG2000, opts: encodeOptions{lossless: false}},
		{name: "large/htj2k", rows: 256, columns: 256, syntax: transfer.HTJ2KLossless, opts: encodeOptions{highThroughput: true, lossless: true}},
	}
	for _, tt := range tests {
		img := benchmarkGrayImage(tt.columns, tt.rows)
		encoded := encodeJ2KWithEncodeOptions(b, img, tt.opts)
		obj, pixel := jpeg2000Object(b, jpeg2000MetadataOptions{
			rows:    uint16(tt.rows),
			columns: uint16(tt.columns),
		}, encoded)
		registry := pixeldata.NewMemoryRegistry()
		if err := Register(registry); err != nil {
			b.Fatal(err)
		}
		wantBytes := tt.rows * tt.columns

		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				frames, err := registry.DecodeFrames(tt.syntax.UID, pixel, obj)
				if err != nil {
					b.Fatal(err)
				}
				if frames.Rows != tt.rows || frames.Columns != tt.columns || len(frames.Data) != 1 || len(frames.Data[0]) != wantBytes {
					b.Fatalf("decoded frames = rows %d columns %d lengths %v, want %dx%d one %d-byte frame", frames.Rows, frames.Columns, frameLengths(frames.Data), tt.rows, tt.columns, wantBytes)
				}
			}
		})
	}
}

func benchmarkGrayImage(columns, rows int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, columns, rows))
	for y := 0; y < rows; y++ {
		for x := 0; x < columns; x++ {
			img.Pix[y*img.Stride+x] = byte((x*3 + y*5) % 256)
		}
	}
	return img
}
