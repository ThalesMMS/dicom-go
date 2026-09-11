package jpegls

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
)

var (
	ErrInvalidCodestream          = errors.New("dicom: invalid JPEG-LS codestream")
	ErrUnsupportedBitsAllocated   = errors.New("dicom: unsupported JPEG-LS BitsAllocated")
	ErrUnsupportedSamplesPerPixel = errors.New("dicom: unsupported JPEG-LS SamplesPerPixel")
)

type frameHeader struct {
	precision    int
	rows         int
	columns      int
	components   int
	componentIDs []byte
	near         int
	ilv          int
	preset       presetCodingParameters
}

type presetCodingParameters struct {
	near   int
	maxval int
	t1     int
	t2     int
	t3     int
	reset  int
}

type scanData struct {
	componentIDs []byte
	entropy      []byte
}

const maxJPEGLSWorkingSetBytes = 512 << 20

// Codec decodes qualified JPEG-LS frames. Its zero value and New enforce NEAR=0;
// only NewNearLossless enables the separately registered .81 profiles.
type Codec struct{ nearLossless bool }

// New returns a JPEG-LS Lossless decoder for the package's qualified subset.
func New() *Codec {
	return &Codec{}
}

// Register registers the JPEG-LS Lossless decoder in registry.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return registry.RegisterCodec(UID, New())
}

// RegisterDefault registers the decoder in pixeldata.DefaultRegistry.
func RegisterDefault() error {
	return pixeldata.RegisterCodec(UID, New())
}

func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	return c.DecodeContext(context.Background(), pixel, obj)
}

func (c *Codec) DecodeContext(ctx context.Context, pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return pixeldata.Frames{}, err
	}
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG-LS requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}
	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	if _, err := validateEncoderInput(metadata); err != nil {
		return pixeldata.Frames{}, err
	}
	if c.nearLossless && metadata.PixelRepresentation != 0 {
		return pixeldata.Frames{}, fmt.Errorf("%w: signed JPEG-LS Near-Lossless is outside the qualified profile", pixeldata.ErrUnsupportedPixelRepresentation)
	}
	if c.nearLossless && strings.EqualFold(strings.TrimSpace(metadata.PhotometricInterpretation), "PALETTE COLOR") {
		return pixeldata.Frames{}, fmt.Errorf("%w: palette color requires the JPEG-LS Lossless transfer syntax", pixeldata.ErrUnsupportedPhotometricInterpretation)
	}
	if err := validateDecodeRequestAllocation(metadata); err != nil {
		return pixeldata.Frames{}, err
	}
	codestreams, err := encapsulated.FromFragments(ctx, pixel.Sequence, obj, metadata.NumberOfFrames, encapsulated.JPEGLS, encapsulated.Limits{})
	if err != nil {
		if errors.Is(err, encapsulated.ErrFrameCount) {
			err = errors.Join(pixeldata.ErrPixelDataSizeMismatch, err)
		}
		return pixeldata.Frames{}, err
	}
	frames := make([][]byte, codestreams.Len())
	for i := range frames {
		codestream, err := codestreams.Frame(ctx, i)
		if err != nil {
			return pixeldata.Frames{}, err
		}
		decoded, err := decodeFrameModeContext(ctx, codestream.Data, metadata, c.nearLossless)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("decode JPEG-LS frame %d: %w", i, err)
		}
		frames[i] = decoded
	}
	return pixeldata.Frames{Rows: int(metadata.Rows), Columns: int(metadata.Columns), Data: frames}, nil
}

func decodeFrame(stream []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return decodeFrameContext(context.Background(), stream, metadata)
}

func decodeFrameContext(ctx context.Context, stream []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return decodeFrameModeContext(ctx, stream, metadata, false)
}

