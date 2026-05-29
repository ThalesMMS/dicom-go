package object

import (
	"bytes"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Run with:
// go test -run '^$' -bench '^(BenchmarkReadFile|BenchmarkWriteFile|BenchmarkRoundTrip)$' ./object -benchmem
//
// Use ns/op, B/op, and allocs/op as the baseline for Part 10 read, write, and
// full-cycle cost. Compare the same sub-benchmark names across commits.

func BenchmarkReadFile(b *testing.B) {
	for _, fixture := range benchmarkPart10ReadFixtures(b) {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if _, err := ReadFile(bytes.NewReader(fixture.data)); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fixture.name+"_streaming_threshold", func(b *testing.B) {
			b.Skip("streaming/threshold benchmarks are covered at the parser layer; object-level fixture needs a larger Pixel Data payload to meaningfully exercise the skip path")
		})
	}
}

func BenchmarkWriteFile(b *testing.B) {
	for _, fixture := range benchmarkPart10WriteFixtures() {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			var buf bytes.Buffer
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := WriteFile(&buf, fixture.file); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWriteFileWriterCalls(b *testing.B) {
	for _, fixture := range benchmarkPart10WriteFixtures() {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			var writer benchmarkCountingWriter
			var totalCalls int64
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				writer.calls = 0
				if err := WriteFile(&writer, fixture.file); err != nil {
					b.Fatal(err)
				}
				totalCalls += writer.calls
			}
			b.ReportMetric(float64(totalCalls)/float64(b.N), "writes/op")
		})
	}
}

type benchmarkCountingWriter struct {
	calls int64
}

func (w *benchmarkCountingWriter) Write(p []byte) (int, error) {
	w.calls++
	return len(p), nil
}

func BenchmarkRoundTrip(b *testing.B) {
	for _, fixture := range benchmarkPart10WriteFixtures() {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			var buf bytes.Buffer
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := WriteFile(&buf, fixture.file); err != nil {
					b.Fatal(err)
				}
				if _, err := ReadFile(bytes.NewReader(buf.Bytes())); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type part10ReadBenchmarkFixture struct {
	name string
	data []byte
}

type part10WriteBenchmarkFixture struct {
	name string
	file *File
}

func benchmarkPart10ReadFixtures(b *testing.B) []part10ReadBenchmarkFixture {
	b.Helper()

	sequenceData, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.BenchmarkSequenceDataSet().Elements...)
	if err != nil {
		b.Fatal(err)
	}
	pixelData, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		b.Fatal(err)
	}

	return []part10ReadBenchmarkFixture{
		{name: "minimal", data: dicomtest.MinimalFile()},
		{name: "with_pixel_data", data: pixelData},
		{name: "with_sequences", data: sequenceData},
	}
}

func benchmarkPart10WriteFixtures() []part10WriteBenchmarkFixture {
	return []part10WriteBenchmarkFixture{
		{
			name: "minimal",
			file: benchmarkFileFixture(core.DataSet{Elements: dicomtest.MinimalDataset()}, transfer.ExplicitVRLittleEndian),
		},
		{
			name: "with_pixel_data",
			file: benchmarkFileFixture(core.DataSet{Elements: dicomtest.DatasetWithPixelData()}, transfer.ExplicitVRLittleEndian),
		},
		{
			name: "with_sequences",
			file: benchmarkFileFixture(dicomtest.BenchmarkSequenceDataSet(), transfer.ExplicitVRLittleEndian),
		},
	}
}

func benchmarkFileFixture(ds core.DataSet, syntax transfer.Syntax) *File {
	return &File{
		Dataset:        FromDataSet(ds, std.Dictionary),
		TransferSyntax: syntax,
	}
}
