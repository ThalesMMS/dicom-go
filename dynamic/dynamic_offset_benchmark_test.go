package dynamic

import (
	"testing"
	"time"
)

var benchmarkDynamicTimeline Timeline
var benchmarkTemporalPosition int

func BenchmarkPerfBuildDynamicTimelineExplicitOffsets1000(b *testing.B) {
	frames := benchmarkExplicitOffsetFrames(1_000)
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(frames)), "frames/op")
	for iteration := 0; iteration < b.N; iteration++ {
		benchmarkDynamicTimeline = Build(frames)
	}
}

func BenchmarkPerfBuildDynamicTimelineExplicitOffsets128x32(b *testing.B) {
	frames := benchmarkVolumetricExplicitOffsetFrames(128, 32)
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(frames)), "frames/op")
	for iteration := 0; iteration < b.N; iteration++ {
		benchmarkDynamicTimeline = Build(frames)
	}
}

func BenchmarkPerfTemporalOffsetIndex1000(b *testing.B) {
	offsets, queries := benchmarkTemporalOffsetLookupFixture(1_000)
	index := newTemporalOffsetIndex(offsets)
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(offsets)), "offsets/op")
	b.ReportMetric(float64(len(queries)), "queries/op")
	for iteration := 0; iteration < b.N; iteration++ {
		for _, query := range queries {
			benchmarkTemporalPosition, _ = index.nearest(query)
		}
	}
}

// BenchmarkLegacyTemporalOffsetLookup1000 preserves the old linear resolver
// for explicit, reproducible before/after measurements.
func BenchmarkLegacyTemporalOffsetLookup1000(b *testing.B) {
	offsets, queries := benchmarkTemporalOffsetLookupFixture(1_000)
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(offsets)), "offsets/op")
	b.ReportMetric(float64(len(queries)), "queries/op")
	for iteration := 0; iteration < b.N; iteration++ {
		for _, query := range queries {
			benchmarkTemporalPosition, _ = linearNearestTemporalPosition(offsets, query)
		}
	}
}

func benchmarkTemporalOffsetLookupFixture(positionCount int) (map[int]time.Duration, []time.Duration) {
	offsets := make(map[int]time.Duration, positionCount)
	queries := make([]time.Duration, positionCount)
	for position := 1; position <= positionCount; position++ {
		offset := time.Duration(position) * 10 * time.Millisecond
		offsets[position] = offset
		queries[position-1] = offset + 500*time.Nanosecond
	}
	return offsets, queries
}

func benchmarkExplicitOffsetFrames(positionCount int) []FrameMetadata {
	frames := make([]FrameMetadata, 0, positionCount*2)
	for position := 1; position <= positionCount; position++ {
		offset := time.Duration(position) * 10 * time.Millisecond
		frames = append(frames,
			FrameMetadata{
				FrameIndex: len(frames), TemporalPosition: position, HasTemporalPosition: true,
				Offset: offset, HasOffset: true,
				SpatialPosition: float64(position), HasSpatialPosition: true,
			},
			FrameMetadata{
				FrameIndex: len(frames) + 1,
				Offset:     offset + 500*time.Nanosecond, HasOffset: true,
				SpatialPosition: float64(position), HasSpatialPosition: true,
			},
		)
	}
	return frames
}

func benchmarkVolumetricExplicitOffsetFrames(positionCount, slicesPerPosition int) []FrameMetadata {
	frames := make([]FrameMetadata, 0, positionCount*slicesPerPosition)
	for position := 1; position <= positionCount; position++ {
		offset := time.Duration(position) * 100 * time.Millisecond
		for slice := 0; slice < slicesPerPosition; slice++ {
			frame := FrameMetadata{
				FrameIndex: len(frames), Offset: offset + time.Duration(slice%3-1)*500*time.Nanosecond, HasOffset: true,
				SpatialPosition: float64(slice), HasSpatialPosition: true,
			}
			if slice == 0 {
				frame.TemporalPosition = position
				frame.HasTemporalPosition = true
				frame.Offset = offset
			}
			frames = append(frames, frame)
		}
	}
	return frames
}