func decodeFrameModeContext(ctx context.Context, stream []byte, metadata pixeldata.Metadata, nearLossless bool) ([]byte, error) {
	header, scans, err := splitScans(stream)
	if err != nil {
		return nil, err
	}
	if header.near != 0 && !nearLossless {
		return nil, fmt.Errorf("%w: NEAR=%d", ErrInvalidCodestream, header.near)
	}
	if header.rows != int(metadata.Rows) || header.columns != int(metadata.Columns) {
		return nil, fmt.Errorf("%w: JPEG-LS size %dx%d metadata %dx%d", pixeldata.ErrPixelDataSizeMismatch, header.columns, header.rows, metadata.Columns, metadata.Rows)
	}
	if header.components != int(metadata.SamplesPerPixel) {
		return nil, fmt.Errorf("%w: JPEG-LS components=%d SamplesPerPixel=%d", pixeldata.ErrPixelDataSizeMismatch, header.components, metadata.SamplesPerPixel)
	}
	if header.precision != int(metadata.BitsStored) && header.precision != int(metadata.BitsAllocated) {
		return nil, fmt.Errorf("%w: JPEG-LS precision=%d BitsStored=%d BitsAllocated=%d", pixeldata.ErrPixelDataSizeMismatch, header.precision, metadata.BitsStored, metadata.BitsAllocated)
	}
	if (header.ilv != 0 || nearLossless) && header.precision != int(metadata.BitsStored) {
		return nil, fmt.Errorf("%w: JPEG-LS precision must match BitsStored for this profile", pixeldata.ErrPixelDataSizeMismatch)
	}
	if err := validateDecodeAllocation(header, metadata); err != nil {
		return nil, err
	}
	maxval := (1 << header.precision) - 1
	preset := header.preset
	if (header.ilv != 0 || nearLossless) && preset.maxval != maxval {
		return nil, fmt.Errorf("%w: custom MAXVAL is outside this qualified profile", ErrInvalidCodestream)
	}
	planes := make([][]int, header.components)
	componentIndex := make(map[byte]int, len(header.componentIDs))
	for index, id := range header.componentIDs {
		componentIndex[id] = index
	}
	for _, scan := range scans {
		if header.ilv != 0 {
			planes, err = decodeInterleavedScan(ctx, scan.entropy, header.columns, header.rows, len(scan.componentIDs), header.ilv, preset)
			if err != nil {
				return nil, err
			}
			break // The qualified interleaved profile has one complete scan in SOF order.
		}
		plane, err := decodeScanContext(ctx, scan.entropy, header.columns, header.rows, preset)
		if err != nil {
			return nil, err
		}
		planes[componentIndex[scan.componentIDs[0]]] = plane
	}
	return samplesToNativeContext(ctx, planes, metadata, preset.maxval)
}

func parseFrameHeader(stream []byte) (frameHeader, error) {
	header, _, err := splitScans(stream)
	return header, err
}

