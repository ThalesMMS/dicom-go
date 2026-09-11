package jpegls

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestCharLSInterleaveFullSamples(t *testing.T) {
	root := filepath.Join("..", "codecfixture", "testdata", "codecfull")
	data, err := os.ReadFile(filepath.Join(root, "jpegls", "generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Fixtures []struct {
			ID, Path, ReferencePath string
			Input                   struct{ Rows, Columns, BitsStored, BitsAllocated uint16 }
		}
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Fixtures) != 57 {
		t.Fatalf("fixture count = %d", len(record.Fixtures))
	}
	for _, f := range record.Fixtures {
		t.Run(f.ID, func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join(root, f.Path))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(root, f.ReferencePath))
			if err != nil {
				t.Fatal(err)
			}
			meta := encoderMetadata(f.Input.Rows, f.Input.Columns, 3, f.Input.BitsAllocated, "RGB")
			meta.BitsStored, meta.HighBit = f.Input.BitsStored, f.Input.BitsStored-1
			got, err := decodeFrame(stream, meta)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("full reconstruction differs at byte %d", firstDifferentByte(got, want))
			}
			if strings.HasSuffix(f.ID, "ilv0") && !strings.Contains(f.ID, "preset") {
				encoded, err := NewEncoder().EncodeFrame(context.Background(), want, meta)
				if err != nil {
					t.Fatal(err)
				}
				_, oracleScans, err := splitScans(stream)
				if err != nil {
					t.Fatal(err)
				}
				_, localScans, err := splitScans(encoded.Data)
				if err != nil {
					t.Fatal(err)
				}
				if len(localScans) != len(oracleScans) {
					t.Fatal("encoder scan count")
				}
				for c := range localScans {
					if !bytes.Equal(localScans[c].entropy, oracleScans[c].entropy) {
						t.Fatalf("encoder entropy differs from CharLS for component %d", c)
					}
				}
			}
		})
	}
}

func interleaveFixture(t testing.TB, ilv int) ([]byte, []byte, pixeldata.Metadata) {
	t.Helper()
	root := filepath.Join("..", "codecfixture", "testdata", "codecfull", "jpegls")
	stream, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("rgb8-129x33-ilv%d.jls", ilv)))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(root, "rgb8-129x33.raw"))
	if err != nil {
		t.Fatal(err)
	}
	return stream, want, encoderMetadata(33, 129, 3, 8, "RGB")
}

func TestInterleaveRejectsHeadersAndIncompleteFrames(t *testing.T) {
	for _, ilv := range []int{1, 2} {
		t.Run(fmt.Sprint(ilv), func(t *testing.T) {
			base, _, meta := interleaveFixture(t, ilv)
			sof, sos := markerOffset(t, base, 0xf7), markerOffset(t, base, 0xda)
			for _, test := range []struct {
				name   string
				stream []byte
			}{
				{"duplicate SOF component", mutateAt(base, sof+13, base[sof+10])},
				{"duplicate SOS component", mutateAt(base, sos+7, base[sos+5])},
				{"missing SOS component", mutateAt(base, sos+4, 2)},
				{"unknown SOS component", mutateAt(base, sos+5, 99)},
				{"mapping table", mutateAt(base, sos+8, 1)},
				{"component order", mutateAt(mutateAt(base, sos+5, base[sos+7]), sos+7, base[sos+5])},
				{"NEAR", mutateAt(base, sos+11, 1)},
				{"ILV", mutateAt(base, sos+12, 3)},
				{"point transform", mutateAt(base, sos+13, 1)},
				{"extra scan", insertAt(base, len(base)-2, scanSegments(t, base)[0])},
				{"missing scan", append(append([]byte(nil), base[:sos]...), 0xff, 0xd9)},
				{"restart", insertAt(base, len(base)-2, []byte{0xff, 0xd0})},
				{"HP color transform", insertAt(base, sos, []byte{0xff, 0xe8, 0, 7, 'm', 'r', 'f', 'x', 1})},
				{"LSE reset limit", insertAt(base, sos, lseID1(255, 3, 7, 21, 256))},
				{"LSE maxval limit", insertAt(base, sos, lseID1(256, 3, 7, 21, 64))},
				{"LSE custom maxval", insertAt(base, sos, lseID1(254, 3, 7, 21, 64))},
			} {
				t.Run(test.name, func(t *testing.T) {
					obj, pixel := jpeglsObjectWithFragment(t, meta, test.stream)
					got, err := New().Decode(pixel, obj)
					if !errors.Is(err, ErrInvalidCodestream) || len(got.Data) != 0 {
						t.Fatalf("frames=%d error=%v", len(got.Data), err)
					}
				})
			}
			// Every byte truncation, including a forged EOI after shortened entropy.
			for end := 0; end < len(base)-2; end++ {
				stream := append(append([]byte(nil), base[:end]...), 0xff, 0xd9)
				got, err := decodeFrame(stream, meta)
				if err == nil || got != nil {
					t.Fatalf("truncation at %d accepted", end)
				}
			}
		})
	}
}

