package jpegls

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const maxJPEGLSFragmentLength = uint64(1<<32 - 2)

// Encoder encodes canonical native frames with ILV=0. Its zero value and
// NewEncoder are lossless. Nonzero NEAR requires NewEncoderWithOptions.
// Encoder has no mutable state and is safe for concurrent use.
type Encoder struct{ near int }

// EncoderOptions selects the maximum absolute error in unsigned stored sample
// units. No clinical error is chosen by default. NEAR>0 requires AllowLossy;
// the dataset transcoder independently requires TranscodeOptions.AllowLossy.
type EncoderOptions struct {
	Near       int
	AllowLossy bool
}

// NewEncoderWithOptions selects lossless .80 for Near=0, or explicitly
// authorized near-lossless .81 for Near>0. The precision-specific upper bound
// min(255, MAXVAL/2) is checked against each frame's metadata before encoding.
func NewEncoderWithOptions(opts EncoderOptions) (*Encoder, error) {
	if opts.Near < 0 || opts.Near > 255 {
		return nil, unsupportedEncoderMetadata("NEAR")
	}
	if opts.Near > 0 && !opts.AllowLossy {
		return nil, pixeldata.ErrTranscodeLossyDisallowed
	}
	return &Encoder{near: opts.Near}, nil
}

// RegisterNearLosslessEncoder explicitly registers .81 in a caller-owned
// registry. It requires Near>0 and AllowLossy; RegisterEncoder stays lossless.
func RegisterNearLosslessEncoder(registry pixeldata.EncoderRegistry, opts EncoderOptions) error {
	if registry == nil {
		return pixeldata.ErrEncoderRegistryNil
	}
	if opts.Near == 0 {
		return unsupportedEncoderMetadata("NEAR")
	}
	encoder, err := NewEncoderWithOptions(opts)
	if err != nil {
		return err
	}
	return registry.RegisterEncoder(NearLosslessUID, encoder)
}

func (e *Encoder) nearParameter() int {
	if e == nil {
		return 0
	}
	return e.near
}

var _ pixeldata.FrameEncoder = (*Encoder)(nil)

// NewEncoder returns a pure-Go JPEG-LS Lossless frame encoder.
func NewEncoder() *Encoder {
	return &Encoder{}
}

// RegisterEncoder registers a JPEG-LS Lossless encoder in registry.
func RegisterEncoder(registry pixeldata.EncoderRegistry) error {
	if registry == nil {
		return pixeldata.ErrEncoderRegistryNil
	}
	return registry.RegisterEncoder(UID, NewEncoder())
}

// Capabilities describes the canonical native frames accepted by Encoder.
func (e *Encoder) Capabilities() pixeldata.EncoderCapabilities {
	caps := pixeldata.EncoderCapabilities{
		TransferSyntaxUID:          UID,
		BitsAllocated:              []uint16{8, 16},
		PixelRepresentations:       []uint16{0, 1},
		SamplesPerPixel:            []uint16{1, 3},
		PhotometricInterpretations: []string{"MONOCHROME1", "MONOCHROME2", "PALETTE COLOR", "RGB"},
		Lossless:                   true,
		SupportsMultiFrame:         true,
		Backend:                    "pure-go",
	}
	if e.nearParameter() > 0 {
		caps.TransferSyntaxUID = NearLosslessUID
		caps.PixelRepresentations = []uint16{0}
		caps.PhotometricInterpretations = []string{"MONOCHROME1", "MONOCHROME2", "RGB"}
		caps.Lossless = false
		caps.LossyMethod = "ISO_14495_1"
	}
	return caps
}