func splitScans(stream []byte) (frameHeader, []scanData, error) {
	var header frameHeader
	if len(stream) < 4 || stream[0] != 0xff || stream[1] != 0xd8 {
		return frameHeader{}, nil, fmt.Errorf("%w: missing SOI", ErrInvalidCodestream)
	}
	i := 2
	var scans []scanData
	seenSOF := false
	seenLSE := false
	seenScan := make(map[byte]bool)
	for i+1 < len(stream) {
		if stream[i] != 0xff {
			return frameHeader{}, nil, fmt.Errorf("%w: expected marker", ErrInvalidCodestream)
		}
		for i < len(stream) && stream[i] == 0xff {
			i++
		}
		if i >= len(stream) {
			return frameHeader{}, nil, fmt.Errorf("%w: truncated marker", ErrInvalidCodestream)
		}
		marker := stream[i]
		i++
		switch marker {
		case 0xd9:
			if !seenSOF {
				return frameHeader{}, nil, fmt.Errorf("%w: missing SOF55", ErrInvalidCodestream)
			}
			if len(seenScan) != header.components {
				return frameHeader{}, nil, fmt.Errorf("%w: incomplete component coverage", ErrInvalidCodestream)
			}
			preset, err := resolveCodingParameters(header.preset, header.precision, header.near)
			if err != nil {
				return frameHeader{}, nil, err
			}
			header.preset = preset
			trailing := stream[i:]
			if len(trailing) > 1 || len(trailing) == 1 && trailing[0] != 0 {
				return frameHeader{}, nil, fmt.Errorf("%w: trailing data after EOI", ErrInvalidCodestream)
			}
			return header, scans, nil
		case 0xf7:
			if seenSOF || len(scans) != 0 {
				return frameHeader{}, nil, fmt.Errorf("%w: duplicate or out-of-order SOF55", ErrInvalidCodestream)
			}
			seg, next, err := readSegment(stream, i)
			if err != nil {
				return frameHeader{}, nil, err
			}
			parsed, err := parseSOF55(seg)
			if err != nil {
				return frameHeader{}, nil, err
			}
			header = parsed
			seenSOF = true
			i = next
		case 0xda:
			if !seenSOF {
				return frameHeader{}, nil, fmt.Errorf("%w: SOS before SOF55", ErrInvalidCodestream)
			}
			seg, next, err := readSegment(stream, i)
			if err != nil {
				return frameHeader{}, nil, err
			}
			componentIDs, near, ilv, err := parseSOS(seg, header)
			if err != nil {
				return frameHeader{}, nil, err
			}
			for _, id := range componentIDs {
				if seenScan[id] {
					return frameHeader{}, nil, fmt.Errorf("%w: duplicate component scan %d", ErrInvalidCodestream, id)
				}
				seenScan[id] = true
			}
			if len(scans) == 0 {
				header.near = near
				header.ilv = ilv
			} else if header.near != near || header.ilv != ilv {
				return frameHeader{}, nil, fmt.Errorf("%w: mixed SOS parameters", ErrInvalidCodestream)
			}
			entropy, end, err := readEntropy(stream, next)
			if err != nil {
				return frameHeader{}, nil, err
			}
			scans = append(scans, scanData{componentIDs: componentIDs, entropy: entropy})
			i = end
		case 0xf8:
			if !seenSOF || len(scans) != 0 || seenLSE {
				return frameHeader{}, nil, fmt.Errorf("%w: duplicate or out-of-order LSE", ErrInvalidCodestream)
			}
			seg, next, err := readSegment(stream, i)
			if err != nil {
				return frameHeader{}, nil, err
			}
			preset, err := parseLSE(seg)
			if err != nil {
				return frameHeader{}, nil, err
			}
			header.preset = preset
			seenLSE = true
			i = next
		case 0xe0, 0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7,
			0xe8, 0xe9, 0xea, 0xeb, 0xec, 0xed, 0xee, 0xef, 0xfe:
			seg, next, err := readSegment(stream, i)
			if err != nil {
				return frameHeader{}, nil, err
			}
			if marker == 0xe8 && len(seg) >= 6 && string(seg[2:6]) == "mrfx" && (len(seg) != 7 || seg[6] != 0) {
				return frameHeader{}, nil, fmt.Errorf("%w: unsupported HP color transform", ErrInvalidCodestream)
			}
			i = next
		default:
			return frameHeader{}, nil, fmt.Errorf("%w: unsupported marker 0x%02x", ErrInvalidCodestream, marker)
		}
	}
	return frameHeader{}, nil, fmt.Errorf("%w: missing EOI", ErrInvalidCodestream)
}

func readSegment(stream []byte, i int) ([]byte, int, error) {
	if i+1 >= len(stream) {
		return nil, 0, fmt.Errorf("%w: truncated segment length", ErrInvalidCodestream)
	}
	length := int(stream[i])<<8 | int(stream[i+1])
	if length < 2 || i+length > len(stream) {
		return nil, 0, fmt.Errorf("%w: invalid segment length", ErrInvalidCodestream)
	}
	return stream[i : i+length], i + length, nil
}

func parseSOF55(seg []byte) (frameHeader, error) {
	if len(seg) < 8 {
		return frameHeader{}, fmt.Errorf("%w: truncated SOF55", ErrInvalidCodestream)
	}
	nf := int(seg[7])
	if nf != 1 && nf != 3 || 8+3*nf != len(seg) {
		return frameHeader{}, fmt.Errorf("%w: invalid SOF55 component count", ErrInvalidCodestream)
	}
	precision := int(seg[2])
	rows := int(seg[3])<<8 | int(seg[4])
	columns := int(seg[5])<<8 | int(seg[6])
	if precision < 2 || precision > 16 || rows == 0 || columns == 0 {
		return frameHeader{}, fmt.Errorf("%w: invalid SOF55 geometry or precision", ErrInvalidCodestream)
	}
	ids := make([]byte, nf)
	seen := make(map[byte]bool, nf)
	for component := 0; component < nf; component++ {
		offset := 8 + 3*component
		id := seg[offset]
		if seen[id] || seg[offset+1] != 0x11 || seg[offset+2] != 0 {
			return frameHeader{}, fmt.Errorf("%w: invalid SOF55 component parameters", ErrInvalidCodestream)
		}
		seen[id] = true
		ids[component] = id
	}
	return frameHeader{
		precision:    precision,
		rows:         rows,
		columns:      columns,
		components:   nf,
		componentIDs: ids,
	}, nil
}

