package codecfixture

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestBaselineFullReferencePairsAndExplicitUnavailableCodecs(t *testing.T) {
	root := filepath.Join("testdata", "codecfull")
	manifest, err := readCodecFullCorpusManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := pixeldata.NewMemoryRegistry()
	if err := RegisterBuiltinCodecs(registry); err != nil {
		t.Fatal(err)
	}
	for _, f := range manifest.Fixtures {
		if filepath.Ext(f.Path) != ".dcm" {
			continue
		}
		t.Run(f.ID, func(t *testing.T) {
			file, err := object.OpenFile(filepath.Join(root, f.Path))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			pixel, err := pixeldata.Extract(file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			frames, err := registry.DecodeFrames(file.TransferSyntax.UID, pixel, file.Dataset)
			if f.BaselineSkip != "" {
				if !errors.Is(err, pixeldata.ErrCodecNotFound) {
					t.Fatalf("declared unavailable capability changed: %v", err)
				}
				t.Logf("unavailable capability=%s reason=%s (asserted rejection, not a passing decode)", f.Family, f.BaselineSkip)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if frames.Rows != f.Input.Rows || frames.Columns != f.Input.Columns || len(frames.Data) != f.Input.Frames {
				t.Fatal("decoded geometry mismatch")
			}
			if f.Reconstruction != nil {
				return
			} // Full raw reference is checked separately.
			if filepath.Ext(f.ReferencePath) != ".dcm" {
				if f.QualificationLimit == "" {
					t.Fatal("missing full reference without declared qualification limit")
				}
				t.Logf("decode only: %s", f.QualificationLimit)
				return
			}
			ref, err := object.OpenFile(filepath.Join(root, f.ReferencePath))
			if err != nil {
				t.Fatal(err)
			}
			defer ref.Close()
			refPixel, err := pixeldata.Extract(ref.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			want, err := registry.DecodeFrames(ref.TransferSyntax.UID, refPixel, ref.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			layout := SampleLayout{Rows: f.Input.Rows, Columns: f.Input.Columns, Components: f.Input.Components, BitsAllocated: f.Input.BitsAllocated, BitsStored: f.Input.BitsStored, HighBit: f.Input.HighBit, Signed: f.Input.Signed}
			r, err := CompareSamples(frames.Data, layout, want.Data, layout, SamplePolicy{})
			if err != nil || !r.Qualified {
				t.Fatalf("full reference: %+v %v", r, err)
			}
		})
	}
}

func TestFullSampleBoundaryCorpus(t *testing.T) {
	for _, c := range BoundaryCases() {
		t.Run(c.Name, func(t *testing.T) {
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCase(registry, c); err != nil {
				t.Fatal(err)
			}
			if c.ExpectedError == ErrorNone && !RunCase(registry, c).Comparison.Qualified {
				t.Fatal("full exact case unqualified")
			}
		})
	}
}

func TestRLEIndependentFullReconstruction(t *testing.T) {
	e, want, err := ReadFullReconstruction("../..", "rle-rgb16-multiframe")
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.OpenFile(filepath.Join("testdata", "codecfull", "pydicom", "SC_rgb_rle_16bit_2frame.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	pixel, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	registry := pixeldata.NewMemoryRegistry()
	if err := RegisterBuiltinCodecs(registry); err != nil {
		t.Fatal(err)
	}
	frames, err := registry.DecodeFrames(file.TransferSyntax.UID, pixel, file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != e.Layout.Rows || frames.Columns != e.Layout.Columns {
		t.Fatal("decoded geometry differs from oracle")
	}
	r, err := CompareSamples(frames.Data, e.Layout, want, e.Layout, SamplePolicy{})
	if err != nil || !r.Qualified || r.Samples != 60000 {
		t.Fatalf("full RLE oracle: %+v %v", r, err)
	}
}
