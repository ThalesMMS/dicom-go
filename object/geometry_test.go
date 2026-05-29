package object

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
)

func dsMulti(tag core.Tag, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRDS}, Value: core.StringValue(values)}
}

func geometrySequence(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength},
		Value:  core.SequenceValue{Items: items},
	}
}

func TestFrameGeometryFull(t *testing.T) {
	obj := FromElements([]core.Element{
		dsMulti(core.NewTag(0x0020, 0x0032), "10", "-20", "30"),
		dsMulti(core.NewTag(0x0020, 0x0037), "1", "0", "0", "0", "1", "0"),
		dsMulti(core.NewTag(0x0028, 0x0030), "0.7", "0.8"),
		dsMulti(core.NewTag(0x0018, 0x0050), "2.5"),
		dsMulti(core.NewTag(0x0018, 0x0088), "3"),
	}, std.Dictionary)

	g := obj.FrameGeometry()
	if len(g.ImagePositionPatient) != 3 || g.ImagePositionPatient[0] != 10 || g.ImagePositionPatient[1] != -20 || g.ImagePositionPatient[2] != 30 {
		t.Errorf("IPP = %v, want [10 -20 30]", g.ImagePositionPatient)
	}
	if len(g.ImageOrientationPatient) != 6 || g.ImageOrientationPatient[4] != 1 {
		t.Errorf("IOP = %v, want row/col cosines", g.ImageOrientationPatient)
	}
	if len(g.PixelSpacing) != 2 || g.PixelSpacing[0] != 0.7 || g.PixelSpacing[1] != 0.8 {
		t.Errorf("PixelSpacing = %v, want [0.7 0.8]", g.PixelSpacing)
	}
	if !g.HasSliceThickness || g.SliceThickness != 2.5 {
		t.Errorf("SliceThickness = %v (has=%v), want 2.5", g.SliceThickness, g.HasSliceThickness)
	}
	if !g.HasSpacingBetweenSlices || g.SpacingBetweenSlices != 3 {
		t.Errorf("SpacingBetweenSlices = %v (has=%v), want 3", g.SpacingBetweenSlices, g.HasSpacingBetweenSlices)
	}
}

func TestFrameGeometryMissingTags(t *testing.T) {
	obj := FromElements([]core.Element{
		dsMulti(core.NewTag(0x0028, 0x0030), "1", "1"),
	}, std.Dictionary)

	g := obj.FrameGeometry()
	if g.ImagePositionPatient != nil || g.ImageOrientationPatient != nil {
		t.Errorf("missing IPP/IOP should be nil, got %v / %v", g.ImagePositionPatient, g.ImageOrientationPatient)
	}
	if g.HasSliceThickness || g.HasSpacingBetweenSlices {
		t.Errorf("absent thickness/spacing should report Has=false")
	}
	if len(g.PixelSpacing) != 2 {
		t.Errorf("present PixelSpacing should be read, got %v", g.PixelSpacing)
	}
}

func TestFrameGeometryAtCombinesSharedAndPerFrameFunctionalGroups(t *testing.T) {
	tagSharedFunctionalGroups := core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups := core.NewTag(0x5200, 0x9230)
	tagPlanePositionSequence := core.NewTag(0x0020, 0x9113)
	tagPlaneOrientationSequence := core.NewTag(0x0020, 0x9116)
	tagPixelMeasuresSequence := core.NewTag(0x0028, 0x9110)

	obj := FromElements([]core.Element{
		geometrySequence(tagSharedFunctionalGroups, core.DataSet{Elements: []core.Element{
			geometrySequence(tagPlaneOrientationSequence, core.DataSet{Elements: []core.Element{
				dsMulti(tagImageOrientationPatient, "1", "0", "0", "0", "1", "0"),
			}}),
			geometrySequence(tagPixelMeasuresSequence, core.DataSet{Elements: []core.Element{
				dsMulti(tagPixelSpacing, "0.5", "0.75"),
				dsMulti(tagSliceThickness, "2.5"),
				dsMulti(tagSpacingBetweenSlices, "2.5"),
			}}),
		}}),
		geometrySequence(tagPerFrameFunctionalGroups,
			core.DataSet{Elements: []core.Element{
				geometrySequence(tagPlanePositionSequence, core.DataSet{Elements: []core.Element{
					dsMulti(tagImagePositionPatient, "0", "0", "10"),
				}}),
			}},
			core.DataSet{Elements: []core.Element{
				geometrySequence(tagPlanePositionSequence, core.DataSet{Elements: []core.Element{
					dsMulti(tagImagePositionPatient, "0", "0", "12.5"),
				}}),
			}},
		),
	}, std.Dictionary)

	first := obj.FrameGeometryAt(0)
	second := obj.FrameGeometryAt(1)
	if len(first.ImagePositionPatient) != 3 || first.ImagePositionPatient[2] != 10 {
		t.Fatalf("frame 0 ImagePositionPatient = %v, want z=10", first.ImagePositionPatient)
	}
	if len(second.ImagePositionPatient) != 3 || second.ImagePositionPatient[2] != 12.5 {
		t.Fatalf("frame 1 ImagePositionPatient = %v, want z=12.5", second.ImagePositionPatient)
	}
	if len(second.ImageOrientationPatient) != 6 || second.ImageOrientationPatient[0] != 1 || second.ImageOrientationPatient[4] != 1 {
		t.Fatalf("frame 1 ImageOrientationPatient = %v, want shared axial orientation", second.ImageOrientationPatient)
	}
	if len(second.PixelSpacing) != 2 || second.PixelSpacing[0] != 0.5 || second.PixelSpacing[1] != 0.75 {
		t.Fatalf("frame 1 PixelSpacing = %v, want shared [0.5 0.75]", second.PixelSpacing)
	}
	if !second.HasSliceThickness || second.SliceThickness != 2.5 {
		t.Fatalf("frame 1 SliceThickness = %v (has=%v), want shared 2.5", second.SliceThickness, second.HasSliceThickness)
	}
	if !second.HasSpacingBetweenSlices || second.SpacingBetweenSlices != 2.5 {
		t.Fatalf("frame 1 SpacingBetweenSlices = %v (has=%v), want shared 2.5", second.SpacingBetweenSlices, second.HasSpacingBetweenSlices)
	}
}

func TestFrameGeometryNilSafe(t *testing.T) {
	var o *Object
	if g := o.FrameGeometry(); g.HasSliceThickness {
		t.Error("nil object should return a zero FrameGeometry")
	}
	var f *File
	if g := f.FrameGeometry(); g.HasSpacingBetweenSlices {
		t.Error("nil file should return a zero FrameGeometry")
	}
}
