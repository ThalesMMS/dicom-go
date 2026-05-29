// Package jpeglossless decodes DICOM JPEG Lossless (ITU-T T.81 process 14,
// SOF3) encapsulated pixel data: Huffman entropy coding with first-order
// (and general 1–7) prediction. It targets single-component grayscale CT/MR at
// 8–16 bits, which is what those transfer syntaxes carry in practice, so such
// series can form a volume in the viewer's MPR/3D model.
//
// Out of scope (return a typed error): multi-component scans, hierarchical
// (SOF55/JPEG-LS) and wavelet (JPEG 2000) syntaxes — those need a different
// codec or an external pipeline.
package jpeglossless

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// DICOM JPEG Lossless transfer syntax UIDs.
const (
	// UIDProcess14 is "JPEG Lossless, Non-Hierarchical (Process 14)".
	UIDProcess14 = "1.2.840.10008.1.2.4.57"
	// UIDProcess14SV1 is "JPEG Lossless, Non-Hierarchical, First-Order
	// Prediction (Process 14 [Selection Value 1])".
	UIDProcess14SV1 = "1.2.840.10008.1.2.4.70"
)

var (
	ErrInvalidStream            = errors.New("dicom: invalid JPEG Lossless stream")
	ErrUnsupportedScan          = errors.New("dicom: unsupported JPEG Lossless scan")
	ErrImageSizeMismatch        = errors.New("dicom: JPEG Lossless image size does not match metadata")
	ErrUnsupportedBitsAllocated = errors.New("dicom: unsupported JPEG Lossless BitsAllocated")
)

// Codec decodes DICOM JPEG Lossless encapsulated pixel data.
type Codec struct {
	selectionValue int
}

// New returns a JPEG Lossless codec.
func New() *Codec { return &Codec{} }

func newSV1() *Codec { return &Codec{selectionValue: 1} }

// Register registers the codec for both JPEG Lossless UIDs in the registry.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	if err := registry.RegisterCodec(UIDProcess14, New()); err != nil {
		return err
	}
	return registry.RegisterCodec(UIDProcess14SV1, newSV1())
}

// RegisterDefault registers the codec in pixeldata.DefaultRegistry.
func RegisterDefault() error {
	if err := pixeldata.RegisterCodec(UIDProcess14, New()); err != nil {
		return err
	}
	return pixeldata.RegisterCodec(UIDProcess14SV1, newSV1())
}

// Decode implements pixeldata.Codec.
func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG Lossless requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}
	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	if metadata.SamplesPerPixel != 1 {
		return pixeldata.Frames{}, fmt.Errorf("%w: SamplesPerPixel=%d (only grayscale)", ErrUnsupportedScan, metadata.SamplesPerPixel)
	}
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" {
		return pixeldata.Frames{}, fmt.Errorf("%w: PhotometricInterpretation=%s", pixeldata.ErrUnsupportedPhotometricInterpretation, strings.TrimSpace(metadata.PhotometricInterpretation))
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return pixeldata.Frames{}, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	fragments := pixel.Sequence.Fragments
	if len(fragments) == 0 {
		return pixeldata.Frames{}, fmt.Errorf("%w: no frame fragments", ErrInvalidStream)
	}
	if metadata.NumberOfFrames != len(fragments) {
		return pixeldata.Frames{}, fmt.Errorf("%w: NumberOfFrames=%d fragments=%d", pixeldata.ErrPixelDataSizeMismatch, metadata.NumberOfFrames, len(fragments))
	}

	rows := int(metadata.Rows)
	cols := int(metadata.Columns)
	frames := make([][]byte, len(fragments))
	for i, fragment := range fragments {
		img, err := decodeFrame(fragment, c.selectionValue)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrInvalidStream, i, err)
		}
		if img.width != cols || img.height != rows {
			return pixeldata.Frames{}, fmt.Errorf("%w: got %dx%d want %dx%d", ErrImageSizeMismatch, img.width, img.height, cols, rows)
		}
		frames[i] = img.toBytes(metadata.BitsAllocated)
	}
	return pixeldata.Frames{Rows: rows, Columns: cols, Data: frames}, nil
}

// decodedImage holds reconstructed samples (row-major).
type decodedImage struct {
	width, height  int
	precision      int
	pointTransform int
	samples        []int32
}

// toBytes packs samples to native frame bytes: 8-bit as one byte each, 16-bit as
// little-endian pairs (matching dicom-go's native frame layout).
func (d *decodedImage) toBytes(bitsAllocated uint16) []byte {
	if bitsAllocated == 8 {
		out := make([]byte, len(d.samples))
		for i, s := range d.samples {
			out[i] = byte(s << uint(d.pointTransform))
		}
		return out
	}
	out := make([]byte, len(d.samples)*2)
	for i, s := range d.samples {
		sample := s << uint(d.pointTransform)
		out[i*2] = byte(sample)
		out[i*2+1] = byte(sample >> 8)
	}
	return out
}

