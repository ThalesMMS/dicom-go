package render

import (
	"bytes"
	"context"
	"errors"
	"image"
	"runtime"
	"testing"
)

func TestParallelObliqueAndSlabMatchSerialPixels(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("parallel equivalence requires at least two CPUs")
	}
	vol, err := BuildVolume(gradientColumnStack(64, 64, 40))
	if err != nil {
		t.Fatal(err)
	}
	plane := obliqueInteriorPlane(vol)
	window := WindowLevel{Center: 128, Width: 256}
	original := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(original)
	serialOblique := ResliceOblique(vol, plane, 128, 96, window)
	serialSlab := ResliceObliqueSlab(vol, plane, 128, 96, 7, SlabAverage, window)

	runtime.GOMAXPROCS(minIntForRenderTest(4, runtime.NumCPU()))
	parallelOblique := ResliceOblique(vol, plane, 128, 96, window)
	parallelSlab := ResliceObliqueSlab(vol, plane, 128, 96, 7, SlabAverage, window)
	assertGrayImagesEqual(t, "oblique", serialOblique, parallelOblique)
	assertGrayImagesEqual(t, "slab", serialSlab, parallelSlab)
}

func TestParallelCPRModesMatchSerialPixels(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("parallel equivalence requires at least two CPUs")
	}
	vol, err := BuildVolume(gradientXZStack(64, 64, 40))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 32, Y: 4, Z: 20}),
		vol.VoxelToPatient(Vec3{X: 32, Y: 59, Z: 20}),
	})
	base := CPRRequest{
		Volume: vol, Path: path, Width: 96, ArcSpacing: 1, CrossSpacing: 1,
		Thickness: 7, SlabMode: SlabMIP, ArcLength: path.Length() / 2,
		Window: WindowLevel{Center: 128, Width: 256},
	}
	original := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(original)
	for _, mode := range []CPRMode{CPRStraightened, CPRStretched, CPRSlab, CPRTransverse} {
		req := base
		req.Mode = mode
		serial, err := RenderCPR(context.Background(), req)
		if err != nil {
			t.Fatalf("serial mode %d: %v", mode, err)
		}
		runtime.GOMAXPROCS(minIntForRenderTest(4, runtime.NumCPU()))
		parallel, err := RenderCPR(context.Background(), req)
		if err != nil {
			t.Fatalf("parallel mode %d: %v", mode, err)
		}
		assertGrayImagesEqual(t, "CPR mode "+string(rune('0'+mode)), serial, parallel)
		runtime.GOMAXPROCS(1)
	}
}

func TestParallelCPRHonorsCanceledContext(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(64, 64, 40))
	if err != nil {
		t.Fatal(err)
	}
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 32, Y: 4, Z: 20}),
		vol.VoxelToPatient(Vec3{X: 32, Y: 59, Z: 20}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = RenderCPR(ctx, CPRRequest{Mode: CPRStraightened, Volume: vol, Path: path, Width: 128})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderCPR canceled error = %v, want context.Canceled", err)
	}
}

func assertGrayImagesEqual(t *testing.T, label string, first, second image.Image) {
	t.Helper()
	firstGray, firstOK := first.(*image.Gray)
	secondGray, secondOK := second.(*image.Gray)
	if !firstOK || !secondOK {
		t.Fatalf("%s image types = %T/%T, want *image.Gray", label, first, second)
	}
	if firstGray.Rect != secondGray.Rect || firstGray.Stride != secondGray.Stride || !bytes.Equal(firstGray.Pix, secondGray.Pix) {
		t.Fatalf("%s parallel pixels differ from serial output", label)
	}
}

func minIntForRenderTest(a, b int) int {
	if a < b {
		return a
	}
	return b
}
