package object

import "github.com/ThalesMMS/dicom-go/core"

// FrameGeometry is a typed accessor for the per-frame spatial geometry tags a
// volume builder needs, so callers do not hand-roll the tag reads. Missing tags
// leave the corresponding field at its zero value (and the Has* flags false).
type FrameGeometry struct {
	// ImagePositionPatient is (0020,0032): the (x,y,z) mm position of the first
	// voxel. Length 3 when present.
	ImagePositionPatient []float64
	// ImageOrientationPatient is (0020,0037): the row/column direction cosines.
	// Length 6 when present.
	ImageOrientationPatient []float64
	// PixelSpacing is (0028,0030): [rowSpacing, colSpacing] in mm. Length 2 when
	// present.
	PixelSpacing []float64
	// SliceThickness is (0018,0050) in mm. HasSliceThickness reports presence.
	SliceThickness    float64
	HasSliceThickness bool
	// SpacingBetweenSlices is (0018,0088) in mm. HasSpacingBetweenSlices reports
	// presence.
	SpacingBetweenSlices    float64
	HasSpacingBetweenSlices bool
}

var (
	tagImagePositionPatient     = core.NewTag(0x0020, 0x0032)
	tagImageOrientationPatient  = core.NewTag(0x0020, 0x0037)
	tagPixelSpacing             = core.NewTag(0x0028, 0x0030)
	tagSliceThickness           = core.NewTag(0x0018, 0x0050)
	tagSpacingBetweenSlices     = core.NewTag(0x0018, 0x0088)
	tagSharedFunctionalGroups   = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups = core.NewTag(0x5200, 0x9230)
	tagPlanePositionSequence    = core.NewTag(0x0020, 0x9113)
	tagPlaneOrientationSequence = core.NewTag(0x0020, 0x9116)
	tagPixelMeasuresSequence    = core.NewTag(0x0028, 0x9110)
)

// FrameGeometry reads the spatial geometry tags from the object's dataset.
func (o *Object) FrameGeometry() FrameGeometry {
	if o == nil {
		return FrameGeometry{}
	}
	return buildFrameGeometry(o.GetFloats, o.GetFloat)
}

// FrameGeometry reads the spatial geometry tags from the file (routing to the
// dataset). It is the convenience the viewer's volume model consumes.
func (f *File) FrameGeometry() FrameGeometry {
	if f == nil {
		return FrameGeometry{}
	}
	return buildFrameGeometry(f.GetFloats, f.GetFloat)
}

// FrameGeometryAt reads the spatial geometry for a zero-based multi-frame
// frame. Top-level Image Plane attributes are used as a compatibility fallback,
// then Shared Functional Groups are applied, followed by the matching item in
// Per-Frame Functional Groups. An invalid frame index simply omits the
// per-frame overlay while retaining top-level and shared geometry.
func (o *Object) FrameGeometryAt(frameIndex int) FrameGeometry {
	geometry := o.FrameGeometry()
	if o == nil {
		return geometry
	}
	if shared := firstSequenceItem(o, tagSharedFunctionalGroups); shared != nil {
		overlayFunctionalGroupGeometry(&geometry, shared)
	}
	if frames, ok := o.GetSequence(tagPerFrameFunctionalGroups); ok && frameIndex >= 0 && frameIndex < len(frames) {
		overlayFunctionalGroupGeometry(&geometry, frames[frameIndex])
	}
	return geometry
}

// FrameGeometryAt reads spatial geometry for a zero-based frame from the file's
// dataset. Like Object.FrameGeometryAt, it starts with top-level Image Plane
// attributes as a compatibility fallback, overlays Shared Functional Groups,
// then overlays the matching Per-Frame Functional Groups item when the index is
// valid.
func (f *File) FrameGeometryAt(frameIndex int) FrameGeometry {
	if f == nil || f.Dataset == nil {
		return FrameGeometry{}
	}
	return f.Dataset.FrameGeometryAt(frameIndex)
}

func overlayFunctionalGroupGeometry(geometry *FrameGeometry, functionalGroup *Object) {
	if geometry == nil || functionalGroup == nil {
		return
	}
	for _, tag := range []core.Tag{tagPlanePositionSequence, tagPlaneOrientationSequence, tagPixelMeasuresSequence} {
		if item := firstSequenceItem(functionalGroup, tag); item != nil {
			overlayFrameGeometry(geometry, item.FrameGeometry())
		}
	}
}

func overlayFrameGeometry(dst *FrameGeometry, src FrameGeometry) {
	if len(src.ImagePositionPatient) >= 3 {
		dst.ImagePositionPatient = src.ImagePositionPatient
	}
	if len(src.ImageOrientationPatient) >= 6 {
		dst.ImageOrientationPatient = src.ImageOrientationPatient
	}
	if len(src.PixelSpacing) >= 2 {
		dst.PixelSpacing = src.PixelSpacing
	}
	if src.HasSliceThickness {
		dst.SliceThickness = src.SliceThickness
		dst.HasSliceThickness = true
	}
	if src.HasSpacingBetweenSlices {
		dst.SpacingBetweenSlices = src.SpacingBetweenSlices
		dst.HasSpacingBetweenSlices = true
	}
}

func firstSequenceItem(obj *Object, tag core.Tag) *Object {
	if obj == nil {
		return nil
	}
	items, ok := obj.GetSequence(tag)
	if !ok || len(items) == 0 {
		return nil
	}
	return items[0]
}

func buildFrameGeometry(getFloats func(core.Tag) ([]float64, error), getFloat func(core.Tag) (float64, error)) FrameGeometry {
	g := FrameGeometry{}
	if v, err := getFloats(tagImagePositionPatient); err == nil {
		g.ImagePositionPatient = v
	}
	if v, err := getFloats(tagImageOrientationPatient); err == nil {
		g.ImageOrientationPatient = v
	}
	if v, err := getFloats(tagPixelSpacing); err == nil {
		g.PixelSpacing = v
	}
	if v, err := getFloat(tagSliceThickness); err == nil {
		g.SliceThickness = v
		g.HasSliceThickness = true
	}
	if v, err := getFloat(tagSpacingBetweenSlices); err == nil {
		g.SpacingBetweenSlices = v
		g.HasSpacingBetweenSlices = true
	}
	return g
}
