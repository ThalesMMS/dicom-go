package jpeg2000

import (
	"errors"
	"image"
	"testing"

	j2k "github.com/mrjoshuak/go-jpeg2000"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
)

func TestCodecFixtureConformanceJPEG2000Profile(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	copy(img.Pix, []byte{0, 64, 128, 255})
	for _, tc := range []codecfixture.Case{
		codecfixture.JPEG2000Lossless(2, 2, encodeJ2K(t, img, false, true), []byte{0, 64, 128, 255}),
		codecfixture.JPEG2000Lossy(2, 2, encodeJ2K(t, img, false, false), []byte{0, 64, 128, 255}, 64),
		codecfixture.JPEG2000Part2Lossless(2, 2, encodeJ2KWithEncodeOptions(t, img, encodeOptions{profile: j2k.ProfilePart2, lossless: true}), []byte{0, 64, 128, 255}),
		codecfixture.JPEG2000Part2Lossy(2, 2, encodeJ2KWithEncodeOptions(t, img, encodeOptions{profile: j2k.ProfilePart2, lossless: false}), []byte{0, 64, 128, 255}, 64),
		codecfixture.HTJ2KLosslessSmall(encodeJ2KWithEncodeOptions(t, img, encodeOptions{highThroughput: true, lossless: true}), []byte{0, 64, 128, 255}),
		codecfixture.HTJ2KLosslessRPCL(2, 2, encodeJ2KWithEncodeOptions(t, img, encodeOptions{highThroughput: true, lossless: true, progressionOrder: j2k.RPCL}), []byte{0, 64, 128, 255}),
		codecfixture.HTJ2KLossy(2, 2, encodeJ2KWithEncodeOptions(t, img, encodeOptions{highThroughput: true, lossless: false}), []byte{0, 64, 128, 255}, 64),
	} {
		t.Run(tc.Name, func(t *testing.T) {
			registry := pixeldata.NewMemoryRegistry()
			if err := Register(registry); err != nil {
				t.Fatal(err)
			}
			if err := codecfixture.ValidateCase(registry, tc); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodecFixtureConformanceJPEG2000FailureModes(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T) error
		want error
	}{
		{
			name: "malformed codestream",
			run: func(t *testing.T) error {
				tc := codecfixture.JPEG2000Lossless(2, 2, []byte{0xff, 0x4f, 0x00, 0x01}, []byte{0, 64, 128, 255})
				registry := pixeldata.NewMemoryRegistry()
				if err := Register(registry); err != nil {
					t.Fatal(err)
				}
				return codecfixture.ValidateCase(registry, tc)
			},
			want: ErrMalformedCodestream,
		},
		{
			name: "unsupported fragment layout",
			run: func(t *testing.T) error {
				encoded := encodeGrayJ2K(t, false, []byte{0, 255})
				obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{numberOfFrames: 2}, encoded, encoded, encoded)
				_, err := New().Decode(pixel, obj)
				return err
			},
			want: ErrUnsupportedFragmentLayout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(t)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}
