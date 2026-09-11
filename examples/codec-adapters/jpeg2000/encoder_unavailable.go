//go:build !jpeg2000_openjpeg && !codecfull

package jpeg2000

import (
	"context"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func probeOpenJPEGEncoder(context.Context, OpenJPEGEncoderOptions) (string, error) {
	return "", ErrOpenJPEGEncoderUnavailable
}
func (*OpenJPEGLosslessEncoder) encode(context.Context, []byte, pixeldata.Metadata) (pixeldata.EncodedFrame, error) {
	return pixeldata.EncodedFrame{}, ErrOpenJPEGEncoderUnavailable
}