// EncodeFrame encodes one little-endian, sample-interleaved native frame.
func (e *Encoder) EncodeFrame(ctx context.Context, frame []byte, metadata pixeldata.Metadata) (pixeldata.EncodedFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	params, err := validateEncoderInput(metadata)
	if err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	near := e.nearParameter()
	if near > 0 {
		if near > minInt(255, params.maxval/2) {
			return pixeldata.EncodedFrame{}, unsupportedEncoderMetadata("NEAR")
		}
		// A small unsigned code error can cross a signed discontinuity or
		// select an unrelated palette entry. Neither profile is qualified.
		if metadata.PixelRepresentation != 0 {
			return pixeldata.EncodedFrame{}, unsupportedEncoderMetadata("PixelRepresentation")
		}
		if strings.EqualFold(strings.TrimSpace(metadata.PhotometricInterpretation), "PALETTE COLOR") {
			return pixeldata.EncodedFrame{}, unsupportedEncoderMetadata("PhotometricInterpretation")
		}
	}
	if uint64(len(frame)) != params.frameLength {
		return pixeldata.EncodedFrame{}, fmt.Errorf("%w: native frame length", pixeldata.ErrPixelDataSizeMismatch)
	}

	samples, err := nativeToSamples(frame, metadata, params)
	if err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	encoded, err := encodeFrameWithNear(ctx, samples, metadata, params, near)
	if err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	if len(encoded) == 0 {
		return pixeldata.EncodedFrame{}, fmt.Errorf("%w: empty frame", pixeldata.ErrEncoderOutputInvalid)
	}
	return pixeldata.EncodedFrame{Data: encoded}, nil
}

type encodeParams struct {
	bytesPerSample int
	maxval         int
	frameLength    uint64
	maxEncoded     uint64
}

