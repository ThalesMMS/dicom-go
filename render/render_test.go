package render

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestEffectiveFrameMetadataNormalizesCodecRGBOutput(t *testing.T) {
	for _, syntax := range []transfer.Syntax{
		transfer.JPEG2000,
		transfer.JPEG2000Part2,
		transfer.HTJ2K,
		transfer.JPEGXL,
	} {
		t.Run(syntax.Name, func(t *testing.T) {
			got := effectiveFrameMetadata(&Frame{
				Encapsulated:      true,
				TransferSyntaxUID: syntax.UID,
				Metadata: pixeldata.Metadata{
					SamplesPerPixel:            3,
					PhotometricInterpretation:  "YBR_ICT",
					PlanarConfiguration:        1,
					PlanarConfigurationPresent: true,
				},
			})
			if got.PhotometricInterpretation != "RGB" || got.PlanarConfiguration != 0 {
				t.Fatalf("effective metadata = %+v, want interleaved RGB", got)
			}
		})
	}

	original := pixeldata.Metadata{
		SamplesPerPixel:            3,
		PhotometricInterpretation:  "YBR_FULL_422",
		PlanarConfiguration:        1,
		PlanarConfigurationPresent: true,
	}
	got := effectiveFrameMetadata(&Frame{
		Encapsulated:      true,
		TransferSyntaxUID: transfer.RLELossless.UID,
		Metadata:          original,
	})
	if got.PhotometricInterpretation != original.PhotometricInterpretation ||
		got.PlanarConfiguration != original.PlanarConfiguration ||
		got.PlanarConfigurationPresent != original.PlanarConfigurationPresent {
		t.Fatalf("RLE metadata = %+v, want unchanged %+v", got, original)
	}
}

func Test_PixelValueAt_applies_rescale_when_frame_is_unsigned_16_bit(t *testing.T) {
	// Given
	data := make([]byte, 8)
	binary.LittleEndian.PutUint16(data[0:2], 0)
	binary.LittleEndian.PutUint16(data[2:4], 1024)
	binary.LittleEndian.PutUint16(data[4:6], 2048)
	binary.LittleEndian.PutUint16(data[6:8], 4095)
	frame := &Frame{
		Metadata: pixeldata.Metadata{
			Rows:                2,
			Columns:             2,
			SamplesPerPixel:     1,
			BitsAllocated:       16,
			BitsStored:          12,
			HighBit:             11,
			PixelRepresentation: 0,
		},
		ByteOrder:  binary.LittleEndian,
		PixelBytes: data,
		Rescale:    Rescale{Slope: 1, Intercept: -1024},
	}

	// When
	value, ok := PixelValueAt(frame, 1, 0)

	// Then
	if !ok {
		t.Fatal("PixelValueAt() ok = false, want true")
	}
	if value.X != 1 || value.Y != 0 {
		t.Fatalf("PixelValueAt() coordinates = (%d,%d), want (1,0)", value.X, value.Y)
	}
	if value.Stored != 1024 {
		t.Fatalf("PixelValueAt() stored = %d, want 1024", value.Stored)
	}
	if value.Rescaled != 0 {
		t.Fatalf("PixelValueAt() rescaled = %v, want 0", value.Rescaled)
	}
}

func Test_SamplePixelValue_applies_rescale_without_frame_allocation(t *testing.T) {
	// Given
	data := make([]byte, 4)
	binary.LittleEndian.PutUint16(data[0:2], 512)
	binary.LittleEndian.PutUint16(data[2:4], 1024)
	metadata := pixeldata.Metadata{
		Rows:                1,
		Columns:             2,
		SamplesPerPixel:     1,
		BitsAllocated:       16,
		BitsStored:          12,
		HighBit:             11,
		PixelRepresentation: 0,
	}

	// When
	value, ok := SamplePixelValue(&metadata, binary.LittleEndian, data, Rescale{Slope: 2, Intercept: -1024}, 1, 0)

	// Then
	if !ok {
		t.Fatal("SamplePixelValue() ok = false, want true")
	}
	if value.Stored != 1024 || value.Rescaled != 1024 {
		t.Fatalf("SamplePixelValue() = %+v, want stored 1024 rescaled 1024", value)
	}
}

