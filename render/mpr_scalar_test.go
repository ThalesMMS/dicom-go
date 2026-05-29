package render

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

func TestScalarOrthogonalPlanesMatchLegacyCPUOracle(t *testing.T) {
	stack := gradientXZStack(19, 23, 11)
	window := WindowLevel{Center: 120, Width: 240}
	stack.DefaultWindow = window
	for _, plane := range []MPRPlane{MPRPlaneAxial, MPRPlaneCoronal, MPRPlaneSagittal} {
		for _, mode := range []SlabMode{SlabNone, SlabMIP, SlabMinIP, SlabAverage} {
			t.Run(string(plane)+"/"+slabModeName(mode), func(t *testing.T) {
				legacy, err := RenderSlabWithOptions(
					stack, plane, 6, 7, mode, window, DefaultMPRRenderOptions(),
				)
				if err != nil {
					t.Fatal(err)
				}
				scalar, err := RenderScalarSlabWithOptionsContext(
					context.Background(), stack, plane, 6, 7, mode, DefaultMPRRenderOptions(),
				)
				if err != nil {
					t.Fatal(err)
				}
				presented, err := scalar.ApplyWindowContext(context.Background(), window)
				if err != nil {
					t.Fatal(err)
				}
				assertGrayImagesEqual(t, "scalar "+string(plane), legacy, presented)
			})
		}
	}
}

func TestScalarObliquePlanesMatchLegacyCPUOracle(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(32, 40, 18))
	if err != nil {
		t.Fatal(err)
	}
	plane := obliqueInteriorPlane(volume)
	window := WindowLevel{Center: 128, Width: 256}
	for _, mode := range []SlabMode{SlabNone, SlabMIP, SlabMinIP, SlabAverage} {
		t.Run(slabModeName(mode), func(t *testing.T) {
			var legacyImage, scalarImage image.Image
			if mode == SlabNone {
				legacyImage = ResliceOblique(volume, plane, 97, 83, window)
				scalar, renderErr := ResliceObliqueScalarContext(context.Background(), volume, plane, 97, 83)
				if renderErr != nil {
					t.Fatal(renderErr)
				}
				scalarImage, err = scalar.ApplyWindowContext(context.Background(), window)
			} else {
				legacyImage = ResliceObliqueSlab(volume, plane, 97, 83, 9, mode, window)
				scalar, renderErr := ResliceObliqueSlabScalarContext(
					context.Background(), volume, plane, 97, 83, 9, mode,
				)
				if renderErr != nil {
					t.Fatal(renderErr)
				}
				scalarImage, err = scalar.ApplyWindowContext(context.Background(), window)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertGrayImagesEqual(t, "scalar oblique", legacyImage, scalarImage)
		})
	}
}

func TestScalarPresentationMatchesLegacyDefaultWindowAndVOILUT(t *testing.T) {
	stack := gradientXZStack(8, 8, 4)
	stack.DefaultWindow = WindowLevel{Center: 91, Width: 37, Function: display.VOISigmoid}
	scalar, err := RenderScalarSlabWithOptionsContext(
		context.Background(), stack, MPRPlaneAxial, 2, 1, SlabNone, DefaultMPRRenderOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	lut, err := display.NewLUT([]int{4, 0, 8}, []uint16{0, 200, 20, 255})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		window WindowLevel
	}{
		{name: "zero uses series default", window: WindowLevel{}},
		{name: "voi lut bypasses invalid width fallback", window: WindowLevel{LUT: lut}},
	} {
		t.Run(test.name, func(t *testing.T) {
			legacy, renderErr := RenderSlabWithOptions(
				stack, MPRPlaneAxial, 2, 1, SlabNone, test.window, DefaultMPRRenderOptions(),
			)
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			presented, presentErr := scalar.ApplyWindowContext(context.Background(), test.window)
			if presentErr != nil {
				t.Fatal(presentErr)
			}
			assertGrayImagesEqual(t, test.name, legacy, presented)
		})
	}
}

