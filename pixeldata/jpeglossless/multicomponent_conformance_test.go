package jpeglossless

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// These two streams were generated independently with libjpeg-turbo 3.1.0
// from the same 4x3 RGB PPM. The reference samples are the exact output of
// `djpeg -strict -rgb -pnm`, not output produced by this package.
//
// Interleaved stream:
//
//	cjpeg -precision 8 -lossless 1,0 -rgb -sample 1x1,1x1,1x1 input.ppm
//	SHA-256 c9643dc57a6e631865084223af411c514d52edc30caaa3fe99eab5c9561d7e68
//
// Separate-component stream (scan predictors 1, 2, and 7):
//
//	cjpeg -precision 8 -rgb -sample 1x1,1x1,1x1 -scans scans.txt input.ppm
//	SHA-256 757c5ba6c9a2628e56f934e316125c34eb6ff4f86c7bff369a678acb6315892d
//
// scans.txt contains:
//
//	0: 1 0 0 0;
//	1: 2 0 0 0;
//	2: 7 0 0 0;
const (
	conformanceInterleavedBase64 = "/9j/7gAOQWRvYmUAZAAAAAAA/8MAEQgAAwAEA1IRAEcRAEIRAP/EABsAAQACAwEBAAAAAAAAAAAAAAgABwIFBgED/9oADANSAEcAQgABAAA/n8/v+QAP+QAP/wByz2G/752txd0CQhH9HxfVvGnT3LUt79u9ej//2Q=="
	conformanceSeparateBase64    = "/9j/7gAOQWRvYmUAZAAAAAAA/8MAEQgAAwAEA1IRAEcRAEIRAP/EABoAAAICAwAAAAAAAAAAAAAAAAcIAAEDBQb/2gAIAVIAAQAAX9/0Al7B7QDkLxl7f//EABgAAQADAQAAAAAAAAAAAAAAAAgAAgcG/9oACAFHAAIAAD/H+ALdDXQHxoYMX/8A/8QAGQABAAIDAAAAAAAAAAAAAAAACAAHAgMG/9oACAFCAAcAAD/I/wDOwG4Ir63dYgf/2Q=="
)

var conformanceRGBReference = []byte{
	0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255,
	1, 2, 3, 17, 33, 65, 254, 253, 252, 128, 64, 32,
	5, 250, 125, 99, 100, 101, 200, 10, 220, 255, 255, 255,
}

var conformanceTagPlanarConfiguration = core.NewTag(0x0028, 0x0006)

func TestJPEGLosslessMulticomponentIndependentFixtures(t *testing.T) {
	tests := []struct {
		name string
		uid  string
		data string
	}{
		{name: "interleaved selection value 1", uid: UIDProcess14SV1, data: conformanceInterleavedBase64},
		{name: "separate scans predictors 1 2 7", uid: UIDProcess14, data: conformanceSeparateBase64},
	}
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := conformanceDecodeBase64(t, test.data)
			frames, err := registry.DecodeFrames(test.uid, encapsulated(stream), conformanceColorObject(3, 4, "RGB", 0, 0))
			if err != nil {
				t.Fatalf("DecodeFrames() independent fixture: %v", err)
			}
			if frames.Rows != 3 || frames.Columns != 4 || len(frames.Data) != 1 {
				t.Fatalf("decoded geometry = %dx%d/%d frames, want 4x3/1", frames.Columns, frames.Rows, len(frames.Data))
			}
			if !bytes.Equal(frames.Data[0], conformanceRGBReference) {
				t.Fatalf("decoded bytes = % x, want independent djpeg bytes % x", frames.Data[0], conformanceRGBReference)
			}
		})
	}
}

func TestJPEGLosslessMulticomponentRejectsInvalidDICOMMetadata(t *testing.T) {
	stream := conformanceDecodeBase64(t, conformanceInterleavedBase64)
	tests := []struct {
		name        string
		samples     uint16
		photometric string
		planar      uint16
		pixelRep    uint16
		bitsStored  uint16
	}{
		{name: "planar color", samples: 3, photometric: "RGB", planar: 1, bitsStored: 8},
		{name: "signed color", samples: 3, photometric: "RGB", pixelRep: 1, bitsStored: 8},
		{name: "subsampled photometric", samples: 3, photometric: "YBR_FULL_422", bitsStored: 8},
		{name: "metadata component mismatch", samples: 1, photometric: "MONOCHROME2", bitsStored: 8},
		{name: "metadata precision mismatch", samples: 3, photometric: "RGB", bitsStored: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			obj := conformanceObject(3, 4, test.samples, test.bitsStored, test.photometric, test.planar, test.pixelRep)
			if _, err := New().Decode(encapsulated(stream), obj); err == nil {
				t.Fatal("Decode() error = nil")
			}
		})
	}
}

