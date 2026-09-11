package jpeg

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const (
	extendedProcess4Precision       = 12
	extendedProcess4MaxDecodedBytes = uint64(512 << 20)
)

var extendedZigZag = [64]int{
	0, 1, 8, 16, 9, 2, 3, 10,
	17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34,
	27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36,
	29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46,
	53, 60, 61, 54, 47, 55, 62, 63,
}

var extendedCos = func() [8][8]float64 {
	var table [8][8]float64
	for frequency := 0; frequency < 8; frequency++ {
		for sample := 0; sample < 8; sample++ {
			table[frequency][sample] = math.Cos(float64((2*sample+1)*frequency) * math.Pi / 16)
		}
	}
	return table
}()

type extendedHuffmanTable struct {
	counts  [17]int
	symbols []byte
	valid   bool
}

type extendedProcess4Decoder struct {
	data []byte
	pos  int

	width       int
	height      int
	componentID byte
	quantID     byte
	precision   int

	quant        [4][64]int
	quantDefined [4]bool
	huffman      [2][4]extendedHuffmanTable
	restart      int
	seenDRI      bool
	seenSOF      bool
	seenSOS      bool
	decoded      []byte
}

func validateExtendedProcess4Request(metadata pixeldata.Metadata, fragments [][]byte) error {
	frames := uint64(metadata.NumberOfFrames)
	if metadata.NumberOfFrames <= 0 || len(fragments) == 0 {
		return fmt.Errorf("%w: invalid JPEG Extended frame count", ErrInvalidFragment)
	}
	pixelsPerFrame := uint64(metadata.Rows) * uint64(metadata.Columns)
	if pixelsPerFrame > ^uint64(0)/2 {
		return fmt.Errorf("%w: JPEG Extended output size overflow", ErrInvalidFragment)
	}
	outputPerFrame := pixelsPerFrame * 2
	if outputPerFrame > ^uint64(0)/frames {
		return fmt.Errorf("%w: JPEG Extended request output overflow", ErrInvalidFragment)
	}
	total := outputPerFrame * frames

	wordBytes := uint64(32<<(^uint(0)>>63)) / 8
	// Account for both the retained input fragment headers and the output frame
	// headers. Each []byte occupies three machine words.
	if wordBytes == 0 || frames > ^uint64(0)/(6*wordBytes) {
		return fmt.Errorf("%w: JPEG Extended request overhead overflow", ErrInvalidFragment)
	}
	var ok bool
	total, ok = addExtendedRequestBytes(total, frames*6*wordBytes)
	if !ok {
		return fmt.Errorf("%w: JPEG Extended request overhead overflow", ErrInvalidFragment)
	}
	for _, fragment := range fragments {
		total, ok = addExtendedRequestBytes(total, uint64(len(fragment)))
		if !ok {
			return fmt.Errorf("%w: JPEG Extended request size overflow", ErrInvalidFragment)
		}
		if total > extendedProcess4MaxDecodedBytes {
			return fmt.Errorf("%w: JPEG Extended request exceeds resource limit", ErrInvalidFragment)
		}
	}
	if total > extendedProcess4MaxDecodedBytes {
		return fmt.Errorf("%w: JPEG Extended request exceeds resource limit", ErrInvalidFragment)
	}
	return nil
}

func addExtendedRequestBytes(total, addition uint64) (uint64, bool) {
	if total > ^uint64(0)-addition {
		return 0, false
	}
	return total + addition, true
}

func jpegFrameSOFMarker(stream []byte) (byte, error) {
	if len(stream) < 4 || stream[0] != 0xff || stream[1] != 0xd8 {
		return 0, fmt.Errorf("missing JPEG SOI")
	}
	for pos := 2; pos < len(stream); {
		if stream[pos] != 0xff {
			return 0, fmt.Errorf("expected JPEG marker at byte %d", pos)
		}
		for pos < len(stream) && stream[pos] == 0xff {
			pos++
		}
		if pos >= len(stream) || stream[pos] == 0 {
			return 0, fmt.Errorf("truncated or stuffed JPEG marker")
		}
		marker := stream[pos]
		pos++
		switch marker {
		case 0xc0, 0xc1, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf:
			return marker, nil
		case 0xd8, 0xd9, 0xda:
			return 0, fmt.Errorf("JPEG frame has no SOF marker")
		case 0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7:
			return 0, fmt.Errorf("JPEG restart marker before SOF")
		}
		if pos+2 > len(stream) {
			return 0, fmt.Errorf("truncated JPEG segment length")
		}
		length := int(binary.BigEndian.Uint16(stream[pos:]))
		if length < 2 || pos+length > len(stream) {
			return 0, fmt.Errorf("invalid JPEG segment length")
		}
		pos += length
	}
	return 0, fmt.Errorf("JPEG frame has no SOF marker")
}