// ---- marker parsing ---------------------------------------------------------

const (
	markerSOI  = 0xD8
	markerEOI  = 0xD9
	markerSOF3 = 0xC3 // lossless, Huffman
	markerDHT  = 0xC4
	markerSOS  = 0xDA
	markerDRI  = 0xDD
)

type frameHeader struct {
	precision int
	height    int
	width     int
	component int // single component id
}

func decodeFrame(data []byte, selectionValue int) (*decodedImage, error) {
	p := 0
	if len(data) < 2 || data[0] != 0xFF || data[1] != markerSOI {
		return nil, fmt.Errorf("%w: missing SOI", ErrInvalidStream)
	}
	p = 2

	var frame frameHeader
	haveFrame := false
	huff := map[int]*huffTable{} // by table id (Td)
	restartInterval := 0

	for p+1 < len(data) {
		if data[p] != 0xFF {
			return nil, fmt.Errorf("%w: expected marker at %d", ErrInvalidStream, p)
		}
		marker := data[p+1]
		p += 2
		switch marker {
		case markerSOI:
			continue
		case markerEOI:
			return nil, fmt.Errorf("%w: EOI before scan", ErrInvalidStream)
		case markerSOF3:
			fh, n, err := parseSOF3(data[p:])
			if err != nil {
				return nil, err
			}
			frame = fh
			haveFrame = true
			p += n
		case markerDHT:
			n, err := parseDHT(data[p:], huff)
			if err != nil {
				return nil, err
			}
			p += n
		case markerDRI:
			if p+4 > len(data) {
				return nil, fmt.Errorf("%w: short DRI", ErrInvalidStream)
			}
			restartInterval = int(data[p+2])<<8 | int(data[p+3])
			p += int(data[p])<<8 | int(data[p+1])
		case markerSOS:
			if !haveFrame {
				return nil, fmt.Errorf("%w: SOS before SOF3", ErrInvalidStream)
			}
			return decodeScan(data, p, frame, huff, restartInterval, selectionValue)
		default:
			// Skip any other marker segment using its length.
			if p+2 > len(data) {
				return nil, fmt.Errorf("%w: truncated segment", ErrInvalidStream)
			}
			seg := int(data[p])<<8 | int(data[p+1])
			if seg < 2 {
				return nil, fmt.Errorf("%w: bad segment length", ErrInvalidStream)
			}
			p += seg
		}
	}
	return nil, fmt.Errorf("%w: no scan found", ErrInvalidStream)
}

func parseSOF3(seg []byte) (frameHeader, int, error) {
	if len(seg) < 2 {
		return frameHeader{}, 0, fmt.Errorf("%w: short SOF3", ErrInvalidStream)
	}
	length := int(seg[0])<<8 | int(seg[1])
	if length < 8 || length > len(seg) {
		return frameHeader{}, 0, fmt.Errorf("%w: bad SOF3 length", ErrInvalidStream)
	}
	precision := int(seg[2])
	if precision < 1 || precision > 16 {
		return frameHeader{}, 0, fmt.Errorf("%w: SOF3 precision=%d", ErrInvalidStream, precision)
	}
	height := int(seg[3])<<8 | int(seg[4])
	width := int(seg[5])<<8 | int(seg[6])
	nf := int(seg[7])
	if nf != 1 {
		return frameHeader{}, 0, fmt.Errorf("%w: %d components (only grayscale)", ErrUnsupportedScan, nf)
	}
	if length < 8+3 {
		return frameHeader{}, 0, fmt.Errorf("%w: short SOF3 component", ErrInvalidStream)
	}
	componentID := int(seg[8])
	return frameHeader{precision: precision, height: height, width: width, component: componentID}, length, nil
}

func parseDHT(seg []byte, tables map[int]*huffTable) (int, error) {
	if len(seg) < 2 {
		return 0, fmt.Errorf("%w: short DHT", ErrInvalidStream)
	}
	length := int(seg[0])<<8 | int(seg[1])
	if length < 2 || length > len(seg) {
		return 0, fmt.Errorf("%w: bad DHT length", ErrInvalidStream)
	}
	pos := 2
	for pos < length {
		if pos+17 > length {
			return 0, fmt.Errorf("%w: truncated DHT", ErrInvalidStream)
		}
		tcth := seg[pos]
		tableID := int(tcth & 0x0F)
		pos++
		var counts [17]int
		total := 0
		for i := 1; i <= 16; i++ {
			counts[i] = int(seg[pos])
			total += counts[i]
			pos++
		}
		if pos+total > length {
			return 0, fmt.Errorf("%w: truncated DHT values", ErrInvalidStream)
		}
		values := make([]byte, total)
		copy(values, seg[pos:pos+total])
		pos += total
		tables[tableID] = buildHuffTable(counts, values)
	}
	return length, nil
}

