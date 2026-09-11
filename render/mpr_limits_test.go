package render

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestMPRResliceRejectsInvalidOrUnboundedRequestsBeforeAllocation(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(8, 8, 3))
	if err != nil {
		t.Fatal(err)
	}
	plane := volume.OrthogonalPlane(MPRPlaneAxial, volume.Center())
	hugeInt := int(^uint(0) >> 1)
	tests := []struct {
		name    string
		wantErr error
		run     func() error
	}{
		{
			name: "zero width", wantErr: ErrMPRInvalidInput,
			run: func() error {
				_, err := ResliceObliqueContext(context.Background(), volume, plane, 0, 8, WindowLevel{})
				return err
			},
		},
		{
			name: "negative height", wantErr: ErrMPRInvalidInput,
			run: func() error {
				_, err := ResliceObliqueScalarContext(context.Background(), volume, plane, 8, -1)
				return err
			},
		},
		{
			name: "integer overflow dimensions", wantErr: ErrMPRResourceLimit,
			run: func() error {
				_, err := ResliceObliqueScalarContext(context.Background(), volume, plane, hugeInt, hugeInt)
				return err
			},
		},
		{
			name: "dimension above default", wantErr: ErrMPRResourceLimit,
			run: func() error {
				_, err := ResliceObliqueContext(context.Background(), volume, plane, MaxObliqueOutputDimension+1, 8, WindowLevel{})
				return err
			},
		},
		{
			name: "custom pixel ceiling", wantErr: ErrMPRResourceLimit,
			run: func() error {
				_, err := ResliceObliqueWithLimitsContext(context.Background(), volume, plane, 8, 8, WindowLevel{}, MPRLimits{MaxOutputPixels: 63})
				return err
			},
		},
		{
			name: "custom scalar byte ceiling", wantErr: ErrMPRResourceLimit,
			run: func() error {
				_, err := ResliceObliqueScalarWithLimitsContext(context.Background(), volume, plane, 4, 4, MPRLimits{MaxWorkingBytes: 100})
				return err
			},
		},
		{
			name: "custom slab sample ceiling", wantErr: ErrMPRResourceLimit,
			run: func() error {
				_, err := ResliceObliqueSlabWithLimitsContext(context.Background(), volume, plane, 8, 8, 5, SlabMIP, WindowLevel{}, MPRLimits{MaxSlabSamples: 4})
				return err
			},
		},
		{
			name: "negative caller limit", wantErr: ErrMPRInvalidInput,
			run: func() error {
				_, err := ResliceObliqueWithLimitsContext(context.Background(), volume, plane, 8, 8, WindowLevel{}, MPRLimits{MaxOutputPixels: -1})
				return err
			},
		},
		{
			name: "nan plane", wantErr: ErrMPRInvalidInput,
			run: func() error {
				invalid := plane
				invalid.Origin.X = math.NaN()
				_, err := ResliceObliqueContext(context.Background(), volume, invalid, 8, 8, WindowLevel{})
				return err
			},
		},
		{
			name: "infinite plane", wantErr: ErrMPRInvalidInput,
			run: func() error {
				invalid := plane
				invalid.U.X = math.Inf(1)
				_, err := ResliceObliqueScalarContext(context.Background(), volume, invalid, 8, 8)
				return err
			},
		},
		{
			name: "degenerate plane", wantErr: ErrMPRInvalidInput,
			run: func() error {
				_, err := ResliceObliqueContext(context.Background(), volume, Plane{}, 8, 8, WindowLevel{})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("reslice error = %v, want %v", err, tt.wantErr)
			}
			var requestErr *MPRRequestError
			if !errors.As(err, &requestErr) || requestErr.Field == "" {
				t.Fatalf("reslice error = %T %v, want typed field context", err, err)
			}
		})
	}
}

