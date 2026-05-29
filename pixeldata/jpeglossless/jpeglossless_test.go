package jpeglossless

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

var (
	tagRows                = core.NewTag(0x0028, 0x0010)
	tagColumns             = core.NewTag(0x0028, 0x0011)
	tagSamplesPerPixel     = core.NewTag(0x0028, 0x0002)
	tagPhotometric         = core.NewTag(0x0028, 0x0004)
	tagNumberOfFrames      = core.NewTag(0x0028, 0x0008)
	tagBitsAllocated       = core.NewTag(0x0028, 0x0100)
	tagBitsStored          = core.NewTag(0x0028, 0x0101)
	tagHighBit             = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation = core.NewTag(0x0028, 0x0103)
)

func metadataElements(rows, cols, bitsAllocated, bitsStored uint16, samples int) []core.Element {
	return metadataElementsWithPhotometric(rows, cols, bitsAllocated, bitsStored, samples, "MONOCHROME2")
}

func metadataElementsWithPhotometric(rows, cols, bitsAllocated, bitsStored uint16, samples int, photometric string) []core.Element {
	return []core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, nil, rows),
		dicomtest.Uint16Element(tagColumns, core.VRUS, nil, cols),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, uint16(samples)),
		dicomtest.NewStringElement(tagPhotometric, core.VRCS, photometric),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, "1"),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, nil, bitsAllocated),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, nil, bitsStored),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, bitsStored-1),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, nil, 0),
	}
}

// ---- minimal JPEG Lossless encoder (test only) ------------------------------

type bitWriter struct {
	out   []byte
	cur   byte
	nbits int
}

func (bw *bitWriter) writeBits(v, n int) {
	for i := n - 1; i >= 0; i-- {
		bw.cur = (bw.cur << 1) | byte((v>>uint(i))&1)
		bw.nbits++
		if bw.nbits == 8 {
			bw.flushByte()
		}
	}
}

func (bw *bitWriter) flushByte() {
	bw.out = append(bw.out, bw.cur)
	if bw.cur == 0xFF {
		bw.out = append(bw.out, 0x00) // byte stuffing
	}
	bw.cur = 0
	bw.nbits = 0
}

func (bw *bitWriter) pad() {
	if bw.nbits > 0 {
		bw.cur = (bw.cur << uint(8-bw.nbits)) | byte((1<<uint(8-bw.nbits))-1) // pad with 1s
		bw.nbits = 8
		bw.flushByte()
	}
}

func bitLen(v int32) int {
	if v < 0 {
		v = -v
	}
	n := 0
	for v > 0 {
		n++
		v >>= 1
	}
	return n
}

// encodeLossless builds a single-component JPEG Lossless fragment from samples.
func encodeLossless(width, height, precision, predictor int, samples []int32) []byte {
	return encodeLosslessWithOptions(width, height, precision, predictor, 0, samples, 0)
}

func encodeLosslessWithRestartInterval(width, height, precision, predictor int, samples []int32, restartInterval int) []byte {
	return encodeLosslessWithOptions(width, height, precision, predictor, 0, samples, restartInterval)
}

func encodeLosslessWithPointTransform(width, height, precision, predictor, pointTransform int, samples []int32) []byte {
	return encodeLosslessWithOptions(width, height, precision, predictor, pointTransform, samples, 0)
}