func Test_decodeGrayscaleSlice_stores_modality_values_as_float32(t *testing.T) {
	// Given
	frame := testRenderFrame([]byte{0, 64, 128, 255})

	// When
	decoded, err := decodeSliceFrame(frame)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	values := decoded.grayscale
	var _ []float32 = values
	if len(values) != 4 || values[2] != 128 {
		t.Fatalf("decoded grayscale = %v, want four modality values with index 2 = 128", values)
	}
	if got, want := decoded.cacheBytes(), int64(len(values)*4); got != want {
		t.Fatalf("cacheBytes() = %d, want %d for float32 grayscale", got, want)
	}
}

func TestWindowedGrayHonorsVOILUTFunction(t *testing.T) {
	for _, function := range []display.VOIFunction{display.VOILinearExact, display.VOISigmoid} {
		window := WindowLevel{Center: 50, Width: 100, Function: function}
		for _, value := range []float64{1, 25, 50, 75, 99} {
			want := display.VOILUT{Center: window.Center, Width: window.Width, Function: function}.WindowByte(value)
			if got := windowedGrayMapped(value, prepareWindow(window)); got != want {
				t.Fatalf("windowedGray(%v, function=%v) = %d, want %d", value, function, got, want)
			}
			if got := WindowedGray(value, window); got != want {
				t.Fatalf("WindowedGray(%v, function=%v) = %d, want %d", value, function, got, want)
			}
		}
	}
}

func TestPreparedWindowPreservesNaNGuard(t *testing.T) {
	for _, window := range []WindowLevel{
		{Center: math.NaN(), Width: 100},
		{Center: 50, Width: math.NaN()},
	} {
		if got := windowedGrayMapped(50, prepareWindow(window)); got != 0 {
			t.Fatalf("windowedGrayMapped with NaN window = %d, want 0", got)
		}
	}
}

func TestRenderFrameHonorsVOILUTFunction(t *testing.T) {
	frame := testRenderFrame([]byte{25, 25, 25, 25})
	window := WindowLevel{Center: 50, Width: 100, Function: display.VOISigmoid}

	img, err := RenderFrame(frame, window)
	if err != nil {
		t.Fatal(err)
	}
	want := display.VOILUT{Center: 50, Width: 100, Function: display.VOISigmoid}.WindowByte(25)
	if got := grayAt(img, 0, 0); got != want {
		t.Fatalf("RenderFrame SIGMOID pixel = %d, want %d", got, want)
	}
}

func TestRenderFrameAndCacheHonorVOILUT(t *testing.T) {
	lut, err := display.NewLUT([]int{4, 0, 8}, []uint16{0, 200, 20, 255})
	if err != nil {
		t.Fatal(err)
	}
	frame := testRenderFrame([]byte{0, 1, 2, 3})
	window := WindowLevel{LUT: lut}

	for name, render := range map[string]func() (image.Image, error){
		"direct": func() (image.Image, error) { return RenderFrame(frame, window) },
		"cache":  func() (image.Image, error) { return NewRenderCache(1<<20).RenderFrame(frame, window) },
	} {
		t.Run(name, func(t *testing.T) {
			img, err := render()
			if err != nil {
				t.Fatal(err)
			}
			for x, want := range []uint8{0, 200, 20, 255} {
				if got := grayAt(img, x%2, x/2); got != want {
					t.Fatalf("pixel %d = %d, want %d", x, got, want)
				}
			}
		})
	}
}

