package pixeldata

import (
	"context"
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// DecodeFramesContext decodes frames while honoring ctx. Codecs that
// implement ContextCodec receive the caller context. A canceled or expired
// context never publishes frames and is returned as context.Canceled or
// context.DeadlineExceeded rather than as image corruption.
func DecodeFramesContext(ctx context.Context, uid string, pixel PixelData, obj *object.Object) (Frames, error) {
	return decodeFramesWithRegistry(ctx, DefaultRegistry, uid, pixel, obj)
}

// DecodeFramesContext is DecodeFrames with cancellation. Existing Codec
// implementations remain valid; context-aware codecs are preferred.
func (r *MemoryRegistry) DecodeFramesContext(ctx context.Context, uid string, pixel PixelData, obj *object.Object) (Frames, error) {
	return decodeFramesWithRegistry(ctx, r, uid, pixel, obj)
}

func decodeFramesWithRegistry(ctx context.Context, registry Registry, uid string, pixel PixelData, obj *object.Object) (Frames, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Frames{}, err
	}

	normalizedUID := transfer.NormalizeUID(uid)
	if IsJPIPReferencedTransferSyntax(normalizedUID) {
		ref, err := ExtractJPIPReference(normalizedUID, obj)
		if err != nil {
			return Frames{}, err
		}
		return Frames{}, &JPIPRetrievalRequiredError{Reference: ref}
	}
	if transfer.IsVideoTransferSyntax(normalizedUID) {
		return Frames{}, mediaPayloadNotRenderableError(normalizedUID, pixel)
	}
	if syntax, ok := transfer.DefaultRegistry.Get(normalizedUID); ok {
		switch {
		case syntax.Encapsulated && !pixel.Encapsulated:
			return Frames{}, fmt.Errorf("%w: transfer syntax %q expects encapsulated pixel data", ErrIncompatiblePixelData, syntax.UID)
		case !syntax.Encapsulated && pixel.Encapsulated:
			return Frames{}, fmt.Errorf("%w: transfer syntax %q expects native pixel data", ErrIncompatiblePixelData, syntax.UID)
		}
	}

	if normalizedUID == transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID {
		frames, err := decodeEncapsulatedUncompressedFrames(pixel, obj)
		return finishDecodeFrames(ctx, frames, err)
	}
	if !pixel.Encapsulated {
		frames, err := decodeNativeFrames(pixel, obj)
		return finishDecodeFrames(ctx, frames, err)
	}
	if registry == nil || isNilMemoryRegistry(registry) {
		return Frames{}, codecAvailabilityError(ErrCodecRegistryNil, normalizedUID, nil)
	}

	codec, ok := registry.GetCodec(normalizedUID)
	if !ok {
		var registered []string
		if snapshot, hasSnapshot := registry.(interface{ RegisteredCodecUIDs() []string }); hasSnapshot {
			registered = snapshot.RegisteredCodecUIDs()
		}
		return Frames{}, codecAvailabilityError(ErrCodecNotFound, normalizedUID, registered)
	}

	frames, err := decodeRegisteredCodec(ctx, codec, pixel, obj)
	if err != nil {
		if isContextTermination(err) {
			return Frames{}, err
		}
		return Frames{}, codecDecodeError(normalizedUID, err)
	}
	return finishDecodeFrames(ctx, frames, nil)
}

func finishDecodeFrames(ctx context.Context, frames Frames, err error) (Frames, error) {
	if err != nil {
		return Frames{}, err
	}
	if err := ctx.Err(); err != nil {
		return Frames{}, err
	}
	return frames, nil
}

func decodeRegisteredCodec(ctx context.Context, codec Codec, pixel PixelData, obj *object.Object) (Frames, error) {
	if contextual, ok := codec.(ContextCodec); ok {
		return contextual.DecodeContext(ctx, pixel, obj)
	}
	if err := ctx.Err(); err != nil {
		return Frames{}, err
	}
	return codec.Decode(pixel, obj)
}

func isContextTermination(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isNilMemoryRegistry(registry Registry) bool {
	memory, ok := registry.(*MemoryRegistry)
	return ok && memory == nil
}
