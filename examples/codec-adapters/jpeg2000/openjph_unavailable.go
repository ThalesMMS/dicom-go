//go:build !codecfull

package jpeg2000

import "github.com/ThalesMMS/dicom-go/pixeldata"

func (openJPHDecoder) DecodeFrame([]byte, pixeldata.Metadata) ([]byte, error) {
	return nil, ErrOpenJPHUnavailable
}

// ValidateClinicalRuntime reports that OpenJPH is not compiled into this build.
func ValidateClinicalRuntime() error {
	return ErrOpenJPHUnavailable
}
