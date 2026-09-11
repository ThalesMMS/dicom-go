package jpeglossless

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

var tagPlanarConfiguration829 = core.NewTag(0x0028, 0x0006)

type losslessScan829 struct {
	components     []int
	predictor      int
	pointTransform int
	restart        int
}

func TestJPEGLosslessMulticomponentInterleavedScan(t *testing.T) {
	planes := [][]int32{
		{0, 40, 80, 120, 160, 200},
		{10, 50, 90, 130, 170, 210},
		{20, 60, 100, 140, 180, 220},
	}
	stream := encodeLosslessMulticomponent829(3, 2, 8, planes, []losslessScan829{{
		components: []int{1, 2, 0}, predictor: 6, restart: 2,
	}})
	obj := losslessColorObject829(2, 3, 8, 8, "RGB", 0)
	frames, err := New().Decode(encapsulated(stream), obj)
	if err != nil {
		t.Fatal(err)
	}
	want := interleavePlanes829(planes, 1)
	if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], want) {
		t.Fatalf("decoded interleaved frame = %v, want %v", frames.Data, want)
	}
}

func TestJPEGLosslessMulticomponentSeparateScansMapIDsAndTransforms(t *testing.T) {
	planes := [][]int32{
		{4, 8, 12, 16, 20, 24},
		{10, 20, 30, 40, 50, 60},
		{1, 2, 3, 4, 5, 6},
	}
	stream := encodeLosslessMulticomponent829(3, 2, 8, planes, []losslessScan829{
		{components: []int{2}, predictor: 4, pointTransform: 0, restart: 2},
		{components: []int{0}, predictor: 1, pointTransform: 2, restart: 2},
		{components: []int{1}, predictor: 7, pointTransform: 1, restart: 2},
	})
	obj := losslessColorObject829(2, 3, 8, 8, "YBR_FULL", 0)
	frames, err := New().Decode(encapsulated(stream), obj)
	if err != nil {
		t.Fatal(err)
	}
	want := interleavePlanes829(planes, 1)
	if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], want) {
		t.Fatalf("decoded component-separated frame = %v, want %v", frames.Data, want)
	}
}

func TestJPEGLosslessMulticomponentTwelveBitOutputIsLittleEndian(t *testing.T) {
	planes := [][]int32{
		{0, 1024, 2048, 4095},
		{17, 513, 1537, 3073},
		{255, 511, 1023, 2047},
	}
	stream := encodeLosslessMulticomponent829(2, 2, 12, planes, []losslessScan829{{
		components: []int{2, 0, 1}, predictor: 5,
	}})
	obj := losslessColorObject829(2, 2, 16, 12, "RGB", 0)
	frames, err := New().Decode(encapsulated(stream), obj)
	if err != nil {
		t.Fatal(err)
	}
	want := interleavePlanes829(planes, 2)
	if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], want) {
		t.Fatalf("decoded 12-bit frame = % x, want % x", frames.Data, want)
	}
}

func TestJPEGLosslessMulticomponentSupportsPredictorsOneThroughSeven(t *testing.T) {
	planes := [][]int32{
		{3, 17, 29, 41, 53, 67},
		{5, 19, 31, 43, 59, 71},
		{7, 23, 37, 47, 61, 73},
	}
	obj := losslessColorObject829(2, 3, 8, 8, "RGB", 0)
	want := interleavePlanes829(planes, 1)
	for predictor := 1; predictor <= 7; predictor++ {
		t.Run(string(rune('0'+predictor)), func(t *testing.T) {
			stream := encodeLosslessMulticomponent829(3, 2, 8, planes, []losslessScan829{{
				components: []int{0, 1, 2}, predictor: predictor,
			}})
			frames, err := New().Decode(encapsulated(stream), obj)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(frames.Data[0], want) {
				t.Fatalf("predictor %d output = %v, want %v", predictor, frames.Data[0], want)
			}
		})
	}
}

func TestJPEGLosslessHuffmanRedefinitionAcrossSegments(t *testing.T) {
	payload := losslessDHT829()[4:]
	tables := make(map[int]*huffTable)
	if err := parseDHTSegment(payload, tables); err != nil {
		t.Fatal(err)
	}
	if err := parseDHTSegment(payload, tables); err != nil {
		t.Fatalf("valid table redefinition across DHT segments: %v", err)
	}
	duplicateInOneSegment := append(append([]byte(nil), payload...), payload...)
	if err := parseDHTSegment(duplicateInOneSegment, make(map[int]*huffTable)); err == nil {
		t.Fatal("duplicate table destination in one DHT segment accepted")
	}
}

