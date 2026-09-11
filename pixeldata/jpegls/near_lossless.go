package jpegls

import "github.com/ThalesMMS/dicom-go/pixeldata"

// NewNearLossless returns a decoder for the qualified unsigned JPEG-LS
// Near-Lossless profiles. New and the zero Codec remain strictly lossless.
func NewNearLossless() *Codec { return &Codec{nearLossless: true} }

// RegisterNearLossless registers the qualified .81 decoder in a caller-owned
// registry, separately from the .80 decoder and the optional CharLS adapter.
func RegisterNearLossless(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return registry.RegisterCodec(NearLosslessUID, NewNearLossless())
}
