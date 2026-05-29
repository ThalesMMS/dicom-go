package render

import (
	"image"
	"testing"
)

var benchVRImage image.Image

func BenchmarkRenderVRRaycast(b *testing.B) {
	vol, err := BuildVolume(sphereStack(48, 16))
	if err != nil {
		b.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	cam.FitVolume(vol)
	preset := opaqueAbovePreset()
	window := WindowLevel{Center: 128, Width: 256}
	quality := DefaultVRQuality(64, 64)
	benchVRImage = RenderVR(vol, cam, preset, window, true, nil, quality) // warm lazy volume caches

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchVRImage = RenderVR(vol, cam, preset, window, true, nil, quality)
	}
}

func BenchmarkRenderVRRaycastColdCache(b *testing.B) {
	stack := sphereStack(48, 16)
	baseVol, err := BuildVolume(stack)
	if err != nil {
		b.Fatal(err)
	}
	cam := NewVRCamera(baseVol.BoundingRadiusMM())
	cam.FitVolume(baseVol)
	preset := opaqueAbovePreset()
	window := WindowLevel{Center: 128, Width: 256}
	quality := DefaultVRQuality(64, 64)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vol, err := BuildVolume(stack)
		if err != nil {
			b.Fatal(err)
		}
		benchVRImage = RenderVR(vol, cam, preset, window, true, nil, quality)
	}
}

func BenchmarkRenderVROrbitPreview(b *testing.B) {
	vol, err := BuildVolume(sphereStack(48, 16))
	if err != nil {
		b.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	cam.FitVolume(vol)
	preset := opaqueAbovePreset()
	window := WindowLevel{Center: 128, Width: 256}
	quality := PreviewVRQuality(48, 48)
	benchVRImage = RenderVR(vol, cam, preset, window, true, nil, quality) // warm lazy volume caches

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frameCam := cam
		frameCam.Rotate(float64((i%6)-3)*8, float64((i%4)-2)*3)
		benchVRImage = RenderVR(vol, frameCam, preset, window, true, nil, quality)
	}
}
