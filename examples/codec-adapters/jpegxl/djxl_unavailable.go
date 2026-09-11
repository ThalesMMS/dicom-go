//go:build !jpegxl_djxl && !codecfull

package jpegxladapter

import (
	"context"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func (decoder djxlDecoder) DecodeFrame(fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return decoder.DecodeFrameContext(context.Background(), fragment, metadata)
}

func (djxlDecoder) DecodeFrameContext(ctx context.Context, _ []byte, _ pixeldata.Metadata) ([]byte, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, ErrDjxlUnavailable
}

// ValidateRuntime reports that this build does not contain the djxl backend.
func ValidateRuntime() error {
	return ErrDjxlUnavailable
}