func decodeExtendedProcess4(stream []byte, metadata pixeldata.Metadata) ([]byte, error) {
	if uint64(len(stream)) > extendedProcess4MaxDecodedBytes {
		return nil, fmt.Errorf("JPEG Extended compressed frame exceeds resource limit")
	}
	if len(stream) < 4 || stream[0] != 0xff || stream[1] != 0xd8 {
		return nil, fmt.Errorf("missing JPEG SOI")
	}
	d := extendedProcess4Decoder{data: stream, pos: 2}
	for {
		marker, err := d.nextMarker()
		if err != nil {
			return nil, err
		}
		switch marker {
		case 0xc1: // SOF1: extended sequential DCT, Huffman coding.
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if err := d.parseSOF1(segment, metadata); err != nil {
				return nil, err
			}
		case 0xc4: // DHT
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if err := d.parseDHT(segment); err != nil {
				return nil, err
			}
		case 0xdb: // DQT
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if err := d.parseDQT(segment); err != nil {
				return nil, err
			}
		case 0xdd: // DRI
			if d.seenDRI || d.seenSOS {
				return nil, fmt.Errorf("duplicate or out-of-order JPEG DRI")
			}
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if len(segment) != 2 {
				return nil, fmt.Errorf("invalid JPEG DRI length")
			}
			d.restart = int(binary.BigEndian.Uint16(segment))
			d.seenDRI = true
		case 0xda: // SOS
			segment, err := d.readSegment()
			if err != nil {
				return nil, err
			}
			if err := d.decodeSOS(segment); err != nil {
				return nil, err
			}
		case 0xd9: // EOI
			if !d.seenSOF || !d.seenSOS || d.decoded == nil {
				return nil, fmt.Errorf("incomplete JPEG Extended Process 4 frame")
			}
			trailing := d.data[d.pos:]
			// DICOM requires even-length Items and permits the final compressed
			// fragment to carry one padding byte; deployed writers do not agree
			// on that byte's value.
			if len(trailing) > 1 {
				return nil, fmt.Errorf("trailing data after JPEG EOI")
			}
			return d.decoded, nil
		case 0xe0, 0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7,
			0xe8, 0xe9, 0xea, 0xeb, 0xec, 0xed, 0xee, 0xef, 0xfe:
			if _, err := d.readSegment(); err != nil {
				return nil, err
			}
		case 0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7:
			return nil, fmt.Errorf("JPEG restart marker outside entropy data")
		default:
			return nil, fmt.Errorf("unsupported JPEG marker 0xff%02x", marker)
		}
	}
}

func (d *extendedProcess4Decoder) nextMarker() (byte, error) {
	if d.pos >= len(d.data) || d.data[d.pos] != 0xff {
		return 0, fmt.Errorf("expected JPEG marker at byte %d", d.pos)
	}
	for d.pos < len(d.data) && d.data[d.pos] == 0xff {
		d.pos++
	}
	if d.pos >= len(d.data) || d.data[d.pos] == 0 {
		return 0, fmt.Errorf("truncated or stuffed JPEG marker")
	}
	marker := d.data[d.pos]
	d.pos++
	return marker, nil
}

func (d *extendedProcess4Decoder) readSegment() ([]byte, error) {
	if d.pos+2 > len(d.data) {
		return nil, fmt.Errorf("truncated JPEG segment length")
	}
	length := int(binary.BigEndian.Uint16(d.data[d.pos:]))
	if length < 2 || d.pos+length > len(d.data) {
		return nil, fmt.Errorf("invalid JPEG segment length")
	}
	segment := d.data[d.pos+2 : d.pos+length]
	d.pos += length
	return segment, nil
}

