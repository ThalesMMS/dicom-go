package display

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

// ErrInvalidShutter reports a Display Shutter that names a shape but is missing
// the geometry attributes that shape requires.
var ErrInvalidShutter = errors.New("dicom/display: invalid display shutter geometry")
var ErrUnsupportedShutter = errors.New("dicom/display: unsupported display shutter shape")

var (
	tagSOPClassUID             = core.NewTag(0x0008, 0x0016)
	tagPresentationLUTSequence = core.NewTag(0x2050, 0x0010)
	tagPresentationLUTShape    = core.NewTag(0x2050, 0x0020)

	// presentationStateUIDPrefix matches the Presentation State Storage SOP
	// Class family (PS3.4 / PS3.6, root 1.2.840.10008.5.1.4.1.1.11.x).
	presentationStateUIDPrefix = "1.2.840.10008.5.1.4.1.1.11."

	tagShutterShape      = core.NewTag(0x0018, 0x1600)
	tagShutterLeft       = core.NewTag(0x0018, 0x1602)
	tagShutterRight      = core.NewTag(0x0018, 0x1604)
	tagShutterUpper      = core.NewTag(0x0018, 0x1606)
	tagShutterLower      = core.NewTag(0x0018, 0x1608)
	tagShutterCircleCtr  = core.NewTag(0x0018, 0x1610)
	tagShutterCircleRad  = core.NewTag(0x0018, 0x1612)
	tagShutterPolyVerts  = core.NewTag(0x0018, 0x1620)
	tagShutterPresValue  = core.NewTag(0x0018, 0x1622)
	tagShutterOverlayGrp = core.NewTag(0x0018, 0x1623)
	tagShutterPresColor  = core.NewTag(0x0018, 0x1624)
	tagOverlayRows       = uint16(0x0010)
	tagOverlayColumns    = uint16(0x0011)
	tagOverlayOrigin     = uint16(0x0050)
	tagOverlayBitsAlloc  = uint16(0x0100)
	tagOverlayData       = uint16(0x3000)
	overlayGroupFirst    = uint16(0x6000)
	overlayGroupLast     = uint16(0x601E)
)

// PresentationFromObject builds the presentation-stage transform from a dataset:
// Presentation LUT Sequence/Shape, Display Shutter, and overlays. MONOCHROME1
// inversion is not read here; it derives from Photometric Interpretation, which
// the caller knows.
func PresentationFromObject(obj *object.Object) (Presentation, error) {
	var pres Presentation
	if obj == nil {
		return pres, nil
	}
	// A Grayscale Softcopy Presentation State object carries spatial transforms,
	// displayed-area, graphic annotations, and per-image softcopy VOI that this
	// pipeline does not interpret. Refuse it rather than apply only its shutter
	// and overlays and render an incorrect result.
	if sop, ok := obj.GetUID(tagSOPClassUID); ok && strings.HasPrefix(strings.TrimSpace(sop), presentationStateUIDPrefix) {
		return pres, ErrUnsupportedGSPS
	}
	if items, ok := obj.GetSequence(tagPresentationLUTSequence); ok && len(items) > 0 {
		lut, err := lutFromItem(items[0])
		if err != nil {
			return pres, err
		}
		pres.LUT = lut
	}
	if shape, ok := obj.GetString(tagPresentationLUTShape); ok {
		if strings.EqualFold(strings.TrimSpace(shape), "INVERSE") {
			pres.Invert = true
		}
	}

	shutter, err := ShutterFromObject(obj)
	if err != nil {
		return pres, err
	}
	pres.Shutter = shutter

	var skipOverlayGroups map[uint16]bool
	if shutter != nil && shutter.Bitmap != nil {
		skipOverlayGroups = map[uint16]bool{shutter.Bitmap.Group: true}
	}
	overlays, err := overlaysFromObject(obj, skipOverlayGroups)
	if err != nil {
		return pres, err
	}
	pres.Overlays = overlays
	return pres, nil
}