func Test_RenderFramePNG_encodes_windowed_monochrome_frame(t *testing.T) {
	// Given
	frame := testRenderFrame([]byte{0, 64, 128, 255})

	// When
	encoded, err := RenderFramePNG(frame, WindowLevel{Center: 128, Width: 256})

	// Then
	if err != nil {
		t.Fatalf("RenderFramePNG() error = %v", err)
	}
	img, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("png.Decode(RenderFramePNG()) error = %v", err)
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("decoded image size = %dx%d, want 2x2", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func Test_RenderFrame_treats_jpeg_ybr_decoded_bytes_as_rgb(t *testing.T) {
	frame := &Frame{
		Encapsulated:       true,
		TransferSyntaxUID:  transfer.JPEGBaseline.UID,
		TransferSyntaxName: transfer.JPEGBaseline.Name,
		Metadata: pixeldata.Metadata{
			Rows:                      1,
			Columns:                   1,
			SamplesPerPixel:           3,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PixelRepresentation:       0,
			PhotometricInterpretation: "YBR_FULL_422",
		},
		PixelBytes: []byte{255, 0, 0},
	}

	img, err := RenderFrame(frame, WindowLevel{})
	if err != nil {
		t.Fatalf("RenderFrame() error = %v", err)
	}
	got := color.RGBAModel.Convert(img.At(0, 0)).(color.RGBA)
	if got.R != 255 || got.G != 0 || got.B != 0 || got.A != 255 {
		t.Fatalf("pixel = %#v, want opaque red", got)
	}
}

func Test_AutoWindow_returns_full_dynamic_range_when_frame_has_contrast(t *testing.T) {
	// Given
	frame := testRenderFrame([]byte{0, 64, 128, 255})

	// When
	wl, ok := AutoWindow(frame)

	// Then
	if !ok {
		t.Fatal("AutoWindow() ok = false, want true")
	}
	if wl.Center != 127.5 || wl.Width != 255 {
		t.Fatalf("AutoWindow() = %+v, want center 127.5 width 255", wl)
	}
}

func Test_AutoWindow_applies_rescale_to_native_16_bit_pixels(t *testing.T) {
	// Given
	data := make([]byte, 4)
	binary.LittleEndian.PutUint16(data[0:2], 0)
	binary.LittleEndian.PutUint16(data[2:4], 4000)
	frame := &Frame{
		Metadata: pixeldata.Metadata{
			Rows:                1,
			Columns:             2,
			SamplesPerPixel:     1,
			BitsAllocated:       16,
			BitsStored:          16,
			HighBit:             15,
			PixelRepresentation: 0,
		},
		ByteOrder:  binary.LittleEndian,
		PixelBytes: data,
		Rescale:    Rescale{Slope: 2, Intercept: -1000},
	}

	// When
	wl, ok := AutoWindow(frame)

	// Then
	if !ok {
		t.Fatal("AutoWindow() ok = false, want true")
	}
	if wl.Center != 3000 || wl.Width != 8000 {
		t.Fatalf("AutoWindow() = %+v, want center 3000 width 8000", wl)
	}
}

func Test_RenderMPRPlane_samples_coronal_and_sagittal_voxels(t *testing.T) {
	// Given
	stack := &Stack{
		DefaultWindow: WindowLevel{Center: 35, Width: 70},
		Frames: []*Frame{
			testRenderFrame([]byte{0, 10, 20, 30}),
			testRenderFrame([]byte{40, 50, 60, 70}),
			testRenderFrame([]byte{80, 90, 100, 110}),
		},
	}

	// When
	coronal, err := RenderMPRPlane(stack, MPRPlaneCoronal, 1, stack.DefaultWindow)

	// Then
	if err != nil {
		t.Fatalf("RenderMPRPlane(coronal) error = %v", err)
	}
	if coronal.Bounds().Dx() != 2 || coronal.Bounds().Dy() != 3 {
		t.Fatalf("coronal size = %dx%d, want 2x3", coronal.Bounds().Dx(), coronal.Bounds().Dy())
	}
	if got, want := grayAt(coronal, 0, 0), displayGrayMapped(100, prepareWindow(stack.DefaultWindow), "MONOCHROME2"); got != want {
		t.Fatalf("coronal top sample = %d, want superior slice sample %d", got, want)
	}

	// When
	sagittal, err := RenderMPRPlane(stack, MPRPlaneSagittal, 0, stack.DefaultWindow)

	// Then
	if err != nil {
		t.Fatalf("RenderMPRPlane(sagittal) error = %v", err)
	}
	if sagittal.Bounds().Dx() != 2 || sagittal.Bounds().Dy() != 3 {
		t.Fatalf("sagittal size = %dx%d, want 2x3", sagittal.Bounds().Dx(), sagittal.Bounds().Dy())
	}
	if got := grayAt(sagittal, 1, 1); got != 222 {
		t.Fatalf("sagittal sample = %d, want 222", got)
	}
}

func Test_RenderSlab_projects_average_minimum_and_maximum(t *testing.T) {
	// Given
	stack := &Stack{
		DefaultWindow: WindowLevel{Center: 20, Width: 40},
		Frames: []*Frame{
			testRenderFrame([]byte{10, 20, 30, 40}),
			testRenderFrame([]byte{30, 40, 50, 60}),
			testRenderFrame([]byte{50, 60, 70, 80}),
		},
	}
	tests := []struct {
		name string
		mode SlabMode
		want float64
	}{
		{name: "maximum", mode: SlabMIP, want: 50},
		{name: "minimum", mode: SlabMinIP, want: 30},
		{name: "average", mode: SlabAverage, want: 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			img, err := RenderSlab(stack, MPRPlaneAxial, 1, 2, tt.mode, stack.DefaultWindow)

			// Then
			if err != nil {
				t.Fatalf("RenderSlab() error = %v", err)
			}
			got := grayAt(img, 0, 0)
			want := displayGrayMapped(tt.want, prepareWindow(stack.DefaultWindow), "MONOCHROME2")
			if got != want {
				t.Fatalf("RenderSlab() gray = %d, want %d", got, want)
			}
		})
	}
}

func Test_RenderSlab_coronal_and_sagittal_match_mpr_vertical_order(t *testing.T) {
	stack := &Stack{
		DefaultWindow: WindowLevel{Center: 35, Width: 70},
		Frames: []*Frame{
			testRenderFrame([]byte{0, 10, 20, 30}),
			testRenderFrame([]byte{40, 50, 60, 70}),
			testRenderFrame([]byte{80, 90, 100, 110}),
		},
	}
	tests := []struct {
		name   string
		plane  MPRPlane
		center int
	}{
		{name: "coronal", plane: MPRPlaneCoronal, center: 1},
		{name: "sagittal", plane: MPRPlaneSagittal, center: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, err := RenderMPRPlane(stack, tt.plane, tt.center, stack.DefaultWindow)
			if err != nil {
				t.Fatalf("RenderMPRPlane(%s) error = %v", tt.plane, err)
			}
			slab, err := RenderSlab(stack, tt.plane, tt.center, 1, SlabAverage, stack.DefaultWindow)
			if err != nil {
				t.Fatalf("RenderSlab(%s) error = %v", tt.plane, err)
			}

			if got, want := grayAt(slab, 0, 0), grayAt(plain, 0, 0); got != want {
				t.Fatalf("RenderSlab(%s) top sample = %d, want MPR vertical order sample %d", tt.plane, got, want)
			}
		})
	}
}

