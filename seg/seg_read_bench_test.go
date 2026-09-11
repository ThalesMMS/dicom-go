package seg

import (
	"runtime"
	"testing"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/roi"
)

var benchmarkReadDocument *Document

func BenchmarkSEGReadBinary512x512x100(b *testing.B) {
	benchmarkSEGRead(b, SegmentationStorage)
}

func BenchmarkSEGReadLabelMap512x512x100(b *testing.B) {
	benchmarkSEGRead(b, LabelMapSegmentationStorage)
}

func benchmarkSEGRead(b *testing.B, sopClassUID string) {
	b.Helper()
	obj := benchmarkSEGObject(b, sopClassUID, 512, 512, 100)
	runtime.GC()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		benchmarkReadDocument, err = Read(obj)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkSEGObject(b *testing.B, sopClassUID string, rows, columns, frameCount int) *object.Object {
	b.Helper()
	doc := &Document{
		SOPClassUID:      sopClassUID,
		SOPInstanceUID:   "1.2.826.0.1.3680043.9.7433.751.1",
		Rows:             rows,
		Columns:          columns,
		SegmentationType: SegmentationTypeBinary,
		Segments:         []Segment{{Number: 1, Label: "Benchmark", AlgorithmType: AlgorithmManual}},
		Frames:           make([]Frame, frameCount),
	}
	if sopClassUID == LabelMapSegmentationStorage {
		doc.SegmentationType = SegmentationTypeLabelMap
	}
	for frameIndex := range doc.Frames {
		if sopClassUID == LabelMapSegmentationStorage {
			labels := make([]uint16, rows*columns)
			for y := frameIndex % 16; y < rows; y += 32 {
				labels[y*columns+columns/2] = 1
			}
			doc.Frames[frameIndex] = Frame{SegmentNumber: 1, LabelMap: labels}
			continue
		}
		mask := roi.NewRasterMask(columns, rows)
		for y := frameIndex % 16; y < rows; y += 32 {
			mask.SetRun(y, columns/4, columns/4+32)
		}
		doc.Frames[frameIndex] = Frame{SegmentNumber: 1, Mask: mask}
	}
	file, err := Write(doc)
	if err != nil {
		b.Fatal(err)
	}
	return file.Dataset
}
