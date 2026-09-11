package parser

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// These benchmarks are informational and intentionally have no pass/fail
// performance threshold. Generate comparable benchstat inputs with identical
// sub-benchmarks on each revision, for example:
//
//	go test ./parser -run '^$' -bench '^Benchmark(ReaderNext|ReadDataSet)Scalable' -benchmem -count=10 > old.txt
//	go test ./parser -run '^$' -bench '^Benchmark(ReaderNext|ReadDataSet)Scalable' -benchmem -count=10 > new.txt
//	benchstat old.txt new.txt

const scalableParserInlineThreshold = 4 << 10

var (
	scalableParserDataSet    core.DataSet
	scalableParserLastToken  Token
	scalableParserLocation   ValueLocation
	scalableParserTokenCount int
)

func BenchmarkReaderNextScalableManyElements(b *testing.B) {
	for _, elementCount := range []int{1_000, 10_000, 50_000} {
		data := scalableManyElementsFixture(elementCount)
		b.Run(fmt.Sprintf("Elements_%06d", elementCount), func(b *testing.B) {
			benchmarkScalableReaderNext(b, data, transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary}, nil)
		})
	}
}

func BenchmarkReadDataSetScalableManyElements(b *testing.B) {
	for _, elementCount := range []int{1_000, 10_000, 50_000} {
		data := scalableManyElementsFixture(elementCount)
		b.Run(fmt.Sprintf("Elements_%06d", elementCount), func(b *testing.B) {
			benchmarkScalableReadDataSet(b, data, transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary}, nil)
		})
	}
}

func BenchmarkReaderNextScalableDeepSequences(b *testing.B) {
	for _, depth := range []int{4, 16, 64} {
		data := scalableDeepSequenceFixture(depth)
		b.Run(fmt.Sprintf("Depth_%03d", depth), func(b *testing.B) {
			benchmarkScalableReaderNext(b, data, transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary}, nil)
		})
	}
}

func BenchmarkReadDataSetScalableDeepSequences(b *testing.B) {
	for _, depth := range []int{4, 16, 64} {
		data := scalableDeepSequenceFixture(depth)
		b.Run(fmt.Sprintf("Depth_%03d", depth), func(b *testing.B) {
			benchmarkScalableReadDataSet(b, data, transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary}, nil)
		})
	}
}

func BenchmarkReaderNextScalableNativePixelData(b *testing.B) {
	benchmarkScalableNativePixelData(b, benchmarkScalableReaderNext)
}

func BenchmarkReadDataSetScalableNativePixelData(b *testing.B) {
	benchmarkScalableNativePixelData(b, benchmarkScalableReadDataSet)
}

func benchmarkScalableNativePixelData(b *testing.B, run scalableParserBenchmark) {
	b.Helper()
	for _, payloadBytes := range []int{64 << 10, 1 << 20, 8 << 20} {
		data := scalableNativePixelFixture(payloadBytes)
		b.Run(fmt.Sprintf("Bytes_%08d", payloadBytes), func(b *testing.B) {
			variants := []struct {
				name        string
				options     ReaderOptions
				locationTag *core.Tag
			}{
				{name: "Materialized", options: ReaderOptions{Dictionary: std.Dictionary}},
				{
					name: "StreamingThreshold",
					options: ReaderOptions{
						Dictionary:                std.Dictionary,
						InlineValueBytesThreshold: scalableParserInlineThreshold,
					},
					locationTag: scalablePixelDataTag(),
				},
				{
					name: "Deferred",
					options: ReaderOptions{
						Dictionary:     std.Dictionary,
						DeferPixelData: true,
					},
					locationTag: scalablePixelDataTag(),
				},
			}
			for _, variant := range variants {
				variant := variant
				b.Run(variant.name, func(b *testing.B) {
					run(b, data, transfer.ExplicitVRLittleEndian, variant.options, variant.locationTag)
				})
			}
		})
	}
}

