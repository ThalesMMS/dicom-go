//go:build (!jpegls_charls && !codecfull) || (!darwin && !linux && !windows)

package jpegls

import (
	"errors"
	"testing"
)

func TestCharLSDecoderUnavailableWithoutNativeBackend(t *testing.T) {
	decoder := NewCharLSDecoder()
	if decoder == nil {
		t.Fatal("NewCharLSDecoder() returned nil")
	}
	_, err := decoder.DecodeJPEGLS([]byte("encoded"), DecoderInput{})
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("DecodeJPEGLS() error = %v, want ErrDecoderUnavailable", err)
	}
}

func TestCharLSRuntimeValidationUnavailableWithoutNativeBackend(t *testing.T) {
	for name, validate := range map[string]func() error{
		"runtime":           ValidateRuntime,
		"qualified runtime": ValidateQualifiedRuntime,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(); !errors.Is(err, ErrDecoderUnavailable) {
				t.Fatalf("validation error = %v, want ErrDecoderUnavailable", err)
			}
		})
	}
}
