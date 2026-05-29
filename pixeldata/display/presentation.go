package display

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
)

var (
	// ErrUnsupportedPresentationLUT reports Presentation LUT content outside the
	// supported Sequence and Shape forms.
	ErrUnsupportedPresentationLUT = errors.New("dicom/display: unsupported Presentation LUT")
	// ErrUnsupportedGSPS reports Grayscale Softcopy Presentation State content
	// that the pipeline does not interpret.
	ErrUnsupportedGSPS = errors.New("dicom/display: Grayscale Softcopy Presentation State is not supported")
)

// Presentation is the presentation-stage transform applied after the VOI
// transform has produced 8-bit MONOCHROME2 display values. It is modeled after
// ModalityLUT and VOILUT so callers can reason about and test each stage
// independently. The stages run in a fixed order:
//
//	VOI output -> Presentation LUT/Shape -> Shutter -> Overlays
//
// GSPS interpretation is an explicit non-goal of this stage (see
// ErrUnsupportedGSPS).
type Presentation struct {
	// LUT maps VOI output values through a Presentation LUT Sequence.
	LUT *LUT
	// Invert inverts the display value (MONOCHROME1, or Presentation LUT Shape
	// INVERSE) after VOI mapping.
	Invert bool
	// Shutter, when set, masks pixels outside the display shutter region.
	Shutter *Shutter
	// Overlays are 1-bit overlay planes burned onto the image last.
	Overlays []Overlay
}

// Apply runs the presentation stages over an 8-bit grayscale image in place.
func (p Presentation) Apply(img *image.Gray) {
	if img == nil {
		return
	}
	if p.LUT != nil {
		applyPresentationLUT(img, p.LUT)
	}
	if p.Invert {
		invertGray(img)
	}
	if p.Shutter != nil {
		p.Shutter.Apply(img)
	}
	for i := range p.Overlays {
		p.Overlays[i].Composite(img, overlayBurnValue)
	}
}

// overlayBurnValue is the grayscale value written where an overlay bit is set.
const overlayBurnValue = 255

func applyPresentationLUT(img *image.Gray, lut *LUT) {
	if lut == nil {
		return
	}
	for i := range img.Pix {
		img.Pix[i] = lutOutputByte(lut, int(img.Pix[i]))
	}
}

func lutOutputByte(lut *LUT, value int) uint8 {
	entry := lut.Lookup(value)
	if lut.BitsPerEntry == 16 {
		return uint8(entry >> 8)
	}
	return uint8(entry)
}

// invertGray inverts every pixel of an 8-bit grayscale image (MONOCHROME1 /
// Presentation LUT Shape INVERSE).
func invertGray(img *image.Gray) {
	for i := range img.Pix {
		img.Pix[i] = 255 - img.Pix[i]
	}
}

// Shutter masks the region outside a display shutter (PS3.3 C.7.6.11). When
// more than one shape is present a pixel is visible only if it lies inside every
// present shape (the shutter region is their intersection); pixels outside the
// region are set to PresentationValue.
type Shutter struct {
	// PresentationValue is the 8-bit grayscale value written to masked-out
	// pixels (derived from Shutter Presentation Value (0018,1622)).
	PresentationValue uint8
	// PresentationPValue preserves the original 16-bit DICOM P-Value when it
	// was explicitly encoded. PresentationValue remains the rendering value.
	PresentationPValue        uint16
	PresentationPValueDefined bool
	// PresentationColorCIELab preserves the optional shutter presentation
	// color. Grayscale rendering continues to use PresentationValue.
	PresentationColorCIELab        [3]uint16
	PresentationColorCIELabDefined bool
	Rectangle                      *RectShutter
	Circle                         *CircleShutter
	Polygon                        *PolygonShutter
	Bitmap                         *BitmapShutter
}

// Clone returns an independent shutter value, including polygon vertices and
// bitmap overlay bytes. It is safe to retain while a caller edits live state.
func (s *Shutter) Clone() *Shutter {
	if s == nil {
		return nil
	}
	out := *s
	if s.Rectangle != nil {
		value := *s.Rectangle
		out.Rectangle = &value
	}
	if s.Circle != nil {
		value := *s.Circle
		out.Circle = &value
	}
	if s.Polygon != nil {
		value := *s.Polygon
		value.Vertices = append([]ShutterPoint(nil), s.Polygon.Vertices...)
		out.Polygon = &value
	}
	if s.Bitmap != nil {
		value := *s.Bitmap
		value.Overlay.Data = append([]byte(nil), s.Bitmap.Overlay.Data...)
		out.Bitmap = &value
	}
	return &out
}

// RectShutter is a rectangular shutter. Edges are 1-based image coordinates as
// stored in DICOM (Shutter Left/Right Vertical Edge = columns, Upper/Lower
// Horizontal Edge = rows); the visible region is inclusive of the edges.
type RectShutter struct {
	Left, Right  int // vertical edges (columns)
	Upper, Lower int // horizontal edges (rows)
}

func (r RectShutter) inside(row, col int) bool {
	c := col + 1 // image column to 1-based DICOM column
	rw := row + 1
	return c >= r.Left && c <= r.Right && rw >= r.Upper && rw <= r.Lower
}

// CircleShutter is a circular shutter. Center is a 1-based (row, column) pair
// (Center of Circular Shutter) and Radius is in pixels.
type CircleShutter struct {
	CenterRow, CenterCol int
	Radius               int
}

func (c CircleShutter) inside(row, col int) bool {
	dr := (row + 1) - c.CenterRow
	dc := (col + 1) - c.CenterCol
	return dr*dr+dc*dc <= c.Radius*c.Radius
}

