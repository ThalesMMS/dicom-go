package jpeg2000

import (
	"errors"
	"os"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var ErrOpenJPEGUnavailable = errors.New("jpeg2000adapter: OpenJPEG decoder unavailable")

const (
	defaultOpenJPEGTimeout   = 30 * time.Second
	QualifiedOpenJPEGVersion = "2.5.4"
)

type openJPEGDecoder struct {
	executable string
	timeout    time.Duration
}

// OpenJPEGOption configures the optional OpenJPEG-backed decoder.
type OpenJPEGOption func(*openJPEGDecoder)

// OpenJPEGExecutable sets the opj_decompress executable path. When unset,
// DICOM_GO_OPENJPEG_DECOMPRESS is checked first, then PATH.
func OpenJPEGExecutable(path string) OpenJPEGOption {
	return func(decoder *openJPEGDecoder) {
		decoder.executable = path
	}
}

// OpenJPEGTimeout sets the maximum time allowed for one opj_decompress
// subprocess. Non-positive values use the package default.
func OpenJPEGTimeout(timeout time.Duration) OpenJPEGOption {
	return func(decoder *openJPEGDecoder) {
		decoder.timeout = timeout
	}
}

// NewOpenJPEGDecoder returns an optional OpenJPEG-backed decoder. Without the
// jpeg2000_openjpeg build tag, DecodeFrame returns ErrOpenJPEGUnavailable.
func NewOpenJPEGDecoder(options ...OpenJPEGOption) Decoder {
	return newOpenJPEGDecoder(options...)
}

func newOpenJPEGDecoder(options ...OpenJPEGOption) openJPEGDecoder {
	decoder := openJPEGDecoder{
		executable: os.Getenv("DICOM_GO_OPENJPEG_DECOMPRESS"),
		timeout:    defaultOpenJPEGTimeout,
	}
	for _, option := range options {
		if option != nil {
			option(&decoder)
		}
	}
	if decoder.timeout <= 0 {
		decoder.timeout = defaultOpenJPEGTimeout
	}
	return decoder
}

// NewOpenJPEGCodec returns a JPEG 2000 codec backed by OpenJPEG.
func NewOpenJPEGCodec(options ...OpenJPEGOption) *Codec {
	return NewWithDecoder(NewOpenJPEGDecoder(options...))
}

// RegisterOpenJPEG registers JPEG 2000 transfer syntaxes using the optional
// OpenJPEG backend. HTJ2K remains on the pure-Go adapter until a native HTJ2K
// fallback is explicitly qualified.
func RegisterOpenJPEG(registry pixeldata.Registry, options ...OpenJPEGOption) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	openJPEGCodec := NewOpenJPEGCodec(options...)
	for _, uid := range openJPEGSupportedUIDs() {
		if err := registry.RegisterCodec(uid, openJPEGCodec); err != nil {
			return err
		}
	}
	pureGoCodec := New()
	for _, uid := range openJPEGPureGoFallbackUIDs() {
		if err := registry.RegisterCodec(uid, pureGoCodec); err != nil {
			return err
		}
	}
	return nil
}

// RegisterOpenJPEGDefault registers JPEG 2000 transfer syntaxes in
// pixeldata.DefaultRegistry using the optional OpenJPEG backend.
func RegisterOpenJPEGDefault(options ...OpenJPEGOption) error {
	if pixeldata.DefaultRegistry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return RegisterOpenJPEG(pixeldata.DefaultRegistry, options...)
}

func openJPEGSupportedUIDs() []string {
	return []string{
		transfer.JPEG2000LosslessOnly.UID,
		transfer.JPEG2000.UID,
	}
}

func openJPEGPureGoFallbackUIDs() []string {
	return []string{
		transfer.JPEG2000Part2Lossless.UID,
		transfer.JPEG2000Part2.UID,
		transfer.HTJ2KLossless.UID,
		transfer.HTJ2KLosslessRPCL.UID,
		transfer.HTJ2K.UID,
	}
}
