package parser

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Run with:
// go test -run '^$' -bench '^(BenchmarkReaderNext|BenchmarkReadDataSet|BenchmarkReadDataSetWithSequences)$' ./parser -benchmem
//
// Track ns/op, B/op, and allocs/op over time. Lower numbers indicate faster
// parsing and less allocation churn.

func BenchmarkReaderNext(b *testing.B) {
	fixtures := benchmarkNativeReaderFixtures()
	streamingFixtures := benchmarkStreamingReaderFixtures()

	for i, fixture := range fixtures {
		fixture := fixture
		streaming := streamingFixtures[i]
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				reader := NewReader(bytes.NewReader(fixture.data), fixture.syntax, ReaderOptions{Dictionary: fixture.dict})
				for {
					_, err := reader.Next()
					if errorsIsEOF(err) {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})

		b.Run(fixture.name+"_streaming_threshold", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				reader := NewReader(bytes.NewReader(streaming.data), streaming.syntax, ReaderOptions{
					Dictionary:                streaming.dict,
					InlineValueBytesThreshold: benchmarkStreamingThreshold,
				})
				for {
					_, err := reader.Next()
					if errorsIsEOF(err) {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func BenchmarkReadDataSet(b *testing.B) {
	fixtures := benchmarkNativeReaderFixtures()
	streamingFixtures := benchmarkStreamingReaderFixtures()

	for i, fixture := range fixtures {
		fixture := fixture
		streaming := streamingFixtures[i]
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				reader := NewReader(bytes.NewReader(fixture.data), fixture.syntax, ReaderOptions{Dictionary: fixture.dict})
				if _, err := reader.ReadDataSet(); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fixture.name+"_streaming_threshold", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				reader := NewReader(bytes.NewReader(streaming.data), streaming.syntax, ReaderOptions{
					Dictionary:                streaming.dict,
					InlineValueBytesThreshold: benchmarkStreamingThreshold,
				})
				if _, err := reader.ReadDataSet(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReadDataSetWithSequences(b *testing.B) {
	ds := dicomtest.BenchmarkSequenceDataSet()
	dict := benchmarkSequenceDictionary()

	for _, syntax := range benchmarkNativeSyntaxes() {
		syntax := syntax
		data := dicomtest.EncodeElements(syntax, ds.Elements...)

		b.Run(syntax.Name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				reader := NewReader(bytes.NewReader(data), syntax, ReaderOptions{Dictionary: dict})
				if _, err := reader.ReadDataSet(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReadDefinedValueBytes(b *testing.B) {
	const payloadSize = 8 << 20
	payload := make([]byte, payloadSize)
	header := core.ElementHeader{
		Tag:    core.TagPixelData,
		VR:     core.VROB,
		Length: core.Length(payloadSize),
	}

	for _, tc := range []struct {
		name string
		opts ReaderOptions
	}{
		{name: "unlimited"},
		{name: "bounded", opts: ReaderOptions{MaxElementBytes: payloadSize}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(payloadSize)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				reader := NewReader(bytes.NewReader(payload), transfer.ExplicitVRLittleEndian, tc.opts)
				var err error
				benchmarkDefinedValue, err = reader.readDefinedValueBytes(header, 0)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

var benchmarkDefinedValue []byte

type readerBenchmarkFixture struct {
	name   string
	syntax transfer.Syntax
	data   []byte
	dict   dictionary.DataDictionary
}

func benchmarkNativeReaderFixtures() []readerBenchmarkFixture {
	var fixtures []readerBenchmarkFixture

	for _, syntax := range benchmarkNativeSyntaxes() {
		fixtures = append(fixtures, readerBenchmarkFixture{
			name:   syntax.Name,
			syntax: syntax,
			data:   dicomtest.EncodeElements(syntax, dicomtest.MinimalDataset()...),
			dict:   std.Dictionary,
		})
	}

	return fixtures
}

// benchmarkStreamingThreshold sits above every non-blob element in the
// streaming fixtures (the longest is a ~34-byte UID) and far below the Pixel
// Data payload, so only blob VRs take the skip/stream path. The reader
// refuses to skip/stream defined-length values for VRs outside OB/OW/OF/OD/UN,
// so a threshold below the string elements would fail the parse.
const benchmarkStreamingThreshold = 256

const benchmarkStreamingPixelBytes = 4 << 10

// benchmarkStreamingReaderFixtures mirrors benchmarkNativeReaderFixtures per
// syntax, adding a Pixel Data blob large enough to exercise the skip/stream
// path under benchmarkStreamingThreshold.
func benchmarkStreamingReaderFixtures() []readerBenchmarkFixture {
	pixels := make([]byte, benchmarkStreamingPixelBytes)
	for i := range pixels {
		pixels[i] = byte(i)
	}
	elements := append([]core.Element{}, dicomtest.MinimalDataset()...)
	elements = append(elements, dicomtest.NewOBElement(core.TagPixelData, pixels))

	var fixtures []readerBenchmarkFixture
	for _, syntax := range benchmarkNativeSyntaxes() {
		fixtures = append(fixtures, readerBenchmarkFixture{
			name:   syntax.Name,
			syntax: syntax,
			data:   dicomtest.EncodeElements(syntax, elements...),
			dict:   std.Dictionary,
		})
	}

	return fixtures
}

func benchmarkNativeSyntaxes() []transfer.Syntax {
	return []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	}
}

func benchmarkSequenceDictionary() dictionary.DataDictionary {
	return &multiCountingDictionary{
		entries: dicomtest.BenchmarkSequenceDictionaryEntries(),
	}
}

func errorsIsEOF(err error) bool {
	return errors.Is(err, io.EOF)
}
