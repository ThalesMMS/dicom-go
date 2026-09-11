// Package jpeglossless decodes DICOM JPEG Lossless (ITU-T T.81 process 14,
// SOF3) encapsulated pixel data with Huffman entropy coding and predictors 1–7.
package jpeglossless

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	encapframes "github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
)

const (
	UIDProcess14    = "1.2.840.10008.1.2.4.57"
	UIDProcess14SV1 = "1.2.840.10008.1.2.4.70"

	maxJPEGLosslessRequestBytes = uint64(512 << 20)
)

var (
	ErrInvalidStream            = errors.New("dicom: invalid JPEG Lossless stream")
	ErrUnsupportedScan          = errors.New("dicom: unsupported JPEG Lossless scan")
	ErrImageSizeMismatch        = errors.New("dicom: JPEG Lossless image size does not match metadata")
	ErrUnsupportedBitsAllocated = errors.New("dicom: unsupported JPEG Lossless BitsAllocated")
)

// Codec decodes DICOM JPEG Lossless encapsulated pixel data. It is immutable
// after construction and safe for concurrent use.
type Codec struct {
	selectionValue int
}

func New() *Codec { return &Codec{} }

func newSV1() *Codec { return &Codec{selectionValue: 1} }

func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	if err := registry.RegisterCodec(UIDProcess14, New()); err != nil {
		return err
	}
	return registry.RegisterCodec(UIDProcess14SV1, newSV1())
}

func RegisterDefault() error {
	if err := pixeldata.RegisterCodec(UIDProcess14, New()); err != nil {
		return err
	}
	return pixeldata.RegisterCodec(UIDProcess14SV1, newSV1())
}

func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG Lossless requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}
	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	if err := validateJPEGLosslessMetadata(metadata); err != nil {
		return pixeldata.Frames{}, err
	}
	codestreams, err := encapframes.FromFragments(context.Background(), pixel.Sequence, obj, metadata.NumberOfFrames, encapframes.JPEG, encapframes.Limits{})
	if err != nil {
		if errors.Is(err, encapframes.ErrFrameCount) {
			err = errors.Join(pixeldata.ErrPixelDataSizeMismatch, err)
		}
		return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrInvalidStream, err)
	}
	if err := validateJPEGLosslessRequest(metadata, pixel.Sequence.Fragments, codestreams.MaxFrameBytes()); err != nil {
		return pixeldata.Frames{}, err
	}

	rows := int(metadata.Rows)
	columns := int(metadata.Columns)
	frames := make([][]byte, codestreams.Len())
	for index := range frames {
		view, err := codestreams.Frame(context.Background(), index)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrInvalidStream, err)
		}
		image, err := decodeFrame(view.Data, c.selectionValue, metadata)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrInvalidStream, index, err)
		}
		if image.width != columns || image.height != rows {
			return pixeldata.Frames{}, fmt.Errorf("%w: got %dx%d want %dx%d", ErrImageSizeMismatch, image.width, image.height, columns, rows)
		}
		if image.precision != int(metadata.BitsStored) {
			return pixeldata.Frames{}, fmt.Errorf("%w: precision=%d BitsStored=%d", ErrImageSizeMismatch, image.precision, metadata.BitsStored)
		}
		if len(image.planes) != int(metadata.SamplesPerPixel) {
			return pixeldata.Frames{}, fmt.Errorf("%w: components=%d SamplesPerPixel=%d", ErrImageSizeMismatch, len(image.planes), metadata.SamplesPerPixel)
		}
		frames[index] = image.toInterleavedBytes(metadata.BitsAllocated)
	}
	return pixeldata.Frames{Rows: rows, Columns: columns, Data: frames}, nil
}

