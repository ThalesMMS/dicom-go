package render

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
)

func TestRenderVRSurfaceShowsThresholdedAnatomy(t *testing.T) {
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	img := RenderVRSurface(vol, cam, 128, color.NRGBA{R: 220, G: 200, B: 160, A: 255}, true, nil, DefaultVRQuality(48, 48)).(*image.NRGBA)

	center := vrBrightness(img, 24, 24)
	corner := vrBrightness(img, 1, 1)
	if center <= corner || center == 0 {
		t.Fatalf("surface brightness center=%d corner=%d, want visible thresholded center", center, corner)
	}
}

func TestRenderVRSurfaceContextHonorsCancellation(t *testing.T) {
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = RenderVRSurfaceContext(
		ctx,
		vol,
		NewVRCamera(vol.BoundingRadiusMM()),
		128,
		color.NRGBA{A: 255},
		false,
		nil,
		DefaultVRQuality(48, 48),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderVRSurfaceContext() error = %v, want context.Canceled", err)
	}
}

func TestRenderVRSurfaceHonorsThresholdAndCrop(t *testing.T) {
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	style := color.NRGBA{R: 220, G: 200, B: 160, A: 255}
	quality := DefaultVRQuality(32, 32)

	aboveMax := RenderVRSurface(vol, cam, 10000, style, false, nil, quality).(*image.NRGBA)
	if got := vrBrightness(aboveMax, 16, 16); got != 0 {
		t.Fatalf("above-max threshold center brightness=%d, want 0", got)
	}

	clip := NewVRClip()
	clip.SetCropBox(Vec3{}, Vec3{X: 0.05, Y: 1, Z: 1})
	cropped := RenderVRSurface(vol, cam, 128, style, false, clip, quality).(*image.NRGBA)
	if got := vrBrightness(cropped, 16, 16); got != 0 {
		t.Fatalf("cropped surface center brightness=%d, want 0", got)
	}
}