func (d *extendedProcess4Decoder) parseSOF1(segment []byte, metadata pixeldata.Metadata) error {
	if d.seenSOF || d.seenSOS {
		return fmt.Errorf("duplicate or out-of-order JPEG SOF1")
	}
	if len(segment) != 9 || segment[5] != 1 {
		return fmt.Errorf("JPEG Extended Process 4 requires one component")
	}
	precision := int(segment[0])
	height := int(binary.BigEndian.Uint16(segment[1:3]))
	width := int(binary.BigEndian.Uint16(segment[3:5]))
	if precision != extendedProcess4Precision {
		return fmt.Errorf("JPEG Extended Process 4 precision=%d, want 12", precision)
	}
	if height == 0 || width == 0 {
		return fmt.Errorf("invalid JPEG Extended dimensions")
	}
	if height != int(metadata.Rows) || width != int(metadata.Columns) {
		return fmt.Errorf("%w: JPEG=%dx%d metadata=%dx%d", ErrImageSizeMismatch, width, height, metadata.Columns, metadata.Rows)
	}
	if segment[7] != 0x11 || segment[8] > 3 {
		return fmt.Errorf("unsupported JPEG Extended component parameters")
	}
	decodedBytes := uint64(width) * uint64(height) * 2
	if decodedBytes > extendedProcess4MaxDecodedBytes || decodedBytes > uint64(int(^uint(0)>>1)) {
		return fmt.Errorf("JPEG Extended decoded frame exceeds resource limit")
	}
	d.precision = precision
	d.height = height
	d.width = width
	d.componentID = segment[6]
	d.quantID = segment[8]
	d.seenSOF = true
	return nil
}

func (d *extendedProcess4Decoder) parseDQT(segment []byte) error {
	if d.seenSOS {
		return fmt.Errorf("JPEG quantization table after SOS")
	}
	for offset := 0; offset < len(segment); {
		info := segment[offset]
		offset++
		precision := info >> 4
		tableID := info & 0x0f
		if precision > 1 || tableID > 3 {
			return fmt.Errorf("invalid JPEG quantization table selector")
		}
		if d.quantDefined[tableID] {
			return fmt.Errorf("duplicate JPEG quantization table %d", tableID)
		}
		valueBytes := 1
		if precision == 1 {
			valueBytes = 2
		}
		if len(segment)-offset < 64*valueBytes {
			return fmt.Errorf("truncated JPEG quantization table")
		}
		for zig := 0; zig < 64; zig++ {
			value := int(segment[offset])
			if valueBytes == 2 {
				value = int(binary.BigEndian.Uint16(segment[offset:]))
			}
			if value == 0 {
				return fmt.Errorf("zero JPEG quantization value")
			}
			d.quant[tableID][extendedZigZag[zig]] = value
			offset += valueBytes
		}
		d.quantDefined[tableID] = true
	}
	if len(segment) == 0 {
		return fmt.Errorf("empty JPEG quantization segment")
	}
	return nil
}

func (d *extendedProcess4Decoder) parseDHT(segment []byte) error {
	if d.seenSOS {
		return fmt.Errorf("JPEG Huffman table after SOS")
	}
	for offset := 0; offset < len(segment); {
		if len(segment)-offset < 17 {
			return fmt.Errorf("truncated JPEG Huffman table")
		}
		info := segment[offset]
		offset++
		class := info >> 4
		tableID := info & 0x0f
		if class > 1 || tableID > 3 {
			return fmt.Errorf("invalid JPEG Huffman table selector")
		}
		if d.huffman[class][tableID].valid {
			return fmt.Errorf("duplicate JPEG Huffman table class=%d id=%d", class, tableID)
		}
		var table extendedHuffmanTable
		symbolCount := 0
		availableCodes := 1
		for length := 1; length <= 16; length++ {
			count := int(segment[offset])
			offset++
			table.counts[length] = count
			symbolCount += count
			availableCodes = availableCodes*2 - count
			if availableCodes < 0 {
				return fmt.Errorf("oversubscribed JPEG Huffman table")
			}
		}
		// JPEG reserves the all-ones code for entropy padding, so a complete
		// canonical code tree is invalid even when it is not oversubscribed.
		if availableCodes == 0 {
			return fmt.Errorf("complete JPEG Huffman code tree")
		}
		if symbolCount == 0 || symbolCount > 256 || len(segment)-offset < symbolCount {
			return fmt.Errorf("invalid JPEG Huffman symbol count")
		}
		table.symbols = append([]byte(nil), segment[offset:offset+symbolCount]...)
		if class == 0 {
			for _, symbol := range table.symbols {
				if symbol > 15 {
					return fmt.Errorf("invalid JPEG DC Huffman symbol %d", symbol)
				}
			}
		}
		table.valid = true
		offset += symbolCount
		d.huffman[class][tableID] = table
	}
	if len(segment) == 0 {
		return fmt.Errorf("empty JPEG Huffman segment")
	}
	return nil
}

