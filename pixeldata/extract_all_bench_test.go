package pixeldata

import (
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

var benchmarkExtractAllResults []ExtractedPixelData

func BenchmarkExtractAll2048Matches4KiB(b *testing.B) {
	obj := extractAllManyMatchesFixture(2048, 4<<10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		benchmarkExtractAllResults, err = ExtractAll(obj)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtractAllBounded128Of2048Matches4KiB(b *testing.B) {
	obj := extractAllManyMatchesFixture(2048, 4<<10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ExtractAllWithOptions(context.Background(), obj, ExtractAllOptions{MaxMatches: 128})
		if !errors.Is(err, ErrExtractAllResourceLimit) {
			b.Fatalf("ExtractAllWithOptions() error = %v, want ErrExtractAllResourceLimit", err)
		}
	}
}

func extractAllManyMatchesFixture(matches, payloadBytes int) *object.Object {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	items := make([]core.DataSet, matches)
	for itemIndex := range items {
		payload := make([]byte, payloadBytes)
		payload[0] = byte(itemIndex)
		items[itemIndex] = core.DataSet{Elements: []core.Element{
			dicomtest.NewOBElement(core.TagPixelData, payload),
		}}
	}
	return object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(sequenceTag, items...),
	}, nil)
}
