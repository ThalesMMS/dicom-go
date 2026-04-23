package parser

import (
	"bytes"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Run with:
// go test -run '^$' -bench '^(BenchmarkWriterWriteElement|BenchmarkWriteDataSet|BenchmarkWriteDataSetWithSequences)$' ./parser -benchmem
//
// Compare ns/op together with B/op and allocs/op to spot regressions in value
// encoding, structured dataset emission, and encapsulated pixel-data handling.

func BenchmarkWriterWriteElement(b *testing.B) {
	elem := dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "BENCH^PATIENT")

	for _, syntax := range benchmarkNativeSyntaxes() {
		syntax := syntax
		b.Run("patient_name/"+syntax.Name, func(b *testing.B) {
			benchmarkWriteElement(b, elem, syntax)
		})
	}

	b.Run("encapsulated_pixeldata/"+transfer.JPEGBaseline.Name, func(b *testing.B) {
		benchmarkWriteElement(
			b,
			dicomtest.NewFragmentSequenceElement(
				core.TagPixelData,
				[]byte{0x00, 0x00, 0x00, 0x00},
				[]byte{0xFF, 0xD8, 0xFF, 0xD9},
				[]byte{0x00, 0x01, 0x02, 0x03},
			),
			transfer.JPEGBaseline,
		)
	})
}

func BenchmarkWriteDataSet(b *testing.B) {
	minimal := core.DataSet{Elements: dicomtest.MinimalDataset()}
	nativePixel := core.DataSet{Elements: dicomtest.DatasetWithPixelData()}
	encapsulatedPixel := core.DataSet{
		Elements: append(
			dicomtest.MinimalDataset(),
			dicomtest.NewFragmentSequenceElement(
				core.TagPixelData,
				[]byte{0x00, 0x00, 0x00, 0x00},
				[]byte{0xFF, 0xD8, 0xFF, 0xD9},
				[]byte{0x00, 0x01, 0x02, 0x03},
			),
		),
	}

	for _, syntax := range benchmarkNativeSyntaxes() {
		syntax := syntax
		b.Run("minimal/"+syntax.Name, func(b *testing.B) {
			benchmarkWriteDataSetFixture(b, minimal, syntax)
		})
		b.Run("native_pixeldata/"+syntax.Name, func(b *testing.B) {
			benchmarkWriteDataSetFixture(b, nativePixel, syntax)
		})
	}

	b.Run("encapsulated_pixeldata/"+transfer.JPEGBaseline.Name, func(b *testing.B) {
		benchmarkWriteDataSetFixture(b, encapsulatedPixel, transfer.JPEGBaseline)
	})
}

func BenchmarkWriteDataSetWithSequences(b *testing.B) {
	ds := dicomtest.BenchmarkSequenceDataSet()

	for _, syntax := range benchmarkNativeSyntaxes() {
		syntax := syntax
		b.Run(syntax.Name, func(b *testing.B) {
			benchmarkWriteDataSetFixture(b, ds, syntax)
		})
	}
}

func benchmarkWriteElement(b *testing.B, elem core.Element, syntax transfer.Syntax) {
	b.Helper()

	var buf bytes.Buffer
	buf.Grow(benchmarkElementBufferCapacity(elem, syntax))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf.Reset()
		writer := NewWriter(&buf, syntax)
		if err := writer.WriteElement(elem); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkWriteDataSetFixture(b *testing.B, ds core.DataSet, syntax transfer.Syntax) {
	b.Helper()

	var buf bytes.Buffer
	buf.Grow(benchmarkDataSetBufferCapacity(ds, syntax))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf.Reset()
		writer := NewWriter(&buf, syntax)
		for _, el := range ds.Elements {
			if err := writer.WriteElement(el); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func benchmarkElementBufferCapacity(elem core.Element, syntax transfer.Syntax) int {
	var buf bytes.Buffer
	writer := NewWriter(&buf, syntax)
	if err := writer.WriteElement(elem); err != nil {
		return 4 << 10
	}
	if buf.Len() < 4<<10 {
		return 4 << 10
	}
	return buf.Len()
}

func benchmarkDataSetBufferCapacity(ds core.DataSet, syntax transfer.Syntax) int {
	var buf bytes.Buffer
	writer := NewWriter(&buf, syntax)
	for _, el := range ds.Elements {
		if err := writer.WriteElement(el); err != nil {
			return 8 << 10
		}
	}
	if buf.Len() < 8<<10 {
		return 8 << 10
	}
	return buf.Len()
}