func Test_RenderMPRPlane_usesCachedVolumeForOrthogonalHotPath(t *testing.T) {
	stack := gradientXZStack(16, 16, 8)
	stack.DefaultWindow = WindowLevel{Center: 128, Width: 256}
	for _, frame := range stack.Frames {
		frame.Metadata.PhotometricInterpretation = " monochrome2\x00"
	}

	if _, err := RenderMPRPlane(stack, MPRPlaneCoronal, 8, stack.DefaultWindow); err != nil {
		t.Fatalf("RenderMPRPlane warmup error = %v", err)
	}
	if stack.mprVolume == nil {
		t.Fatal("RenderMPRPlane did not cache Stack.Volume")
	}

	var renderErr error
	allocs := testing.AllocsPerRun(5, func() {
		_, renderErr = RenderMPRPlane(stack, MPRPlaneCoronal, 8, stack.DefaultWindow)
	})
	if renderErr != nil {
		t.Fatalf("RenderMPRPlane hot error = %v", renderErr)
	}
	if allocs > 32 {
		t.Fatalf("RenderMPRPlane hot allocations = %.1f, want cached path without per-pixel string allocations", allocs)
	}
}

func Test_RenderSlab_usesCachedVolumeForProjectionHotPath(t *testing.T) {
	stack := gradientXZStack(16, 16, 8)
	stack.DefaultWindow = WindowLevel{Center: 128, Width: 256}
	for _, frame := range stack.Frames {
		frame.Metadata.PhotometricInterpretation = " monochrome2\x00"
	}

	if _, err := RenderSlab(stack, MPRPlaneAxial, 4, 5, SlabAverage, stack.DefaultWindow); err != nil {
		t.Fatalf("RenderSlab warmup error = %v", err)
	}
	if stack.mprVolume == nil {
		t.Fatal("RenderSlab did not cache Stack.Volume")
	}

	var renderErr error
	allocs := testing.AllocsPerRun(5, func() {
		_, renderErr = RenderSlab(stack, MPRPlaneAxial, 4, 5, SlabAverage, stack.DefaultWindow)
	})
	if renderErr != nil {
		t.Fatalf("RenderSlab hot error = %v", renderErr)
	}
	if allocs > 32 {
		t.Fatalf("RenderSlab hot allocations = %.1f, want cached path without per-pixel string allocations", allocs)
	}
}

