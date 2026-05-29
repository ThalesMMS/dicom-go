//go:build !jpeg2000_openjpeg && !codecfull

package jpeg2000

import "github.com/ThalesMMS/dicom-go/pixeldata"

func (openJPEGDecoder) DecodeFrame([]byte, pixeldata.Metadata) ([]byte, error) {
	return nil, ErrOpenJPEGUnavailable
}

// ValidateOpenJPEGRuntime reports that OpenJPEG is not compiled into this
// build.
func ValidateOpenJPEGRuntime() error {
	return ErrOpenJPEGUnavailable
}