func TestJPEGLosslessConformanceSignedMonochromeBitPatterns(t *testing.T) {
	// JPEG lossless codes unsigned sample values. Pixel Representation gives
	// those same stored bits their signed DICOM interpretation; decoding must
	// not bias, clamp, or otherwise transform the native little-endian words.
	samples := []int32{0x8000, 0x7fff, 0x0000, 0x7fff, 0xfffe, 0xffff}
	stream := encodeLossless(6, 1, 16, 1, samples)
	obj := object.FromElements([]core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, nil, 1),
		dicomtest.Uint16Element(tagColumns, core.VRUS, nil, 6),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, 1),
		dicomtest.NewStringElement(tagPhotometric, core.VRCS, "MONOCHROME2"),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, "1"),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, nil, 16),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, nil, 16),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, 15),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, nil, 1),
	}, nil)
	frames, err := newSV1().Decode(encapsulated(stream), obj)
	if err != nil {
		t.Fatalf("Decode() signed monochrome bit patterns: %v", err)
	}
	want := []byte{0x00, 0x80, 0xff, 0x7f, 0x00, 0x00, 0xff, 0x7f, 0xfe, 0xff, 0xff, 0xff}
	if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], want) {
		t.Fatalf("decoded signed stored bits = % x, want % x", frames.Data, want)
	}
}

func TestJPEGLosslessMulticomponentRejectsMalformedStructure(t *testing.T) {
	interleaved := conformanceDecodeBase64(t, conformanceInterleavedBase64)
	separate := conformanceDecodeBase64(t, conformanceSeparateBase64)
	tests := []struct {
		name   string
		base   []byte
		mutate func(*testing.T, []byte) []byte
	}{
		{
			name: "component sampling is not one by one",
			base: interleaved,
			mutate: func(t *testing.T, data []byte) []byte {
				sof := conformanceSegmentPayload(t, data, markerSOF3, 0)
				data[sof+7] = 0x21
				return data
			},
		},
		{
			name: "duplicate SOF component identifier",
			base: interleaved,
			mutate: func(t *testing.T, data []byte) []byte {
				sof := conformanceSegmentPayload(t, data, markerSOF3, 0)
				data[sof+9] = data[sof+6]
				return data
			},
		},
		{
			name: "duplicate separate scan leaves component missing",
			base: separate,
			mutate: func(t *testing.T, data []byte) []byte {
				thirdSOS := conformanceSegmentPayload(t, data, markerSOS, 2)
				data[thirdSOS+1] = 1
				return data
			},
		},
		{
			name: "AC Huffman table in lossless stream",
			base: interleaved,
			mutate: func(t *testing.T, data []byte) []byte {
				dht := conformanceSegmentPayload(t, data, markerDHT, 0)
				data[dht] |= 0x10
				return data
			},
		},
		{
			name: "out of range difference category",
			base: interleaved,
			mutate: func(t *testing.T, data []byte) []byte {
				dht := conformanceSegmentPayload(t, data, markerDHT, 0)
				data[dht+17] = 17
				return data
			},
		},
		{
			name: "oversubscribed canonical table",
			base: interleaved,
			mutate: func(t *testing.T, data []byte) []byte {
				dht := conformanceSegmentPayload(t, data, markerDHT, 0)
				data[dht+1] = 3
				return data
			},
		},
		{
			name: "complete all ones table",
			base: interleaved,
			mutate: func(t *testing.T, data []byte) []byte {
				dht := conformanceSegmentPayload(t, data, markerDHT, 0)
				data[dht+1] = 2
				for i := 2; i <= 16; i++ {
					data[dht+i] = 0
				}
				return data
			},
		},
		{
			name: "missing EOI",
			base: interleaved,
			mutate: func(t *testing.T, data []byte) []byte {
				if !bytes.HasSuffix(data, []byte{0xff, markerEOI}) {
					t.Fatal("fixture has no terminal EOI")
				}
				return data[:len(data)-2]
			},
		},
		{
			name: "more than one trailing padding byte",
			base: interleaved,
			mutate: func(_ *testing.T, data []byte) []byte {
				return append(data, 0, 0)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := append([]byte(nil), test.base...)
			stream = test.mutate(t, stream)
			if _, err := New().Decode(encapsulated(stream), conformanceColorObject(3, 4, "RGB", 0, 0)); err == nil {
				t.Fatal("Decode() malformed stream error = nil")
			}
		})
	}
}

