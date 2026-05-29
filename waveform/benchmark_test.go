package waveform

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"
)

func BenchmarkEnvelopeViewportBounded(b *testing.B) {
	for _, sampleCount := range []int{10_000, 1_000_000} {
		b.Run(fmt.Sprintf("samples_%d", sampleCount), func(b *testing.B) {
			group := benchmarkGroup(sampleCount)
			recording, err := Open(
				testFile(uidAmbulatoryECG, binary.LittleEndian, nil, group),
				Options{MaxIndexEntries: 65_536},
			)
			if err != nil {
				b.Fatal(err)
			}
			defer recording.Close()
			if err := recording.BuildIndex(context.Background()); err != nil {
				b.Fatal(err)
			}
			duration := durationFromFloat(float64(sampleCount)/group.frequency, time.Second)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				envelopes, err := recording.Envelope(
					context.Background(),
					0,
					nil,
					0,
					duration,
					1024,
				)
				if err != nil {
					b.Fatal(err)
				}
				if len(envelopes[0].Buckets) > 1024 {
					b.Fatal("viewport bound exceeded")
				}
			}
		})
	}
}

func BenchmarkBuildMinMaxIndex(b *testing.B) {
	group := benchmarkGroup(250_000)
	file := testFile(uidRoutineScalpEEG, binary.LittleEndian, nil, group)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recording, err := Open(file, Options{MaxIndexEntries: 65_536})
		if err != nil {
			b.Fatal(err)
		}
		if err := recording.BuildIndex(context.Background()); err != nil {
			b.Fatal(err)
		}
		if err := recording.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkGroup(sampleCount int) testGroup {
	values := make([]int64, sampleCount)
	for i := range values {
		values[i] = int64(i%257 - 128)
	}
	if sampleCount > 2 {
		values[sampleCount/2] = 32_000
	}
	return testGroup{
		frequency:      500,
		bits:           16,
		interpretation: "SS",
		samples:        int64(sampleCount),
		channels:       []testChannel{{bitsStored: 16}},
		raw:            encodeSigned(binary.LittleEndian, 16, values...),
	}
}
