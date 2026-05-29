package jpegls

// charLSDecoder adapts the optional CharLS native backend to Decoder.
type charLSDecoder struct{}

// NewCharLSDecoder returns the optional CharLS-backed JPEG-LS decoder.
//
// Without the jpegls_charls build tag and a loadable CharLS shared library, the
// returned decoder reports ErrDecoderUnavailable from DecodeJPEGLS.
func NewCharLSDecoder() Decoder {
	return charLSDecoder{}
}