func validateEncoderInput(metadata pixeldata.Metadata) (encodeParams, error) {
	if metadata.Rows == 0 {
		return encodeParams{}, unsupportedEncoderMetadata("Rows")
	}
	if metadata.Columns == 0 {
		return encodeParams{}, unsupportedEncoderMetadata("Columns")
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return encodeParams{}, unsupportedEncoderMetadata("BitsAllocated")
	}
	if metadata.BitsStored < 2 || metadata.BitsStored > metadata.BitsAllocated {
		return encodeParams{}, unsupportedEncoderMetadata("BitsStored")
	}
	if metadata.HighBit != metadata.BitsStored-1 {
		return encodeParams{}, unsupportedEncoderMetadata("HighBit")
	}
	if metadata.PixelRepresentation > 1 {
		return encodeParams{}, unsupportedEncoderMetadata("PixelRepresentation")
	}
	if metadata.SamplesPerPixel != 1 && metadata.SamplesPerPixel != 3 {
		return encodeParams{}, unsupportedEncoderMetadata("SamplesPerPixel")
	}
	if metadata.NumberOfFrames < 1 {
		return encodeParams{}, unsupportedEncoderMetadata("NumberOfFrames")
	}
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	switch metadata.SamplesPerPixel {
	case 1:
		if metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration != 0 {
			return encodeParams{}, unsupportedEncoderMetadata("PlanarConfiguration")
		}
		if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" && photometric != "PALETTE COLOR" {
			return encodeParams{}, unsupportedEncoderMetadata("PhotometricInterpretation")
		}
		if photometric == "PALETTE COLOR" && metadata.PixelRepresentation != 0 {
			return encodeParams{}, unsupportedEncoderMetadata("PixelRepresentation")
		}
	case 3:
		if photometric != "RGB" {
			return encodeParams{}, unsupportedEncoderMetadata("PhotometricInterpretation")
		}
		if metadata.PixelRepresentation != 0 {
			return encodeParams{}, unsupportedEncoderMetadata("PixelRepresentation")
		}
		if !metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration != 0 {
			return encodeParams{}, unsupportedEncoderMetadata("PlanarConfiguration")
		}
	}

	bytesPerSample := int(metadata.BitsAllocated / 8)
	pixels, ok := checkedMul(uint64(metadata.Rows), uint64(metadata.Columns))
	if !ok {
		return encodeParams{}, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	samples, ok := checkedMul(pixels, uint64(metadata.SamplesPerPixel))
	if !ok {
		return encodeParams{}, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	frameLength, ok := checkedMul(samples, uint64(bytesPerSample))
	if !ok {
		return encodeParams{}, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	limitBits := uint64(2 * (maxInt(2, int(metadata.BitsStored)) + 16))
	maxEncoded, ok := checkedMul(samples, (limitBits+7)/8+2)
	if !ok {
		return encodeParams{}, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	maxEncoded += 64 + uint64(metadata.SamplesPerPixel)*16
	if maxEncoded > maxJPEGLSFragmentLength || maxEncoded > uint64(int(^uint(0)>>1)) {
		return encodeParams{}, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	return encodeParams{
		bytesPerSample: bytesPerSample,
		maxval:         (1 << metadata.BitsStored) - 1,
		frameLength:    frameLength,
		maxEncoded:     maxEncoded,
	}, nil
}

func unsupportedEncoderMetadata(field string) error {
	return &pixeldata.UnsupportedEncoderMetadataError{Field: field}
}

func nativeToSamples(frame []byte, metadata pixeldata.Metadata, params encodeParams) ([]int, error) {
	count := int(metadata.Rows) * int(metadata.Columns) * int(metadata.SamplesPerPixel)
	if count < 0 || uint64(count) > uint64(math.MaxInt/4) {
		return nil, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	samples := make([]int, count)
	mask := params.maxval
	for i := 0; i < count; i++ {
		var raw int
		if params.bytesPerSample == 1 {
			raw = int(frame[i])
		} else {
			raw = int(frame[i*2]) | int(frame[i*2+1])<<8
		}
		raw &= (1 << metadata.BitsAllocated) - 1
		// JPEG-LS has no signed sample mode. DICOM signed pixels are encoded as
		// their BitsStored-wide two's-complement bit pattern and Pixel
		// Representation supplies the interpretation after decompression.
		samples[i] = raw & mask
		if samples[i] < 0 || samples[i] > mask {
			return nil, unsupportedEncoderMetadata("PixelData")
		}
	}
	return samples, nil
}

func encodeFrameWithNear(ctx context.Context, samples []int, metadata pixeldata.Metadata, params encodeParams, near int) ([]byte, error) {
	rows := int(metadata.Rows)
	cols := int(metadata.Columns)
	comps := int(metadata.SamplesPerPixel)
	w := &bitWriter{buf: make([]byte, 0, 64)}
	w.buf = append(w.buf, 0xff, 0xd8)
	writeSOF55(w, rows, cols, comps, int(metadata.BitsStored))
	for c := 0; c < comps; c++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		writeSOS(w, c+1)
		w.buf[len(w.buf)-3] = byte(near)
		plane := extractComponent(samples, rows, cols, comps, c)
		if err := encodeScanWithNear(ctx, w, plane, cols, rows, params.maxval, near); err != nil {
			return nil, err
		}
		w.padToByte()
	}
	w.buf = append(w.buf, 0xff, 0xd9)
	if uint64(len(w.buf)) > params.maxEncoded {
		return nil, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	return w.buf, nil
}

func writeSOF55(w *bitWriter, rows, cols, comps, precision int) {
	length := 8 + 3*comps
	w.buf = append(w.buf, 0xff, 0xf7, byte(length>>8), byte(length), byte(precision))
	w.buf = append(w.buf, byte(rows>>8), byte(rows), byte(cols>>8), byte(cols), byte(comps))
	for c := 0; c < comps; c++ {
		w.buf = append(w.buf, byte(c+1), 0x11, 0x00)
	}
}

func writeSOS(w *bitWriter, componentID int) {
	w.buf = append(w.buf, 0xff, 0xda, 0x00, 0x08, 0x01, byte(componentID), 0x00, 0x00, 0x00, 0x00)
}

func extractComponent(samples []int, rows, cols, comps, component int) []int {
	if comps == 1 {
		return samples
	}
	plane := make([]int, rows*cols)
	for i := range plane {
		plane[i] = samples[i*comps+component]
	}
	return plane
}

func encodeScanWithNear(ctx context.Context, w *bitWriter, samples []int, width, height, maxval, near int) error {
	t1, t2, t3 := defaultNearThresholds(maxval, near)
	st := newLocoStateForPreset(presetCodingParameters{maxval: maxval, near: near, t1: t1, t2: t2, t3: t3, reset: basicReset})
	recon := make([]int, width*height)
	copy(recon, samples)
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := 0; x < width; {
			ra, rb, rc, rd := neighbors(recon, width, x, y)
			q1 := st.quantize(rd - rb)
			q2 := st.quantize(rb - rc)
			q3 := st.quantize(rc - ra)
			if q1 == 0 && q2 == 0 && q3 == 0 {
				x = encodeRun(w, st, recon, width, x, y, ra, rb)
				continue
			}
			recon[y*width+x] = encodeRegular(w, st, recon[y*width+x], ra, rb, rc, q1, q2, q3)
			x++
		}
	}
	return nil
}

func encodeRegular(w *bitWriter, st *locoState, ix, ra, rb, rc, q1, q2, q3 int) int {
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
	err := quantizePredictionError(sign*(ix-px), st.near)
	rx := clampInt(px+sign*err*(2*st.near+1), 0, st.maxval)
	err = st.modRange(err)
	k := st.golombK(st.a[q], st.n[q])
	mapped := st.mapRegular(err, q, k)
	writeLimitedGolomb(w, mapped, k, st.limit, st.qbpp)
	st.updateRegular(q, err)
	return rx
}

// T.87 A.4.4 quantizes prediction error symmetrically, with reconstruction
// retained for subsequent predictions. This is distinct from gradient bins.
func quantizePredictionError(err, near int) int {
	if err < 0 {
		return -((near - err) / (2*near + 1))
	}
	return (err + near) / (2*near + 1)
}

func encodeRun(w *bitWriter, st *locoState, recon []int, width, x, y, ra, rb int) int {
	runVal := ra
	run := 0
	xx := x
	for xx < width && absInt(recon[y*width+xx]-runVal) <= st.near {
		recon[y*width+xx] = runVal
		run++
		xx++
	}
	interrupted := xx < width
	for run >= 1<<runJ[st.runIdx] {
		w.writeBit(1)
		run -= 1 << runJ[st.runIdx]
		if st.runIdx < 31 {
			st.runIdx++
		}
	}
	if interrupted {
		w.writeBit(0)
		if bits := runJ[st.runIdx]; bits > 0 {
			w.writeBits(uint32(run), bits)
		}
		glimitIndex := st.runIdx
		if st.runIdx > 0 {
			st.runIdx--
		}
		ix := recon[y*width+xx]
		_, rb, _, _ = neighbors(recon, width, xx, y)
		recon[y*width+xx] = encodeRunInterruption(w, st, ix, ra, rb, glimitIndex)
		return xx + 1
	}
	if run > 0 {
		w.writeBit(1)
	}
	return width
}

func encodeRunInterruption(w *bitWriter, st *locoState, ix, ra, rb, runIndexBefore int) int {
	riType := 0
	if absInt(ra-rb) <= st.near {
		riType = 1
	}
	px := rb
	if riType == 1 {
		px = ra
	}
	err := ix - px
	sign := 1
	if riType == 0 && ra > rb {
		err = -err
		sign = -1
	}
	err = quantizePredictionError(err, st.near)
	rx := clampInt(px+sign*err*(2*st.near+1), 0, st.maxval)
	err = st.modRange(err)
	q := 365 + riType
	temp := st.a[q]
	if riType == 1 {
		temp = st.a[q] + st.n[q]>>1
	}
	k := st.golombK(temp, st.n[q])
	nn := st.nn[riType]
	mapFlag := 0
	if (k == 0 && err > 0 && 2*nn < st.n[q]) || (err < 0 && 2*nn >= st.n[q]) || (err < 0 && k != 0) {
		mapFlag = 1
	}
	mapped := 2*absInt(err) - riType - mapFlag
	glimit := st.limit - runJ[runIndexBefore] - 1
	writeLimitedGolomb(w, mapped, k, glimit, st.qbpp)
	if err < 0 {
		st.nn[riType]++
	}
	st.a[q] += (mapped + 1 - riType) >> 1
	if st.n[q] == st.reset {
		st.a[q] >>= 1
		st.n[q] >>= 1
		st.nn[riType] >>= 1
	}
	st.n[q]++
	return rx
}

func checkedMul(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > math.MaxUint64/b {
		return 0, false
	}
	return a * b, true
}