func TestMPRBoundedVariantsPreservePixelsAndScalarGeometry(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(16, 18, 4))
	if err != nil {
		t.Fatal(err)
	}
	plane := volume.OrthogonalPlane(MPRPlaneAxial, volume.Center())
	window := WindowLevel{Center: 64, Width: 128}
	limits := MPRLimits{
		MaxOutputDimension: 64,
		MaxOutputPixels:    64 * 64,
		MaxWorkingBytes:    1 << 20,
		MaxSlabSamples:     16,
	}

	wantImage, err := ResliceObliqueContext(context.Background(), volume, plane, 31, 29, window)
	if err != nil {
		t.Fatal(err)
	}
	gotImage, err := ResliceObliqueWithLimitsContext(context.Background(), volume, plane, 31, 29, window, limits)
	if err != nil {
		t.Fatal(err)
	}
	assertGrayImagesEqual(t, "bounded MPR", wantImage, gotImage)

	wantScalar, err := ResliceObliqueScalarContext(context.Background(), volume, plane, 31, 29)
	if err != nil {
		t.Fatal(err)
	}
	gotScalar, err := ResliceObliqueScalarWithLimitsContext(context.Background(), volume, plane, 31, 29, limits)
	if err != nil {
		t.Fatal(err)
	}
	if gotScalar.width != wantScalar.width || gotScalar.height != wantScalar.height ||
		!reflect.DeepEqual(gotScalar.values, wantScalar.values) || !reflect.DeepEqual(gotScalar.valid, wantScalar.valid) {
		t.Fatal("bounded scalar reslice changed pixels or geometry")
	}
}

func TestMPROrthogonalRenderersHonorOptionsLimits(t *testing.T) {
	stack := gradientXZStack(8, 8, 3)
	options := DefaultMPRRenderOptions()
	options.Limits.MaxOutputDimension = 4
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "plane",
			run: func() error {
				_, err := RenderMPRPlaneWithOptions(stack, MPRPlaneAxial, 0, WindowLevel{}, options)
				return err
			},
		},
		{
			name: "slab",
			run: func() error {
				_, err := RenderSlabWithOptions(stack, MPRPlaneAxial, 1, 3, SlabMIP, WindowLevel{}, options)
				return err
			},
		},
		{
			name: "scalar",
			run: func() error {
				_, err := RenderScalarSlabWithOptionsContext(context.Background(), stack, MPRPlaneAxial, 1, 3, SlabMIP, options)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, ErrMPRResourceLimit) {
				t.Fatalf("renderer error = %v, want ErrMPRResourceLimit", err)
			}
		})
	}
}

func TestMPRResliceHonorsCanceledContextBeforeValidationAndAllocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResliceObliqueWithLimitsContext(ctx, nil, Plane{}, int(^uint(0)>>1), int(^uint(0)>>1), WindowLevel{}, MPRLimits{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResliceObliqueWithLimitsContext() error = %v, want context.Canceled", err)
	}
}

func TestPlanObliqueResolutionRejectsNonFinitePlaneAndNegativeSamples(t *testing.T) {
	volume := &Volume{ColSpacing: 1, RowSpacing: 1, SliceSpacing: 1}
	plane := Plane{U: Vec3{X: 10}, V: Vec3{Y: 10}}
	invalidPlane := plane
	invalidPlane.V.Y = math.Inf(1)
	if _, err := volume.PlanObliqueResolution(invalidPlane, ObliqueResolutionRequest{}); !errors.Is(err, ErrMPRInvalidInput) {
		t.Fatalf("non-finite plane error = %v, want ErrMPRInvalidInput", err)
	}
	if _, err := volume.PlanObliqueResolution(plane, ObliqueResolutionRequest{SlabSamples: -1}); !errors.Is(err, ErrMPRInvalidInput) {
		t.Fatalf("negative slab samples error = %v, want ErrMPRInvalidInput", err)
	}
}

func BenchmarkValidateMPRReslice(b *testing.B) {
	plane := Plane{Origin: Vec3{X: 1, Y: 2, Z: 3}, U: Vec3{X: 200}, V: Vec3{Y: 150}}
	limits := DefaultMPRLimits()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := validateMPRReslice(plane, 512, 384, 1, 9, limits); err != nil {
			b.Fatal(err)
		}
	}
}
