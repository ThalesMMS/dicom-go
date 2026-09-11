package jpeg

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Generated independently with libjpeg-turbo 3.1.0 using 12-bit precision,
// quality 100 and a restart interval of one block. The 16x8 source sample at
// (x,y) is x*256+y*16.
const process4RestartFixtureBase64 = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQH/wQALDAAIABABAREA/8QAFQABAQAAAAAAAAAAAAAAAAAADQ7/xAAcEAAABgMAAAAAAAAAAAAAAAAACQ0lOURYYnb/3QAEAAH/2gAIAQEAAD8Anb9Nxi24F2DG2tzWo//QdwE3GLbgXYMba3Naj//Z"

func TestJPEGExtendedProcess4DecodesRestartIntervals(t *testing.T) {
	stream, err := base64.StdEncoding.DecodeString(process4RestartFixtureBase64)
	if err != nil {
		t.Fatal(err)
	}
	obj, pixel := process4ObjectNoTest(stream, 8, 16, 16, 12, 11, 0)
	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 || len(frames.Data[0]) != 16*8*2 {
		t.Fatalf("decoded frame layout = %d frames/%d bytes", len(frames.Data), len(frames.Data[0]))
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < 16; x++ {
			index := y*16 + x
			got := int(binary.LittleEndian.Uint16(frames.Data[0][index*2:]))
			want := x*256 + y*16
			if delta := got - want; delta < -1 || delta > 1 {
				t.Fatalf("sample (%d,%d) = %d, want %d (+/-1)", x, y, got, want)
			}
		}
	}
}

func TestRegisteredJPEGCodecsKeepBaselineAndExtendedModesSeparate(t *testing.T) {
	sof0 := encodeGrayJPEG(t, 2, 1, []byte{0, 255})
	sof1 := makeJPEGExtendedSOF1(t, encodeGrayJPEG(t, 2, 1, []byte{0, 255}))
	obj, pixel := jpegObjectWithFragments(t, 1, 2, sof1)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.DecodeFrames(transfer.JPEGBaseline.UID, pixel, obj); !errors.Is(err, ErrInvalidFragment) {
		t.Fatalf("Baseline decoding SOF1 error = %v, want ErrInvalidFragment", err)
	}
	if _, err := registry.DecodeFrames(transfer.JPEGExtended.UID, pixel, obj); err != nil {
		t.Fatalf("Extended decoding SOF1 error = %v", err)
	}

	baselineObject, baselinePixel := jpegObjectWithFragments(t, 1, 2, sof0)
	if _, err := registry.DecodeFrames(transfer.JPEGBaseline.UID, baselinePixel, baselineObject); err != nil {
		t.Fatalf("Baseline decoding SOF0 error = %v", err)
	}
	if _, err := registry.DecodeFrames(transfer.JPEGExtended.UID, baselinePixel, baselineObject); !errors.Is(err, ErrInvalidFragment) {
		t.Fatalf("Extended decoding SOF0 error = %v, want ErrInvalidFragment", err)
	}
}

func TestJPEGExtendedProcess4RejectsDuplicateAndOutOfOrderMarkers(t *testing.T) {
	base := process4Fixture(t)
	dqt := jpegMarkerBytes(t, base, 0xdb)
	sof := jpegMarkerBytes(t, base, 0xc1)
	sos := jpegMarkerBytes(t, base, 0xda)
	eoi := bytes.Index(base, []byte{0xff, 0xd9})
	if eoi < 0 {
		t.Fatal("fixture has no EOI")
	}
	restart, err := base64.StdEncoding.DecodeString(process4RestartFixtureBase64)
	if err != nil {
		t.Fatal(err)
	}
	dri := jpegMarkerBytes(t, restart, 0xdd)

	tests := []struct {
		name          string
		stream        []byte
		rows, columns uint16
	}{
		{name: "duplicate DQT", stream: insertJPEGBytes(base, bytes.Index(base, dqt)+len(dqt), dqt), rows: 8, columns: 8},
		{name: "duplicate SOF1", stream: insertJPEGBytes(base, bytes.Index(base, sos), sof), rows: 8, columns: 8},
		{name: "duplicate SOS", stream: insertJPEGBytes(base, eoi, sos), rows: 8, columns: 8},
		{name: "DQT after SOS", stream: insertJPEGBytes(base, eoi, dqt), rows: 8, columns: 8},
		{name: "duplicate DRI", stream: insertJPEGBytes(restart, bytes.Index(restart, dri)+len(dri), dri), rows: 8, columns: 16},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			obj, pixel := process4ObjectNoTest(test.stream, test.rows, test.columns, 16, 12, 11, 0)
			if _, err := New().Decode(pixel, obj); err == nil {
				t.Fatal("Decode() error = nil")
			}
		})
	}
}