func validateJPEGLosslessMetadata(metadata pixeldata.Metadata) error {
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated || metadata.HighBit != metadata.BitsStored-1 {
		return fmt.Errorf("%w: invalid BitsStored/HighBit", ErrUnsupportedBitsAllocated)
	}
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	switch metadata.SamplesPerPixel {
	case 1:
		if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" {
			return fmt.Errorf("%w: PhotometricInterpretation=%s", pixeldata.ErrUnsupportedPhotometricInterpretation, strings.TrimSpace(metadata.PhotometricInterpretation))
		}
	case 3:
		if photometric != "RGB" && photometric != "YBR_FULL" {
			return fmt.Errorf("%w: PhotometricInterpretation=%s", pixeldata.ErrUnsupportedPhotometricInterpretation, strings.TrimSpace(metadata.PhotometricInterpretation))
		}
		if metadata.PixelRepresentation != 0 {
			return fmt.Errorf("%w: color PixelRepresentation=%d", pixeldata.ErrUnsupportedPixelRepresentation, metadata.PixelRepresentation)
		}
		if !metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration != 0 {
			return fmt.Errorf("%w: PlanarConfiguration=%d", pixeldata.ErrUnsupportedPlanarConfiguration, metadata.PlanarConfiguration)
		}
	default:
		return fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedScan, metadata.SamplesPerPixel)
	}
	return nil
}

func validateJPEGLosslessRequest(metadata pixeldata.Metadata, fragments [][]byte, joinedBytes uint64) error {
	frames := uint64(metadata.NumberOfFrames)
	pixels := uint64(metadata.Rows) * uint64(metadata.Columns)
	samples := pixels * uint64(metadata.SamplesPerPixel)
	bytesPerSample := uint64(metadata.BitsAllocated / 8)
	if metadata.NumberOfFrames <= 0 || samples == 0 || bytesPerSample == 0 || samples > ^uint64(0)/bytesPerSample {
		return fmt.Errorf("%w: decoded request size overflow", ErrInvalidStream)
	}
	frameOutput := samples * bytesPerSample
	if frameOutput > ^uint64(0)/frames {
		return fmt.Errorf("%w: decoded request size overflow", ErrInvalidStream)
	}
	total := frameOutput * frames
	wordBytes := uint64(32<<(^uint(0)>>63)) / 8
	if frames > ^uint64(0)/(6*wordBytes) {
		return fmt.Errorf("%w: decoded request overhead overflow", ErrInvalidStream)
	}
	var ok bool
	total, ok = addJPEGLosslessBytes(total, frames*6*wordBytes)
	if !ok {
		return fmt.Errorf("%w: decoded request overhead overflow", ErrInvalidStream)
	}
	total, ok = addJPEGLosslessBytes(total, joinedBytes)
	if !ok {
		return fmt.Errorf("%w: joined frame size overflow", ErrInvalidStream)
	}
	// The current frame's int32 component planes coexist with all retained
	// native frame outputs.
	if samples > ^uint64(0)/4 {
		return fmt.Errorf("%w: decoded working-set overflow", ErrInvalidStream)
	}
	total, ok = addJPEGLosslessBytes(total, samples*4)
	if !ok {
		return fmt.Errorf("%w: decoded working-set overflow", ErrInvalidStream)
	}
	for _, fragment := range fragments {
		total, ok = addJPEGLosslessBytes(total, uint64(len(fragment)))
		if !ok || total > maxJPEGLosslessRequestBytes {
			return fmt.Errorf("%w: decoded request exceeds resource limit", ErrInvalidStream)
		}
	}
	if total > maxJPEGLosslessRequestBytes {
		return fmt.Errorf("%w: decoded request exceeds resource limit", ErrInvalidStream)
	}
	return nil
}

func addJPEGLosslessBytes(total, addition uint64) (uint64, bool) {
	if total > ^uint64(0)-addition {
		return 0, false
	}
	return total + addition, true
}

type decodedImage struct {
	width, height  int
	precision      int
	planes         [][]int32
	pointTransform []int
}

