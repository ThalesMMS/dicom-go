package jpeg2000

import (
	"errors"
	"os"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var ErrOpenJPHUnavailable = errors.New("jpeg2000adapter: OpenJPH decoder unavailable")

const (
	defaultOpenJPHTimeout        = 30 * time.Second
	QualifiedOpenJPHVersion      = "0.31.0"
	QualifiedOpenJPHCommit       = "c68064d0e4cad8e96bab9a068f6cc4e7799744fc"
	QualifiedOpenJPHMarkerSuffix = ".codecfull"
	QualifiedOpenJPHMarker       = "OpenJPH " + QualifiedOpenJPHVersion + "\ncommit " + QualifiedOpenJPHCommit
)

type openJPHDecoder struct {
	executable string
	timeout    time.Duration
}

// OpenJPHOption configures the codecfull OpenJPH decoder.
type OpenJPHOption func(*openJPHDecoder)

// OpenJPHExecutable sets the ojph_expand executable path.
func OpenJPHExecutable(path string) OpenJPHOption {
	return func(decoder *openJPHDecoder) {
		decoder.executable = path
	}
}

// OpenJPHTimeout sets the maximum time allowed for one ojph_expand process.
func OpenJPHTimeout(timeout time.Duration) OpenJPHOption {
	return func(decoder *openJPHDecoder) {
		decoder.timeout = timeout
	}
}

// NewOpenJPHDecoder returns the codecfull OpenJPH-backed decoder.
func NewOpenJPHDecoder(options ...OpenJPHOption) Decoder {
	decoder := newOpenJPHDecoder(options...)
	return decoder
}

func newOpenJPHDecoder(options ...OpenJPHOption) openJPHDecoder {
	decoder := openJPHDecoder{
		executable: os.Getenv("DICOM_GO_OPENJPH_EXPAND"),
		timeout:    defaultOpenJPHTimeout,
	}
	for _, option := range options {
		if option != nil {
			option(&decoder)
		}
	}
	if decoder.timeout <= 0 {
		decoder.timeout = defaultOpenJPHTimeout
	}
	return decoder
}

// RegisterClinical registers all JPEG 2000 Part 1/2 and HTJ2K syntaxes with
// the fail-closed OpenJPH backend.
func RegisterClinical(registry pixeldata.Registry, options ...OpenJPHOption) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	openJPEGCodec := NewOpenJPEGCodec()
	for _, uid := range []string{
		transfer.JPEG2000LosslessOnly.UID,
		transfer.JPEG2000.UID,
		transfer.JPEG2000Part2Lossless.UID,
		transfer.JPEG2000Part2.UID,
	} {
		if err := registry.RegisterCodec(uid, openJPEGCodec); err != nil {
			return err
		}
	}
	openJPHCodec := NewWithDecoder(NewOpenJPHDecoder(options...))
	for _, uid := range []string{
		transfer.HTJ2KLossless.UID,
		transfer.HTJ2KLosslessRPCL.UID,
		transfer.HTJ2K.UID,
	} {
		if err := registry.RegisterCodec(uid, openJPHCodec); err != nil {
			return err
		}
	}
	return nil
}

// RegisterClinicalDefault registers the codecfull OpenJPH backend in the
// package-level registry.
func RegisterClinicalDefault(options ...OpenJPHOption) error {
	if pixeldata.DefaultRegistry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return RegisterClinical(pixeldata.DefaultRegistry, options...)
}
