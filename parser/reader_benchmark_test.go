package parser

import (
	"bytes"
	"errors"
	"io"
	"testing"

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

	for _, fixture := range fixtures {
		fixture := fixture
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
	}
}

func BenchmarkReadDataSet(b *testing.B) {
	fixtures := benchmarkNativeReaderFixtures()

	for _, fixture := range fixtures {
		fixture := fixture
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
