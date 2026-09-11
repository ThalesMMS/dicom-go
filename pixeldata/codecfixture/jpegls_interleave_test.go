package codecfixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestJPEGLSInterleaveCorpusFullReconstruction(t *testing.T) {
	testJPEGLSCorpusFullReconstruction(t, "jpegls/", 57)
}

func TestJPEGLSNearLosslessCorpusFullReconstruction(t *testing.T) {
	testJPEGLSCorpusFullReconstruction(t, "jpegls-near/", 116)
}

func TestJPEGLSNearEncoderCorpusFullReconstruction(t *testing.T) {
	testJPEGLSCorpusFullReconstruction(t, "jpegls-encode-near/", 216)
}

func testJPEGLSCorpusFullReconstruction(t *testing.T, prefix string, expectedCount int) {
	root := filepath.Join("testdata", "codecfull")
	manifest, err := readCodecFullCorpusManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, f := range manifest.Fixtures {
		if !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		count++
		t.Run(f.ID, func(t *testing.T) {
			e, want, err := ReadFullReconstruction("../..", f.ID)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := readCodecFullCorpusFile(root, f.Path, f.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise the public builtin registration and DICOM frame assembly,
			// with two even-sized Items and an empty BOT instead of one Item/frame.
			mid := (len(stream) / 2) &^ 1
			if len(stream)%2 != 0 {
				stream = append(stream, 0)
			}
			elements := metadataElements(f.ID, f.Input.Rows, f.Input.Columns, 1, fragmentElement(stream[:mid], stream[mid:]))
			for _, el := range []core.Element{
				stringElement(tagPhotometricInterpretation, core.VRCS, f.Input.Photometric),
				uint16Element(tagSamplesPerPixel, uint16(f.Input.Components)), uint16Element(tagPlanarConfiguration, 0),
				uint16Element(tagBitsAllocated, uint16(f.Input.BitsAllocated)),
				uint16Element(tagBitsStored, uint16(f.Input.BitsStored)), uint16Element(tagHighBit, uint16(f.Input.HighBit)),
			} {
				elements = replaceElement(elements, el)
			}
			syntax, ok := transfer.DefaultRegistry.Get(f.Input.TransferSyntax)
			if !ok {
				t.Fatal("unknown corpus syntax")
			}
			c := Case{Name: f.ID, Syntax: syntax, Elements: elements, RegisterCodecs: RegisterBuiltinCodecs}
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			obj := c.Object()
			pixel, err := c.PixelData()
			if err != nil {
				t.Fatal(err)
			}
			got, err := registry.DecodeFrames(f.Input.TransferSyntax, pixel, obj)
			if err != nil {
				t.Fatal(err)
			}
			if got.Rows != e.Layout.Rows || got.Columns != e.Layout.Columns {
				t.Fatal("geometry mismatch")
			}
			r, err := CompareSamples(got.Data, e.Layout, want, e.Layout, SamplePolicy{})
			if err != nil || !r.Qualified || r.Mismatches != 0 || r.Samples != uint64(f.Input.Rows*f.Input.Columns*f.Input.Components) {
				t.Fatalf("full samples: %+v %v", r, err)
			}
			if f.SourceSamples != nil {
				if err := validateSourceSamples(root, f); err != nil {
					t.Fatal(err)
				}
				source, err := readCodecFullCorpusFile(root, f.SourceSamples.Path, f.SourceSamples.SHA256)
				if err != nil {
					t.Fatal(err)
				}
				// Only verify the normative source-error bound here. No PSNR,
				// tile-quality or clinical qualification is inferred from NEAR.
				bound, err := CompareSamples(got.Data, e.Layout, [][]byte{source}, e.Layout, SamplePolicy{MaxAbsoluteError: uint64(f.SourceSamples.Near)})
				if err != nil || bound.OutsideLimit != 0 || bound.Samples != r.Samples {
					t.Fatalf("source bound: %+v %v", bound, err)
				}
			}
		})
	}
	if count != expectedCount {
		t.Fatalf("fixture count=%d", count)
	}
}

func readCodecFullCorpusFile(root, path, hash string) ([]byte, error) {
	if err := validateSHA256(filepath.Join(root, path), hash); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(root, path))
}
