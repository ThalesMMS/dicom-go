package render

import (
	"context"
	"encoding/binary"
	"image"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func Test_RenderCPR_transverse_samples_volume_centerline(t *testing.T) {
	// Given
	vol, err := BuildVolume(gradientColumnStack(12, 12, 6))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 6, Y: 2, Z: 1}),
		vol.VoxelToPatient(Vec3{X: 6, Y: 9, Z: 4}),
	})

	// When
	img, err := RenderCPR(context.Background(), CPRRequest{
		Mode:         CPRTransverse,
		Volume:       vol,
		Path:         path,
		Window:       WindowLevel{Center: 128, Width: 256},
		Width:        9,
		CrossSpacing: 1,
	})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 9 || img.Bounds().Dy() != 9 {
		t.Fatalf("CPR transverse size = %dx%d, want 9x9", img.Bounds().Dx(), img.Bounds().Dy())
	}
	if grayAt(img, 4, 4) == 0 {
		t.Fatal("CPR center sample is black, want sampled volume data")
	}
}

func Test_RenderCPR_straightened_and_slab_sample_centerline_and_projection(t *testing.T) {
	// Given
	vol, err := BuildVolume(gradientXZStack(12, 12, 5))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 6, Y: 2, Z: 1}),
		vol.VoxelToPatient(Vec3{X: 6, Y: 9, Z: 1}),
	})
	window := WindowLevel{Center: 128, Width: 256}
	req := CPRRequest{
		Mode:         CPRStraightened,
		Volume:       vol,
		Path:         path,
		Window:       window,
		Width:        7,
		ArcSpacing:   1,
		CrossSpacing: 1,
	}

	// When
	straight, err := RenderCPR(context.Background(), req)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if straight.Bounds().Dx() != 8 || straight.Bounds().Dy() != 7 {
		t.Fatalf("straight CPR size = %dx%d, want 8x7", straight.Bounds().Dx(), straight.Bounds().Dy())
	}
	straightCenter := grayAt(straight, 3, 3)
	wantCenter := displayGrayMapped(56, prepareWindow(window), "MONOCHROME2")
	if diff := int(straightCenter) - int(wantCenter); diff < -1 || diff > 1 {
		t.Fatalf("straight CPR center gray = %d, want ~%d", straightCenter, wantCenter)
	}

	// When
	req.Mode = CPRSlab
	req.Thickness = 3
	req.SlabMode = SlabMIP
	slab, err := RenderCPR(context.Background(), req)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	slabCenter := grayAt(slab, 3, 3)
	if slabCenter <= straightCenter {
		t.Fatalf("slab center gray = %d, straight center = %d; want slab MIP brighter", slabCenter, straightCenter)
	}
}