func Test_OrthogonalPlane_reslice_matches_mpr_vertical_order(t *testing.T) {
	stack := gradientXZStack(2, 2, 3)
	window := WindowLevel{Center: 50, Width: 100}
	vol, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		plane       MPRPlane
		index       int
		centerVoxel Vec3
	}{
		{name: "coronal", plane: MPRPlaneCoronal, index: 1, centerVoxel: Vec3{X: 0, Y: 1, Z: 1}},
		{name: "sagittal", plane: MPRPlaneSagittal, index: 0, centerVoxel: Vec3{X: 0, Y: 0, Z: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, err := RenderMPRPlane(stack, tt.plane, tt.index, window)
			if err != nil {
				t.Fatalf("RenderMPRPlane(%s) error = %v", tt.plane, err)
			}
			plane := vol.OrthogonalPlane(tt.plane, vol.VoxelToPatient(tt.centerVoxel))
			reslice := ResliceOblique(vol, plane, plain.Bounds().Dx(), plain.Bounds().Dy(), window)
			bottom := plain.Bounds().Dy() - 1

			if got, want := grayAt(reslice, 0, 0), grayAt(plain, 0, 0); got != want {
				t.Fatalf("ResliceOblique(%s OrthogonalPlane) top sample = %d, want MPR sample %d", tt.plane, got, want)
			}
			if got, want := grayAt(reslice, 0, bottom), grayAt(plain, 0, bottom); got != want {
				t.Fatalf("ResliceOblique(%s OrthogonalPlane) bottom sample = %d, want MPR sample %d", tt.plane, got, want)
			}
		})
	}
}

func testRenderFrame(data []byte) *Frame {
	return &Frame{
		Metadata: pixeldata.Metadata{
			Rows:                      2,
			Columns:                   2,
			SamplesPerPixel:           1,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PhotometricInterpretation: "MONOCHROME2",
			PixelRepresentation:       0,
		},
		ByteOrder:     binary.LittleEndian,
		PixelBytes:    data,
		DefaultWindow: WindowLevel{Center: 128, Width: 256},
		Rescale:       Rescale{Slope: 1},
	}
}

func grayAt(img image.Image, x, y int) uint8 {
	r, g, b, _ := img.At(x, y).RGBA()
	if r != g || g != b {
		return uint8(color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y)
	}
	return uint8(r >> 8)
}

func TestWorldMeasurementGeometry(t *testing.T) {
	a := WorldPoint{X: 0, Y: 0, Z: 0}
	b := WorldPoint{X: 3, Y: 4, Z: 12}
	if got := WorldDistance(a, b); got != 13 {
		t.Fatalf("WorldDistance = %g, want 13", got)
	}
	angle, ok := WorldAngle(WorldPoint{X: 1}, a, WorldPoint{Y: 1}, false)
	if !ok || math.Abs(angle-90) > 1e-12 {
		t.Fatalf("WorldAngle = %g/%v, want 90/true", angle, ok)
	}
	acute, ok := WorldSegmentAngle(a, WorldPoint{X: 1}, a, WorldPoint{X: -1, Y: 1}, true)
	if !ok || math.Abs(acute-45) > 1e-12 {
		t.Fatalf("WorldSegmentAngle acute = %g/%v, want 45/true", acute, ok)
	}
	if _, ok := WorldAngle(a, a, b, false); ok {
		t.Fatal("WorldAngle accepted a degenerate segment")
	}
}