func (d *extendedProcess4Decoder) decodeSOS(segment []byte) error {
	if !d.seenSOF || d.seenSOS {
		return fmt.Errorf("duplicate or out-of-order JPEG SOS")
	}
	if len(segment) != 6 || segment[0] != 1 || segment[1] != d.componentID ||
		segment[3] != 0 || segment[4] != 63 || segment[5] != 0 {
		return fmt.Errorf("unsupported JPEG Extended sequential scan")
	}
	dcID := segment[2] >> 4
	acID := segment[2] & 0x0f
	if dcID > 3 || acID > 3 || !d.huffman[0][dcID].valid || !d.huffman[1][acID].valid {
		return fmt.Errorf("undefined JPEG Huffman table")
	}
	if !d.quantDefined[d.quantID] {
		return fmt.Errorf("undefined JPEG quantization table")
	}
	entropy, end, err := d.entropySegment()
	if err != nil {
		return err
	}
	decoded, err := d.decodeBlocks(entropy, d.huffman[0][dcID], d.huffman[1][acID])
	if err != nil {
		return err
	}
	d.decoded = decoded
	d.seenSOS = true
	d.pos = end
	return nil
}

func (d *extendedProcess4Decoder) entropySegment() ([]byte, int, error) {
	start := d.pos
	for i := start; i+1 < len(d.data); {
		if d.data[i] != 0xff {
			i++
			continue
		}
		j := i + 1
		for j < len(d.data) && d.data[j] == 0xff {
			j++
		}
		if j >= len(d.data) {
			return nil, 0, fmt.Errorf("truncated JPEG entropy marker")
		}
		marker := d.data[j]
		if marker == 0 {
			i = j + 1
			continue
		}
		if marker >= 0xd0 && marker <= 0xd7 {
			i = j + 1
			continue
		}
		return d.data[start:i], i, nil
	}
	return nil, 0, fmt.Errorf("unterminated JPEG entropy data")
}

func (d *extendedProcess4Decoder) decodeBlocks(entropy []byte, dcTable, acTable extendedHuffmanTable) ([]byte, error) {
	reader := extendedBitReader{data: entropy}
	blocksX := (d.width + 7) / 8
	blocksY := (d.height + 7) / 8
	blockCount := blocksX * blocksY
	out := make([]byte, d.width*d.height*2)
	dcPredictor := 0
	restartIndex := byte(0)
	for blockIndex := 0; blockIndex < blockCount; blockIndex++ {
		if d.restart > 0 && blockIndex > 0 && blockIndex%d.restart == 0 {
			if err := reader.consumeRestart(0xd0 + restartIndex); err != nil {
				return nil, err
			}
			restartIndex = (restartIndex + 1) & 7
			dcPredictor = 0
		}
		var coefficients [64]float64
		category, err := reader.decodeHuffman(dcTable)
		if err != nil {
			return nil, fmt.Errorf("decode JPEG DC block %d: %w", blockIndex, err)
		}
		if category > 15 {
			return nil, fmt.Errorf("invalid JPEG DC coefficient category %d", category)
		}
		difference, err := reader.receiveExtend(int(category))
		if err != nil {
			return nil, err
		}
		if difference > 0 && dcPredictor > math.MaxInt32-difference || difference < 0 && dcPredictor < math.MinInt32-difference {
			return nil, fmt.Errorf("JPEG DC predictor overflow")
		}
		dcPredictor += difference
		coefficients[0] = float64(dcPredictor) * float64(d.quant[d.quantID][0])

		for zig := 1; zig < 64; {
			symbol, err := reader.decodeHuffman(acTable)
			if err != nil {
				return nil, fmt.Errorf("decode JPEG AC block %d: %w", blockIndex, err)
			}
			run := int(symbol >> 4)
			size := int(symbol & 0x0f)
			if size == 0 {
				if run == 0 {
					break
				}
				if run != 15 || zig+16 > 64 {
					return nil, fmt.Errorf("invalid JPEG zero run")
				}
				zig += 16
				continue
			}
			zig += run
			if zig >= 64 {
				return nil, fmt.Errorf("JPEG AC run exceeds block")
			}
			value, err := reader.receiveExtend(size)
			if err != nil {
				return nil, err
			}
			natural := extendedZigZag[zig]
			coefficients[natural] = float64(value) * float64(d.quant[d.quantID][natural])
			zig++
		}
		d.writeIDCTBlock(out, blockIndex%blocksX, blockIndex/blocksX, coefficients)
	}
	if err := reader.finish(); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *extendedProcess4Decoder) writeIDCTBlock(out []byte, blockX, blockY int, coefficients [64]float64) {
	center := 1 << (d.precision - 1)
	maximum := (1 << d.precision) - 1
	if onlyDC := func() bool {
		for i := 1; i < 64; i++ {
			if coefficients[i] != 0 {
				return false
			}
		}
		return true
	}(); onlyDC {
		value := clampExtendedSample(int(math.Round(coefficients[0]/8))+center, maximum)
		for y := 0; y < 8 && blockY*8+y < d.height; y++ {
			for x := 0; x < 8 && blockX*8+x < d.width; x++ {
				offset := ((blockY*8+y)*d.width + blockX*8 + x) * 2
				binary.LittleEndian.PutUint16(out[offset:], uint16(value))
			}
		}
		return
	}

	var intermediate [64]float64
	for v := 0; v < 8; v++ {
		for x := 0; x < 8; x++ {
			sum := coefficients[v*8] / math.Sqrt2
			for u := 1; u < 8; u++ {
				sum += coefficients[v*8+u] * extendedCos[u][x]
			}
			intermediate[v*8+x] = sum
		}
	}
	for y := 0; y < 8 && blockY*8+y < d.height; y++ {
		for x := 0; x < 8 && blockX*8+x < d.width; x++ {
			sum := intermediate[x] / math.Sqrt2
			for v := 1; v < 8; v++ {
				sum += intermediate[v*8+x] * extendedCos[v][y]
			}
			value := clampExtendedSample(int(math.Round(sum/4))+center, maximum)
			offset := ((blockY*8+y)*d.width + blockX*8 + x) * 2
			binary.LittleEndian.PutUint16(out[offset:], uint16(value))
		}
	}
}

