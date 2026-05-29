package seg

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/roi"
)

func TestBinaryPixelEncodeDecodeMatchesBitReference(t *testing.T) {
	random := rand.New(rand.NewSource(316))
	for _, dimensions := range [][2]int{{1, 1}, {3, 3}, {7, 5}, {8, 8}, {9, 9}, {31, 17}} {
		rows, columns := dimensions[0], dimensions[1]
		doc := randomBinaryDocument(random, rows, columns, 5)
		gotData := binaryPixelData(doc)
		wantData := binaryPixelDataReference(doc)
		if !bytes.Equal(gotData, wantData) {
			t.Fatalf("%dx%d encoded bytes differ\ngot  %08b\nwant %08b", columns, rows, gotData, wantData)
		}
		obj := object.FromElements([]core.Element{derivedio.Raw(derivedio.TagPixelData, core.VROB, gotData)}, nil)
		gotFrames := readBinaryFrames(obj, doc, make([]*object.Object, len(doc.Frames)))
		wantFrames := readBinaryFramesReference(gotData, doc, len(doc.Frames), doc.Rows*doc.Columns)
		assertBinaryFrameMasksEqual(t, gotFrames, wantFrames)
	}
}

func TestReadBinaryFramesMatchesReferenceForLegacyByteAlignedFrames(t *testing.T) {
	random := rand.New(rand.NewSource(316_001))
	doc := randomBinaryDocument(random, 3, 3, 7)
	bytesPerFrame := (doc.Rows*doc.Columns + 7) / 8
	legacy := make([]byte, bytesPerFrame*len(doc.Frames))
	for frameIndex, frame := range doc.Frames {
		one := *doc
		one.Frames = []Frame{frame}
		copy(legacy[frameIndex*bytesPerFrame:], binaryPixelDataReference(&one))
	}
	obj := object.FromElements([]core.Element{derivedio.Raw(derivedio.TagPixelData, core.VROB, legacy)}, nil)
	got := readBinaryFrames(obj, doc, make([]*object.Object, len(doc.Frames)))
	want := readBinaryFramesReference(legacy, doc, len(doc.Frames), bytesPerFrame*8)
	assertBinaryFrameMasksEqual(t, got, want)
}

func TestReadBinaryFramesMatchesReferenceForTruncatedData(t *testing.T) {
	doc := &Document{Rows: 5, Columns: 7, Segments: []Segment{{Number: 1}}, Frames: make([]Frame, 3)}
	data := []byte{0xFF, 0x81, 0x42}
	obj := object.FromElements([]core.Element{derivedio.Raw(derivedio.TagPixelData, core.VROB, data)}, nil)
	got := readBinaryFrames(obj, doc, make([]*object.Object, len(doc.Frames)))
	want := readBinaryFramesReference(data, doc, len(doc.Frames), binaryFrameBitStride(doc.Rows*doc.Columns, len(doc.Frames), len(data)))
	assertBinaryFrameMasksEqual(t, got, want)
}

func randomBinaryDocument(random *rand.Rand, rows, columns, frameCount int) *Document {
	doc := &Document{Rows: rows, Columns: columns, Frames: make([]Frame, frameCount)}
	for frameIndex := range doc.Frames {
		mask := roi.NewRasterMask(columns, rows)
		for y := 0; y < rows; y++ {
			x := 0
			for x < columns {
				if random.Intn(4) != 0 {
					x++
					continue
				}
				start := x
				for x < columns && random.Intn(3) != 0 {
					x++
				}
				if x == start {
					x++
				}
				mask.SetRun(y, start, x)
			}
		}
		doc.Frames[frameIndex] = Frame{SegmentNumber: 1, Mask: mask}
	}
	return doc
}

func binaryPixelDataReference(doc *Document) []byte {
	bitsPerFrame := doc.Rows * doc.Columns
	if bitsPerFrame <= 0 {
		return nil
	}
	out := make([]byte, (bitsPerFrame*len(doc.Frames)+7)/8)
	for frameIndex, frame := range doc.Frames {
		if frame.Mask == nil {
			continue
		}
		baseBit := frameIndex * bitsPerFrame
		frame.Mask.ForEachPixel(func(x, y int) {
			bit := baseBit + y*doc.Columns + x
			out[bit/8] |= 1 << uint(bit%8)
		})
	}
	return out
}

func readBinaryFramesReference(data []byte, doc *Document, frameCount, frameBitStride int) []Frame {
	bitsPerFrame := doc.Rows * doc.Columns
	frames := make([]Frame, frameCount)
	for frameIndex := range frames {
		mask := roi.NewRasterMask(doc.Columns, doc.Rows)
		baseBit := frameIndex * frameBitStride
		for bit := 0; bit < bitsPerFrame; bit++ {
			streamBit := baseBit + bit
			if streamBit/8 >= len(data) {
				break
			}
			if data[streamBit/8]&(1<<uint(streamBit%8)) != 0 {
				mask.Set(bit%doc.Columns, bit/doc.Columns, true)
			}
		}
		frames[frameIndex].Mask = mask
	}
	return frames
}

func assertBinaryFrameMasksEqual(t *testing.T, got, want []Frame) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("frame count = %d, want %d", len(got), len(want))
	}
	for frameIndex := range got {
		for y := 0; y < got[frameIndex].Mask.Rows; y++ {
			if !reflect.DeepEqual(got[frameIndex].Mask.Runs(y), want[frameIndex].Mask.Runs(y)) {
				t.Fatalf("frame %d row %d runs = %v, want %v", frameIndex, y, got[frameIndex].Mask.Runs(y), want[frameIndex].Mask.Runs(y))
			}
		}
	}
}
