//go:build (!jpegls_charls && !codecfull) || (!darwin && !linux && !windows)

package jpegls

func (charLSDecoder) DecodeJPEGLS([]byte, DecoderInput) ([]byte, error) {
	return nil, ErrDecoderUnavailable
}

// ValidateRuntime reports that this build does not contain the CharLS backend.
func ValidateRuntime() error {
	return ErrDecoderUnavailable
}

// ValidateQualifiedRuntime reports that this build does not contain the
// qualified CharLS backend required by codecfull.
func ValidateQualifiedRuntime() error {
	return ErrDecoderUnavailable
}