// ---- scan decoding ----------------------------------------------------------

func decodeScan(data []byte, p int, frame frameHeader, huff map[int]*huffTable, restartInterval, selectionValue int) (*decodedImage, error) {
	if p+2 > len(data) {
		return nil, fmt.Errorf("%w: short SOS", ErrInvalidStream)
	}
	length := int(data[p])<<8 | int(data[p+1])
	if length < 6 || p+length > len(data) {
		return nil, fmt.Errorf("%w: bad SOS length", ErrInvalidStream)
	}
	ns := int(data[p+2])
	if ns != 1 {
		return nil, fmt.Errorf("%w: %d scan components", ErrUnsupportedScan, ns)
	}
	tableID := int(data[p+4] >> 4) // Td (DC table selector reused as the lossless table)
	predictor := int(data[p+length-3])
	pointTransform := int(data[p+length-1] & 0x0F)
	table, ok := huff[tableID]
	if !ok {
		return nil, fmt.Errorf("%w: missing Huffman table %d", ErrInvalidStream, tableID)
	}
	if predictor < 1 || predictor > 7 {
		// Predictor 0 is "no prediction" (not used for SOF3 scans); reject.
		return nil, fmt.Errorf("%w: predictor=%d", ErrUnsupportedScan, predictor)
	}
	if selectionValue != 0 && predictor != selectionValue {
		return nil, fmt.Errorf("%w: predictor=%d selection value=%d", ErrUnsupportedScan, predictor, selectionValue)
	}
	if pointTransform >= frame.precision {
		return nil, fmt.Errorf("%w: point transform=%d precision=%d", ErrInvalidStream, pointTransform, frame.precision)
	}

	br := &bitReader{data: data, pos: p + length}
	w, h := frame.width, frame.height
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: dimensions %dx%d", ErrInvalidStream, w, h)
	}
	samples := make([]int32, w*h)
	defaultPred := int32(1) << (frame.precision - pointTransform - 1)
	restartCount := 0
	restartStart := 0

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sampleIndex := y*w + x
			s, err := table.decodeSymbol(br)
			if err != nil {
				return nil, err
			}
			diff, err := receiveExtend(br, int(s))
			if err != nil {
				return nil, err
			}
			px := predict(samples, x, y, w, predictor, defaultPred)
			if sampleIndex == restartStart {
				px = defaultPred
			}
			val := (px + diff) & ((1 << frame.precision) - 1)
			samples[sampleIndex] = val

			if restartInterval > 0 {
				restartCount++
				if restartCount == restartInterval && !(y == h-1 && x == w-1) {
					if err := br.alignToRestart(); err != nil {
						return nil, err
					}
					restartCount = 0
					restartStart = sampleIndex + 1
				}
			}
		}
	}

	return &decodedImage{width: w, height: h, precision: frame.precision, pointTransform: pointTransform, samples: samples}, nil
}

// predict returns the lossless predictor value for sample (x,y).
func predict(samples []int32, x, y, w, predictor int, defaultPred int32) int32 {
	if x == 0 && y == 0 {
		return defaultPred
	}
	ra := func() int32 { return samples[y*w+(x-1)] }
	rb := func() int32 { return samples[(y-1)*w+x] }
	rc := func() int32 { return samples[(y-1)*w+(x-1)] }
	if y == 0 {
		return ra()
	}
	if x == 0 {
		return rb()
	}
	a, b, c := ra(), rb(), rc()
	switch predictor {
	case 1:
		return a
	case 2:
		return b
	case 3:
		return c
	case 4:
		return a + b - c
	case 5:
		return a + ((b - c) >> 1)
	case 6:
		return b + ((a - c) >> 1)
	case 7:
		return (a + b) / 2
	default:
		return a
	}
}

// receiveExtend reads s bits and sign-extends per the JPEG DIFF convention.
func receiveExtend(br *bitReader, s int) (int32, error) {
	if s == 0 {
		return 0, nil
	}
	if s == 16 {
		// Special case: DIFF is exactly 32768 (T.81 lossless).
		return 32768, nil
	}
	v, err := br.readBits(s)
	if err != nil {
		return 0, err
	}
	val := int32(v)
	if val < (1 << (s - 1)) {
		val += (-1 << s) + 1
	}
	return val, nil
}
