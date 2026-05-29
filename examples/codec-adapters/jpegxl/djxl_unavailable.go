//go:build !jpegxl_djxl && !codecfull

package jpegxladapter

import "github.com/ThalesMMS/dicom-go/pixeldata"

func (djxlDecoder) DecodeFrame([]byte, pixeldata.Metadata) ([]byte, error) {
	return nil, ErrDjxlUnavailable
}

// ValidateRuntime reports that this build does not contain the djxl backend.
func ValidateRuntime() error {
	return ErrDjxlUnavailable
}