func encodeLosslessWithOptions(width, height, precision, predictor, pointTransform int, samples []int32, restartInterval int) []byte {
	codedSamples := samples
	if pointTransform > 0 {
		codedSamples = make([]int32, len(samples))
		for i, sample := range samples {
			codedSamples[i] = sample >> uint(pointTransform)
		}
	}

	var out []byte
	out = append(out, 0xFF, 0xD8) // SOI
	// SOF3
	out = append(out, 0xFF, 0xC3, 0x00, 0x0B, byte(precision),
		byte(height>>8), byte(height), byte(width>>8), byte(width), 0x01,
		0x01, 0x11, 0x00)
	if restartInterval > 0 {
		out = append(out, 0xFF, 0xDD, 0x00, 0x04, byte(restartInterval>>8), byte(restartInterval))
	}
	// DHT: class 0, table 0; 17 symbols all at length 5; values 0..16.
	dht := []byte{0xFF, 0xC4, 0x00, 0x24, 0x00,
		0, 0, 0, 0, 17, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0} // counts[1..16], counts[5]=17
	for s := 0; s <= 16; s++ {
		dht = append(dht, byte(s))
	}
	out = append(out, dht...)
	// SOS: Ns=1, Cs=1, Td=0, Ss=predictor, Se=0, Al=pointTransform.
	out = append(out, 0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, byte(predictor), 0x00, byte(pointTransform&0x0F))

	bw := &bitWriter{}
	defaultPred := int32(1) << (precision - pointTransform - 1)
	restartCount := 0
	restartMarker := 0
	restartStart := 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sampleIndex := y*width + x
			px := predict(codedSamples, x, y, width, predictor, defaultPred)
			if sampleIndex == restartStart {
				px = defaultPred
			}
			diff := codedSamples[y*width+x] - px
			s := bitLen(diff)
			bw.writeBits(s, 5) // Huffman code for symbol s == s (this table)
			if s > 0 {
				enc := diff
				if diff < 0 {
					enc = diff + (1 << uint(s)) - 1
				}
				bw.writeBits(int(enc)&((1<<uint(s))-1), s)
			}
			if restartInterval > 0 {
				restartCount++
				if restartCount == restartInterval && sampleIndex != len(samples)-1 {
					bw.pad()
					bw.out = append(bw.out, 0xFF, byte(0xD0+restartMarker%8))
					restartMarker++
					restartCount = 0
					restartStart = sampleIndex + 1
				}
			}
		}
	}
	bw.pad()
	out = append(out, bw.out...)
	out = append(out, 0xFF, 0xD9) // EOI
	return out
}

func setSOF3Precision(t *testing.T, fragment []byte, precision byte) {
	t.Helper()
	for i := 0; i+6 < len(fragment); i++ {
		if fragment[i] != 0xFF || fragment[i+1] != markerSOF3 {
			continue
		}
		fragment[i+4] = precision
		return
	}
	t.Fatal("SOF3 marker not found")
}

func setSOSPointTransform(t *testing.T, fragment []byte, pointTransform byte) {
	t.Helper()
	for i := 0; i+3 < len(fragment); i++ {
		if fragment[i] != 0xFF || fragment[i+1] != markerSOS {
			continue
		}
		length := int(fragment[i+2])<<8 | int(fragment[i+3])
		pointTransformIndex := i + 2 + length - 1
		if pointTransformIndex >= len(fragment) {
			t.Fatalf("SOS length %d points past fragment length %d", length, len(fragment))
		}
		fragment[pointTransformIndex] = (fragment[pointTransformIndex] & 0xF0) | (pointTransform & 0x0F)
		return
	}
	t.Fatal("SOS marker not found")
}

func encapsulated(fragment []byte) pixeldata.PixelData {
	return pixeldata.PixelData{
		Encapsulated: true,
		Sequence:     core.FragmentSequence{Fragments: [][]byte{fragment}},
	}
}

func TestJPEGLosslessRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		w, h, p   int
		predictor int
		bitsAlloc uint16
	}{
		{"8-bit pred1", 8, 6, 8, 1, 8},
		{"8-bit pred4", 7, 5, 8, 4, 8},
		{"16-bit(12) pred1", 10, 8, 12, 1, 16},
		{"16-bit(12) pred6", 9, 9, 12, 6, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rng := uint32(12345)
			maxVal := int32(1)<<tc.p - 1
			samples := make([]int32, tc.w*tc.h)
			for i := range samples {
				rng = rng*1664525 + 1013904223
				switch i % 3 {
				case 0:
					samples[i] = int32(i) % (maxVal + 1) // gradient
				case 1:
					samples[i] = maxVal / 2 // flat
				default:
					samples[i] = int32(rng>>16) % (maxVal + 1) // pseudo-random
				}
			}
			frag := encodeLossless(tc.w, tc.h, tc.p, tc.predictor, samples)
			obj := object.FromElements(metadataElements(uint16(tc.h), uint16(tc.w), tc.bitsAlloc, uint16(tc.p), 1), nil)

			frames, err := New().Decode(encapsulated(frag), obj)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if frames.Rows != tc.h || frames.Columns != tc.w {
				t.Fatalf("size = %dx%d, want %dx%d", frames.Columns, frames.Rows, tc.w, tc.h)
			}
			got := frames.Data[0]
			for i, want := range samples {
				var g int32
				if tc.bitsAlloc == 8 {
					g = int32(got[i])
				} else {
					g = int32(got[i*2]) | int32(got[i*2+1])<<8
				}
				if g != want {
					t.Fatalf("pixel %d = %d, want %d", i, g, want)
				}
			}
		})
	}
}

