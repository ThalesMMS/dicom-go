package seg

import (
	"fmt"
	"runtime"
	"testing"
)

func BenchmarkPerfSEGSourceReferenceLookup(b *testing.B) {
	for _, benchmark := range benchmarkSEGSourceReferenceCases() {
		b.Run(benchmark.name, func(b *testing.B) {
			index, err := newSourceReferenceIndex(benchmark.refs)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ReportMetric(float64(len(benchmark.refs)), "references/op")
			b.ReportMetric(float64(len(benchmark.frames)), "frames/op")
			b.ResetTimer()
			var result ReferencedImage
			for iteration := 0; iteration < b.N; iteration++ {
				for _, frame := range benchmark.frames {
					result, _ = index.lookup(frame)
				}
			}
			runtime.KeepAlive(result)
		})
	}
}

func BenchmarkPerfBuildSEGSourceReferenceIndex(b *testing.B) {
	for _, benchmark := range benchmarkSEGSourceReferenceCases() {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(len(benchmark.refs)), "references/op")
			var result *sourceReferenceIndex
			for iteration := 0; iteration < b.N; iteration++ {
				var err error
				result, err = newSourceReferenceIndex(benchmark.refs)
				if err != nil {
					b.Fatal(err)
				}
			}
			runtime.KeepAlive(result)
		})
	}
}

// BenchmarkLegacySEGSourceReferenceLookup retains the pre-index lookup for
// explicit before/after measurements. It is intentionally separate from the
// candidate benchmark name used by performance smoke runs.
func BenchmarkLegacySEGSourceReferenceLookup(b *testing.B) {
	for _, benchmark := range benchmarkSEGSourceReferenceCases() {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(len(benchmark.refs)), "references/op")
			b.ReportMetric(float64(len(benchmark.frames)), "frames/op")
			var result ReferencedImage
			for iteration := 0; iteration < b.N; iteration++ {
				for _, frame := range benchmark.frames {
					result = legacySourceReferenceForFrame(benchmark.refs, frame)
				}
			}
			runtime.KeepAlive(result)
		})
	}
}

func benchmarkSEGSourceReferenceCases() []struct {
	name   string
	refs   []ReferencedImage
	frames []Frame
} {
	singleRefs, singleFrames := benchmarkSEGSourceReferences(4_000)
	multiframeRefs, multiframeFrames := benchmarkSEGMultiframeSourceReferences(8, 512)
	return []struct {
		name   string
		refs   []ReferencedImage
		frames []Frame
	}{
		{name: "single-frame-4000", refs: singleRefs, frames: singleFrames},
		{name: "multiframe-8x512", refs: multiframeRefs, frames: multiframeFrames},
	}
}

func legacySourceReferenceForFrame(refs []ReferencedImage, frame Frame) ReferencedImage {
	for _, ref := range refs {
		if ref.SOPInstanceUID != frame.ReferencedSOPInstanceUID {
			continue
		}
		if frame.ReferencedFrameNumber == 0 || legacyContainsFrame(ref.Frames, frame.ReferencedFrameNumber) || len(ref.Frames) == 0 {
			return ref
		}
	}
	return ReferencedImage{SOPInstanceUID: frame.ReferencedSOPInstanceUID}
}

func legacyContainsFrame(frames []int, want int) bool {
	for _, frame := range frames {
		if frame == want {
			return true
		}
	}
	return false
}

func benchmarkSEGSourceReferences(count int) ([]ReferencedImage, []Frame) {
	refs := make([]ReferencedImage, count)
	frames := make([]Frame, count)
	for index := range refs {
		uid := fmt.Sprintf("1.2.826.0.1.3680043.9.7433.859.%d", index+1)
		frameNumber := index + 1
		refs[index] = ReferencedImage{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.859",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2.1",
			SOPInstanceUID:    uid,
			Frames:            []int{frameNumber},
		}
		frames[index] = Frame{ReferencedSOPInstanceUID: uid, ReferencedFrameNumber: frameNumber}
	}
	return refs, frames
}

func benchmarkSEGMultiframeSourceReferences(sopCount, framesPerSOP int) ([]ReferencedImage, []Frame) {
	refs := make([]ReferencedImage, sopCount)
	frames := make([]Frame, 0, sopCount*framesPerSOP)
	for sopIndex := range refs {
		uid := fmt.Sprintf("1.2.826.0.1.3680043.9.7433.859.50.%d", sopIndex+1)
		referencedFrames := make([]int, framesPerSOP)
		for frameIndex := range referencedFrames {
			frameNumber := frameIndex + 1
			referencedFrames[frameIndex] = frameNumber
			frames = append(frames, Frame{ReferencedSOPInstanceUID: uid, ReferencedFrameNumber: frameNumber})
		}
		refs[sopIndex] = ReferencedImage{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.859.50",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2.1",
			SOPInstanceUID:    uid,
			Frames:            referencedFrames,
		}
	}
	return refs, frames
}