func (d *decodedImage) toInterleavedBytes(bitsAllocated uint16) []byte {
	pixels := d.width * d.height
	components := len(d.planes)
	bytesPerSample := int(bitsAllocated / 8)
	out := make([]byte, pixels*components*bytesPerSample)
	for pixelIndex := 0; pixelIndex < pixels; pixelIndex++ {
		for component := 0; component < components; component++ {
			sample := d.planes[component][pixelIndex] << uint(d.pointTransform[component])
			outputIndex := pixelIndex*components + component
			if bytesPerSample == 1 {
				out[outputIndex] = byte(sample)
			} else {
				binary.LittleEndian.PutUint16(out[outputIndex*2:], uint16(sample))
			}
		}
	}
	return out
}

const (
	markerSOI  = 0xd8
	markerEOI  = 0xd9
	markerSOF3 = 0xc3
	markerDHT  = 0xc4
	markerSOS  = 0xda
	markerDRI  = 0xdd
)

type frameComponent struct {
	id byte
}

type frameHeader struct {
	precision  int
	height     int
	width      int
	components []frameComponent
}

type losslessDecoder struct {
	data []byte
	pos  int

	frame            frameHeader
	image            *decodedImage
	componentByID    map[byte]int
	componentScanned []bool
	huffman          map[int]*huffTable
	restartInterval  int
	seenSOF          bool
	seenSOS          bool
	scanCount        int
	driAtScanCount   int
	selectionValue   int
	metadata         pixeldata.Metadata
}

func decodeFrame(data []byte, selectionValue int, metadata pixeldata.Metadata) (*decodedImage, error) {
	if len(data) < 4 || data[0] != 0xff || data[1] != markerSOI {
		return nil, fmt.Errorf("%w: missing SOI", ErrInvalidStream)
	}
	d := losslessDecoder{
		data:           data,
		pos:            2,
		huffman:        make(map[int]*huffTable),
		selectionValue: selectionValue,
		componentByID:  make(map[byte]int),
		driAtScanCount: -1,
		metadata:       metadata,
	}
	for {
		marker, err := d.nextMarker()
		if err != nil {
			return nil, err
		}
		switch marker {
		case markerSOF3:
			if d.seenSOF || d.seenSOS {
				return nil, fmt.Errorf("%w: duplicate or out-of-order SOF3", ErrInvalidStream)
			}
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			frame, err := parseSOF3Segment(segment)
			if err != nil {
				return nil, err
			}
			d.frame = frame
			if frame.width != int(d.metadata.Columns) || frame.height != int(d.metadata.Rows) {
				return nil, fmt.Errorf("%w: got %dx%d want %dx%d", ErrImageSizeMismatch, frame.width, frame.height, d.metadata.Columns, d.metadata.Rows)
			}
			if frame.precision != int(d.metadata.BitsStored) || len(frame.components) != int(d.metadata.SamplesPerPixel) {
				return nil, fmt.Errorf("%w: SOF3 precision/components disagree with metadata", ErrImageSizeMismatch)
			}
			if err := validateJPEGLosslessFrameAllocation(frame); err != nil {
				return nil, err
			}
			for index, component := range frame.components {
				d.componentByID[component.id] = index
			}
			pixels := frame.width * frame.height
			d.image = &decodedImage{
				width: frame.width, height: frame.height, precision: frame.precision,
				planes: make([][]int32, len(frame.components)), pointTransform: make([]int, len(frame.components)),
			}
			for index := range d.image.planes {
				d.image.planes[index] = make([]int32, pixels)
			}
			d.componentScanned = make([]bool, len(frame.components))
			d.seenSOF = true
		case markerDHT:
			if d.seenSOS && allComponentsScanned(d.componentScanned) {
				return nil, fmt.Errorf("%w: DHT after complete scan", ErrInvalidStream)
			}
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if d.seenSOS {
				parsed := make(map[int]*huffTable)
				if err := parseDHTSegment(segment, parsed); err != nil {
					return nil, err
				}
				for tableID, table := range parsed {
					d.huffman[tableID] = table
				}
			} else if err := parseDHTSegment(segment, d.huffman); err != nil {
				return nil, err
			}
		case markerDRI:
			if !d.seenSOF || allComponentsScanned(d.componentScanned) || d.driAtScanCount == d.scanCount {
				return nil, fmt.Errorf("%w: duplicate or out-of-order DRI", ErrInvalidStream)
			}
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if len(segment) != 2 {
				return nil, fmt.Errorf("%w: invalid DRI length", ErrInvalidStream)
			}
			d.restartInterval = int(binary.BigEndian.Uint16(segment))
			d.driAtScanCount = d.scanCount
		case markerSOS:
			if !d.seenSOF {
				return nil, fmt.Errorf("%w: SOS before SOF3", ErrInvalidStream)
			}
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if err := d.decodeScan(segment); err != nil {
				return nil, err
			}
			d.seenSOS = true
			d.scanCount++
		case markerEOI:
			if !d.seenSOS || !allComponentsScanned(d.componentScanned) {
				return nil, fmt.Errorf("%w: EOI before all component scans", ErrInvalidStream)
			}
			if trailing := len(d.data) - d.pos; trailing > 1 {
				return nil, fmt.Errorf("%w: trailing data after EOI", ErrInvalidStream)
			}
			return d.image, nil
		case 0xe0, 0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7,
			0xe8, 0xe9, 0xea, 0xeb, 0xec, 0xed, 0xee, 0xef, 0xfe:
			if _, err := d.readSegment(); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: unsupported marker 0xff%02x", ErrInvalidStream, marker)
		}
	}
}

