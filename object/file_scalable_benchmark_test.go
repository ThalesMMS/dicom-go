package object

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const scalablePart10InlineThreshold = 32 << 10

type scalablePart10Fixture struct {
	name         string
	file         *File
	data         []byte
	pixelKind    string
	payloadBytes int
}

func BenchmarkScalablePart10Read(b *testing.B) {
	for _, fixture := range scalablePart10Fixtures(b) {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				file, err := ReadFile(bytes.NewReader(fixture.data))
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(file)
			}
		})
	}
}

func BenchmarkScalablePart10Write(b *testing.B) {
	for _, fixture := range scalablePart10Fixtures(b) {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			var output bytes.Buffer
			output.Grow(len(fixture.data))
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				output.Reset()
				if err := WriteFile(&output, fixture.file); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkScalablePart10RoundTrip(b *testing.B) {
	for _, fixture := range scalablePart10Fixtures(b) {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			var output bytes.Buffer
			output.Grow(len(fixture.data))
			b.ReportAllocs()
			b.SetBytes(2 * int64(len(fixture.data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				output.Reset()
				if err := WriteFile(&output, fixture.file); err != nil {
					b.Fatal(err)
				}
				file, err := ReadFile(bytes.NewReader(output.Bytes()))
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(file)
			}
		})
	}
}

func BenchmarkScalablePart10DeferredRead(b *testing.B) {
	for _, fixture := range scalablePart10Fixtures(b) {
		if fixture.pixelKind == "" {
			continue
		}
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "fixture.dcm")
			if err := os.WriteFile(path, fixture.data, 0o600); err != nil {
				b.Fatal(err)
			}
			modes := []struct {
				name string
				opts ReadFileOptions
				copy bool
			}{
				{name: "DeferPixelData", opts: ReadFileOptions{DeferPixelData: true}},
				{name: "DeferAndCopy", opts: ReadFileOptions{DeferPixelData: true}, copy: true},
			}
			if fixture.pixelKind == "NativePixel" {
				modes = append(modes, struct {
					name string
					opts ReadFileOptions
					copy bool
				}{name: "InlineThreshold", opts: ReadFileOptions{InlineValueBytesThreshold: scalablePart10InlineThreshold}})
			}
			for _, mode := range modes {
				mode := mode
				b.Run(mode.name, func(b *testing.B) {
					verifyScalableDeferredRead(b, path, mode.opts, mode.copy)
					b.ReportAllocs()
					b.SetBytes(int64(len(fixture.data)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						file, err := OpenFileWithOptions(path, mode.opts)
						if err != nil {
							b.Fatal(err)
						}
						if mode.copy {
							if _, err := file.Dataset.CopyValueTo(core.TagPixelData, io.Discard); err != nil {
								_ = file.Close()
								b.Fatal(err)
							}
						}
						if err := file.Close(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

func verifyScalableDeferredRead(b *testing.B, path string, opts ReadFileOptions, copyValue bool) {
	b.Helper()
	file, err := OpenFileWithOptions(path, opts)
	if err != nil {
		b.Fatal(err)
	}
	if len(file.ValueLocations(core.TagPixelData)) == 0 {
		_ = file.Close()
		b.Fatal("deferred Pixel Data location was not retained")
	}
	if copyValue {
		if _, err := file.Dataset.CopyValueTo(core.TagPixelData, io.Discard); err != nil {
			_ = file.Close()
			b.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}
}

func scalablePart10Fixtures(tb testing.TB) []scalablePart10Fixture {
	tb.Helper()
	var fixtures []scalablePart10Fixture
	for _, count := range []int{128, 1024, 8192} {
		fixtures = append(fixtures, newScalablePart10Fixture(tb,
			fmt.Sprintf("ManyElements/Elements_%06d", count),
			benchmarkFileFixture(scalableManyElementDataSet(count), transfer.ExplicitVRLittleEndian), "", 0))
	}
	for _, depth := range []int{4, 16, 64} {
		fixtures = append(fixtures, newScalablePart10Fixture(tb,
			fmt.Sprintf("DeepSequence/Depth_%03d", depth),
			benchmarkFileFixture(scalableDeepSequenceDataSet(depth), transfer.ExplicitVRLittleEndian), "", 0))
	}
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		fixtures = append(fixtures, newScalablePart10Fixture(tb,
			fmt.Sprintf("NativePixel/Bytes_%04dKiB", size>>10),
			benchmarkFileFixture(scalableNativePixelDataSet(size), transfer.ExplicitVRLittleEndian), "NativePixel", size))
		fixtures = append(fixtures, newScalablePart10Fixture(tb,
			fmt.Sprintf("EncapsulatedPixel/Bytes_%04dKiB", size>>10),
			benchmarkFileFixture(scalableEncapsulatedPixelDataSet(size), transfer.RLELossless), "EncapsulatedPixel", size))
	}
	return fixtures
}

func newScalablePart10Fixture(tb testing.TB, name string, file *File, pixelKind string, payloadBytes int) scalablePart10Fixture {
	tb.Helper()
	var encoded bytes.Buffer
	if err := WriteFile(&encoded, file); err != nil {
		tb.Fatalf("build %s fixture: %v", name, err)
	}
	fixture := scalablePart10Fixture{name: name, file: file, data: bytes.Clone(encoded.Bytes()), pixelKind: pixelKind, payloadBytes: payloadBytes}
	verifyScalablePart10Fixture(tb, fixture)
	return fixture
}

func verifyScalablePart10Fixture(tb testing.TB, fixture scalablePart10Fixture) {
	tb.Helper()
	file, err := ReadFile(bytes.NewReader(fixture.data))
	if err != nil {
		tb.Fatalf("read generated %s fixture: %v", fixture.name, err)
	}
	if fixture.pixelKind == "" {
		return
	}
	element, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		tb.Fatalf("generated %s fixture has no Pixel Data", fixture.name)
	}
	switch fixture.pixelKind {
	case "NativePixel":
		raw, ok := element.RawBytes()
		if !ok || len(raw) != fixture.payloadBytes {
			tb.Fatalf("generated %s native Pixel Data bytes=%d ok=%t, want %d", fixture.name, len(raw), ok, fixture.payloadBytes)
		}
	case "EncapsulatedPixel":
		fragments, ok := element.Value.(core.FragmentSequence)
		if !ok || len(fragments.Fragments) == 0 {
			tb.Fatalf("generated %s encapsulated Pixel Data=%T", fixture.name, element.Value)
		}
		total := 0
		for _, fragment := range fragments.Fragments {
			total += len(fragment)
		}
		if total != fixture.payloadBytes {
			tb.Fatalf("generated %s fragment bytes=%d, want %d", fixture.name, total, fixture.payloadBytes)
		}
	default:
		tb.Fatalf("unknown scalable Pixel Data kind %q", fixture.pixelKind)
	}
}

func scalableManyElementDataSet(count int) core.DataSet {
	elements := append([]core.Element(nil), dicomtest.MinimalDataset()...)
	for index := 0; index < count; index++ {
		tag := core.NewTag(0x1011, uint16(0x1000+index))
		value := fmt.Sprintf("SYNTHETIC-%08d", index)
		elements = append(elements, core.NewRawElement(tag, core.VRLO, []byte(value)))
	}
	return core.DataSet{Elements: elements}
}

func scalableDeepSequenceDataSet(depth int) core.DataSet {
	leaf := core.DataSet{Elements: []core.Element{
		dicomtest.NewUIElement(core.NewTag(0x0008, 0x1150), dicomtest.TestSOPClassUID),
		dicomtest.NewUIElement(core.NewTag(0x0008, 0x1155), dicomtest.TestSOPInstanceUID),
	}}
	for level := depth - 1; level >= 0; level-- {
		sequence := core.Element{
			Header: core.ElementHeader{Tag: core.NewTag(0x3011, uint16(0x1000+level)), VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{leaf}},
		}
		leaf = core.DataSet{Elements: []core.Element{sequence}}
	}
	elements := append([]core.Element(nil), dicomtest.MinimalDataset()...)
	elements = append(elements, leaf.Elements...)
	return core.DataSet{Elements: elements}
}

func scalableNativePixelDataSet(size int) core.DataSet {
	elements := scalablePixelMetadata(size)
	elements = append(elements, core.NewRawElement(core.TagPixelData, core.VROW, scalablePayload(size)))
	return core.DataSet{Elements: elements}
}

func scalableEncapsulatedPixelDataSet(size int) core.DataSet {
	elements := scalablePixelMetadata(size)
	// These synthetic fragments measure Part 10 encapsulation, fragment and
	// deferred-I/O costs. They are deliberately not codec conformance fixtures;
	// no RLE decoder runs in this benchmark.
	const fragmentBytes = 64 << 10
	fragments := make([][]byte, 0, (size+fragmentBytes-1)/fragmentBytes)
	for offset := 0; offset < size; offset += fragmentBytes {
		length := fragmentBytes
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		fragments = append(fragments, scalablePayloadAt(length, offset))
	}
	elements = append(elements, dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...))
	return core.DataSet{Elements: elements}
}

func scalablePixelMetadata(payloadBytes int) []core.Element {
	elements := append([]core.Element(nil), dicomtest.MinimalDataset()...)
	return append(elements,
		dicomtest.NewUIElement(core.NewTag(0x0020, 0x000E), dicomtest.TestSeriesInstanceUID),
		dicomtest.NewStringElement(core.NewTag(0x0008, 0x0060), core.VRCS, "OT"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0002), 1),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "MONOCHROME2"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 512),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), uint16(payloadBytes/(512*2))),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0100), 16),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0101), 16),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0102), 15),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0103), 0),
	)
}

func scalablePayload(size int) []byte {
	return scalablePayloadAt(size, 0)
}

func scalablePayloadAt(size, offset int) []byte {
	payload := make([]byte, size)
	for index := range payload {
		payload[index] = byte((offset + index*31) & 0xFF)
	}
	return payload
}