func Test_RenderCPR_longitudinal_path_runs_left_to_right(t *testing.T) {
	vol, err := BuildVolume(gradientXZStack(12, 12, 5))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 6, Y: 2, Z: 1}),
		vol.VoxelToPatient(Vec3{X: 6, Y: 9, Z: 1}),
	})

	img, err := RenderCPR(context.Background(), CPRRequest{
		Mode: CPRStraightened, Volume: vol, Path: path,
		Window: WindowLevel{Center: 128, Width: 256},
		Width:  7, ArcSpacing: 1, CrossSpacing: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if img.Bounds().Dx() <= img.Bounds().Dy() {
		t.Fatalf("longitudinal CPR size = %dx%d, want arc-length dimension on horizontal axis", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func Test_RenderCPR_explicit_high_resolution_spacing_preserves_physical_fov(t *testing.T) {
	vol, err := BuildVolume(gradientXZStack(24, 24, 5))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 12, Y: 2, Z: 2}),
		vol.VoxelToPatient(Vec3{X: 12, Y: 19, Z: 2}),
	})
	standard := CPRRequest{
		Mode: CPRStraightened, Volume: vol, Path: path,
		Window: WindowLevel{Center: 128, Width: 256},
		Width:  9, ArcSpacing: 2, CrossSpacing: 1.5,
	}
	high := standard
	high.Width = standard.Width*2 - 1
	high.ArcSpacing = standard.ArcSpacing / 2
	high.CrossSpacing = standard.CrossSpacing / 2

	standardImage, err := RenderCPR(context.Background(), standard)
	if err != nil {
		t.Fatal(err)
	}
	highImage, err := RenderCPR(context.Background(), high)
	if err != nil {
		t.Fatal(err)
	}

	standardCrossFOV := float64(standard.Width-1) * standard.CrossSpacing
	highCrossFOV := float64(high.Width-1) * high.CrossSpacing
	if math.Abs(standardCrossFOV-highCrossFOV) > 1e-9 {
		t.Fatalf("cross-section FOV standard/high = %.3f/%.3f mm, want equal", standardCrossFOV, highCrossFOV)
	}
	if got, want := highImage.Bounds().Dy(), standardImage.Bounds().Dy()*2-1; got != want {
		t.Fatalf("high-res cross samples = %d, want %d", got, want)
	}
	if got, standardSamples := highImage.Bounds().Dx(), standardImage.Bounds().Dx(); got <= standardSamples {
		t.Fatalf("high-res longitudinal samples = %d, want more than standard %d", got, standardSamples)
	}
	if got := path.Resample(standard.ArcSpacing); math.Abs(got[len(got)-1].ArcLength-path.Length()) > 1e-9 {
		t.Fatalf("standard longitudinal FOV ends at %.3f mm, want path end %.3f mm", got[len(got)-1].ArcLength, path.Length())
	}
	if got := path.Resample(high.ArcSpacing); math.Abs(got[len(got)-1].ArcLength-path.Length()) > 1e-9 {
		t.Fatalf("high-res longitudinal FOV ends at %.3f mm, want path end %.3f mm", got[len(got)-1].ArcLength, path.Length())
	}
}

func Test_RenderCPR_transverse_high_resolution_preserves_physical_fov(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(24, 24, 8))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 12, Y: 4, Z: 3}),
		vol.VoxelToPatient(Vec3{X: 12, Y: 19, Z: 3}),
	})
	standard := CPRRequest{
		Mode: CPRTransverse, Volume: vol, Path: path,
		Window: WindowLevel{Center: 128, Width: 256},
		Width:  11, CrossSpacing: 1.2, ArcLength: path.Length() / 2,
	}
	high := standard
	high.Width = standard.Width*2 - 1
	high.CrossSpacing = standard.CrossSpacing / 2

	standardImage, err := RenderCPR(context.Background(), standard)
	if err != nil {
		t.Fatal(err)
	}
	highImage, err := RenderCPR(context.Background(), high)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := highImage.Bounds().Dx(), standardImage.Bounds().Dx()*2-1; got != want {
		t.Fatalf("high-res transverse width = %d, want %d", got, want)
	}
	standardFOV := float64(standard.Width-1) * standard.CrossSpacing
	highFOV := float64(high.Width-1) * high.CrossSpacing
	if math.Abs(standardFOV-highFOV) > 1e-9 {
		t.Fatalf("transverse FOV standard/high = %.3f/%.3f mm, want equal", standardFOV, highFOV)
	}
}

func Test_RenderCPR_slab_projection_is_independent_of_reformation_type(t *testing.T) {
	vol, err := BuildVolume(gradientXZStack(12, 12, 5))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 6, Y: 2, Z: 1}),
		vol.VoxelToPatient(Vec3{X: 6, Y: 9, Z: 1}),
	})
	base := CPRRequest{
		Mode: CPRStretched, Volume: vol, Path: path,
		Window: WindowLevel{Center: 128, Width: 256},
		Width:  7, ArcSpacing: 1, CrossSpacing: 1,
	}

	plain, err := RenderCPR(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	base.Thickness = 3
	base.SlabMode = SlabMIP
	slab, err := RenderCPR(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}

	if grayAt(slab, 3, 3) <= grayAt(plain, 3, 3) {
		t.Fatalf("stretched MIP center = %d, plain stretched center = %d; want independent slab projection", grayAt(slab, 3, 3), grayAt(plain, 3, 3))
	}
}