func TestJPEGLosslessMulticomponentAllowsSingleDICOMPaddingByte(t *testing.T) {
	stream := append(conformanceDecodeBase64(t, conformanceInterleavedBase64), 0xa2)
	frames, err := newSV1().Decode(encapsulated(stream), conformanceColorObject(3, 4, "RGB", 0, 0))
	if err != nil {
		t.Fatalf("Decode() one DICOM Item padding byte: %v", err)
	}
	if !bytes.Equal(frames.Data[0], conformanceRGBReference) {
		t.Fatalf("decoded padded frame differs from independent reference")
	}
}

func TestJPEGLosslessMulticomponentRejectsDimensionsBeforeAllocation(t *testing.T) {
	stream := conformanceDecodeBase64(t, conformanceInterleavedBase64)
	sof := conformanceSegmentPayload(t, stream, markerSOF3, 0)
	// SOF payload is P, Y, X, Nf. A hostile stream must not be allowed to use
	// its own dimensions to allocate component planes before it is reconciled
	// with the bounded DICOM metadata.
	stream[sof+1], stream[sof+2] = 0xff, 0xff
	stream[sof+3], stream[sof+4] = 0xff, 0xff
	if _, err := New().Decode(encapsulated(stream), conformanceColorObject(3, 4, "RGB", 0, 0)); err == nil {
		t.Fatal("Decode() hostile SOF dimensions error = nil")
	}

	// Conversely, oversized metadata is rejected by the finite request budget
	// before any codestream parsing or decoded-frame allocation.
	if _, err := New().Decode(
		encapsulated(conformanceDecodeBase64(t, conformanceInterleavedBase64)),
		conformanceColorObject(0xffff, 0xffff, "RGB", 0, 0),
	); err == nil {
		t.Fatal("Decode() oversized metadata error = nil")
	}
}

func TestJPEGLosslessMulticomponentConcurrentDecode(t *testing.T) {
	codec := New()
	obj := conformanceColorObject(3, 4, "RGB", 0, 0)
	streams := [][]byte{
		conformanceDecodeBase64(t, conformanceInterleavedBase64),
		conformanceDecodeBase64(t, conformanceSeparateBase64),
	}
	const workers = 24
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(stream []byte) {
			defer group.Done()
			frames, err := codec.Decode(encapsulated(stream), obj)
			if err != nil {
				errors <- err
				return
			}
			if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], conformanceRGBReference) {
				errors <- fmt.Errorf("decoded output differs from independent reference")
			}
		}(streams[worker%len(streams)])
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

