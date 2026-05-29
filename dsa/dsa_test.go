package dsa

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

func uint16sBytes(values ...uint16) []byte {
	out := make([]byte, len(values)*2)
	for i, v := range values {
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], v)
	}
	return out
}

func float32sBytes(values ...float32) []byte {
	out := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(out[i*4:i*4+4], math.Float32bits(v))
	}
	return out
}

func sequenceElement(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	}
}

func TestReadMaskSubtractionSharedMaskFrame(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagMaskOperation, core.VRCS, []byte("AVG_SUB")),
		core.NewRawElement(tagApplicableFrameRange, core.VRUS, uint16sBytes(2, 10)),
		core.NewRawElement(tagMaskFrameNumbers, core.VRUS, uint16sBytes(1)),
		core.NewRawElement(tagMaskOperationExplanation, core.VRST, []byte("Single shared mask")),
	}}
	obj := object.FromElements([]core.Element{
		sequenceElement(tagMaskSubtractionSequence, item),
		core.NewRawElement(tagRecommendedViewingMode, core.VRCS, []byte("VOI")),
	}, std.Dictionary)

	items, ok := ReadMaskSubtraction(obj)
	if !ok || len(items) != 1 {
		t.Fatalf("ReadMaskSubtraction() = %#v, ok=%v, want one item", items, ok)
	}
	got := items[0]
	if got.MaskOperation != MaskOperationAverageSubtraction {
		t.Errorf("MaskOperation = %q, want %q", got.MaskOperation, MaskOperationAverageSubtraction)
	}
	if len(got.ApplicableFrameRange) != 2 || got.ApplicableFrameRange[0] != 2 || got.ApplicableFrameRange[1] != 10 {
		t.Errorf("ApplicableFrameRange = %v, want [2 10]", got.ApplicableFrameRange)
	}
	if len(got.MaskFrameNumbers) != 1 || got.MaskFrameNumbers[0] != 1 {
		t.Errorf("MaskFrameNumbers = %v, want [1]", got.MaskFrameNumbers)
	}
	if got.Explanation != "Single shared mask" {
		t.Errorf("Explanation = %q, want %q", got.Explanation, "Single shared mask")
	}
	if got.ShiftPresent {
		t.Error("ShiftPresent = true, want false (no shift element)")
	}

	for frame, want := range map[int]bool{1: false, 2: true, 5: true, 10: true, 11: false} {
		if got := got.AppliesToFrame(frame); got != want {
			t.Errorf("AppliesToFrame(%d) = %v, want %v", frame, got, want)
		}
	}
	if maskFrame, ok := got.MaskFrameForContrastFrame(3); !ok || maskFrame != 1 {
		t.Errorf("MaskFrameForContrastFrame(3) = %d, %v, want 1, true (shared mask)", maskFrame, ok)
	}

	if mode := RecommendedViewingMode(obj); mode != "VOI" {
		t.Errorf("RecommendedViewingMode() = %q, want VOI", mode)
	}
}

func TestReadMaskSubtractionParallelMaskFramesAndSubPixelShift(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagMaskOperation, core.VRCS, []byte("AVG_SUB")),
		core.NewRawElement(tagMaskFrameNumbers, core.VRUS, uint16sBytes(1, 1, 2)),
		core.NewRawElement(tagContrastFrameAveraging, core.VRUS, uint16sBytes(2)),
		core.NewRawElement(tagMaskSubPixelShift, core.VRFL, float32sBytes(0.5, -1.25)),
	}}
	obj := object.FromElements([]core.Element{
		sequenceElement(tagMaskSubtractionSequence, item),
	}, std.Dictionary)

	items, ok := ReadMaskSubtraction(obj)
	if !ok || len(items) != 1 {
		t.Fatalf("ReadMaskSubtraction() = %#v, ok=%v, want one item", items, ok)
	}
	got := items[0]
	if !got.ShiftPresent {
		t.Fatal("ShiftPresent = false, want true")
	}
	if got.MaskSubPixelShift != [2]float64{0.5, -1.25} {
		t.Errorf("MaskSubPixelShift = %v, want [0.5 -1.25]", got.MaskSubPixelShift)
	}
	if got.ContrastFrameAveraging != 2 {
		t.Errorf("ContrastFrameAveraging = %d, want 2", got.ContrastFrameAveraging)
	}
	// One mask frame number per contrast frame position (parallel arrays).
	if maskFrame, ok := got.MaskFrameForContrastFrame(0); !ok || maskFrame != 1 {
		t.Errorf("MaskFrameForContrastFrame(0) = %d, %v, want 1, true", maskFrame, ok)
	}
	if maskFrame, ok := got.MaskFrameForContrastFrame(2); !ok || maskFrame != 2 {
		t.Errorf("MaskFrameForContrastFrame(2) = %d, %v, want 2, true", maskFrame, ok)
	}
	if _, ok := got.MaskFrameForContrastFrame(5); ok {
		t.Error("MaskFrameForContrastFrame(5) = ok, want false (index out of range)")
	}
}

func TestReadMaskSubtractionAbsentModule(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0060), core.VRCS, []byte("XA")),
	}, std.Dictionary)

	if items, ok := ReadMaskSubtraction(obj); ok || items != nil {
		t.Fatalf("ReadMaskSubtraction() on object without the module = %#v, ok=%v, want nil, false", items, ok)
	}
	if mode := RecommendedViewingMode(obj); mode != "" {
		t.Errorf("RecommendedViewingMode() = %q, want empty", mode)
	}
}

func TestMaskSubtractionItemAppliesToFrameEmptyRangeMeansAllFrames(t *testing.T) {
	item := MaskSubtractionItem{}
	for _, frame := range []int{1, 2, 100} {
		if !item.AppliesToFrame(frame) {
			t.Errorf("AppliesToFrame(%d) = false, want true for an empty ApplicableFrameRange", frame)
		}
	}
}
