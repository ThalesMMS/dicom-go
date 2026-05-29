package parser

import (
	"bytes"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Run with:
// go test -run '^$' -bench '^BenchmarkReadLargeDefinedValue$' ./parser -benchmem
//
// Exercises materialization of a CT-slice-sized (~512 KiB) native Pixel Data
// value with default (unbounded) reader options. The sized_source variant
// reads from an io.ReadSeeker whose physical size is discoverable; the
// unsized_source variant reads from a plain stream, which must keep the
// incremental-growth allocation guard against forged value lengths.

const largeValueBenchmarkPixelBytes = 512 * 512 * 2

// unseekableReader hides Seek so the reader treats the source as unsized.
type unseekableReader struct {
	r io.Reader
}

func (u unseekableReader) Read(p []byte) (int, error) {
	return u.r.Read(p)
}

func largeValueBenchmarkStream(b *testing.B) []byte {
	b.Helper()
	pixels := make([]byte, largeValueBenchmarkPixelBytes)
	for i := range pixels {
		pixels[i] = byte(i)
	}
	return dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewUShortElement(core.Tag{Group: 0x0028, Element: 0x0010}, 512),
		dicomtest.NewUShortElement(core.Tag{Group: 0x0028, Element: 0x0011}, 512),
		dicomtest.NewUShortElement(core.Tag{Group: 0x0028, Element: 0x0100}, 16),
		core.NewRawElement(core.TagPixelData, core.VROW, pixels),
	)
}

func BenchmarkReadLargeDefinedValue(b *testing.B) {
	data := largeValueBenchmarkStream(b)
	syntax := transfer.ExplicitVRLittleEndian

	b.Run("sized_source", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			reader := NewReader(bytes.NewReader(data), syntax, ReaderOptions{Dictionary: std.Dictionary})
			if _, err := reader.ReadDataSet(); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("unsized_source", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			reader := NewReader(unseekableReader{r: bytes.NewReader(data)}, syntax, ReaderOptions{Dictionary: std.Dictionary})
			if _, err := reader.ReadDataSet(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
