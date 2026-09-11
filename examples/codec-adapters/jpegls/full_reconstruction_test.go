//go:build (jpegls_charls || codecfull) && (darwin || linux || windows)

package jpegls

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
)

func TestCharLSFullNearLosslessReconstruction(t *testing.T) {
	for _, tc := range []struct{ id, name string }{
		{"jpegls-near-lossless-8", "JPEGLSNearLossless_08.dcm"},
		{"jpegls-near-lossless-16", "JPEGLSNearLossless_16.dcm"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			e, want, err := codecfixture.ReadFullReconstruction("../../..", tc.id)
			if err != nil {
				t.Fatal(err)
			}
			file, err := object.OpenFile(filepath.Join("../../..", "pixeldata/codecfixture/testdata/codecfull/pydicom", tc.name))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			meta, err := pixeldata.ExtractMetadata(file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			pixel, err := pixeldata.Extract(file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			got, err := NewCharLSDecoder().DecodeJPEGLS(bytes.Join(pixel.Sequence.Fragments, nil), DecoderInput{Metadata: meta, NearLossless: true})
			if err != nil {
				t.Fatal(err)
			}
			r, err := codecfixture.CompareSamples([][]byte{got}, e.Layout, want, e.Layout, codecfixture.SamplePolicy{})
			if err != nil || !r.Qualified {
				t.Fatalf("full reconstruction: %+v %v", r, err)
			}
			stream := bytes.Join(pixel.Sequence.Fragments, nil)
			if len(stream)&1 != 0 {
				stream = append(stream, 0)
			}
			file.Dataset.Put(core.Element{Header: core.ElementHeader{Tag: tagNumberOfFrames, VR: core.VRIS}, Value: core.StringValue{"2"}})
			for split := 2; split < len(stream); split += 2 {
				for _, bot := range []bool{false, true} {
					sequence := core.FragmentSequence{Fragments: [][]byte{stream[:split], stream[split:], stream}}
					if bot {
						sequence.OffsetTable = make([]byte, 8)
						binary.LittleEndian.PutUint32(sequence.OffsetTable[4:], uint32(len(stream)+16))
					}
					frames, err := NewNearLossless(NewCharLSDecoder()).Decode(pixeldata.PixelData{Encapsulated: true, Sequence: sequence}, file.Dataset)
					if err != nil {
						t.Fatalf("shared assembly split=%d BOT=%t: %v", split, bot, err)
					}
					if len(frames.Data) != 2 {
						t.Fatal("shared assembly frame count")
					}
					for _, frame := range frames.Data {
						if !bytes.Equal(frame, want[0]) {
							t.Fatal("shared assembly differs from full independent reference")
						}
					}
				}
			}
		})
	}
}