// ValidateShutter verifies that a display shutter is representable by the
// standard Display Shutter module. A nil shutter is valid and means absent.
func ValidateShutter(shutter *Shutter) error {
	if shutter == nil {
		return nil
	}
	shapes := 0
	if rect := shutter.Rectangle; rect != nil {
		shapes++
		if rect.Left > rect.Right || rect.Upper > rect.Lower {
			return fmt.Errorf("%w: invalid rectangular shutter %+v", ErrInvalidShutter, *rect)
		}
	}
	if circle := shutter.Circle; circle != nil {
		shapes++
		if circle.Radius <= 0 {
			return fmt.Errorf("%w: circular shutter radius must be positive", ErrInvalidShutter)
		}
	}
	if polygon := shutter.Polygon; polygon != nil {
		shapes++
		if len(polygon.Vertices) < 3 {
			return fmt.Errorf("%w: polygonal shutter requires at least three vertices", ErrInvalidShutter)
		}
	}
	if bitmap := shutter.Bitmap; bitmap != nil {
		shapes++
		if bitmap.Group < overlayGroupFirst || bitmap.Group > overlayGroupLast || bitmap.Group%2 != 0 {
			return fmt.Errorf("%w: invalid bitmap shutter overlay group %#04x", ErrInvalidShutter, bitmap.Group)
		}
		if bitmap.Overlay.Rows <= 0 || bitmap.Overlay.Columns <= 0 ||
			bitmap.Overlay.Rows > 1<<16-1 || bitmap.Overlay.Columns > 1<<16-1 {
			return fmt.Errorf("%w: bitmap shutter requires valid overlay dimensions", ErrInvalidShutter)
		}
		required := (bitmap.Overlay.Rows*bitmap.Overlay.Columns + 7) / 8
		if len(bitmap.Overlay.Data) < required {
			return fmt.Errorf("%w: bitmap shutter overlay has %d bytes, requires %d", ErrInvalidShutter, len(bitmap.Overlay.Data), required)
		}
		if bitmap.Overlay.OriginRow < -(1<<15) || bitmap.Overlay.OriginRow > 1<<15-1 ||
			bitmap.Overlay.OriginCol < -(1<<15) || bitmap.Overlay.OriginCol > 1<<15-1 {
			return fmt.Errorf("%w: bitmap shutter overlay origin is outside SS", ErrInvalidShutter)
		}
	}
	if shapes == 0 {
		return fmt.Errorf("%w: shutter has no shape", ErrInvalidShutter)
	}
	if shapes > 3 {
		return fmt.Errorf("%w: Shutter Shape VM is 1-3, got %d shapes", ErrInvalidShutter, shapes)
	}
	return nil
}

