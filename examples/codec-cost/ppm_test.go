package codeccost

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestPPMToFrameBytesRejectsUnsupportedBitsAllocated(t *testing.T) {
	cases := []struct {
		name string
		bits uint16
		ppm  []byte
		want string
	}{
		{
			name: "one-bit",
			bits: 1,
			ppm:  []byte("P5\n1 1\n1\n\x01"),
			want: "BitsAllocated",
		},
		{
			name: "twelve-bit",
			bits: 12,
			ppm:  []byte("P5\n1 1\n4095\n\x0f\xff"),
			want: "BitsAllocated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ppmToFrameBytes(tc.ppm, pixeldata.Metadata{
				Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: tc.bits,
			})
			if err == nil {
				t.Fatal("unsupported BitsAllocated accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestPPMToFrameBytesRequiresMaxValToMatchBitsStored(t *testing.T) {
	meta := pixeldata.Metadata{
		Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 12,
	}
	_, err := ppmToFrameBytes([]byte("P5\n1 1\n255\n\x7f"), meta)
	if err == nil {
		t.Fatal("maxval 255 accepted for BitsStored=12")
	}
	if !strings.Contains(err.Error(), "BitsStored") && !strings.Contains(err.Error(), "maxval") {
		t.Fatalf("error = %v, want maxval/BitsStored mismatch", err)
	}
}

func TestPPMToFrameBytesRejectsSamplesOutsideBitsStoredRange(t *testing.T) {
	meta := pixeldata.Metadata{
		Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 12,
	}
	_, err := ppmToFrameBytes([]byte("P5\n1 1\n4095\n\x10\x00"), meta)
	if err == nil {
		t.Fatal("sample 4096 accepted for BitsStored=12")
	}
	if !strings.Contains(err.Error(), "range") && !strings.Contains(err.Error(), "sample") {
		t.Fatalf("error = %v, want out-of-range sample", err)
	}
}

func TestPPMToFrameBytesCopiesBitsStoredSamplesWithoutRescale(t *testing.T) {
	meta := pixeldata.Metadata{
		Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 12,
	}
	got, err := ppmToFrameBytes([]byte("P5\n1 1\n4095\n\x0f\xff"), meta)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xff, 0x0f} // 4095 little-endian, not rescaled to 16-bit full range
	if !bytes.Equal(got, want) {
		t.Fatalf("pixels = %v, want %v without rescale", got, want)
	}
}