func BenchmarkReaderNextScalableEncapsulatedPixelData(b *testing.B) {
	benchmarkScalableEncapsulatedPixelData(b, benchmarkScalableReaderNext, false)
}

func BenchmarkReadDataSetScalableEncapsulatedPixelData(b *testing.B) {
	benchmarkScalableEncapsulatedPixelData(b, benchmarkScalableReadDataSet, true)
}

func benchmarkScalableEncapsulatedPixelData(b *testing.B, run scalableParserBenchmark, deferredLocation bool) {
	b.Helper()
	fixtures := []struct {
		payloadBytes int
		fragments    int
	}{
		{payloadBytes: 64 << 10, fragments: 8},
		{payloadBytes: 1 << 20, fragments: 64},
		{payloadBytes: 8 << 20, fragments: 128},
	}
	for _, fixture := range fixtures {
		data := scalableEncapsulatedPixelFixture(fixture.payloadBytes, fixture.fragments)
		name := fmt.Sprintf("Bytes_%08d_Fragments_%04d", fixture.payloadBytes, fixture.fragments)
		b.Run(name, func(b *testing.B) {
			b.Run("Materialized", func(b *testing.B) {
				run(b, data, transfer.JPEGBaseline, ReaderOptions{Dictionary: std.Dictionary}, nil)
			})
			b.Run("Deferred", func(b *testing.B) {
				var locationTag *core.Tag
				if deferredLocation {
					locationTag = scalablePixelDataTag()
				}
				run(b, data, transfer.JPEGBaseline, ReaderOptions{Dictionary: std.Dictionary, DeferPixelData: true}, locationTag)
			})
		})
	}
}

type scalableParserBenchmark func(*testing.B, []byte, transfer.Syntax, ReaderOptions, *core.Tag)

func benchmarkScalableReaderNext(b *testing.B, data []byte, syntax transfer.Syntax, options ReaderOptions, locationTag *core.Tag) {
	b.Helper()
	validateScalableReaderNext(b, data, syntax, options, locationTag)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		reader := NewReader(bytes.NewReader(data), syntax, options)
		tokenCount := 0
		for {
			token, err := reader.Next()
			if errorsIsEOF(err) {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			scalableParserLastToken = token
			tokenCount++
		}
		if locationTag != nil {
			location, ok := reader.ValueLocation(*locationTag)
			if !ok {
				b.Fatalf("ValueLocation(%s) missing", *locationTag)
			}
			scalableParserLocation = location
		}
		scalableParserTokenCount = tokenCount
	}
}

func benchmarkScalableReadDataSet(b *testing.B, data []byte, syntax transfer.Syntax, options ReaderOptions, locationTag *core.Tag) {
	b.Helper()
	validateScalableReadDataSet(b, data, syntax, options, locationTag)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		reader := NewReader(bytes.NewReader(data), syntax, options)
		dataset, err := reader.ReadDataSet()
		if err != nil {
			b.Fatal(err)
		}
		if locationTag != nil {
			location, ok := reader.ValueLocation(*locationTag)
			if !ok {
				b.Fatalf("ValueLocation(%s) missing", *locationTag)
			}
			scalableParserLocation = location
		}
		scalableParserDataSet = dataset
	}
}

func validateScalableReaderNext(b *testing.B, data []byte, syntax transfer.Syntax, options ReaderOptions, locationTag *core.Tag) {
	b.Helper()
	reader := NewReader(bytes.NewReader(data), syntax, options)
	tokens := 0
	inPixelSequence := false
	sawDeferredPixelValue := false
	for {
		token, err := reader.Next()
		if errorsIsEOF(err) {
			break
		}
		if err != nil {
			b.Fatal(err)
		}
		tokens++
		switch token.Kind {
		case TokenStartPixelSequence:
			inPixelSequence = true
		case TokenEndSequence:
			inPixelSequence = false
		case TokenElement:
			if token.Element.Value == nil && (token.Header.Tag == core.TagPixelData || inPixelSequence) {
				sawDeferredPixelValue = true
			}
		}
	}
	if tokens == 0 {
		b.Fatal("synthetic fixture produced no tokens")
	}
	if options.DeferPixelData && !sawDeferredPixelValue {
		b.Fatal("DeferPixelData fixture did not exercise a deferred Pixel Data value")
	}
	validateScalableLocation(b, reader, locationTag)
}