func parseSOS(seg []byte, header frameHeader) (componentIDs []byte, near, ilv int, err error) {
	if len(seg) < 3 {
		return nil, 0, 0, fmt.Errorf("%w: truncated SOS", ErrInvalidCodestream)
	}
	ns := int(seg[2])
	need := 3 + 2*ns + 3
	if ns != 1 && ns != header.components || len(seg) != need {
		return nil, 0, 0, fmt.Errorf("%w: invalid SOS", ErrInvalidCodestream)
	}
	near = int(seg[3+2*ns])
	ilv = int(seg[3+2*ns+1])
	if ilv > 2 || (ns == 1) != (ilv == 0) || seg[3+2*ns+2] != 0 {
		return nil, 0, 0, fmt.Errorf("%w: unsupported SOS parameters", ErrInvalidCodestream)
	}
	componentIDs = make([]byte, ns)
	for component := 0; component < ns; component++ {
		id := seg[3+2*component]
		found := false
		for _, frameID := range header.componentIDs {
			found = found || frameID == id
		}
		// Interleaved scans cover every component, in frame-header order (T.87 B.4).
		if !found || seg[4+2*component] != 0 || ilv != 0 && id != header.componentIDs[component] {
			return nil, 0, 0, fmt.Errorf("%w: invalid SOS component selector or order", ErrInvalidCodestream)
		}
		componentIDs[component] = id
	}
	return componentIDs, near, ilv, nil
}

func parseLSE(seg []byte) (presetCodingParameters, error) {
	if len(seg) != 13 || seg[2] != 1 {
		return presetCodingParameters{}, fmt.Errorf("%w: unsupported LSE parameters", ErrInvalidCodestream)
	}
	preset := presetCodingParameters{
		maxval: int(binary.BigEndian.Uint16(seg[3:5])),
		t1:     int(binary.BigEndian.Uint16(seg[5:7])),
		t2:     int(binary.BigEndian.Uint16(seg[7:9])),
		t3:     int(binary.BigEndian.Uint16(seg[9:11])),
		reset:  int(binary.BigEndian.Uint16(seg[11:13])),
	}
	// NEAR is in SOS, so default thresholds and their validation are deferred
	// until the complete scan parameters are known.
	return preset, nil
}

func resolveCodingParameters(preset presetCodingParameters, precision, near int) (presetCodingParameters, error) {
	maximumForPrecision := (1 << precision) - 1
	// Zero fields select their defaults, per T.87 C.2.4.1.1.
	if preset.maxval == 0 {
		preset.maxval = maximumForPrecision
	}
	if near < 0 || near > minInt(255, preset.maxval/2) {
		return presetCodingParameters{}, fmtInvalid("NEAR exceeds MAXVAL bound")
	}
	preset.near = near
	t1, t2, t3 := defaultNearThresholds(preset.maxval, near)
	if preset.t1 == 0 {
		preset.t1 = t1
	}
	if preset.t2 == 0 {
		preset.t2 = t2
	}
	if preset.t3 == 0 {
		preset.t3 = t3
	}
	if preset.reset == 0 {
		preset.reset = basicReset
	}
	if preset.maxval < 1 || preset.maxval > maximumForPrecision ||
		preset.t1 < near+1 || preset.t1 > preset.t2 || preset.t2 > preset.t3 || preset.t3 > preset.maxval ||
		preset.reset < 3 || preset.reset > maxInt(255, preset.maxval) {
		return presetCodingParameters{}, fmt.Errorf("%w: invalid LSE preset coding parameters", ErrInvalidCodestream)
	}
	return preset, nil
}