func validateJPEGLosslessFrameAllocation(frame frameHeader) error {
	pixels := uint64(frame.width) * uint64(frame.height)
	components := uint64(len(frame.components))
	if pixels == 0 || components == 0 || pixels > ^uint64(0)/components {
		return fmt.Errorf("%w: decoded frame size overflow", ErrInvalidStream)
	}
	samples := pixels * components
	maxInt := uint64(^uint(0) >> 1)
	if samples > maxInt/4 || samples*4 > maxJPEGLosslessRequestBytes {
		return fmt.Errorf("%w: decoded frame exceeds resource limit", ErrInvalidStream)
	}
	return nil
}

func (d *losslessDecoder) nextMarker() (byte, error) {
	if d.pos >= len(d.data) || d.data[d.pos] != 0xff {
		return 0, fmt.Errorf("%w: expected marker at %d", ErrInvalidStream, d.pos)
	}
	for d.pos < len(d.data) && d.data[d.pos] == 0xff {
		d.pos++
	}
	if d.pos >= len(d.data) || d.data[d.pos] == 0 {
		return 0, fmt.Errorf("%w: truncated marker", ErrInvalidStream)
	}
	marker := d.data[d.pos]
	d.pos++
	return marker, nil
}

func (d *losslessDecoder) readSegment() ([]byte, error) {
	if d.pos+2 > len(d.data) {
		return nil, fmt.Errorf("%w: truncated segment length", ErrInvalidStream)
	}
	length := int(binary.BigEndian.Uint16(d.data[d.pos:]))
	if length < 2 || d.pos+length > len(d.data) {
		return nil, fmt.Errorf("%w: invalid segment length", ErrInvalidStream)
	}
	segment := d.data[d.pos+2 : d.pos+length]
	d.pos += length
	return segment, nil
}