func validateScalableReadDataSet(b *testing.B, data []byte, syntax transfer.Syntax, options ReaderOptions, locationTag *core.Tag) {
	b.Helper()
	reader := NewReader(bytes.NewReader(data), syntax, options)
	dataset, err := reader.ReadDataSet()
	if err != nil {
		b.Fatal(err)
	}
	if len(dataset.Elements) == 0 {
		b.Fatal("synthetic fixture produced an empty data set")
	}
	if options.DeferPixelData || options.InlineValueBytesThreshold > 0 {
		pixel, ok := scalableElement(dataset, core.TagPixelData)
		if !ok || pixel.Value != nil {
			b.Fatalf("skip/defer fixture Pixel Data = (%#v, %t), want nil value", pixel.Value, ok)
		}
	}
	validateScalableLocation(b, reader, locationTag)
}

func scalableElement(dataset core.DataSet, tag core.Tag) (core.Element, bool) {
	for _, element := range dataset.Elements {
		if element.Tag() == tag {
			return element, true
		}
	}
	return core.Element{}, false
}

func validateScalableLocation(b *testing.B, reader *Reader, locationTag *core.Tag) {
	b.Helper()
	if locationTag == nil {
		return
	}
	location, ok := reader.ValueLocation(*locationTag)
	if !ok || location.Length <= 0 {
		b.Fatalf("ValueLocation(%s) = (%+v, %t)", *locationTag, location, ok)
	}
}

func scalableManyElementsFixture(elementCount int) []byte {
	elements := make([]core.Element, elementCount)
	value := []byte("0123456789ABCDEF")
	for index := range elements {
		elements[index] = core.NewRawElement(
			core.NewTag(0x7777, uint16(index+1)),
			core.VROB,
			value,
		)
	}
	return dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, elements...)
}

func scalableDeepSequenceFixture(depth int) []byte {
	elements := []core.Element{
		core.NewRawElement(core.NewTag(0x7779, 0x0001), core.VROB, []byte("LEAF")),
	}
	for level := depth; level > 0; level-- {
		elements = []core.Element{
			dicomtest.NewSequenceElement(
				core.NewTag(0x7779, uint16(0x1000+level)),
				core.DataSet{Elements: elements},
			),
		}
	}
	return dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, elements...)
}

func scalableNativePixelFixture(payloadBytes int) []byte {
	payload := scalablePayload(payloadBytes)
	element := core.NewRawElement(core.TagPixelData, core.VROW, payload)
	return dicomtest.EncodeElement(element, transfer.ExplicitVRLittleEndian)
}

func scalableEncapsulatedPixelFixture(payloadBytes, fragmentCount int) []byte {
	// The payload models encapsulation and fragment framing only. It is not a
	// JPEG codestream, and these parser benchmarks never invoke a codec.
	fragmentBytes := payloadBytes / fragmentCount
	fragments := make([][]byte, fragmentCount)
	for index := range fragments {
		fragments[index] = scalablePayload(fragmentBytes)
		fragments[index][0] = byte(index)
	}
	element := dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...)
	return dicomtest.EncodeElement(element, transfer.JPEGBaseline)
}

func scalablePayload(size int) []byte {
	payload := make([]byte, size)
	for offset := 0; offset < len(payload); offset += 4 << 10 {
		payload[offset] = byte(offset >> 12)
	}
	return payload
}

func scalablePixelDataTag() *core.Tag {
	tag := core.TagPixelData
	return &tag
}
