package render

import (
	"errors"
	"fmt"
	"math"
)

const (
	FrameOutputContractVersion uint32 = 1
	// FrameOutputHeaderSizeV1 covers the complete fixed numeric prefix through
	// WarningBits. It matches the frozen C++ FrameOutputV1 size; BackendID and
	// Pixels remain variable Go/process-boundary data.
	FrameOutputHeaderSizeV1 uint32 = 96
	MaxFrameBackendIDBytes         = 128
)

var ErrInvalidFrameOutput = errors.New("render: invalid frame output")

type FrameFormat uint32

const (
	FrameFormatUnknown FrameFormat = iota
	FrameFormatGrayF32TopLeft
	FrameFormatRGBA8NRGBATopLeft
)

const (
	// FrameWarningHostRenderTiming means RenderTimeNS is host encode+submit
	// latency because the adapter has no active timestamp-query path.
	FrameWarningHostRenderTiming uint64 = 1 << iota
	// FrameWarningInteractiveLOD means VR was rendered at one-half image
	// resolution and nearest-upscaled for interactive parity.
	FrameWarningInteractiveLOD
)

// FrameOutput owns a Go byte slice at the process/worker boundary. WrittenBytes
// is zero until a complete frame has been validated; partial frames are never
// publishable.
type FrameOutput struct {
	ContractVersion        uint32
	StructSize             uint32
	Format                 FrameFormat
	Width                  uint32
	Height                 uint32
	StrideBytes            uint64
	CapacityBytes          uint64
	WrittenBytes           uint64
	VolumeGeneration       uint64
	ViewGeneration         uint64
	PresentationGeneration uint64
	RenderTimeNS           uint64
	ReadbackTimeNS         uint64
	WarningBits            uint64
	BackendID              string
	Pixels                 []byte `json:"-"`
}

func ValidateFrameOutput(output FrameOutput, expected ViewState) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidFrameOutput, fmt.Sprintf(format, args...))
	}
	if output.ContractVersion != FrameOutputContractVersion {
		return fail("contract version %d", output.ContractVersion)
	}
	if output.StructSize < FrameOutputHeaderSizeV1 {
		return fail("struct size %d below V1 minimum %d", output.StructSize, FrameOutputHeaderSizeV1)
	}
	bytesPerPixel := uint64(0)
	switch output.Format {
	case FrameFormatGrayF32TopLeft, FrameFormatRGBA8NRGBATopLeft:
		bytesPerPixel = 4
	default:
		return fail("unsupported format %d", output.Format)
	}
	if output.Width == 0 || output.Height == 0 ||
		output.Width > MaxViewOutputDimension || output.Height > MaxViewOutputDimension {
		return fail("output dimensions %dx%d outside bound", output.Width, output.Height)
	}
	packed, ok := frameCheckedMul(uint64(output.Width), bytesPerPixel)
	if !ok || output.StrideBytes < packed {
		return fail("stride %d below packed row %d", output.StrideBytes, packed)
	}
	lastRow, ok := frameCheckedMul(uint64(output.Height-1), output.StrideBytes)
	if !ok {
		return fail("frame byte size overflow")
	}
	addressed, ok := frameCheckedAdd(lastRow, packed)
	if !ok || output.CapacityBytes < addressed || output.WrittenBytes != addressed {
		return fail("capacity/written mismatch: capacity=%d written=%d addressed=%d", output.CapacityBytes, output.WrittenBytes, addressed)
	}
	if output.CapacityBytes > MaxViewOutputBytes || output.WrittenBytes > MaxViewOutputBytes {
		return fail("frame bytes exceed %d", MaxViewOutputBytes)
	}
	if uint64(len(output.Pixels)) < output.WrittenBytes {
		return fail("pixel buffer length %d below written bytes %d", len(output.Pixels), output.WrittenBytes)
	}
	if output.CapacityBytes > uint64(len(output.Pixels)) {
		return fail("capacity %d exceeds pixel buffer length %d", output.CapacityBytes, len(output.Pixels))
	}
	if output.BackendID == "" || len(output.BackendID) > MaxFrameBackendIDBytes {
		return fail("backend id length %d outside 1..%d", len(output.BackendID), MaxFrameBackendIDBytes)
	}
	if output.VolumeGeneration == 0 || output.ViewGeneration == 0 {
		return fail("zero generation")
	}
	if expected.ContractVersion != 0 &&
		(output.Width != expected.OutputWidth || output.Height != expected.OutputHeight ||
			output.VolumeGeneration != expected.VolumeGeneration ||
			output.ViewGeneration != expected.ViewGeneration ||
			output.PresentationGeneration != expected.PresentationGeneration) {
		return fail("output does not match requested dimensions/generations")
	}
	if expected.Kind == ViewKindMPR && output.Format != FrameFormatGrayF32TopLeft {
		return fail("MPR output format %d is not GRAY_F32", output.Format)
	}
	if expected.Kind == ViewKindVR && output.Format != FrameFormatRGBA8NRGBATopLeft {
		return fail("VR output format %d is not RGBA8_NRGBA", output.Format)
	}
	return nil
}

func frameCheckedMul(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}

func frameCheckedAdd(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}