func parseSOF3Segment(segment []byte) (frameHeader, error) {
	if len(segment) < 6 {
		return frameHeader{}, fmt.Errorf("%w: short SOF3", ErrInvalidStream)
	}
	precision := int(segment[0])
	height := int(binary.BigEndian.Uint16(segment[1:3]))
	width := int(binary.BigEndian.Uint16(segment[3:5]))
	components := int(segment[5])
	if precision < 2 || precision > 16 || width == 0 || height == 0 {
		return frameHeader{}, fmt.Errorf("%w: invalid SOF3 precision or dimensions", ErrInvalidStream)
	}
	if components != 1 && components != 3 {
		return frameHeader{}, fmt.Errorf("%w: SOF3 components=%d", ErrUnsupportedScan, components)
	}
	if len(segment) != 6+3*components {
		return frameHeader{}, fmt.Errorf("%w: invalid SOF3 component length", ErrInvalidStream)
	}
	frame := frameHeader{precision: precision, height: height, width: width, components: make([]frameComponent, components)}
	seen := make(map[byte]bool, components)
	for index := 0; index < components; index++ {
		offset := 6 + index*3
		id := segment[offset]
		if seen[id] || segment[offset+1] != 0x11 || segment[offset+2] != 0 {
			return frameHeader{}, fmt.Errorf("%w: invalid SOF3 component parameters", ErrUnsupportedScan)
		}
		seen[id] = true
		frame.components[index] = frameComponent{id: id}
	}
	return frame, nil
}

func parseDHTSegment(segment []byte, tables map[int]*huffTable) error {
	definedInSegment := make(map[int]bool)
	for position := 0; position < len(segment); {
		if len(segment)-position < 17 {
			return fmt.Errorf("%w: truncated DHT", ErrInvalidStream)
		}
		selector := segment[position]
		position++
		if selector>>4 != 0 || selector&0x0f > 3 {
			return fmt.Errorf("%w: invalid lossless Huffman table selector", ErrInvalidStream)
		}
		tableID := int(selector & 0x0f)
		if definedInSegment[tableID] {
			return fmt.Errorf("%w: duplicate Huffman table %d in one DHT segment", ErrInvalidStream, tableID)
		}
		definedInSegment[tableID] = true
		var counts [17]int
		total := 0
		availableCodes := 1
		for length := 1; length <= 16; length++ {
			count := int(segment[position])
			position++
			counts[length] = count
			total += count
			availableCodes = availableCodes*2 - count
			if availableCodes < 0 {
				return fmt.Errorf("%w: oversubscribed Huffman table", ErrInvalidStream)
			}
		}
		if availableCodes == 0 || total == 0 || total > 256 || len(segment)-position < total {
			return fmt.Errorf("%w: invalid Huffman table", ErrInvalidStream)
		}
		values := append([]byte(nil), segment[position:position+total]...)
		position += total
		for _, value := range values {
			if value > 16 {
				return fmt.Errorf("%w: invalid lossless Huffman symbol %d", ErrInvalidStream, value)
			}
		}
		tables[tableID] = buildHuffTable(counts, values)
	}
	if len(segment) == 0 {
		return fmt.Errorf("%w: empty DHT", ErrInvalidStream)
	}
	return nil
}

type scanComponent struct {
	index int
	table *huffTable
}