func clampExtendedSample(value, maximum int) int {
	if value < 0 {
		return 0
	}
	if value > maximum {
		return maximum
	}
	return value
}

type extendedBitReader struct {
	data  []byte
	pos   int
	value byte
	bits  int
}

func (r *extendedBitReader) readBit() (int, error) {
	if r.bits == 0 {
		value, err := r.readEntropyByte()
		if err != nil {
			return 0, err
		}
		r.value = value
		r.bits = 8
	}
	r.bits--
	return int(r.value>>r.bits) & 1, nil
}

func (r *extendedBitReader) readEntropyByte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, fmt.Errorf("unexpected end of JPEG entropy data")
	}
	value := r.data[r.pos]
	r.pos++
	if value != 0xff {
		return value, nil
	}
	if r.pos >= len(r.data) || r.data[r.pos] != 0 {
		return 0, fmt.Errorf("unexpected JPEG marker in entropy data")
	}
	r.pos++
	return 0xff, nil
}

func (r *extendedBitReader) readBits(count int) (int, error) {
	value := 0
	for i := 0; i < count; i++ {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		value = value<<1 | bit
	}
	return value, nil
}

func (r *extendedBitReader) receiveExtend(count int) (int, error) {
	if count == 0 {
		return 0, nil
	}
	if count < 0 || count > 16 {
		return 0, fmt.Errorf("invalid JPEG coefficient category %d", count)
	}
	value, err := r.readBits(count)
	if err != nil {
		return 0, err
	}
	threshold := 1 << (count - 1)
	if value < threshold {
		value -= (1 << count) - 1
	}
	return value, nil
}

func (r *extendedBitReader) decodeHuffman(table extendedHuffmanTable) (byte, error) {
	code := 0
	firstCode := 0
	symbolOffset := 0
	for length := 1; length <= 16; length++ {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		code = code<<1 | bit
		count := table.counts[length]
		if code >= firstCode && code-firstCode < count {
			index := symbolOffset + code - firstCode
			if index < 0 || index >= len(table.symbols) {
				return 0, fmt.Errorf("invalid JPEG Huffman symbol index")
			}
			return table.symbols[index], nil
		}
		symbolOffset += count
		firstCode = (firstCode + count) << 1
	}
	return 0, fmt.Errorf("invalid JPEG Huffman code")
}

func (r *extendedBitReader) consumeRestart(want byte) error {
	if err := r.validatePadding(); err != nil {
		return err
	}
	r.bits = 0
	if r.pos >= len(r.data) || r.data[r.pos] != 0xff {
		return fmt.Errorf("missing JPEG restart marker 0xff%02x", want)
	}
	for r.pos < len(r.data) && r.data[r.pos] == 0xff {
		r.pos++
	}
	if r.pos >= len(r.data) || r.data[r.pos] != want {
		return fmt.Errorf("invalid JPEG restart marker, want 0xff%02x", want)
	}
	r.pos++
	return nil
}

func (r *extendedBitReader) validatePadding() error {
	if r.bits == 0 {
		return nil
	}
	mask := byte((1 << r.bits) - 1)
	if r.value&mask != mask {
		return fmt.Errorf("invalid JPEG entropy padding")
	}
	return nil
}

func (r *extendedBitReader) finish() error {
	if err := r.validatePadding(); err != nil {
		return err
	}
	if r.pos != len(r.data) {
		return fmt.Errorf("trailing JPEG entropy data")
	}
	return nil
}