// TestJPEGLosslessMulticomponentGDCMDataInterop compares a separately authored
// compressed object with its separately stored native twin. The fixtures are
// deliberately not vendored: GDCMData has no clear repository-level fixture
// license. Set DICOMGO_GDCMDATA_ROOT to an independently obtained checkout at
// commit 9b38ac7a8baee4e943505ab3c56fb7d19d54754f to run this gate.
func TestJPEGLosslessMulticomponentGDCMDataInterop(t *testing.T) {
	root := os.Getenv("DICOMGO_GDCMDATA_ROOT")
	if root == "" {
		t.Skip("set DICOMGO_GDCMDATA_ROOT to a separately obtained GDCMData checkout")
	}
	compressed := conformanceReadPinnedFixture(
		t,
		filepath.Join(root, "LEADTOOLS_FLOWERS-24-RGB-JpegLossless.dcm"),
		"2f8b3822faa88dd60c2ba702514ca7ff23a11574be1286cdabbc9e292671becb",
	)
	native := conformanceReadPinnedFixture(
		t,
		filepath.Join(root, "LEADTOOLS_FLOWERS-24-RGB-Uncompressed.dcm"),
		"c89dda036deffbf4af478b465605f9e54ab03406a6831ae5b06306992fc67f8e",
	)
	compressedFile, err := object.ReadFile(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	if compressedFile.TransferSyntax.UID != UIDProcess14SV1 {
		t.Fatalf("compressed Transfer Syntax = %s, want %s", compressedFile.TransferSyntax.UID, UIDProcess14SV1)
	}
	pixel, err := pixeldata.Extract(compressedFile.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := newSV1().Decode(pixel, compressedFile.Dataset)
	if err != nil {
		t.Fatalf("Decode() independent GDCMData fixture: %v", err)
	}
	nativeFile, err := object.ReadFile(bytes.NewReader(native))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := pixeldata.ExtractNativeFrames(nativeFile.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Rows != int(reference.Metadata.Rows) || decoded.Columns != int(reference.Metadata.Columns) {
		t.Fatalf("decoded geometry = %dx%d, native = %dx%d", decoded.Columns, decoded.Rows, reference.Metadata.Columns, reference.Metadata.Rows)
	}
	if len(decoded.Data) != len(reference.Data) {
		t.Fatalf("decoded frames = %d, native = %d", len(decoded.Data), len(reference.Data))
	}
	for index := range decoded.Data {
		if !bytes.Equal(decoded.Data[index], reference.Data[index]) {
			t.Fatalf("decoded frame %d differs from independent native twin", index)
		}
	}
}

func conformanceReadPinnedFixture(t testing.TB, path, wantSHA256 string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != wantSHA256 {
		t.Fatalf("fixture %s SHA-256 = %s, want %s", filepath.Base(path), got, wantSHA256)
	}
	return data
}

func FuzzJPEGLosslessMulticomponentMalformed(f *testing.F) {
	f.Add(conformanceMustDecodeBase64(conformanceInterleavedBase64))
	f.Add(conformanceMustDecodeBase64(conformanceSeparateBase64))
	obj := conformanceColorObject(3, 4, "RGB", 0, 0)
	codec := New()
	f.Fuzz(func(t *testing.T, stream []byte) {
		if len(stream) > 1<<20 {
			t.Skip()
		}
		_, _ = codec.Decode(encapsulated(stream), obj)
	})
}

func conformanceColorObject(rows, columns uint16, photometric string, planar, pixelRepresentation uint16) *object.Object {
	return conformanceObject(rows, columns, 3, 8, photometric, planar, pixelRepresentation)
}

func conformanceObject(rows, columns, samples, bitsStored uint16, photometric string, planar, pixelRepresentation uint16) *object.Object {
	elements := []core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, nil, rows),
		dicomtest.Uint16Element(tagColumns, core.VRUS, nil, columns),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, samples),
		dicomtest.NewStringElement(tagPhotometric, core.VRCS, photometric),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, "1"),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, nil, 8),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, nil, bitsStored),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, bitsStored-1),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, nil, pixelRepresentation),
	}
	if samples > 1 {
		elements = append(elements, dicomtest.Uint16Element(conformanceTagPlanarConfiguration, core.VRUS, nil, planar))
	}
	return object.FromElements(elements, nil)
}

func conformanceDecodeBase64(t testing.TB, value string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func conformanceMustDecodeBase64(value string) []byte {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return data
}

// conformanceSegmentPayload returns the first byte after a marker segment's
// two-byte length field. Marker bytes in entropy data are always stuffed, so
// searching for a non-stuffed marker is unambiguous for these test fixtures.
func conformanceSegmentPayload(t testing.TB, data []byte, marker byte, occurrence int) int {
	t.Helper()
	needle := []byte{0xff, marker}
	position := 0
	for found := 0; ; found++ {
		index := bytes.Index(data[position:], needle)
		if index < 0 {
			t.Fatalf("marker ff%02x occurrence %d not found", marker, occurrence)
		}
		index += position
		if found == occurrence {
			if index+4 > len(data) {
				t.Fatalf("marker ff%02x has no complete length", marker)
			}
			return index + 4
		}
		position = index + 2
	}
}