func (d *losslessDecoder) decodeScan(segment []byte) error {
	if len(segment) < 6 {
		return fmt.Errorf("%w: short SOS", ErrInvalidStream)
	}
	count := int(segment[0])
	if count != 1 && count != len(d.frame.components) {
		return fmt.Errorf("%w: scan components=%d", ErrUnsupportedScan, count)
	}
	if len(segment) != 1+2*count+3 {
		return fmt.Errorf("%w: invalid SOS length", ErrInvalidStream)
	}
	components := make([]scanComponent, count)
	selected := make(map[int]bool, count)
	for scanIndex := 0; scanIndex < count; scanIndex++ {
		offset := 1 + 2*scanIndex
		componentIndex, ok := d.componentByID[segment[offset]]
		if !ok || selected[componentIndex] || d.componentScanned[componentIndex] {
			return fmt.Errorf("%w: invalid or duplicate SOS component", ErrInvalidStream)
		}
		selector := segment[offset+1]
		if selector&0x0f != 0 {
			return fmt.Errorf("%w: lossless AC table selector is nonzero", ErrUnsupportedScan)
		}
		table, ok := d.huffman[int(selector>>4)]
		if !ok {
			return fmt.Errorf("%w: missing Huffman table %d", ErrInvalidStream, selector>>4)
		}
		selected[componentIndex] = true
		components[scanIndex] = scanComponent{index: componentIndex, table: table}
	}
	predictor := int(segment[1+2*count])
	if predictor < 1 || predictor > 7 {
		return fmt.Errorf("%w: predictor=%d", ErrUnsupportedScan, predictor)
	}
	if d.selectionValue != 0 && predictor != d.selectionValue {
		return fmt.Errorf("%w: predictor=%d selection value=%d", ErrUnsupportedScan, predictor, d.selectionValue)
	}
	if segment[1+2*count+1] != 0 || segment[1+2*count+2]>>4 != 0 {
		return fmt.Errorf("%w: unsupported SOS spectral/successive parameters", ErrUnsupportedScan)
	}
	pointTransform := int(segment[1+2*count+2] & 0x0f)
	if pointTransform >= d.frame.precision {
		return fmt.Errorf("%w: point transform=%d precision=%d", ErrInvalidStream, pointTransform, d.frame.precision)
	}

	reader := &bitReader{data: d.data, pos: d.pos}
	pixels := d.frame.width * d.frame.height
	codedPrecision := d.frame.precision - pointTransform
	defaultPrediction := int32(1) << (codedPrecision - 1)
	modulusMask := int32((uint32(1) << codedPrecision) - 1)
	restartCount := 0
	restartStart := 0
	restartMarker := byte(0xd0)
	for pixelIndex := 0; pixelIndex < pixels; pixelIndex++ {
		x := pixelIndex % d.frame.width
		y := pixelIndex / d.frame.width
		for _, component := range components {
			symbol, err := component.table.decodeSymbol(reader)
			if err != nil {
				return err
			}
			difference, err := receiveExtend(reader, int(symbol))
			if err != nil {
				return err
			}
			plane := d.image.planes[component.index]
			prediction := predict(plane, x, y, d.frame.width, predictor, defaultPrediction)
			if pixelIndex == restartStart {
				prediction = defaultPrediction
			}
			plane[pixelIndex] = (prediction + difference) & modulusMask
		}
		if d.restartInterval > 0 {
			restartCount++
			if restartCount == d.restartInterval && pixelIndex != pixels-1 {
				if err := reader.alignToRestart(restartMarker); err != nil {
					return err
				}
				restartMarker = 0xd0 + ((restartMarker - 0xd0 + 1) & 7)
				restartCount = 0
				restartStart = pixelIndex + 1
			}
		}
	}
	nextMarker, err := reader.finishEntropy()
	if err != nil {
		return err
	}
	d.pos = nextMarker
	for _, component := range components {
		d.componentScanned[component.index] = true
		d.image.pointTransform[component.index] = pointTransform
	}
	return nil
}

func allComponentsScanned(scanned []bool) bool {
	if len(scanned) == 0 {
		return false
	}
	for _, value := range scanned {
		if !value {
			return false
		}
	}
	return true
}

func predict(samples []int32, x, y, width, predictor int, defaultPrediction int32) int32 {
	if x == 0 && y == 0 {
		return defaultPrediction
	}
	if y == 0 {
		return samples[y*width+x-1]
	}
	if x == 0 {
		return samples[(y-1)*width+x]
	}
	a := samples[y*width+x-1]
	b := samples[(y-1)*width+x]
	c := samples[(y-1)*width+x-1]
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

func receiveExtend(reader *bitReader, size int) (int32, error) {
	if size == 0 {
		return 0, nil
	}
	if size == 16 {
		return 32768, nil
	}
	if size < 0 || size > 16 {
		return 0, fmt.Errorf("%w: difference category=%d", ErrInvalidStream, size)
	}
	value, err := reader.readBits(size)
	if err != nil {
		return 0, err
	}
	difference := int32(value)
	if difference < 1<<(size-1) {
		difference += (-1 << size) + 1
	}
	return difference, nil
}