func readEntropy(stream []byte, start int) ([]byte, int, error) {
	i := start
	for i+1 < len(stream) {
		if stream[i] != 0xff {
			i++
			continue
		}
		if i+1 < len(stream) && stream[i+1] == 0x00 {
			i += 2
			continue
		}
		// JPEG-LS stuffed 0 bit means next byte has MSB 0, so byte < 0x80.
		if i+1 < len(stream) && stream[i+1] < 0x80 {
			i++
			continue
		}
		return stream[start:i], i, nil
	}
	return nil, 0, fmt.Errorf("%w: truncated entropy", ErrInvalidCodestream)
}

func decodeScan(entropy []byte, width, height, maxval int) ([]int, error) {
	t1, t2, t3 := defaultThresholds(maxval)
	return decodeScanContext(context.Background(), entropy, width, height, presetCodingParameters{maxval: maxval, t1: t1, t2: t2, t3: t3, reset: basicReset})
}

func decodeScanContext(ctx context.Context, entropy []byte, width, height int, preset presetCodingParameters) ([]int, error) {
	st := newLocoStateForPreset(preset)
	r := &bitReader{buf: entropy}
	recon := make([]int, width*height)
	for y := 0; y < height; y++ {
		if err := decodeLine(ctx, r, st, recon, width, y); err != nil {
			return nil, err
		}
	}
	if err := validateEntropyPadding(r); err != nil {
		return nil, err
	}
	return recon, nil
}

func decodeLine(ctx context.Context, r *bitReader, st *locoState, recon []int, width, y int) error {
	for x := 0; x < width; {
		if x&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		ra, rb, rc, rd := neighbors(recon, width, x, y)
		q1 := st.quantize(rd - rb)
		q2 := st.quantize(rb - rc)
		q3 := st.quantize(rc - ra)
		if q1 == 0 && q2 == 0 && q3 == 0 {
			next, err := decodeRun(r, st, recon, width, x, y, ra, rb)
			if err != nil {
				return fmt.Errorf("run mode at (%d,%d): %w", x, y, err)
			}
			x = next
			continue
		}
		ix, err := decodeRegular(r, st, ra, rb, rc, q1, q2, q3)
		if err != nil {
			return fmt.Errorf("regular mode at (%d,%d): %w", x, y, err)
		}
		recon[y*width+x] = ix
		x++
	}
	return nil
}

func validateEntropyPadding(r *bitReader) error {
	paddingBits := 0
	for r.count > 0 || r.pos < len(r.buf) {
		bit, err := r.readBit()
		if err != nil {
			return fmt.Errorf("%w: invalid entropy padding: %v", ErrInvalidCodestream, err)
		}
		paddingBits++
		if paddingBits > 7 || bit != 0 {
			return fmt.Errorf("%w: trailing entropy data", ErrInvalidCodestream)
		}
	}
	if r.lastWasFF {
		return fmt.Errorf("%w: missing stuffed padding after 0xff", ErrInvalidCodestream)
	}
	return nil
}

func decodeRegular(r *bitReader, st *locoState, ra, rb, rc, q1, q2, q3 int) (int, error) {
	q := 81*q1 + 9*q2 + q3
	sign := 1
	if q < 0 {
		q = -q
		sign = -1
	}
	px := predict(ra, rb, rc) + sign*st.c[q]
	if px > st.maxval {
		px = st.maxval
	}
	if px < 0 {
		px = 0
	}
	k := st.golombK(st.a[q], st.n[q])
	mapped, err := readLimitedGolomb(r, k, st.limit, st.qbpp)
	if err != nil {
		return 0, err
	}
	errval := st.unmapRegular(mapped, q, k)
	st.updateRegular(q, errval)
	if sign < 0 {
		errval = -errval
	}
	return st.reconstruct(px, errval), nil
}

