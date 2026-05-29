package display

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

func grayFromRows(rows [][]uint8) *image.Gray {
	h := len(rows)
	w := len(rows[0])
	img := image.NewGray(image.Rect(0, 0, w, h))
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			img.Pix[r*img.Stride+c] = rows[r][c]
		}
	}
	return img
}

func TestPresentationInvertAfterVOI(t *testing.T) {
	// Render a window that yields [0,255], then invert (MONOCHROME1 / INVERSE).
	img, err := RenderGray(Frame{
		Rows:    1,
		Columns: 2,
		Pixels:  []byte{0, 255},
		Format:  PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		VOI:     VOILUT{Center: 128, Width: 256},
	})
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[0] != 0 || img.Pix[1] != 255 {
		t.Fatalf("pre-invert pixels = %v, want [0 255]", img.Pix)
	}
	Presentation{Invert: true}.Apply(img)
	if img.Pix[0] != 255 || img.Pix[1] != 0 {
		t.Fatalf("post-invert pixels = %v, want [255 0]", img.Pix)
	}
}

func TestOverlayComposite(t *testing.T) {
	// 2x2 overlay with bits set at (0,0) and (1,1); bit index = row*cols+col,
	// LSB first: bit 0 and bit 3 -> 0b00001001 = 0x09.
	overlay := Overlay{Rows: 2, Columns: 2, OriginRow: 1, OriginCol: 1, Data: []byte{0x09}}
	img := image.NewGray(image.Rect(0, 0, 4, 4))
	overlay.Composite(img, 255)
	if img.Pix[0*img.Stride+0] != 255 {
		t.Errorf("overlay (0,0) not burned")
	}
	if img.Pix[1*img.Stride+1] != 255 {
		t.Errorf("overlay (1,1) not burned")
	}
	if img.Pix[0*img.Stride+1] != 0 || img.Pix[1*img.Stride+0] != 0 {
		t.Errorf("unset overlay bits should not be burned")
	}
}

func TestOverlayCompositeRespectsOrigin(t *testing.T) {
	// 1x1 overlay with its single bit set, placed at image (2,3) (1-based 3,4).
	overlay := Overlay{Rows: 1, Columns: 1, OriginRow: 3, OriginCol: 4, Data: []byte{0x01}}
	img := image.NewGray(image.Rect(0, 0, 5, 5))
	overlay.Composite(img, 200)
	if img.Pix[2*img.Stride+3] != 200 {
		t.Errorf("overlay not burned at origin-shifted location")
	}
	if img.Pix[0] != 0 {
		t.Errorf("origin-shifted overlay should not touch (0,0)")
	}
}

func TestOverlaysFromObjectUsesObjectValueByteOrder(t *testing.T) {
	const group = uint16(0x6000)
	obj := readBigEndianDisplayObject(t,
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayRows), core.VRUS, binary.BigEndian, 512),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayColumns), core.VRUS, binary.BigEndian, 258),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayBitsAlloc), core.VRUS, binary.BigEndian, 1),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayOrigin), core.VRSS, binary.BigEndian, 2, 3),
		dicomtest.NewOBElement(core.NewTag(group, tagOverlayData), []byte{0x80}),
	)

	overlays, err := OverlaysFromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlays) != 1 {
		t.Fatalf("overlay count = %d, want 1", len(overlays))
	}
	overlay := overlays[0]
	if overlay.Rows != 512 || overlay.Columns != 258 || overlay.OriginRow != 2 || overlay.OriginCol != 3 {
		t.Fatalf("overlay = %#v, want big-endian geometry and origin decoded", overlay)
	}
}

