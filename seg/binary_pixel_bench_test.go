package seg

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/roi"
)

var (
	benchmarkBinaryPixelBytes []byte
	benchmarkBinaryPixelFrame []Frame
)

func BenchmarkBinaryPixelEncodeDense256x256x4(b *testing.B) {
	doc := benchmarkBinaryDocument(256, 256, 4, true)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchmarkBinaryPixelBytes = binaryPixelData(doc)
	}
}

func BenchmarkBinaryPixelEncodeSparse256x256x4(b *testing.B) {
	doc := benchmarkBinaryDocument(256, 256, 4, false)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchmarkBinaryPixelBytes = binaryPixelData(doc)
	}
}

func BenchmarkBinaryPixelDecodeDense256x256x4(b *testing.B) {
	benchmarkBinaryPixelDecode(b, true)
}

func BenchmarkBinaryPixelDecodeSparse256x256x4(b *testing.B) {
	benchmarkBinaryPixelDecode(b, false)
}

func benchmarkBinaryPixelDecode(b *testing.B, dense bool) {
	b.Helper()
	doc := benchmarkBinaryDocument(256, 256, 4, dense)
	data := binaryPixelDataReference(doc)
	obj := object.FromElements([]core.Element{derivedio.Raw(derivedio.TagPixelData, core.VROB, data)}, nil)
	items := make([]*object.Object, len(doc.Frames))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkBinaryPixelFrame = readBinaryFrames(obj, doc, items)
	}
}

func benchmarkBinaryDocument(rows, columns, frameCount int, dense bool) *Document {
	doc := &Document{Rows: rows, Columns: columns, Segments: []Segment{{Number: 1}}, Frames: make([]Frame, frameCount)}
	for frameIndex := range doc.Frames {
		mask := roi.NewRasterMask(columns, rows)
		for y := 0; y < rows; y++ {
			if dense {
				mask.SetRun(y, 0, columns)
			} else if y%8 == 0 {
				mask.SetRun(y, columns/4, columns/4+32)
			}
		}
		doc.Frames[frameIndex] = Frame{SegmentNumber: 1, Mask: mask}
	}
	return doc
}