func TestScalarObliqueZeroWindowUsesLegacyDefault(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(16, 16, 8))
	if err != nil {
		t.Fatal(err)
	}
	plane := obliqueInteriorPlane(volume)
	legacy := ResliceOblique(volume, plane, 31, 29, WindowLevel{})
	scalar, err := ResliceObliqueScalarContext(context.Background(), volume, plane, 31, 29)
	if err != nil {
		t.Fatal(err)
	}
	presented, err := scalar.ApplyWindowContext(context.Background(), WindowLevel{})
	if err != nil {
		t.Fatal(err)
	}
	assertGrayImagesEqual(t, "oblique zero window", legacy, presented)
}

func TestScalarPlanePreservesFloat64ModalityPrecision(t *testing.T) {
	const (
		stored    = uint16(60000)
		slope     = 0.1234567890123
		intercept = -17.25
	)
	stack := preciseRescaleStack(stored, slope, intercept)
	value := float64(stored)*slope + intercept
	window := WindowLevel{
		Center:   value + 0.00005,
		Width:    0.0002,
		Function: display.VOISigmoid,
	}
	legacy, err := RenderSlabWithOptions(
		stack, MPRPlaneAxial, 0, 1, SlabNone, window, DefaultMPRRenderOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	scalar, err := RenderScalarSlabWithOptionsContext(
		context.Background(), stack, MPRPlaneAxial, 0, 1, SlabNone, DefaultMPRRenderOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	presented, err := scalar.ApplyWindowContext(context.Background(), window)
	if err != nil {
		t.Fatal(err)
	}
	assertGrayImagesEqual(t, "float64 modality precision", legacy, presented)
}

func TestScalarOrthogonalSlabCancellationReturnsWithinBudget(t *testing.T) {
	stack := gradientXZStack(256, 256, 64)
	// Populate the immutable volume cache before measuring only the render
	// cancellation checkpoint latency.
	if _, err := RenderScalarSlabWithOptionsContext(
		context.Background(), stack, MPRPlaneAxial, 32, 1, SlabNone, DefaultMPRRenderOptions(),
	); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, renderErr := RenderScalarSlabWithOptionsContext(
			ctx, stack, MPRPlaneAxial, 32, 64, SlabAverage, DefaultMPRRenderOptions(),
		)
		result <- renderErr
	}()
	time.Sleep(2 * time.Millisecond)
	cancelledAt := time.Now()
	cancel()
	latencyLimit := 33 * time.Millisecond
	if raceDetectorEnabled {
		// The race runtime instruments every voxel access; cancellation
		// correctness remains covered here while the quantitative 33 ms gate is
		// measured by the normal optimized test run.
		latencyLimit = 500 * time.Millisecond
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("scalar slab error = %v, want context.Canceled", err)
		}
		if latency := time.Since(cancelledAt); latency > latencyLimit {
			t.Fatalf("scalar slab cancellation latency = %v, want <= %v", latency, latencyLimit)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("scalar slab did not stop after cancellation")
	}
}

func preciseRescaleStack(stored uint16, slope, intercept float64) *Stack {
	pixels := make([]byte, 2*2*2)
	for index := 0; index < 4; index++ {
		binary.LittleEndian.PutUint16(pixels[index*2:], stored+uint16(index))
	}
	frame := &Frame{
		Metadata: pixeldata.Metadata{
			Rows:                      2,
			Columns:                   2,
			SamplesPerPixel:           1,
			BitsAllocated:             16,
			BitsStored:                16,
			HighBit:                   15,
			PhotometricInterpretation: "MONOCHROME2",
		},
		ByteOrder:        binary.LittleEndian,
		PixelBytes:       pixels,
		Rescale:          Rescale{Slope: slope, Intercept: intercept},
		ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
		ImagePosition:    []float64{0, 0, 0},
	}
	return &Stack{
		DefaultWindow: WindowLevel{Center: 40, Width: 400},
		PixelSpacing:  []float64{1, 1},
		Frames:        []*Frame{frame},
	}
}

func slabModeName(mode SlabMode) string {
	switch mode {
	case SlabNone:
		return "none"
	case SlabMIP:
		return "mip"
	case SlabMinIP:
		return "minip"
	case SlabAverage:
		return "average"
	default:
		return "unknown"
	}
}