func TestRectangularShutterMasks(t *testing.T) {
	img := grayFromRows([][]uint8{
		{200, 200, 200, 200, 200},
		{200, 200, 200, 200, 200},
		{200, 200, 200, 200, 200},
		{200, 200, 200, 200, 200},
		{200, 200, 200, 200, 200},
	})
	sh := &Shutter{PresentationValue: 0, Rectangle: &RectShutter{Left: 2, Right: 4, Upper: 2, Lower: 4}}
	sh.Apply(img)
	// Corner (0,0) outside -> masked.
	if img.Pix[0] != 0 {
		t.Errorf("corner = %d, want masked 0", img.Pix[0])
	}
	// Center pixel (2,2) inside -> preserved.
	if img.Pix[2*img.Stride+2] != 200 {
		t.Errorf("center = %d, want preserved 200", img.Pix[2*img.Stride+2])
	}
}

func TestShutterApplyToCopiesSourceAndSupportsColorImages(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			source.SetRGBA(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	shutter := &Shutter{
		PresentationValue: 17,
		Rectangle:         &RectShutter{Left: 2, Right: 2, Upper: 2, Lower: 2},
	}
	masked := shutter.ApplyTo(source)
	if masked == source {
		t.Fatal("ApplyTo returned the mutable source image")
	}
	if got := color.GrayModel.Convert(masked.At(0, 0)).(color.Gray).Y; got != 17 {
		t.Fatalf("masked corner = %d, want 17", got)
	}
	if got := source.RGBAAt(0, 0); got != (color.RGBA{R: 200, G: 100, B: 50, A: 255}) {
		t.Fatalf("source mutated to %+v", got)
	}
}

func TestCircularShutterMasks(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 11, 11))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	sh := &Shutter{PresentationValue: 0, Circle: &CircleShutter{CenterRow: 6, CenterCol: 6, Radius: 3}}
	sh.Apply(img)
	if img.Pix[5*img.Stride+5] != 200 {
		t.Errorf("circle center = %d, want preserved 200", img.Pix[5*img.Stride+5])
	}
	if img.Pix[0] != 0 {
		t.Errorf("far corner = %d, want masked 0", img.Pix[0])
	}
}

func TestPolygonShutterMasks(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 6, 6))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	// Right triangle with vertices (row,col) = (1,1),(1,5),(5,1); interior is
	// row+col <= 6.
	sh := &Shutter{PresentationValue: 0, Polygon: &PolygonShutter{Vertices: []ShutterPoint{
		{Row: 1, Col: 1}, {Row: 1, Col: 5}, {Row: 5, Col: 1},
	}}}
	sh.Apply(img)
	// (1,1) image -> (2,2) 1-based -> 4 <= 6 inside.
	if img.Pix[1*img.Stride+1] != 200 {
		t.Errorf("polygon interior = %d, want preserved 200", img.Pix[1*img.Stride+1])
	}
	// (4,4) image -> (5,5) 1-based -> 10 > 6 outside.
	if img.Pix[4*img.Stride+4] != 0 {
		t.Errorf("polygon exterior = %d, want masked 0", img.Pix[4*img.Stride+4])
	}
}

func TestShutterIntersection(t *testing.T) {
	// Rect + circle: a pixel must be inside both to be visible.
	img := image.NewGray(image.Rect(0, 0, 11, 11))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	sh := &Shutter{
		Rectangle: &RectShutter{Left: 1, Right: 11, Upper: 6, Lower: 6}, // only row 6 (1-based)
		Circle:    &CircleShutter{CenterRow: 6, CenterCol: 6, Radius: 3},
	}
	sh.Apply(img)
	// (5,5) image -> 1-based (6,6): inside circle and on rect row -> visible.
	if img.Pix[5*img.Stride+5] != 200 {
		t.Errorf("intersection-visible pixel masked")
	}
	// (4,5) image -> 1-based (5,6): inside circle but not rect row 6 -> masked.
	if img.Pix[4*img.Stride+5] != 0 {
		t.Errorf("pixel outside rect should be masked even if inside circle")
	}
}

