//go:build !jpeg2000_openjpeg && !codecfull

package jpeg2000

import (
	"context"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func (decoder openJPEGDecoder) DecodeFrame(payload []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return decoder.DecodeFrameContext(context.Background(), payload, metadata)
}

func (openJPEGDecoder) DecodeFrameContext(ctx context.Context, _ []byte, _ pixeldata.Metadata) ([]byte, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, ErrOpenJPEGUnavailable
}

// ValidateOpenJPEGRuntime reports that OpenJPEG is not compiled into this
// build.
func ValidateOpenJPEGRuntime() error {
	return ErrOpenJPEGUnavailable
}