func TestInterleaveComponentIDsAndDefaultLSE(t *testing.T) {
	for _, ilv := range []int{1, 2} {
		base, want, meta := interleaveFixture(t, ilv)
		sof, sos := markerOffset(t, base, 0xf7), markerOffset(t, base, 0xda)
		for c, id := range []byte{0, 42, 255} {
			base[sof+10+3*c], base[sos+5+2*c] = id, id
		}
		base = insertAt(base, sos, lseID1(0, 0, 0, 0, 0))
		got, err := decodeFrame(base, meta)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("ILV=%d: %v", ilv, err)
		}
	}
}

func TestInterleaveMetadataCancellationAndConcurrency(t *testing.T) {
	for _, ilv := range []int{1, 2} {
		stream, want, meta := interleaveFixture(t, ilv)
		for _, test := range []struct {
			name   string
			change func(*pixeldata.Metadata)
		}{
			{"rows", func(m *pixeldata.Metadata) { m.Rows++ }},
			{"columns", func(m *pixeldata.Metadata) { m.Columns++ }},
			{"components", func(m *pixeldata.Metadata) { m.SamplesPerPixel = 1; m.PhotometricInterpretation = "MONOCHROME2" }},
			{"precision", func(m *pixeldata.Metadata) { m.BitsStored = 7; m.HighBit = 6 }},
			{"high bit", func(m *pixeldata.Metadata) { m.HighBit = 6 }},
			{"signed RGB", func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }},
			{"YBR", func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "YBR_FULL" }},
			{"planar", func(m *pixeldata.Metadata) { m.PlanarConfiguration = 1 }},
			{"allocation", func(m *pixeldata.Metadata) { m.Rows = 65535; m.Columns = 65535 }},
		} {
			t.Run(fmt.Sprintf("%d/%s", ilv, test.name), func(t *testing.T) {
				m := meta
				test.change(&m)
				obj, pixel := jpeglsObjectWithFragment(t, m, stream)
				got, err := New().Decode(pixel, obj)
				if err == nil || len(got.Data) != 0 {
					t.Fatalf("frames=%d error=%v", len(got.Data), err)
				}
			})
		}
		ctx := &decoderCancelContext{Context: context.Background(), remaining: 5}
		if got, err := decodeFrameContext(ctx, stream, meta); !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("cancel: %v", err)
		}
		obj, pixel := jpeglsObjectWithFragment(t, meta, stream)
		codec := New()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := codec.Decode(pixel, obj)
				if err != nil || len(got.Data) != 1 || !bytes.Equal(got.Data[0], want) {
					t.Errorf("concurrent decode: %v", err)
				}
			}()
		}
		wg.Wait()
		// Frame 1 may decode internally, but a bad frame 2 must publish no frames.
		obj.Put(dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "2"))
		pixel.Sequence.Fragments = [][]byte{stream, stream[:len(stream)/2]}
		if got, err := codec.Decode(pixel, obj); err == nil || len(got.Data) != 0 {
			t.Fatalf("partial publication: %v", err)
		}
	}
}

func FuzzDecodeInterleave(f *testing.F) {
	for _, ilv := range []int{1, 2} {
		stream, err := os.ReadFile(filepath.Join("..", "codecfixture", "testdata", "codecfull", "jpegls", fmt.Sprintf("rgb8-9x1-ilv%d.jls", ilv)))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(stream)
	}
	f.Fuzz(func(t *testing.T, stream []byte) {
		if len(stream) > 1<<16 {
			return
		}
		header, err := parseFrameHeader(stream)
		if err != nil || header.columns > 129 || header.rows > 33 {
			return
		}
		allocated := uint16(8)
		if header.precision > 8 {
			allocated = 16
		}
		meta := encoderMetadata(uint16(header.rows), uint16(header.columns), 3, allocated, "RGB")
		meta.BitsStored, meta.HighBit = uint16(header.precision), uint16(header.precision-1)
		_, _ = decodeFrame(stream, meta)
	})
}

func TestGolombKDoesNotOverflowInt(t *testing.T) {
	// Valid 16-bit MAXVAL and a large LSE RESET can require k=16 while
	// N<<16 itself does not fit a signed 32-bit int.
	st := newLocoState(65535)
	if got := st.golombK(65535*32768+1024, 65535); got != 16 {
		t.Fatalf("k=%d, want 16", got)
	}
}
