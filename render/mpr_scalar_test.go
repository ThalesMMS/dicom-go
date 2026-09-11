package render

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"sync"
	"sync/atomic"
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

func TestScalarOrthogonalSlabCancellationIsAtomic(t *testing.T) {
	stack := gradientXZStack(256, 256, 64)
	if _, err := RenderScalarSlabWithOptionsContext(
		context.Background(), stack, MPRPlaneAxial, 32, 1, SlabNone, DefaultMPRRenderOptions(),
	); err != nil {
		t.Fatal(err)
	}

	base, cancel := context.WithCancel(context.Background())
	ctx := newCancellationBarrierContext(base, 4096)
	type renderResult struct {
		plane *ScalarPlane
		err   error
	}
	result := make(chan renderResult, 1)
	go func() {
		plane, renderErr := RenderScalarSlabWithOptionsContext(
			ctx, stack, MPRPlaneAxial, 32, 64, SlabAverage, DefaultMPRRenderOptions(),
		)
		result <- renderResult{plane: plane, err: renderErr}
	}()
	select {
	case <-ctx.renderStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("scalar slab did not reach the render cancellation barrier")
	}
	cancel()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("scalar slab error = %v, want context.Canceled", got.err)
		}
		if got.plane != nil {
			t.Fatal("canceled scalar slab published a partial plane")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scalar slab did not stop after cancellation")
	}
}

func TestScalarPlanePresentationCancellationIsAtomic(t *testing.T) {
	stack := gradientXZStack(256, 256, 8)
	plane, err := RenderScalarSlabWithOptionsContext(
		context.Background(), stack, MPRPlaneAxial, 4, 1, SlabNone, DefaultMPRRenderOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	ctx := newCancellationBarrierContext(base, 256)
	type presentationResult struct {
		image image.Image
		err   error
	}
	result := make(chan presentationResult, 1)
	go func() {
		presented, presentErr := plane.ApplyWindowContext(ctx, WindowLevel{Center: 128, Width: 256})
		result <- presentationResult{image: presented, err: presentErr}
	}()
	select {
	case <-ctx.renderStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("scalar presentation did not reach the cancellation barrier")
	}
	cancel()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("scalar presentation error = %v, want context.Canceled", got.err)
		}
		if got.image != nil {
			t.Fatal("canceled scalar presentation published a partial image")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scalar presentation did not stop after cancellation")
	}
}

const scalarSlabCancellationBudget = 33 * time.Millisecond

func BenchmarkScalarOrthogonalSlabCancellationLatency(b *testing.B) {
	stack := gradientXZStack(256, 256, 64)
	if _, err := RenderScalarSlabWithOptionsContext(
		context.Background(), stack, MPRPlaneAxial, 32, 1, SlabNone, DefaultMPRRenderOptions(),
	); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(scalarSlabCancellationBudget.Nanoseconds()), "budget-ns/op")
	b.StopTimer()
	for range b.N {
		base, cancel := context.WithCancel(context.Background())
		ctx := newCancellationBarrierContext(base, 4096)
		result := make(chan error, 1)
		go func() {
			plane, err := RenderScalarSlabWithOptionsContext(
				ctx, stack, MPRPlaneAxial, 32, 64, SlabAverage, DefaultMPRRenderOptions(),
			)
			if plane != nil && err != nil {
				err = errors.New("canceled scalar slab published partial output")
			}
			result <- err
		}()
		<-ctx.renderStarted
		b.StartTimer()
		cancel()
		err := <-result
		b.StopTimer()
		if !errors.Is(err, context.Canceled) {
			b.Fatalf("scalar slab error = %v, want context.Canceled", err)
		}
	}
}

type cancellationBarrierContext struct {
	context.Context
	errCalls      atomic.Int64
	passErrCalls  int64
	renderStarted chan struct{}
	startOnce     sync.Once
}

func newCancellationBarrierContext(ctx context.Context, passErrCalls int64) *cancellationBarrierContext {
	return &cancellationBarrierContext{Context: ctx, passErrCalls: passErrCalls, renderStarted: make(chan struct{})}
}

func (c *cancellationBarrierContext) Err() error {
	if c.errCalls.Add(1) <= c.passErrCalls {
		return c.Context.Err()
	}
	c.startOnce.Do(func() { close(c.renderStarted) })
	<-c.Context.Done()
	return c.Context.Err()
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