func TestJPEGLosslessRestartMarkersResetPrediction(t *testing.T) {
	samples := []int32{100, 110, 150, 160}
	frag := encodeLosslessWithRestartInterval(4, 1, 8, 1, samples, 2)
	obj := object.FromElements(metadataElements(1, 4, 8, 8, 1), nil)

	frames, err := New().Decode(encapsulated(frag), obj)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got := frames.Data[0]
	for i, want := range samples {
		if int32(got[i]) != want {
			t.Fatalf("pixel %d = %d, want %d after restart reset", i, got[i], want)
		}
	}
}

func TestJPEGLosslessPointTransformScalesOutputSamples(t *testing.T) {
	samples := []int32{100, 104, 112, 116}
	frag := encodeLosslessWithPointTransform(4, 1, 8, 1, 2, samples)
	obj := object.FromElements(metadataElements(1, 4, 8, 8, 1), nil)

	frames, err := New().Decode(encapsulated(frag), obj)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got := frames.Data[0]
	for i, want := range samples {
		if int32(got[i]) != want {
			t.Fatalf("pixel %d = %d, want %d after Pt scaling", i, got[i], want)
		}
	}
}

func TestJPEGLosslessRejectsNonEncapsulated(t *testing.T) {
	obj := object.FromElements(metadataElements(2, 2, 8, 8, 1), nil)
	if _, err := New().Decode(pixeldata.PixelData{Raw: []byte{0, 1, 2, 3}}, obj); err == nil {
		t.Error("expected an error for non-encapsulated pixel data")
	}
}

func TestJPEGLosslessRejectsMultiComponent(t *testing.T) {
	obj := object.FromElements(metadataElements(2, 2, 8, 8, 3), nil)
	frag := encodeLossless(2, 2, 8, 1, []int32{1, 2, 3, 4})
	if _, err := New().Decode(encapsulated(frag), obj); err == nil {
		t.Error("expected an error for multi-component (color) input")
	}
}

func TestJPEGLosslessRejectsUnsupportedPhotometricInterpretation(t *testing.T) {
	obj := object.FromElements(metadataElementsWithPhotometric(2, 2, 8, 8, 1, "PALETTE COLOR"), nil)
	frag := encodeLossless(2, 2, 8, 1, []int32{1, 2, 3, 4})

	_, err := New().Decode(encapsulated(frag), obj)
	if !errors.Is(err, pixeldata.ErrUnsupportedPhotometricInterpretation) {
		t.Fatalf("Decode() error = %v, want ErrUnsupportedPhotometricInterpretation", err)
	}
}

func TestJPEGLosslessRejectsInvalidSOF3PrecisionAndPointTransform(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, []byte)
	}{
		{
			name: "zero precision",
			mutate: func(t *testing.T, fragment []byte) {
				setSOF3Precision(t, fragment, 0)
			},
		},
		{
			name: "precision above 16 bits",
			mutate: func(t *testing.T, fragment []byte) {
				setSOF3Precision(t, fragment, 17)
			},
		},
		{
			name: "point transform equal to precision",
			mutate: func(t *testing.T, fragment []byte) {
				setSOSPointTransform(t, fragment, 8)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fragment := encodeLossless(1, 1, 8, 1, []int32{1})
			tc.mutate(t, fragment)
			obj := object.FromElements(metadataElements(1, 1, 8, 8, 1), nil)

			_, err := New().Decode(encapsulated(fragment), obj)
			if !errors.Is(err, ErrInvalidStream) {
				t.Fatalf("Decode() error = %v, want ErrInvalidStream", err)
			}
		})
	}
}

func TestJPEGLosslessSV1RejectsNonFirstOrderPredictor(t *testing.T) {
	reg := pixeldata.NewMemoryRegistry()
	if err := Register(reg); err != nil {
		t.Fatal(err)
	}
	frag := encodeLossless(2, 2, 8, 2, []int32{10, 20, 30, 40})
	obj := object.FromElements(metadataElements(2, 2, 8, 8, 1), nil)

	_, err := reg.DecodeFrames(UIDProcess14SV1, encapsulated(frag), obj)
	if !errors.Is(err, ErrUnsupportedScan) {
		t.Fatalf("DecodeFrames(SV1 predictor 2) error = %v, want ErrUnsupportedScan", err)
	}
}

func TestJPEGLosslessRegisters(t *testing.T) {
	reg := pixeldata.NewMemoryRegistry()
	if err := Register(reg); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{UIDProcess14, UIDProcess14SV1} {
		frag := encodeLossless(2, 2, 8, 1, []int32{10, 20, 30, 40})
		obj := object.FromElements(metadataElements(2, 2, 8, 8, 1), nil)
		if _, err := reg.DecodeFrames(uid, encapsulated(frag), obj); err != nil {
			t.Errorf("DecodeFrames(%s): %v", uid, err)
		}
	}
}
