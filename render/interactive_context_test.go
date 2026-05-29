package render

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResliceObliqueContextHonorsCancellation(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(48, 48, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ResliceObliqueContext(
		ctx,
		volume,
		volume.OrthogonalPlane(MPRPlaneAxial, volume.Center()),
		2048,
		2048,
		WindowLevel{Center: 128, Width: 256},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResliceObliqueContext error = %v, want context.Canceled", err)
	}
}

func TestRenderVRContextCancelsActiveCPURenderer(t *testing.T) {
	volume, err := BuildVolume(sphereStack(64, 22))
	if err != nil {
		t.Fatal(err)
	}
	camera := NewVRCamera(volume.BoundingRadiusMM())
	camera.FitVolume(volume)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, renderErr := RenderVRContext(
			ctx,
			volume,
			camera,
			opaqueAbovePreset(),
			WindowLevel{Center: 128, Width: 256},
			true,
			nil,
			VRQuality{Width: 512, Height: 512, MaxSteps: 1024},
		)
		result <- renderErr
	}()
	time.Sleep(time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RenderVRContext error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active CPU VR renderer did not stop after cancellation")
	}
}