// ShutterFromObject builds a Display Shutter from a dataset, returning nil when
// no Shutter Shape is present.
func ShutterFromObject(obj *object.Object) (*Shutter, error) {
	shapes, ok := obj.GetStrings(tagShutterShape)
	if !ok || len(shapes) == 0 {
		for _, tag := range []core.Tag{
			tagShutterLeft, tagShutterRight, tagShutterUpper, tagShutterLower,
			tagShutterCircleCtr, tagShutterCircleRad, tagShutterPolyVerts,
			tagShutterPresValue, tagShutterOverlayGrp, tagShutterPresColor,
		} {
			if obj.Has(tag) {
				return nil, fmt.Errorf("%w: shutter attributes are present without Shutter Shape", ErrInvalidShutter)
			}
		}
		return nil, nil
	}
	sh := &Shutter{}
	if pvalue, ok := rawUint16(obj, tagShutterPresValue); ok {
		sh.PresentationPValue = pvalue
		sh.PresentationPValueDefined = true
		sh.PresentationValue = uint8(math.Round(float64(pvalue) * 255 / 65535))
	}
	if color, ok := rawUint16s(obj, tagShutterPresColor); ok && len(color) == 3 {
		sh.PresentationColorCIELab = [3]uint16{color[0], color[1], color[2]}
		sh.PresentationColorCIELabDefined = true
	} else if obj.Has(tagShutterPresColor) {
		return nil, fmt.Errorf("%w: shutter presentation color requires three CIELab values", ErrInvalidShutter)
	}
	for _, shape := range shapes {
		switch strings.ToUpper(strings.TrimSpace(shape)) {
		case "RECTANGULAR":
			rect, err := rectShutterFromObject(obj)
			if err != nil {
				return nil, err
			}
			sh.Rectangle = rect
		case "CIRCULAR":
			circle, err := circleShutterFromObject(obj)
			if err != nil {
				return nil, err
			}
			sh.Circle = circle
		case "POLYGONAL":
			poly, err := polygonShutterFromObject(obj)
			if err != nil {
				return nil, err
			}
			sh.Polygon = poly
		case "BITMAP":
			bitmap, err := bitmapShutterFromObject(obj)
			if err != nil {
				return nil, err
			}
			sh.Bitmap = bitmap
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedShutter, shape)
		}
	}
	if sh.Rectangle == nil && anyTag(obj, tagShutterLeft, tagShutterRight, tagShutterUpper, tagShutterLower) {
		return nil, fmt.Errorf("%w: rectangular geometry is present without RECTANGULAR Shutter Shape", ErrInvalidShutter)
	}
	if sh.Circle == nil && anyTag(obj, tagShutterCircleCtr, tagShutterCircleRad) {
		return nil, fmt.Errorf("%w: circular geometry is present without CIRCULAR Shutter Shape", ErrInvalidShutter)
	}
	if sh.Polygon == nil && anyTag(obj, tagShutterPolyVerts) {
		return nil, fmt.Errorf("%w: polygon geometry is present without POLYGONAL Shutter Shape", ErrInvalidShutter)
	}
	if sh.Bitmap == nil && anyTag(obj, tagShutterOverlayGrp) {
		return nil, fmt.Errorf("%w: shutter overlay group is present without BITMAP Shutter Shape", ErrInvalidShutter)
	}
	if err := ValidateShutter(sh); err != nil {
		return nil, err
	}
	return sh, nil
}

func anyTag(obj *object.Object, tags ...core.Tag) bool {
	for _, tag := range tags {
		if obj.Has(tag) {
			return true
		}
	}
	return false
}

func rawUint16s(obj *object.Object, tag core.Tag) ([]uint16, bool) {
	raw, ok := obj.GetRaw(tag)
	if !ok || len(raw) == 0 || len(raw)%2 != 0 {
		return nil, false
	}
	values := make([]uint16, len(raw)/2)
	for i := range values {
		values[i] = obj.ValueByteOrder().Uint16(raw[i*2:])
	}
	return values, true
}

func rectShutterFromObject(obj *object.Object) (*RectShutter, error) {
	left, lErr := obj.GetInt(tagShutterLeft)
	right, rErr := obj.GetInt(tagShutterRight)
	upper, uErr := obj.GetInt(tagShutterUpper)
	lower, dErr := obj.GetInt(tagShutterLower)
	if lErr != nil || rErr != nil || uErr != nil || dErr != nil {
		return nil, fmt.Errorf("%w: rectangular shutter requires four edges", ErrInvalidShutter)
	}
	return &RectShutter{Left: int(left), Right: int(right), Upper: int(upper), Lower: int(lower)}, nil
}

func circleShutterFromObject(obj *object.Object) (*CircleShutter, error) {
	center, cErr := obj.GetInts(tagShutterCircleCtr)
	radius, rErr := obj.GetInt(tagShutterCircleRad)
	if cErr != nil || rErr != nil || len(center) < 2 {
		return nil, fmt.Errorf("%w: circular shutter requires center and radius", ErrInvalidShutter)
	}
	return &CircleShutter{CenterRow: int(center[0]), CenterCol: int(center[1]), Radius: int(radius)}, nil
}

func polygonShutterFromObject(obj *object.Object) (*PolygonShutter, error) {
	values, err := obj.GetInts(tagShutterPolyVerts)
	if err != nil || len(values) < 6 || len(values)%2 != 0 {
		return nil, fmt.Errorf("%w: polygonal shutter requires >=3 (row,column) vertices", ErrInvalidShutter)
	}
	verts := make([]ShutterPoint, 0, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		verts = append(verts, ShutterPoint{Row: int(values[i]), Col: int(values[i+1])})
	}
	return &PolygonShutter{Vertices: verts}, nil
}