func TestJPEGLosslessMulticomponentRejectsPlanarConfigurationOne(t *testing.T) {
	planes := [][]int32{{1}, {2}, {3}}
	stream := encodeLosslessMulticomponent829(1, 1, 8, planes, []losslessScan829{{components: []int{0, 1, 2}, predictor: 1}})
	obj := losslessColorObject829(1, 1, 8, 8, "RGB", 1)
	if _, err := New().Decode(encapsulated(stream), obj); err == nil {
		t.Fatal("PlanarConfiguration=1 accepted")
	}
}

func losslessColorObject829(rows, columns, bitsAllocated, bitsStored uint16, photometric string, planar uint16) *object.Object {
	elements := metadataElementsWithPhotometric(rows, columns, bitsAllocated, bitsStored, 3, photometric)
	elements = append(elements, dicomtest.Uint16Element(tagPlanarConfiguration829, core.VRUS, nil, planar))
	return object.FromElements(elements, nil)
}

func encodeLosslessMulticomponent829(width, height, precision int, planes [][]int32, scans []losslessScan829) []byte {
	componentIDs := []byte{5, 7, 9}
	out := []byte{0xff, markerSOI}
	sofLength := 8 + 3*len(planes)
	out = append(out, 0xff, markerSOF3, byte(sofLength>>8), byte(sofLength), byte(precision),
		byte(height>>8), byte(height), byte(width>>8), byte(width), byte(len(planes)))
	for component := range planes {
		out = append(out, componentIDs[component], 0x11, 0)
	}
	out = append(out, losslessDHT829()...)
	for _, scan := range scans {
		if scan.restart > 0 {
			out = append(out, 0xff, markerDRI, 0, 4, byte(scan.restart>>8), byte(scan.restart))
		}
		sosLength := 6 + 2*len(scan.components)
		out = append(out, 0xff, markerSOS, byte(sosLength>>8), byte(sosLength), byte(len(scan.components)))
		for _, component := range scan.components {
			out = append(out, componentIDs[component], 0)
		}
		out = append(out, byte(scan.predictor), 0, byte(scan.pointTransform))
		out = append(out, losslessEntropy829(width, height, precision, planes, scan)...)
	}
	return append(out, 0xff, markerEOI)
}

func losslessDHT829() []byte {
	dht := []byte{0xff, markerDHT, 0, 0x24, 0,
		0, 0, 0, 0, 17, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	for symbol := 0; symbol <= 16; symbol++ {
		dht = append(dht, byte(symbol))
	}
	return dht
}

func losslessEntropy829(width, height, precision int, planes [][]int32, scan losslessScan829) []byte {
	coded := make([][]int32, len(planes))
	for component := range planes {
		coded[component] = make([]int32, len(planes[component]))
		for index, sample := range planes[component] {
			coded[component][index] = sample >> uint(scan.pointTransform)
		}
	}
	writer := &bitWriter{}
	defaultPrediction := int32(1) << (precision - scan.pointTransform - 1)
	restartStart := 0
	restartCount := 0
	restartMarker := byte(0xd0)
	for pixelIndex := 0; pixelIndex < width*height; pixelIndex++ {
		x, y := pixelIndex%width, pixelIndex/width
		for _, component := range scan.components {
			prediction := predict(coded[component], x, y, width, scan.predictor, defaultPrediction)
			if pixelIndex == restartStart {
				prediction = defaultPrediction
			}
			difference := coded[component][pixelIndex] - prediction
			category := bitLen(difference)
			writer.writeBits(category, 5)
			if category > 0 {
				encoded := difference
				if difference < 0 {
					encoded = difference + (1 << category) - 1
				}
				writer.writeBits(int(encoded)&((1<<category)-1), category)
			}
		}
		if scan.restart > 0 {
			restartCount++
			if restartCount == scan.restart && pixelIndex != width*height-1 {
				writer.pad()
				writer.out = append(writer.out, 0xff, restartMarker)
				restartMarker = 0xd0 + ((restartMarker - 0xd0 + 1) & 7)
				restartCount = 0
				restartStart = pixelIndex + 1
			}
		}
	}
	writer.pad()
	return writer.out
}

func interleavePlanes829(planes [][]int32, bytesPerSample int) []byte {
	out := make([]byte, len(planes)*len(planes[0])*bytesPerSample)
	for pixel := range planes[0] {
		for component := range planes {
			index := pixel*len(planes) + component
			if bytesPerSample == 1 {
				out[index] = byte(planes[component][pixel])
			} else {
				binary.LittleEndian.PutUint16(out[index*2:], uint16(planes[component][pixel]))
			}
		}
	}
	return out
}
