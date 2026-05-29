// Package codecfull combines every pixel decoder qualified for the Twin Viewer
// clinical release profile. Build consumers with the codecfull tag.
package codecfull

import (
	"fmt"

	jpeg2000 "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpeg2000"
	jpegls "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegls"
	jpegxl "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegxl"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeglossless"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
)

const BuildTag = "codecfull"

// ValidateRuntime fails unless every external runtime required by codecfull is
// present and ABI-compatible.
func ValidateRuntime() error {
	if err := jpegls.ValidateQualifiedRuntime(); err != nil {
		return fmt.Errorf("codecfull requires CharLS: %w", err)
	}
	if err := jpeg2000.ValidateClinicalRuntime(); err != nil {
		return fmt.Errorf("codecfull requires OpenJPH: %w", err)
	}
	if err := jpeg2000.ValidateOpenJPEGRuntime(); err != nil {
		return fmt.Errorf("codecfull requires OpenJPEG: %w", err)
	}
	if err := jpegxl.ValidateRuntime(); err != nil {
		return fmt.Errorf("codecfull requires djxl: %w", err)
	}
	return nil
}

// Register validates external runtimes, then registers all qualified codecs.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	if err := ValidateRuntime(); err != nil {
		return err
	}
	for name, register := range map[string]func() error{
		"JPEG Baseline/Extended": func() error { return jpeg.Register(registry) },
		"JPEG Lossless":          func() error { return jpeglossless.Register(registry) },
		"RLE Lossless":           func() error { return rle.Register(registry) },
		"JPEG 2000/HTJ2K":        func() error { return jpeg2000.RegisterClinical(registry) },
		"JPEG-LS":                func() error { return jpegls.Register(registry, jpegls.NewCharLSDecoder()) },
		"JPEG XL":                func() error { return jpegxl.Register(registry) },
	} {
		if err := register(); err != nil {
			return fmt.Errorf("register %s: %w", name, err)
		}
	}
	return nil
}

// NewRegistry returns a fresh, fully validated codecfull registry.
func NewRegistry() (*pixeldata.MemoryRegistry, error) {
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		return nil, err
	}
	return registry, nil
}
