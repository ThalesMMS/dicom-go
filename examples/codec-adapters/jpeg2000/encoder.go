package jpeg2000

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrOpenJPEGEncoderUnavailable = errors.New("jpeg2000adapter: qualified OpenJPEG encoder unavailable")
	ErrOpenJPEGEncoderOptions     = errors.New("jpeg2000adapter: invalid encoder options")
	ErrOpenJPEGEncoderLimit       = errors.New("jpeg2000adapter: encoder resource limit")
)

// OpenJPEGEncoderOptions controls an explicit Part 1 lossless encoder. Zero
// limits select finite defaults; a successful constructor probes OpenJPEG 2.5.4.
type OpenJPEGEncoderOptions struct {
	Executable                    string
	Timeout                       time.Duration
	MaxFrameBytes, MaxOutputBytes int64
	MaxConcurrent                 int
}

// OpenJPEGLosslessEncoder owns no persistent native resource. Each bounded
// operation runs a single-threaded opj_compress process, killed on cancellation.
// Callers must keep the qualified executable and its runtime dependencies in a
// trusted namespace. A shared instance is safe for concurrent registry use.
type OpenJPEGLosslessEncoder struct {
	executable string
	opts       OpenJPEGEncoderOptions
	gate       chan struct{}
}

func NewOpenJPEGLosslessEncoder(ctx context.Context, opts OpenJPEGEncoderOptions) (*OpenJPEGLosslessEncoder, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxFrameBytes == 0 {
		opts.MaxFrameBytes = 16 << 20
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = 32 << 20
	}
	if opts.MaxConcurrent == 0 {
		opts.MaxConcurrent = 1
	}
	if opts.Timeout < 0 || opts.Timeout > 2*time.Minute || opts.MaxFrameBytes < 1 || opts.MaxFrameBytes > 64<<20 || opts.MaxOutputBytes < 1 || opts.MaxOutputBytes > 128<<20 || opts.MaxConcurrent < 1 || opts.MaxConcurrent > 4 {
		return nil, ErrOpenJPEGEncoderOptions
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := probeOpenJPEGEncoder(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &OpenJPEGLosslessEncoder{executable: executable, opts: opts, gate: make(chan struct{}, opts.MaxConcurrent)}, nil
}

// RegisterOpenJPEGLosslessEncoder adds only .90 after successful runtime
// preflight. It does not change decoder registration or a global registry.
func RegisterOpenJPEGLosslessEncoder(ctx context.Context, r pixeldata.EncoderRegistry, opts OpenJPEGEncoderOptions) error {
	if r == nil {
		return pixeldata.ErrEncoderRegistryNil
	}
	e, err := NewOpenJPEGLosslessEncoder(ctx, opts)
	if err != nil {
		return err
	}
	return r.RegisterEncoder(transfer.JPEG2000LosslessOnly.UID, e)
}

func (*OpenJPEGLosslessEncoder) Capabilities() pixeldata.EncoderCapabilities {
	return pixeldata.EncoderCapabilities{TransferSyntaxUID: transfer.JPEG2000LosslessOnly.UID, BitsAllocated: []uint16{8, 16}, PixelRepresentations: []uint16{0}, SamplesPerPixel: []uint16{1, 3}, PhotometricInterpretations: []string{"MONOCHROME1", "MONOCHROME2", "RGB"}, Lossless: true, SupportsMultiFrame: true, Backend: "OpenJPEG 2.5.4 opj_compress (optional subprocess)"}
}

func (e *OpenJPEGLosslessEncoder) EncodeFrame(ctx context.Context, frame []byte, m pixeldata.Metadata) (pixeldata.EncodedFrame, error) {
	if e == nil || e.gate == nil {
		return pixeldata.EncodedFrame{}, ErrOpenJPEGEncoderUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	if err := validateOpenJPEGEncodeFrame(frame, m, e.opts.MaxFrameBytes); err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	case <-ctx.Done():
		return pixeldata.EncodedFrame{}, ctx.Err()
	}
	return e.encode(ctx, frame, m)
}

func validateOpenJPEGEncodeFrame(frame []byte, m pixeldata.Metadata, limit int64) error {
	bad := func(field string) error { return &pixeldata.UnsupportedEncoderMetadataError{Field: field} }
	if m.Rows == 0 || m.Columns == 0 {
		return bad("geometry")
	}
	if m.BitsAllocated != 8 && m.BitsAllocated != 16 {
		return bad("BitsAllocated")
	}
	if m.BitsStored < 8 || m.BitsStored > m.BitsAllocated || m.HighBit != m.BitsStored-1 {
		return bad("BitsStored/HighBit")
	}
	if m.PixelRepresentation != 0 {
		return bad("PixelRepresentation")
	}
	if m.NumberOfFrames < 1 {
		return bad("NumberOfFrames")
	}
	if m.SamplesPerPixel != 1 && m.SamplesPerPixel != 3 {
		return bad("SamplesPerPixel")
	}
	photo := strings.ToUpper(strings.TrimSpace(m.PhotometricInterpretation))
	if (m.SamplesPerPixel == 1 && photo != "MONOCHROME1" && photo != "MONOCHROME2") || (m.SamplesPerPixel == 3 && photo != "RGB") {
		return bad("PhotometricInterpretation")
	}
	if m.PlanarConfiguration != 0 || (m.SamplesPerPixel == 3 && !m.PlanarConfigurationPresent) {
		return bad("PlanarConfiguration")
	}
	n := int64(m.Rows) * int64(m.Columns) * int64(m.SamplesPerPixel) * int64(m.BitsAllocated/8)
	if n > limit {
		return ErrOpenJPEGEncoderLimit
	}
	if n != int64(len(frame)) {
		return pixeldata.ErrPixelDataSizeMismatch
	}
	return nil
}

// Binary PGM/PPM preserves 8..16-bit precision without the OpenJPEG 2.5.4 RAW
// reader's invalid bitwise dimension check. Its 16-bit raster is big endian;
// reject nonzero unused bits rather than silently clipping samples.
func openJPEGPNMInput(ctx context.Context, frame []byte, m pixeldata.Metadata) ([]byte, error) {
	pixels := int(m.Rows) * int(m.Columns)
	components := int(m.SamplesPerPixel)
	sourceWidth := int(m.BitsAllocated / 8)
	targetWidth := int((m.BitsStored + 7) / 8)
	maxValue := uint32(1)<<m.BitsStored - 1
	magic := "P5"
	if components == 3 {
		magic = "P6"
	}
	header := fmt.Sprintf("%s\n%d %d\n%d\n", magic, m.Columns, m.Rows, maxValue)
	out := make([]byte, len(header)+pixels*components*targetWidth)
	copy(out, header)
	for i := 0; i < pixels; i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for c := 0; c < components; c++ {
			pos := (i*components + c) * sourceWidth
			v := uint32(frame[pos])
			if sourceWidth == 2 {
				v = uint32(binary.LittleEndian.Uint16(frame[pos:]))
			}
			if v > maxValue {
				return nil, &pixeldata.UnsupportedEncoderMetadataError{Field: "sample padding"}
			}
			pos = len(header) + (i*components+c)*targetWidth
			out[pos] = byte(v)
			if targetWidth == 2 {
				binary.BigEndian.PutUint16(out[pos:], uint16(v))
			}
		}
	}
	return out, nil
}