func decodeRun(r *bitReader, st *locoState, recon []int, width, x, y, ra, rb int) (int, error) {
	runVal := ra
	xx := x
	for {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if bit == 1 {
			fill := 1 << runJ[st.runIdx]
			remaining := width - xx
			if remaining >= fill {
				for i := 0; i < fill; i++ {
					recon[y*width+xx] = runVal
					xx++
				}
				if st.runIdx < 31 {
					st.runIdx++
				}
				if xx == width {
					return width, nil
				}
				continue
			}
			for xx < width {
				recon[y*width+xx] = runVal
				xx++
			}
			return width, nil
		}
		bits := runJ[st.runIdx]
		rem := 0
		if bits > 0 {
			value, err := r.readBits(bits)
			if err != nil {
				return 0, err
			}
			rem = int(value)
		}
		for i := 0; i < rem; i++ {
			if xx >= width {
				return 0, fmt.Errorf("%w: run exceeds line", ErrInvalidCodestream)
			}
			recon[y*width+xx] = runVal
			xx++
		}
		if xx >= width {
			return 0, fmt.Errorf("%w: missing run interruption sample", ErrInvalidCodestream)
		}
		glimitIndex := st.runIdx
		if st.runIdx > 0 {
			st.runIdx--
		}
		// The interruption is relative to xx, not the start of the run.
		_, rb, _, _ = neighbors(recon, width, xx, y)
		ix, err := decodeRunInterruption(r, st, ra, rb, glimitIndex)
		if err != nil {
			return 0, err
		}
		recon[y*width+xx] = ix
		return xx + 1, nil
	}
}

func decodeRunInterruption(r *bitReader, st *locoState, ra, rb, glimitIndex int) (int, error) {
	riType := 0
	if absInt(ra-rb) <= st.near {
		riType = 1
	}
	return decodeRunInterruptionType(r, st, ra, rb, glimitIndex, riType)
}

func decodeRunInterruptionType(r *bitReader, st *locoState, ra, rb, glimitIndex, riType int) (int, error) {
	px := rb
	if riType == 1 {
		px = ra
	}
	q := 365 + riType
	temp := st.a[q]
	if riType == 1 {
		temp = st.a[q] + st.n[q]>>1
	}
	k := st.golombK(temp, st.n[q])
	glimit := st.limit - runJ[glimitIndex] - 1
	mapped, err := readLimitedGolomb(r, k, glimit, st.qbpp)
	if err != nil {
		return 0, err
	}
	posBias := k == 0 && 2*st.nn[riType] < st.n[q]
	t := mapped + riType
	var errval int
	if posBias {
		if t%2 != 0 {
			errval = (t + 1) / 2
		} else {
			errval = -t / 2
		}
	} else if t%2 == 0 {
		errval = t / 2
	} else {
		errval = -(t + 1) / 2
	}
	mapFlag := 0
	if (k == 0 && errval > 0 && 2*st.nn[riType] < st.n[q]) || (errval < 0 && 2*st.nn[riType] >= st.n[q]) || (errval < 0 && k != 0) {
		mapFlag = 1
	}
	if 2*absInt(errval)-riType-mapFlag != mapped {
		return 0, fmt.Errorf("%w: run interruption mapping", ErrInvalidCodestream)
	}
	if errval < 0 {
		st.nn[riType]++
	}
	st.a[q] += (mapped + 1 - riType) >> 1
	if st.n[q] == st.reset {
		st.a[q] >>= 1
		st.n[q] >>= 1
		st.nn[riType] >>= 1
	}
	st.n[q]++
	// Adapt Nn using the mapped, sign-normalized error (T.87 A.7.2).
	if riType == 0 && ra > rb {
		errval = -errval
	}
	return st.reconstruct(px, errval), nil
}

func samplesToNative(planes [][]int, metadata pixeldata.Metadata, maxval int) ([]byte, error) {
	return samplesToNativeContext(context.Background(), planes, metadata, maxval)
}

func samplesToNativeContext(ctx context.Context, planes [][]int, metadata pixeldata.Metadata, maxval int) ([]byte, error) {
	comps := int(metadata.SamplesPerPixel)
	pixels := int(metadata.Rows) * int(metadata.Columns)
	if comps == 1 {
		return planeToNativeContext(ctx, planes[0], metadata, maxval)
	}
	bytesPerSample := int(metadata.BitsAllocated / 8)
	out := make([]byte, pixels*comps*bytesPerSample)
	for i := 0; i < pixels; i++ {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for c := 0; c < comps; c++ {
			sample := planes[c][i]
			if sample < 0 || sample > maxval {
				return nil, fmt.Errorf("%w: reconstructed sample", ErrInvalidCodestream)
			}
			stored := sample
			index := i*comps + c
			if bytesPerSample == 1 {
				out[index] = byte(stored)
			} else {
				binary.LittleEndian.PutUint16(out[index*2:], uint16(stored))
			}
		}
	}
	return out, nil
}

