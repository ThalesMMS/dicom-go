package deid

import (
	"context"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

// guardProfileCallback provides the fail-closed contract shared by every
// de-identification extension callback. It clears output on failure, gives a
// canceled context precedence, and redacts callback errors and panic values.
func guardProfileCallback[Input, Output any](
	ctx context.Context,
	callback func(context.Context, Input) (Output, error),
	input Input,
) (output Output, err error) {
	completed := false
	defer func() {
		recover()
		var zero Output
		if contextErr := ctx.Err(); contextErr != nil {
			output, err = zero, contextErr
			return
		}
		if !completed || err != nil {
			output, err = zero, ErrProfileCallback
		}
	}()

	output, err = callback(ctx, input)
	completed = true
	return output, err
}

func callRequirementResolver(ctx context.Context, callback AttributeRequirementResolver, input AttributeContext) (AttributeRequirement, error) {
	return guardProfileCallback(ctx, callback, input)
}

func callElementCleaner(ctx context.Context, callback ElementCleaner, input CleanContext) (core.Element, error) {
	return guardProfileCallback(ctx, callback, input)
}

func callDummyProvider(ctx context.Context, callback DummyValueProvider, input DummyContext) (core.Element, error) {
	return guardProfileCallback(ctx, callback, input)
}

func callDateShiftPolicy(ctx context.Context, callback DateShiftPolicy, input *object.Object) (int, error) {
	return guardProfileCallback(ctx, callback, DateShiftContext{Object: input})
}

func callPixelCleaner(ctx context.Context, callback ProfilePixelCleaner, input *object.Object) ([]PixelRegion, error) {
	regions, err := guardProfileCallback(ctx, callback, input)
	if err != nil {
		return nil, err
	}
	return append([]PixelRegion(nil), regions...), nil
}

func callVisualCleaner(ctx context.Context, callback VisualFeaturesCleaner, input *object.Object) error {
	_, err := guardProfileCallback(ctx, func(ctx context.Context, input *object.Object) (struct{}, error) {
		return struct{}{}, callback(ctx, input)
	}, input)
	return err
}
