package jpegls_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
	"os"
	"path/filepath"
	"testing"
)

func TestNearEncoderMatchesIndependentCharLSCorpus(t *testing.T) {
	var samples uint64
	for _, tc := range codecfixture.JPEGLSNearEncoderCases() {
		t.Run(tc.Name, func(t *testing.T) {
			e, err := jpegls.NewEncoderWithOptions(jpegls.EncoderOptions{Near: tc.Near, AllowLossy: true})
			if err != nil {
				t.Fatal(err)
			}
			for i, source := range tc.ExpectedFrames {
				before := bytes.Clone(source)
				got, err := e.EncodeFrame(context.Background(), source, tc.Metadata)
				if err != nil {
					t.Fatal(err)
				}
				id := fmt.Sprintf("%s-f%d", tc.Name, i)
				evidence, want, err := codecfixture.ReadFullReconstruction("../..", id)
				if err != nil {
					t.Fatal(err)
				}
				stream, err := os.ReadFile(filepath.Join("../codecfixture/testdata/codecfull/jpegls-encode-near", id+".jls"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got.Data, stream) {
					t.Fatal("encoded bytes changed; independent oracle must be regenerated and reviewed")
				}
				obj := tc.Object()
				obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0008), VR: core.VRIS}, Value: core.StringValue{"1"}})
				decodedFrames, err := jpegls.NewNearLossless().Decode(pixeldata.PixelData{Encapsulated: true, Sequence: core.FragmentSequence{Fragments: [][]byte{got.Data}}}, obj)
				obj.Close()
				if err != nil || len(decodedFrames.Data) != 1 {
					t.Fatalf("decode: %v", err)
				}
				decoded := decodedFrames.Data[0]
				r, err := codecfixture.CompareSamples([][]byte{decoded}, evidence.Layout, want, evidence.Layout, codecfixture.SamplePolicy{})
				if err != nil || !r.Qualified {
					t.Fatalf("independent exact reconstruction: %+v %v", r, err)
				}
				samples += r.Samples
				step := int(tc.Metadata.BitsAllocated / 8)
				for p := 0; p < len(source); p += step {
					a, b := int(source[p]), int(decoded[p])
					if step == 2 {
						a, b = int(binary.LittleEndian.Uint16(source[p:])), int(binary.LittleEndian.Uint16(decoded[p:]))
					}
					if a-b > tc.Near || b-a > tc.Near {
						t.Fatalf("source bound sample=%d", p/step)
					}
				}
				if !bytes.Equal(before, source) {
					t.Fatal("encoder changed origin")
				}
			}
		})
	}
	if samples != 1839024 {
		t.Fatalf("incomplete qualification: %d samples", samples)
	}
}