func TestPresentationFromObjectInverse(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagPresentationLUTShape, core.VRCS, "INVERSE"),
	}, nil)
	pres, err := PresentationFromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if !pres.Invert {
		t.Fatal("Presentation.Invert = false, want true for INVERSE shape")
	}
}

func TestPresentationFromObjectAppliesPresentationLUTSequence(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		dicomtest.Uint16Element(tagLUTDescriptor, core.VRUS, nil, 3, 0, 8),
		dicomtest.Uint16Element(tagLUTData, core.VRUS, nil, 10, 20, 30),
	}}
	obj := object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(tagPresentationLUTSequence, item),
	}, nil)
	pres, err := PresentationFromObject(obj)
	if err != nil {
		t.Fatalf("PresentationFromObject() error = %v", err)
	}

	img := grayFromRows([][]uint8{{0, 1, 2}})
	pres.Apply(img)
	if got, want := img.Pix[:3], []uint8{10, 20, 30}; !bytes.Equal(got, want) {
		t.Fatalf("Presentation.Apply() pixels = %v, want %v", got, want)
	}
}

func TestPresentationFromObjectBitmapShutterMasksWithReferencedOverlay(t *testing.T) {
	const group = uint16(0x6000)
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagShutterShape, core.VRCS, "BITMAP"),
		dicomtest.Uint16Element(core.NewTag(0x0018, 0x1623), core.VRUS, nil, group),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayRows), core.VRUS, nil, 2),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayColumns), core.VRUS, nil, 2),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayBitsAlloc), core.VRUS, nil, 1),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayOrigin), core.VRSS, nil, 1, 1),
		dicomtest.NewOBElement(core.NewTag(group, tagOverlayData), []byte{0x09}),
	}, nil)

	pres, err := PresentationFromObject(obj)
	if err != nil {
		t.Fatalf("PresentationFromObject() error = %v", err)
	}
	img := grayFromRows([][]uint8{
		{10, 20},
		{30, 40},
	})
	pres.Apply(img)

	if got, want := img.Pix[:4], []uint8{10, 0, 0, 40}; !bytes.Equal(got, want) {
		t.Fatalf("Presentation.Apply() pixels = %v, want %v", got, want)
	}
}

func TestPresentationFromObjectUnsupportedGSPS(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewUIElement(tagSOPClassUID, "1.2.840.10008.5.1.4.1.1.11.1"),
	}, nil)
	if _, err := PresentationFromObject(obj); !errors.Is(err, ErrUnsupportedGSPS) {
		t.Fatalf("error = %v, want ErrUnsupportedGSPS", err)
	}
}

func TestShutterFromObjectRectangular(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagShutterShape, core.VRCS, "RECTANGULAR"),
		dicomtest.NewStringElement(tagShutterLeft, core.VRIS, "2"),
		dicomtest.NewStringElement(tagShutterRight, core.VRIS, "8"),
		dicomtest.NewStringElement(tagShutterUpper, core.VRIS, "3"),
		dicomtest.NewStringElement(tagShutterLower, core.VRIS, "9"),
	}, nil)
	sh, err := ShutterFromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if sh == nil || sh.Rectangle == nil {
		t.Fatal("rectangular shutter not parsed")
	}
	if sh.Rectangle.Left != 2 || sh.Rectangle.Right != 8 || sh.Rectangle.Upper != 3 || sh.Rectangle.Lower != 9 {
		t.Fatalf("rect = %#v", sh.Rectangle)
	}
}

func TestShutterFromObjectCircularAndPolygon(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.StringsElement(tagShutterShape, core.VRCS, "CIRCULAR", "POLYGONAL"),
		dicomtest.StringsElement(tagShutterCircleCtr, core.VRIS, "6", "6"),
		dicomtest.NewStringElement(tagShutterCircleRad, core.VRIS, "3"),
		dicomtest.StringsElement(tagShutterPolyVerts, core.VRIS, "1", "1", "1", "5", "5", "1"),
	}, nil)
	sh, err := ShutterFromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Circle == nil || sh.Circle.CenterRow != 6 || sh.Circle.Radius != 3 {
		t.Fatalf("circle = %#v", sh.Circle)
	}
	if sh.Polygon == nil || len(sh.Polygon.Vertices) != 3 {
		t.Fatalf("polygon = %#v", sh.Polygon)
	}
}

