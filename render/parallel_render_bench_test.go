package render

import (
	"context"
	"image"
	"testing"
)

var (
	benchmarkParallelRenderImage image.Image
	benchmarkParallelRenderErr   error
)

func benchmarkParallelRenderVolume(b *testing.B) *Volume {
	b.Helper()
	vol, err := BuildVolume(gradientColumnStack(128, 128, 96))
	if err != nil {
		b.Fatal(err)
	}
	return vol
}

func BenchmarkParallelIssue314ResliceOblique512(b *testing.B) {
	vol := benchmarkParallelRenderVolume(b)
	plane := obliqueInteriorPlane(vol)
	window := WindowLevel{Center: 128, Width: 256}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkParallelRenderImage = ResliceOblique(vol, plane, 512, 512, window)
	}
}

func BenchmarkParallelIssue314ResliceObliqueSlab256x9(b *testing.B) {
	vol := benchmarkParallelRenderVolume(b)
	plane := obliqueInteriorPlane(vol)
	window := WindowLevel{Center: 128, Width: 256}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkParallelRenderImage = ResliceObliqueSlab(vol, plane, 256, 256, 9, SlabMIP, window)
	}
}

func BenchmarkParallelIssue314CPRStraightened(b *testing.B) {
	benchmarkParallelIssue314CPR(b, CPRStraightened, 1)
}

func BenchmarkParallelIssue314CPRStretched(b *testing.B) {
	benchmarkParallelIssue314CPR(b, CPRStretched, 1)
}

func BenchmarkParallelIssue314CPRSlab9(b *testing.B) {
	benchmarkParallelIssue314CPR(b, CPRSlab, 9)
}

func benchmarkParallelIssue314CPR(b *testing.B, mode CPRMode, thickness int) {
	b.Helper()
	vol := benchmarkParallelRenderVolume(b)
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 64, Y: 8, Z: 48}),
		vol.VoxelToPatient(Vec3{X: 64, Y: 119, Z: 48}),
	})
	req := CPRRequest{
		Mode: mode, Volume: vol, Path: path, Width: 256,
		ArcSpacing: 1, CrossSpacing: 1, Thickness: thickness,
		SlabMode: SlabMIP, Window: WindowLevel{Center: 128, Width: 256},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkParallelRenderImage, benchmarkParallelRenderErr = RenderCPR(context.Background(), req)
		if benchmarkParallelRenderErr != nil {
			b.Fatal(benchmarkParallelRenderErr)
		}
	}
}

func BenchmarkParallelIssue314CPRTransverse256(b *testing.B) {
	vol := benchmarkParallelRenderVolume(b)
	path := NewCPRPath([]Vec3{
		vol.VoxelToPatient(Vec3{X: 64, Y: 8, Z: 48}),
		vol.VoxelToPatient(Vec3{X: 64, Y: 119, Z: 48}),
	})
	req := CPRRequest{
		Mode: CPRTransverse, Volume: vol, Path: path, Width: 256,
		ArcLength: path.Length() / 2, CrossSpacing: 1,
		Window: WindowLevel{Center: 128, Width: 256},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkParallelRenderImage, benchmarkParallelRenderErr = RenderCPR(context.Background(), req)
		if benchmarkParallelRenderErr != nil {
			b.Fatal(benchmarkParallelRenderErr)
		}
	}
}
