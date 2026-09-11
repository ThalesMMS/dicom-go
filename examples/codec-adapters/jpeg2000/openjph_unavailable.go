//go:build !codecfull

package jpeg2000

import (
	"context"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func (decoder openJPHDecoder) DecodeFrame(payload []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return decoder.DecodeFrameContext(context.Background(), payload, metadata)
}

func (openJPHDecoder) DecodeFrameContext(ctx context.Context, _ []byte, _ pixeldata.Metadata) ([]byte, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, ErrOpenJPHUnavailable
}

// ValidateClinicalRuntime reports that OpenJPH is not compiled into this build.
func ValidateClinicalRuntime() error {
	return ErrOpenJPHUnavailable
}