func bitmapShutterFromObject(obj *object.Object) (*BitmapShutter, error) {
	group, ok := rawUint16(obj, tagShutterOverlayGrp)
	if !ok {
		return nil, fmt.Errorf("%w: bitmap shutter requires Shutter Overlay Group", ErrInvalidShutter)
	}
	if group < overlayGroupFirst || group > overlayGroupLast || group%2 != 0 {
		return nil, fmt.Errorf("%w: invalid shutter overlay group %#04x", ErrInvalidShutter, group)
	}
	overlay, ok := overlayFromGroup(obj, group)
	if !ok {
		return nil, fmt.Errorf("%w: bitmap shutter requires a valid referenced overlay", ErrInvalidShutter)
	}
	required := (overlay.Rows*overlay.Columns + 7) / 8
	if len(overlay.Data) < required {
		return nil, fmt.Errorf("%w: bitmap shutter overlay has %d bytes, requires %d", ErrInvalidShutter, len(overlay.Data), required)
	}
	overlay.Data = overlay.Data[:required]
	return &BitmapShutter{Group: group, Overlay: overlay}, nil
}

// OverlaysFromObject extracts 1-bit overlay planes from the repeating overlay
// groups 0x6000-0x601E. Overlays whose data lives in the image pixel high bits
// (Overlay Bits Allocated != 1) are skipped as an explicit limitation.
func OverlaysFromObject(obj *object.Object) ([]Overlay, error) {
	return overlaysFromObject(obj, nil)
}

func overlaysFromObject(obj *object.Object, skipGroups map[uint16]bool) ([]Overlay, error) {
	if obj == nil {
		return nil, nil
	}
	var overlays []Overlay
	for group := overlayGroupFirst; group <= overlayGroupLast; group += 2 {
		if skipGroups != nil && skipGroups[group] {
			continue
		}
		overlay, ok := overlayFromGroup(obj, group)
		if !ok {
			continue
		}
		overlays = append(overlays, overlay)
	}
	return overlays, nil
}

func overlayFromGroup(obj *object.Object, group uint16) (Overlay, bool) {
	data, ok := obj.GetRaw(core.NewTag(group, tagOverlayData))
	if !ok || len(data) == 0 {
		return Overlay{}, false
	}
	if bits, ok := overlayUint16(obj, group, tagOverlayBitsAlloc); ok && bits != 1 {
		// Overlay data embedded in pixel high bits is not supported here.
		return Overlay{}, false
	}
	rows, rOK := overlayUint16(obj, group, tagOverlayRows)
	cols, cOK := overlayUint16(obj, group, tagOverlayColumns)
	if !rOK || !cOK || rows == 0 || cols == 0 {
		return Overlay{}, false
	}
	originRow, originCol := overlayOrigin(obj, group)
	return Overlay{
		Rows:      int(rows),
		Columns:   int(cols),
		OriginRow: originRow,
		OriginCol: originCol,
		Data:      data,
	}, true
}

// overlayUint16 reads a binary US overlay attribute using the object's value byte order.
func overlayUint16(obj *object.Object, group, element uint16) (uint16, bool) {
	raw, ok := obj.GetRaw(core.NewTag(group, element))
	if !ok || len(raw) < 2 {
		return 0, false
	}
	return obj.ValueByteOrder().Uint16(raw[:2]), true
}

// overlayOrigin reads Overlay Origin (60xx,0050), a signed (row, column) pair.
// It defaults to (1,1) when absent.
func overlayOrigin(obj *object.Object, group uint16) (row, col int) {
	row, col = 1, 1
	raw, ok := obj.GetRaw(core.NewTag(group, tagOverlayOrigin))
	if !ok || len(raw) < 4 {
		return row, col
	}
	order := obj.ValueByteOrder()
	row = int(int16(order.Uint16(raw[0:2])))
	col = int(int16(order.Uint16(raw[2:4])))
	return row, col
}