func Test_RenderCPR_physical_slab_offsets_preserve_extent_across_output_spacing(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStackWithSpacing(12, 12, 6, 1.2, 0.8, 2.5))
	if err != nil {
		t.Fatal(err)
	}
	req := CPRRequest{Volume: vol, SlabMode: SlabMIP, ThicknessMM: 0.5}
	fine := cprSlabSampleOffsets(req, 0.25)
	coarse := cprSlabSampleOffsets(req, 4)
	if len(fine) != 2 || len(coarse) != 2 {
		t.Fatalf("0.5 mm slab samples = %v / %v, want two boundary samples", fine, coarse)
	}
	for _, offsets := range [][]float64{fine, coarse} {
		if math.Abs(offsets[0]+0.25) > 1e-9 || math.Abs(offsets[len(offsets)-1]-0.25) > 1e-9 {
			t.Fatalf("0.5 mm slab offsets = %v, want physical extent [-0.25,+0.25]", offsets)
		}
	}
}

func Test_RenderCPR_physical_slab_applies_to_longitudinal_and_transverse_views(t *testing.T) {
	vol, err := BuildVolume(cprXYZGradientStack(12, 12, 8))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 6, Y: 3, Z: 3}),
		vol.VoxelToPatient(Vec3{X: 6, Y: 8, Z: 3}),
	})
	base := CPRRequest{
		Volume: vol, Path: path, Window: WindowLevel{Center: 128, Width: 256},
		Width: 7, ArcSpacing: 1, CrossSpacing: 0.5,
	}
	for _, mode := range []CPRMode{CPRStraightened, CPRTransverse} {
		base.Mode = mode
		base.ThicknessMM = 0
		base.SlabMode = SlabNone
		plain, err := RenderCPR(context.Background(), base)
		if err != nil {
			t.Fatal(err)
		}
		base.ThicknessMM = 2
		base.SlabMode = SlabMIP
		slab, err := RenderCPR(context.Background(), base)
		if err != nil {
			t.Fatal(err)
		}
		x, y := plain.Bounds().Dx()/2, plain.Bounds().Dy()/2
		if grayAt(slab, x, y) <= grayAt(plain, x, y) {
			t.Fatalf("mode %v physical MIP center = %d, plain = %d; want slab applied", mode, grayAt(slab, x, y), grayAt(plain, x, y))
		}
	}
}

func cprXYZGradientStack(rows, cols, depth int) *Stack {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z := 0; z < depth; z++ {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				data[y*cols+x] = byte(x + y*5 + z*20)
			}
		}
		stack.Frames = append(stack.Frames, &Frame{
			Metadata: pixeldata.Metadata{
				Rows: uint16(rows), Columns: uint16(cols), SamplesPerPixel: 1,
				BitsAllocated: 8, BitsStored: 8, HighBit: 7,
				PhotometricInterpretation: "MONOCHROME2",
			},
			ByteOrder: binary.LittleEndian, PixelBytes: data, Rescale: Rescale{Slope: 1},
			ImageOrientation: []float64{1, 0, 0, 0, 1, 0}, ImagePosition: []float64{0, 0, float64(z)},
		})
	}
	return stack
}

func Test_RenderCPR_rotation_changes_transverse_sampling_basis(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(12, 12, 6))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 6, Y: 2, Z: 1}),
		vol.VoxelToPatient(Vec3{X: 6, Y: 9, Z: 4}),
	})
	req := CPRRequest{
		Mode: CPRTransverse, Volume: vol, Path: path,
		Window: WindowLevel{Center: 128, Width: 256},
		Width:  9, CrossSpacing: 1, ArcLength: path.Length() / 2,
	}

	zero, err := RenderCPR(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.RotationDegrees = 90
	rotated, err := RenderCPR(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if cprImagesEqual(zero, rotated) {
		t.Fatal("90-degree CPR angle produced the same transverse image")
	}
}

func cprImagesEqual(a, b image.Image) bool {
	if a == nil || b == nil || a.Bounds() != b.Bounds() {
		return false
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}
