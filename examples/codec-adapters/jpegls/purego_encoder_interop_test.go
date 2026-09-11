//go:build (jpegls_charls || codecfull) && (darwin || linux || windows)

package jpegls

import (
	"bytes"
	"context"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	puregojpegls "github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
)

func TestCharLSDecodesPureGoLosslessEncoderOutput(t *testing.T) {
	decoder := NewCharLSDecoder()
	for _, test := range []struct {
		name     string
		metadata pixeldata.Metadata
		frame    []byte
	}{
		{
			name: "monochrome 8-bit runs and gradients",
			metadata: pixeldata.Metadata{
				Rows: 2, Columns: 4, SamplesPerPixel: 1,
				BitsAllocated: 8, BitsStored: 8, HighBit: 7,
				NumberOfFrames: 1, PhotometricInterpretation: "MONOCHROME2",
			},
			frame: []byte{0, 0, 32, 64, 128, 192, 255, 255},
		},
		{
			name: "monochrome 16-bit",
			metadata: pixeldata.Metadata{
				Rows: 2, Columns: 3, SamplesPerPixel: 1,
				BitsAllocated: 16, BitsStored: 16, HighBit: 15,
				NumberOfFrames: 1, PhotometricInterpretation: "MONOCHROME2",
			},
			frame: []byte{0, 0, 1, 0, 255, 0, 0, 128, 254, 255, 255, 255},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := puregojpegls.NewEncoder().EncodeFrame(context.Background(), test.frame, test.metadata)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decoder.DecodeJPEGLS(encoded.Data, DecoderInput{Metadata: test.metadata})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(decoded, test.frame) {
				t.Fatalf("CharLS decoded pixels differ from pure-Go encoder input")
			}
		})
	}
}
