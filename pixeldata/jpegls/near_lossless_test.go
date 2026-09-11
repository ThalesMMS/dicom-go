package jpegls

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestNearLosslessExistingIndependentReconstructions(t *testing.T) {
	for _, name := range []string{"JPEGLSNearLossless_08", "JPEGLSNearLossless_16"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join("..", "codecfixture", "testdata", "codecfull", "pydicom")
			file, err := object.OpenFile(filepath.Join(root, name+".dcm"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			want, err := os.ReadFile(filepath.Join(root, name+".raw"))
			if err != nil {
				t.Fatal(err)
			}
			pixel, err := pixeldata.Extract(file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			got, err := NewNearLossless().Decode(pixel, file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Data) != 1 || !bytes.Equal(got.Data[0], want) {
				t.Fatal("independent full reconstruction mismatch")
			}
		})
	}
}

func nearFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "codecfixture", "testdata", "codecfull", "jpegls-near", name+".jls"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNearLosslessRejectsInvalidProfilesAndParameters(t *testing.T) {
	meta := encoderMetadata(33, 129, 3, 8, "RGB")
	base := nearFixture(t, "near-3-3c-8-129x33-ilv2")
	sos := markerOffset(t, base, 0xda)
	for _, test := range []struct {
		name   string
		stream []byte
	}{
		{"NEAR exceeds MAXVAL", mutateAt(base, sos+11, 128)},
		{"threshold below NEAR", insertAt(base, sos, lseID1(255, 3, 17, 41, 64))},
		{"unordered thresholds", insertAt(base, sos, lseID1(255, 5, 4, 41, 64))},
		{"invalid RESET", insertAt(base, sos, lseID1(255, 5, 17, 41, 256))},
		{"custom MAXVAL", insertAt(base, sos, lseID1(254, 5, 17, 41, 64))},
		{"unknown LSE", insertAt(base, sos, []byte{0xff, 0xf8, 0, 3, 2})},
		{"duplicate component", mutateAt(base, sos+7, base[sos+5])},
		{"restart", insertAt(base, len(base)-2, []byte{0xff, 0xd0})},
	} {
		t.Run(test.name, func(t *testing.T) {
			obj, pixel := jpeglsObjectWithFragment(t, meta, test.stream)
			got, err := NewNearLossless().Decode(pixel, obj)
			if !errors.Is(err, ErrInvalidCodestream) || len(got.Data) != 0 {
				t.Fatalf("frames=%d error=%v", len(got.Data), err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(*pixeldata.Metadata)
	}{
		{"signed", func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }},
		{"YBR", func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "YBR_FULL" }},
		{"stored precision", func(m *pixeldata.Metadata) { m.BitsStored = 7; m.HighBit = 6 }},
		{"high bit", func(m *pixeldata.Metadata) { m.HighBit = 6 }},
		{"planar", func(m *pixeldata.Metadata) { m.PlanarConfiguration = 1 }},
		{"rows", func(m *pixeldata.Metadata) { m.Rows++ }},
		{"allocation", func(m *pixeldata.Metadata) { m.Rows = 65535; m.Columns = 65535 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := meta
			test.change(&m)
			obj, pixel := jpeglsObjectWithFragment(t, m, base)
			got, err := NewNearLossless().Decode(pixel, obj)
			if err == nil || len(got.Data) != 0 {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	mono := nearFixture(t, "near-1-1c-8-129x33-ilv0")
	for _, photo := range []string{"MONOCHROME1", "MONOCHROME2", "PALETTE COLOR", " palette color "} {
		m := encoderMetadata(33, 129, 1, 8, photo)
		obj, pixel := jpeglsObjectWithFragment(t, m, mono)
		_, err := NewNearLossless().Decode(pixel, obj)
		if strings.EqualFold(strings.TrimSpace(photo), "PALETTE COLOR") {
			if !errors.Is(err, pixeldata.ErrUnsupportedPhotometricInterpretation) {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	for _, bits := range []uint16{8, 12, 16} {
		allocated := uint16(16)
		if bits == 8 {
			allocated = 8
		}
		m := encoderMetadata(33, 129, 1, allocated, "MONOCHROME2")
		m.BitsStored, m.HighBit, m.PixelRepresentation = bits, bits-1, 1
		obj, pixel := jpeglsObjectWithFragment(t, m, nearFixture(t, fmt.Sprintf("near-1-1c-%d-129x33-ilv0", bits)))
		if _, err := NewNearLossless().Decode(pixel, obj); !errors.Is(err, pixeldata.ErrUnsupportedPixelRepresentation) {
			t.Fatalf("signed %d: %v", bits, err)
		}
	}
	// Invalid parameters in only one of the three ILV=0 scans are not hidden.
	component := nearFixture(t, "near-3-3c-8-129x33-ilv0")
	scans := scanSegments(t, component)
	first := markerOffset(t, component, 0xda)
	component[first+len(scans[0])+7] = 7
	obj, pixel := jpeglsObjectWithFragment(t, meta, component)
	if _, err := NewNearLossless().Decode(pixel, obj); !errors.Is(err, ErrInvalidCodestream) {
		t.Fatalf("mixed NEAR: %v", err)
	}
}

func TestNearLosslessLifecycleAndRegistration(t *testing.T) {
	if err := RegisterNearLossless(nil); !errors.Is(err, pixeldata.ErrCodecRegistryNil) {
		t.Fatal(err)
	}
	r := pixeldata.NewMemoryRegistry()
	if err := RegisterNearLossless(r); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.GetCodec(UID); ok {
		t.Fatal("near registration changed the lossless UID")
	}
	if err := Register(r); err != nil {
		t.Fatal(err)
	}
	meta := encoderMetadata(33, 129, 3, 8, "RGB")
	stream := nearFixture(t, "near-3-3c-8-129x33-ilv1")
	obj, pixel := jpeglsObjectWithFragment(t, meta, stream)
	if _, err := r.DecodeFrames(UID, pixel, obj); err == nil {
		t.Fatal("lossless UID accepted NEAR")
	}
	if _, err := r.DecodeFrames(NearLosslessUID, pixel, obj); err != nil {
		t.Fatal(err)
	}
	codec := NewNearLossless()
	ctx := &decoderCancelContext{Context: context.Background(), remaining: 5}
	if got, err := codec.DecodeContext(ctx, pixel, obj); !errors.Is(err, context.Canceled) || len(got.Data) != 0 {
		t.Fatalf("cancellation: %v", err)
	}
	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := codec.DecodeContext(deadline, pixel, obj); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := codec.Decode(pixel, obj); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	obj.Put(dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "2"))
	pixel.Sequence.Fragments = [][]byte{stream, append(append([]byte(nil), stream[:len(stream)/2]...), 0xff, 0xd9)}
	if frames, err := codec.Decode(pixel, obj); err == nil || len(frames.Data) != 0 {
		t.Fatalf("partial output: %v", err)
	}
	large := encoderMetadata(1024, 1024, 1, 8, "MONOCHROME2")
	largeObj, largePixel := jpeglsObjectWithFragment(t, large, stream)
	largeObj.Put(dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "1000"))
	if _, err := codec.Decode(largePixel, largeObj); !errors.Is(err, ErrInvalidCodestream) {
		t.Fatalf("request budget: %v", err)
	}
	for _, ilv := range []int{0, 1, 2} {
		small := nearFixture(t, fmt.Sprintf("near-1-3c-8-9x1-ilv%d", ilv))
		m := encoderMetadata(1, 9, 3, 8, "RGB")
		for end := 0; end < len(small)-2; end++ {
			truncated := append(append([]byte(nil), small[:end]...), 0xff, 0xd9)
			if got, err := decodeFrameModeContext(context.Background(), truncated, m, true); err == nil || got != nil {
				t.Fatalf("truncation ILV=%d offset=%d", ilv, end)
			}
		}
	}
}

func FuzzDecodeNearLossless(f *testing.F) {
	for _, ilv := range []int{0, 1, 2} {
		f.Add(nearFixture(f, fmt.Sprintf("near-1-3c-8-9x1-ilv%d", ilv)))
	}
	f.Fuzz(func(t *testing.T, stream []byte) {
		if len(stream) > 1<<16 {
			return
		}
		h, err := parseFrameHeader(stream)
		if err != nil || h.columns > 129 || h.rows > 33 {
			return
		}
		allocated := uint16(8)
		if h.precision > 8 {
			allocated = 16
		}
		photo := "MONOCHROME2"
		if h.components == 3 {
			photo = "RGB"
		}
		meta := encoderMetadata(uint16(h.rows), uint16(h.columns), uint16(h.components), allocated, photo)
		meta.BitsStored, meta.HighBit = uint16(h.precision), uint16(h.precision-1)
		_, _ = decodeFrameModeContext(context.Background(), stream, meta, true)
	})
}

func TestNearLosslessCharLSFullSamplesAndSourceBounds(t *testing.T) {
	root := filepath.Join("..", "codecfixture", "testdata", "codecfull")
	data, err := os.ReadFile(filepath.Join(root, "jpegls-near", "generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Fixtures []struct {
			ID, Path, SHA256, ReferencePath, ReferenceSHA256 string
			Input                                            struct {
				Rows, Columns, Components, BitsAllocated, BitsStored uint16
				Photometric                                          string
			}
			SourceSamples struct {
				Path, SHA256 string
				Near         int
			}
		}
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Fixtures) != 116 {
		t.Fatalf("fixtures=%d", len(record.Fixtures))
	}
	read := func(t *testing.T, path, hash string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != hash {
			t.Fatal("fixture hash mismatch")
		}
		return b
	}
	for _, f := range record.Fixtures {
		t.Run(f.ID, func(t *testing.T) {
			stream, want, source := read(t, f.Path, f.SHA256), read(t, f.ReferencePath, f.ReferenceSHA256), read(t, f.SourceSamples.Path, f.SourceSamples.SHA256)
			meta := encoderMetadata(f.Input.Rows, f.Input.Columns, f.Input.Components, f.Input.BitsAllocated, f.Input.Photometric)
			meta.BitsStored, meta.HighBit = f.Input.BitsStored, f.Input.BitsStored-1
			obj, pixel := jpeglsObjectWithFragment(t, meta, stream)
			got, err := NewNearLossless().DecodeContext(context.Background(), pixel, obj)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Data) != 1 || !bytes.Equal(got.Data[0], want) {
				t.Fatal("full reconstruction differs from CharLS")
			}
			if len(want) != len(source) {
				t.Fatal("source layout mismatch")
			}
			step := int(meta.BitsAllocated / 8)
			for i := 0; i < len(source); i += step {
				a, b := int(source[i]), int(got.Data[0][i])
				if step == 2 {
					a, b = int(binary.LittleEndian.Uint16(source[i:])), int(binary.LittleEndian.Uint16(got.Data[0][i:]))
				}
				if absInt(a-b) > f.SourceSamples.Near {
					t.Fatalf("source bound exceeded at sample %d", i/step)
				}
			}
			if f.SourceSamples.Near > 0 {
				if frames, err := New().Decode(pixel, obj); err == nil || len(frames.Data) != 0 {
					t.Fatal("lossless decoder accepted NEAR>0")
				}
			}
		})
	}
}