// PolygonShutter is a polygonal shutter. Vertices are 1-based (row, column)
// points (Vertices of the Polygonal Shutter). The polygon is implicitly closed.
type PolygonShutter struct {
	Vertices []ShutterPoint
}

// ShutterPoint is a 1-based (row, column) vertex.
type ShutterPoint struct {
	Row, Col int
}

// inside reports whether an image pixel lies within the polygon using the
// even-odd ray-casting rule.
func (p PolygonShutter) inside(row, col int) bool {
	if len(p.Vertices) < 3 {
		return false
	}
	r := float64(row + 1)
	c := float64(col + 1)
	in := false
	n := len(p.Vertices)
	j := n - 1
	for i := 0; i < n; i++ {
		ri := float64(p.Vertices[i].Row)
		ci := float64(p.Vertices[i].Col)
		rj := float64(p.Vertices[j].Row)
		cj := float64(p.Vertices[j].Col)
		if (ri > r) != (rj > r) {
			crossCol := (cj-ci)*(r-ri)/(rj-ri) + ci
			if c < crossCol {
				in = !in
			}
		}
		j = i
	}
	return in
}

// BitmapShutter uses a referenced overlay plane as the visible-region mask.
type BitmapShutter struct {
	Group   uint16
	Overlay Overlay
}

func (b BitmapShutter) inside(row, col int) bool {
	overlayRow := row - (b.Overlay.OriginRow - 1)
	overlayCol := col - (b.Overlay.OriginCol - 1)
	if overlayRow < 0 || overlayRow >= b.Overlay.Rows || overlayCol < 0 || overlayCol >= b.Overlay.Columns {
		return false
	}
	return b.Overlay.isSet(overlayRow, overlayCol)
}

// visible reports whether a pixel lies inside every present shutter shape.
func (s *Shutter) visible(row, col int) bool {
	if s.Rectangle != nil && !s.Rectangle.inside(row, col) {
		return false
	}
	if s.Circle != nil && !s.Circle.inside(row, col) {
		return false
	}
	if s.Polygon != nil && !s.Polygon.inside(row, col) {
		return false
	}
	if s.Bitmap != nil && !s.Bitmap.inside(row, col) {
		return false
	}
	return true
}

// Apply masks every pixel outside the shutter region in place.
func (s *Shutter) Apply(img *image.Gray) {
	if s == nil || img == nil {
		return
	}
	// A shutter with no shapes masks nothing.
	if s.Rectangle == nil && s.Circle == nil && s.Polygon == nil && s.Bitmap == nil {
		return
	}
	bounds := img.Bounds()
	for row := 0; row < bounds.Dy(); row++ {
		for col := 0; col < bounds.Dx(); col++ {
			if !s.visible(row, col) {
				img.Pix[row*img.Stride+col] = s.PresentationValue
			}
		}
	}
}

// ApplyTo returns a shuttered copy of src without mutating render caches owned
// by the caller. Grayscale inputs remain grayscale; other image types are
// copied to RGBA and masked with an equivalent neutral gray.
func (s *Shutter) ApplyTo(src image.Image) image.Image {
	if s == nil || src == nil ||
		(s.Rectangle == nil && s.Circle == nil && s.Polygon == nil && s.Bitmap == nil) {
		return src
	}
	bounds := src.Bounds()
	if gray, ok := src.(*image.Gray); ok {
		out := image.NewGray(bounds)
		draw.Draw(out, bounds, gray, bounds.Min, draw.Src)
		s.Apply(out)
		return out
	}
	out := image.NewRGBA(bounds)
	draw.Draw(out, bounds, src, bounds.Min, draw.Src)
	masked := color.Gray{Y: s.PresentationValue}
	for row := 0; row < bounds.Dy(); row++ {
		for col := 0; col < bounds.Dx(); col++ {
			if !s.visible(row, col) {
				out.Set(bounds.Min.X+col, bounds.Min.Y+row, masked)
			}
		}
	}
	return out
}

// Overlay is a 1-bit DICOM overlay plane (repeating group 0x6000-0x601E,
// PS3.3 C.9 / PS3.5 8.1.2). Data is bit-packed row-major, least-significant bit
// first. Origin is the 1-based (row, column) position of the overlay's top-left
// pixel within the image (Overlay Origin (60xx,0050)).
type Overlay struct {
	Rows, Columns int
	OriginRow     int
	OriginCol     int
	Data          []byte
}

// isSet reports whether the overlay bit at (row, col) within the overlay plane
// is set.
func (o Overlay) isSet(row, col int) bool {
	idx := row*o.Columns + col
	byteIdx := idx / 8
	if byteIdx < 0 || byteIdx >= len(o.Data) {
		return false
	}
	return o.Data[byteIdx]&(1<<uint(idx%8)) != 0
}

// Composite burns the overlay onto img, writing value at every set overlay bit
// that falls within the image bounds.
func (o Overlay) Composite(img *image.Gray, value uint8) {
	if img == nil || o.Rows <= 0 || o.Columns <= 0 {
		return
	}
	bounds := img.Bounds()
	for r := 0; r < o.Rows; r++ {
		for c := 0; c < o.Columns; c++ {
			if !o.isSet(r, c) {
				continue
			}
			imgRow := (o.OriginRow - 1) + r
			imgCol := (o.OriginCol - 1) + c
			if imgRow < 0 || imgRow >= bounds.Dy() || imgCol < 0 || imgCol >= bounds.Dx() {
				continue
			}
			img.Pix[imgRow*img.Stride+imgCol] = value
		}
	}
}
