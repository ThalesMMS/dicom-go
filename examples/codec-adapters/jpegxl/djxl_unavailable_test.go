//go:build !jpegxl_djxl && !codecfull

package jpegxladapter

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestDjxlDecoderUnavailableWithoutBackendTag(t *testing.T) {
	decoder := NewDjxlDecoder()
	if decoder == nil {
		t.Fatal("NewDjxlDecoder() returned nil")
	}
	_, err := decoder.DecodeFrame([]byte("encoded"), pixeldata.Metadata{})
	if !errors.Is(err, ErrDjxlUnavailable) {
		t.Fatalf("DecodeFrame() error = %v, want ErrDjxlUnavailable", err)
	}
}