func TestJPEGExtendedProcess4AcceptsOneArbitraryDICOMPaddingByte(t *testing.T) {
	base := process4Fixture(t)
	for _, padding := range []byte{0x00, 0xa2, 0xff} {
		stream := append(append([]byte(nil), base...), padding)
		if _, err := decodeProcess4Fixture(stream, 8, 8, 12); err != nil {
			t.Fatalf("padding 0x%02x: %v", padding, err)
		}
	}
	stream := append(append([]byte(nil), base...), 0, 0)
	if _, err := decodeProcess4Fixture(stream, 8, 8, 12); err == nil {
		t.Fatal("two padding bytes accepted")
	}
}

func TestJPEGExtendedProcess4RejectsCompleteHuffmanCodeTree(t *testing.T) {
	// Two one-bit codes consume the entire tree, including the all-ones code
	// that JPEG reserves so fill bits cannot become a symbol.
	segment := make([]byte, 0, 19)
	segment = append(segment, 0x00, 0x02)
	segment = append(segment, make([]byte, 15)...)
	segment = append(segment, 0x00, 0x01)
	if err := (&extendedProcess4Decoder{}).parseDHT(segment); err == nil {
		t.Fatal("complete Huffman tree accepted")
	}
}

func TestJPEGExtendedProcess4RejectsOutOfRangeDCHuffmanSymbol(t *testing.T) {
	segment := make([]byte, 0, 18)
	segment = append(segment, 0x00, 0x01)
	segment = append(segment, make([]byte, 15)...)
	segment = append(segment, 0x10)
	if err := (&extendedProcess4Decoder{}).parseDHT(segment); err == nil {
		t.Fatal("DC Huffman symbol 16 accepted")
	}
}

func TestJPEGExtendedProcess4RejectsAggregateRequestAboveLimit(t *testing.T) {
	tests := []struct {
		name      string
		metadata  pixeldata.Metadata
		fragments [][]byte
	}{
		{
			name: "decoded frames accumulate above limit",
			metadata: pixeldata.Metadata{
				Rows: 32768, Columns: 4096, SamplesPerPixel: 1,
				BitsAllocated: 16, BitsStored: 12, HighBit: 11, NumberOfFrames: 2,
			},
			fragments: [][]byte{{0xff, 0xd8}, {0xff, 0xd8}},
		},
		{
			name: "compressed fragments accumulate above limit",
			metadata: pixeldata.Metadata{
				Rows: 1, Columns: 1, SamplesPerPixel: 1,
				BitsAllocated: 16, BitsStored: 12, HighBit: 11, NumberOfFrames: 513,
			},
			fragments: func() [][]byte {
				// Reusing one backing buffer keeps the regression lightweight while
				// exercising the logical retained-input sum.
				fragment := make([]byte, 1<<20)
				fragments := make([][]byte, 513)
				for i := range fragments {
					fragments[i] = fragment
				}
				return fragments
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateExtendedProcess4Request(test.metadata, test.fragments); !errors.Is(err, ErrInvalidFragment) {
				t.Fatalf("error = %v, want ErrInvalidFragment", err)
			}
		})
	}
}

func jpegMarkerBytes(t *testing.T, stream []byte, marker byte) []byte {
	t.Helper()
	offset := bytes.Index(stream, []byte{0xff, marker})
	if offset < 0 || offset+4 > len(stream) {
		t.Fatalf("marker 0xff%02x not found", marker)
	}
	length := int(binary.BigEndian.Uint16(stream[offset+2:]))
	end := offset + 2 + length
	if length < 2 || end > len(stream) {
		t.Fatalf("marker 0xff%02x length=%d", marker, length)
	}
	return append([]byte(nil), stream[offset:end]...)
}

func insertJPEGBytes(stream []byte, offset int, addition []byte) []byte {
	result := make([]byte, 0, len(stream)+len(addition))
	result = append(result, stream[:offset]...)
	result = append(result, addition...)
	return append(result, stream[offset:]...)
}
