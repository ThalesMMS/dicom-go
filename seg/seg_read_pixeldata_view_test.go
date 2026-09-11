package seg

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/roi"
)

func TestReadPixelDataViewPreservesSourceAndDecodedPayload(t *testing.T) {
	binaryMask := roi.NewRasterMask(5, 3)
	binaryMask.SetRun(0, 1, 4)
	binaryMask.Set(4, 2, true)

	tests := []struct {
		name string
		doc  *Document
	}{
		{
			name: "binary",
			doc: &Document{
				SOPClassUID:      SegmentationStorage,
				SOPInstanceUID:   "1.2.826.0.1.3680043.9.7433.751.2",
				Rows:             3,
				Columns:          5,
				SegmentationType: SegmentationTypeBinary,
				Segments:         []Segment{{Number: 1, Label: "Binary", AlgorithmType: AlgorithmManual}},
				Frames:           []Frame{{SegmentNumber: 1, Mask: binaryMask}},
			},
		},
		{
			name: "label map",
			doc: &Document{
				SOPClassUID:      LabelMapSegmentationStorage,
				SOPInstanceUID:   "1.2.826.0.1.3680043.9.7433.751.3",
				Rows:             2,
				Columns:          3,
				SegmentationType: SegmentationTypeLabelMap,
				Segments:         []Segment{{Number: 7, Label: "Label map", AlgorithmType: AlgorithmManual}},
				Frames:           []Frame{{SegmentNumber: 7, LabelMap: []uint16{0, 7, 0, 3, 0, 7}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := Write(tt.doc)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			pixelDataBefore, ok := file.Dataset.GetRaw(derivedio.TagPixelData)
			if !ok {
				t.Fatal("written object has no raw Pixel Data")
			}

			got, err := Read(file.Dataset)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			pixelDataAfter, _ := file.Dataset.GetRaw(derivedio.TagPixelData)
			if !bytes.Equal(pixelDataAfter, pixelDataBefore) {
				t.Fatal("Read mutated the source Pixel Data")
			}
			if len(got.Frames) != 1 {
				t.Fatalf("frame count = %d, want 1", len(got.Frames))
			}

			switch tt.doc.SOPClassUID {
			case LabelMapSegmentationStorage:
				if !reflect.DeepEqual(got.Frames[0].LabelMap, tt.doc.Frames[0].LabelMap) {
					t.Fatalf("LabelMap = %v, want %v", got.Frames[0].LabelMap, tt.doc.Frames[0].LabelMap)
				}
				got.Frames[0].LabelMap[0] = 99
			default:
				for y := 0; y < tt.doc.Rows; y++ {
					if !reflect.DeepEqual(got.Frames[0].Mask.Runs(y), tt.doc.Frames[0].Mask.Runs(y)) {
						t.Fatalf("row %d runs = %v, want %v", y, got.Frames[0].Mask.Runs(y), tt.doc.Frames[0].Mask.Runs(y))
					}
				}
				got.Frames[0].Mask.Set(0, 0, true)
			}

			pixelDataAfterOutputMutation, _ := file.Dataset.GetRaw(derivedio.TagPixelData)
			if !bytes.Equal(pixelDataAfterOutputMutation, pixelDataBefore) {
				t.Fatal("mutating the decoded document changed the source Pixel Data")
			}
		})
	}
}
