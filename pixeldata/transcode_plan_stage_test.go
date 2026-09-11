package pixeldata

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestBuildTranscodePlanRoutePrecedence(t *testing.T) {
	tests := []struct {
		name          string
		source        transfer.Syntax
		target        transfer.Syntax
		forceReencode bool
		hasPixel      bool
		want          transcodeMode
	}{
		{
			name:     "native clone wins without pixel data",
			source:   transfer.ExplicitVRLittleEndian,
			target:   transfer.ImplicitVRLittleEndian,
			hasPixel: false,
			want:     transcodeNativeClone,
		},
		{
			name:     "missing pixel wins before compressed equivalence",
			source:   transfer.RLELossless,
			target:   transfer.RLELossless,
			hasPixel: false,
			want:     transcodeMissingPixelData,
		},
		{
			name:     "equivalent compressed syntax uses detached clone",
			source:   transfer.RLELossless,
			target:   transfer.RLELossless,
			hasPixel: true,
			want:     transcodeEquivalentClone,
		},
		{
			name:          "force reencode defeats compressed equivalence",
			source:        transfer.RLELossless,
			target:        transfer.RLELossless,
			forceReencode: true,
			hasPixel:      true,
			want:          transcodeDecodeEncode,
		},
		{
			name:     "encapsulated uncompressed target has its own route",
			source:   transfer.RLELossless,
			target:   transfer.EncapsulatedUncompressedExplicitVRLittleEndian,
			hasPixel: true,
			want:     transcodeDecodeEncapsulatedUncompressed,
		},
		{
			name:     "equivalent encapsulated uncompressed stays a clone",
			source:   transfer.EncapsulatedUncompressedExplicitVRLittleEndian,
			target:   transfer.EncapsulatedUncompressedExplicitVRLittleEndian,
			hasPixel: true,
			want:     transcodeEquivalentClone,
		},
		{
			name:     "compressed to native decodes without encoder",
			source:   transfer.RLELossless,
			target:   transfer.ExplicitVRBigEndian,
			hasPixel: true,
			want:     transcodeDecodeNative,
		},
		{
			name:     "native to compressed decodes then encodes",
			source:   transfer.ExplicitVRLittleEndian,
			target:   transfer.RLELossless,
			hasPixel: true,
			want:     transcodeDecodeEncode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := buildTranscodePlan(test.source, test.target, test.forceReencode, test.hasPixel)
			if got.mode != test.want {
				t.Fatalf("buildTranscodePlan() mode = %v, want %v", got.mode, test.want)
			}
			if got.source != test.source || got.target != test.target || got.forceReencode != test.forceReencode {
				t.Fatalf("buildTranscodePlan() = %#v, want source/target/force preserved", got)
			}
		})
	}
}
