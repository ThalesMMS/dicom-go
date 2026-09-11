// Package codecfull combines every pixel decoder qualified for the Twin Viewer
// clinical release profile. Build consumers with the codecfull tag.
package codecfull

import (
	"fmt"

	jpeg2000 "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpeg2000"
	jpegls "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegls"
	jpegxl "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegxl"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/builtin"
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
	if err := builtin.Register(registry); err != nil {
		return err
	}
	optionalCodecs := []struct {
		name     string
		register func() error
	}{
		{name: "JPEG 2000/HTJ2K", register: func() error { return jpeg2000.RegisterClinical(registry) }},
		{name: "JPEG-LS Near-Lossless", register: func() error { return jpegls.RegisterNearLossless(registry, jpegls.NewCharLSDecoder()) }},
		{name: "JPEG XL", register: func() error { return jpegxl.Register(registry) }},
	}
	for _, codec := range optionalCodecs {
		if err := codec.register(); err != nil {
			return fmt.Errorf("register %s: %w", codec.name, err)
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
