package render

import "testing"

func TestRenderVolumeSlabCompositesEveryOrthogonalPlane(t *testing.T) {
	stack := gradientXZStack(12, 10, 8)
	stack.DefaultWindow = WindowLevel{Center: 128, Width: 256}
	options := DefaultVolumeSlabOptions()

	for _, test := range []struct {
		plane  MPRPlane
		center int
	}{
		{MPRPlaneAxial, 4},
		{MPRPlaneCoronal, 6},
		{MPRPlaneSagittal, 5},
	} {
		img, err := RenderVolumeSlab(stack, test.plane, test.center, 5, stack.DefaultWindow, options)
		if err != nil {
			t.Fatalf("RenderVolumeSlab(%s) error = %v", test.plane, err)
		}
		if img == nil || img.Bounds().Dx() == 0 || img.Bounds().Dy() == 0 {
			t.Fatalf("RenderVolumeSlab(%s) returned an empty image", test.plane)
		}
	}
}

func TestRenderVolumeSlabCorrectsGantryTiltInPatientSpace(t *testing.T) {
	stack := tiltedGradientRowStack(
		4,
		5,
		[]Vec3{{0, 0, 0}, {0, 1, 1}, {0, 2, 2}},
		1,
		1,
	)
	window := WindowLevel{Center: 127.5, Width: 255}
	stack.DefaultWindow = window
	options := DefaultVolumeSlabOptions()

	tests := []struct {
		name      string
		plane     MPRPlane
		center    int
		sampleX   int
		sourceRow int
		want      uint8
	}{
		{name: "coronal", plane: MPRPlaneCoronal, center: 2, sampleX: 2, sourceRow: 0, want: 0},
		{name: "sagittal", plane: MPRPlaneSagittal, center: 2, sampleX: 3, sourceRow: 1, want: 9},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			img, err := RenderVolumeSlab(stack, test.plane, test.center, 1, window, options)
			if err != nil {
				t.Fatal(err)
			}
			if got := grayAt(img, test.sampleX, 0); got != test.want {
				t.Fatalf("corrected %s volume slab = %d, want source row %d (%d)", test.plane, got, test.sourceRow, test.want)
			}
		})
	}

	sourceOptions := options
	sourceOptions.GantryTiltMode = GantryTiltSourceGeometry
	source, err := RenderVolumeSlab(stack, MPRPlaneCoronal, 2, 1, window, sourceOptions)
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := RenderVolumeSlab(stack, MPRPlaneCoronal, 2, 1, window, options)
	if err != nil {
		t.Fatal(err)
	}
	if sourcePixel, correctedPixel := grayAt(source, 2, 0), grayAt(corrected, 2, 0); sourcePixel <= correctedPixel {
		t.Fatalf("source-geometry volume slab = %d, want acquired row brighter than corrected row %d", sourcePixel, correctedPixel)
	}
}

func TestRenderVolumeSlabOpacityTablesProduceDifferentComposites(t *testing.T) {
	stack := gradientXZStack(16, 16, 10)
	stack.DefaultWindow = WindowLevel{Center: 128, Width: 256}

	render := func(mode SlabOpacityMode) uint8 {
		options := DefaultVolumeSlabOptions()
		options.Opacity = mode
		img, err := RenderVolumeSlab(stack, MPRPlaneAxial, 5, 9, stack.DefaultWindow, options)
		if err != nil {
			t.Fatalf("RenderVolumeSlab(%v) error = %v", mode, err)
		}
		return grayAt(img, 8, 8)
	}

	linear := render(SlabOpacityLinear)
	logarithmic := render(SlabOpacityLogarithmic)
	inverse := render(SlabOpacityLogarithmicInverse)
	if linear == logarithmic || linear == inverse || logarithmic == inverse {
		t.Fatalf("volume slab opacity samples = linear %d log %d inverse %d, want distinct composites", linear, logarithmic, inverse)
	}
}

func TestRenderVolumeSlabShadingChangesGradientSurface(t *testing.T) {
	stack := sphereStack(24, 8)
	stack.DefaultWindow = WindowLevel{Center: 128, Width: 256}
	plain := DefaultVolumeSlabOptions()
	shaded := plain
	shaded.Shading = true

	plainImage, err := RenderVolumeSlab(stack, MPRPlaneAxial, 12, 18, stack.DefaultWindow, plain)
	if err != nil {
		t.Fatal(err)
	}
	shadedImage, err := RenderVolumeSlab(stack, MPRPlaneAxial, 12, 18, stack.DefaultWindow, shaded)
	if err != nil {
		t.Fatal(err)
	}

	different := false
	for y := 0; y < plainImage.Bounds().Dy() && !different; y++ {
		for x := 0; x < plainImage.Bounds().Dx(); x++ {
			if grayAt(plainImage, x, y) != grayAt(shadedImage, x, y) {
				different = true
				break
			}
		}
	}
	if !different {
		t.Fatal("enabling MPR volume-slab shading did not change any rendered pixel")
	}
}