func TestShutterFromObjectInvalidGeometry(t *testing.T) {
	// Shape named RECTANGULAR but edges missing -> explicit error.
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagShutterShape, core.VRCS, "RECTANGULAR"),
	}, nil)
	if _, err := ShutterFromObject(obj); !errors.Is(err, ErrInvalidShutter) {
		t.Fatalf("error = %v, want ErrInvalidShutter", err)
	}
}

func TestShutterFromObjectRejectsGeometryForUnannouncedShape(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagShutterShape, core.VRCS, "RECTANGULAR"),
		dicomtest.NewStringElement(tagShutterLeft, core.VRIS, "1"),
		dicomtest.NewStringElement(tagShutterRight, core.VRIS, "8"),
		dicomtest.NewStringElement(tagShutterUpper, core.VRIS, "1"),
		dicomtest.NewStringElement(tagShutterLower, core.VRIS, "8"),
		dicomtest.NewStringElement(tagShutterCircleRad, core.VRIS, "3"),
	}, nil)
	if _, err := ShutterFromObject(obj); !errors.Is(err, ErrInvalidShutter) {
		t.Fatalf("error = %v, want ErrInvalidShutter for silently discardable circular geometry", err)
	}
}

func TestValidateShutterRejectsNonRepresentableGeometry(t *testing.T) {
	cases := []*Shutter{
		{},
		{Circle: &CircleShutter{Radius: 0}},
		{Polygon: &PolygonShutter{Vertices: []ShutterPoint{{Row: 1, Col: 1}, {Row: 2, Col: 2}}}},
		{Bitmap: &BitmapShutter{Group: 0x6001, Overlay: Overlay{Rows: 1, Columns: 1, Data: []byte{1}}}},
		{
			Rectangle: &RectShutter{Left: 1, Right: 2, Upper: 1, Lower: 2},
			Circle:    &CircleShutter{Radius: 1},
			Polygon:   &PolygonShutter{Vertices: []ShutterPoint{{Row: 1, Col: 1}, {Row: 1, Col: 2}, {Row: 2, Col: 1}}},
			Bitmap:    &BitmapShutter{Group: 0x6000, Overlay: Overlay{Rows: 1, Columns: 1, OriginRow: 1, OriginCol: 1, Data: []byte{1}}},
		},
	}
	for index, shutter := range cases {
		if err := ValidateShutter(shutter); !errors.Is(err, ErrInvalidShutter) {
			t.Fatalf("case %d error = %v, want ErrInvalidShutter", index, err)
		}
	}
}

func TestOverlaysFromObject(t *testing.T) {
	group := uint16(0x6000)
	obj := object.FromElements([]core.Element{
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayRows), core.VRUS, nil, 2),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayColumns), core.VRUS, nil, 2),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayBitsAlloc), core.VRUS, nil, 1),
		dicomtest.Uint16Element(core.NewTag(group, tagOverlayOrigin), core.VRSS, nil, 1, 1),
		dicomtest.NewOBElement(core.NewTag(group, tagOverlayData), []byte{0x09}),
	}, nil)
	overlays, err := OverlaysFromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlays) != 1 {
		t.Fatalf("overlay count = %d, want 1", len(overlays))
	}
	o := overlays[0]
	if o.Rows != 2 || o.Columns != 2 || o.OriginRow != 1 || o.OriginCol != 1 {
		t.Fatalf("overlay = %#v", o)
	}
	if !o.isSet(0, 0) || !o.isSet(1, 1) || o.isSet(0, 1) {
		t.Fatalf("overlay bits mismatch")
	}
}