func planeToNativeContext(ctx context.Context, samples []int, metadata pixeldata.Metadata, maxval int) ([]byte, error) {
	bytesPerSample := int(metadata.BitsAllocated / 8)
	out := make([]byte, len(samples)*bytesPerSample)
	if bytesPerSample == 1 {
		for i, sample := range samples {
			if i&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if sample < 0 || sample > maxval {
				return nil, fmt.Errorf("%w: reconstructed sample", ErrInvalidCodestream)
			}
			stored := sample
			out[i] = byte(stored)
		}
		return out, nil
	}
	for i, sample := range samples {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if sample < 0 || sample > maxval {
			return nil, fmt.Errorf("%w: reconstructed sample", ErrInvalidCodestream)
		}
		stored := sample
		binary.LittleEndian.PutUint16(out[i*2:], uint16(stored))
	}
	return out, nil
}

func validateDecodeAllocation(header frameHeader, metadata pixeldata.Metadata) error {
	maxInt := uint64(^uint(0) >> 1)
	pixels := uint64(header.rows) * uint64(header.columns)
	components := uint64(header.components)
	bytesPerSample := uint64(metadata.BitsAllocated / 8)
	if pixels == 0 || components == 0 || bytesPerSample == 0 ||
		pixels > maxInt || components > maxInt/pixels {
		return fmt.Errorf("%w: decoded frame size overflow", ErrInvalidCodestream)
	}
	samples := pixels * components
	if bytesPerSample > maxInt/samples {
		return fmt.Errorf("%w: decoded frame size overflow", ErrInvalidCodestream)
	}
	outputBytes := samples * bytesPerSample
	intBytes := nativeIntBytes()
	if intBytes == 0 || samples > ^uint64(0)/intBytes {
		return fmt.Errorf("%w: decoded working-set overflow", ErrInvalidCodestream)
	}
	workingBytes := samples*intBytes + outputBytes
	if workingBytes > maxJPEGLSWorkingSetBytes {
		return fmt.Errorf("%w: decoded frame exceeds working-set limit", ErrInvalidCodestream)
	}
	return nil
}

func validateDecodeRequestAllocation(metadata pixeldata.Metadata) error {
	frameSize := metadata.FrameSize()
	frameCount := metadata.NumberOfFrames
	if frameSize <= 0 || frameCount <= 0 {
		return fmt.Errorf("%w: invalid decoded request size", ErrInvalidCodestream)
	}
	frames := uint64(frameCount)
	frameBytes := uint64(frameSize)
	if frameBytes > ^uint64(0)/frames {
		return fmt.Errorf("%w: decoded request size overflow", ErrInvalidCodestream)
	}
	totalOutputBytes := frameBytes * frames

	intBytes := nativeIntBytes()
	// A [][]byte retains one three-word slice header per decoded frame.
	if frames > ^uint64(0)/(3*intBytes) {
		return fmt.Errorf("%w: decoded request overhead overflow", ErrInvalidCodestream)
	}
	frameSliceBytes := frames * 3 * intBytes

	// During a decode the completed native frames coexist with the current
	// frame's int reconstruction planes. Include that peak transient memory in
	// the request-wide bound, not only in the per-frame check.
	samplesPerFrame := uint64(metadata.Rows) * uint64(metadata.Columns) * uint64(metadata.SamplesPerPixel)
	if samplesPerFrame > ^uint64(0)/intBytes {
		return fmt.Errorf("%w: decoded request working-set overflow", ErrInvalidCodestream)
	}
	planeBytes := samplesPerFrame * intBytes
	if totalOutputBytes > ^uint64(0)-frameSliceBytes || totalOutputBytes+frameSliceBytes > ^uint64(0)-planeBytes {
		return fmt.Errorf("%w: decoded request working-set overflow", ErrInvalidCodestream)
	}
	if totalOutputBytes+frameSliceBytes+planeBytes > maxJPEGLSWorkingSetBytes {
		return fmt.Errorf("%w: decoded request exceeds working-set limit", ErrInvalidCodestream)
	}
	return nil
}

func nativeIntBytes() uint64 {
	return uint64(32<<(^uint(0)>>63)) / 8
}

func fmtInvalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCodestream, detail)
}
