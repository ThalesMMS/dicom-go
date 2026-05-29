package display

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagSamplesPerPixel     = core.NewTag(0x0028, 0x0002)
	tagPhotometric         = core.NewTag(0x0028, 0x0004)
	tagPlanarConfiguration = core.NewTag(0x0028, 0x0006)

	tagRedPaletteDesc   = core.NewTag(0x0028, 0x1101)
	tagGreenPaletteDesc = core.NewTag(0x0028, 0x1102)
	tagBluePaletteDesc  = core.NewTag(0x0028, 0x1103)
	tagRedPaletteData   = core.NewTag(0x0028, 0x1201)
	tagGreenPaletteData = core.NewTag(0x0028, 0x1202)
	tagBluePaletteData  = core.NewTag(0x0028, 0x1203)
	tagSegRedPalette    = core.NewTag(0x0028, 0x1221)
	tagSegGreenPalette  = core.NewTag(0x0028, 0x1222)
	tagSegBluePalette   = core.NewTag(0x0028, 0x1223)

	tagICCProfile = core.NewTag(0x0028, 0x2000)
	tagColorSpace = core.NewTag(0x0028, 0x2002)
)

// ColorMetadata exposes color-layout and color-management attributes without
// performing color management. The ICC profile and color space are reported as
// metadata only, preserving the lightweight default dependency policy.
type ColorMetadata struct {
	Photometric                string
	SamplesPerPixel            int
	PlanarConfiguration        int
	PlanarConfigurationPresent bool
	ColorSpace                 string
	// ICCProfile is the raw ICC Profile (0028,2000) bytes, if present.
	ICCProfile []byte
}

// ColorMetadataFromObject reads color layout and ICC/color-space metadata.
func ColorMetadataFromObject(obj *object.Object) ColorMetadata {
	var m ColorMetadata
	if obj == nil {
		return m
	}
	if p, ok := obj.GetString(tagPhotometric); ok {
		m.Photometric = strings.TrimSpace(p)
	}
	if spp, ok := rawUint16(obj, tagSamplesPerPixel); ok {
		m.SamplesPerPixel = int(spp)
	}
	if pc, ok := rawUint16(obj, tagPlanarConfiguration); ok {
		m.PlanarConfiguration = int(pc)
		m.PlanarConfigurationPresent = true
	}
	if cs, ok := obj.GetString(tagColorSpace); ok {
		m.ColorSpace = strings.TrimSpace(cs)
	}
	if icc, ok := obj.GetRaw(tagICCProfile); ok && len(icc) > 0 {
		m.ICCProfile = icc
	}
	return m
}

// PaletteFromObject reads the red/green/blue palette color LUTs. It returns
// (nil, nil) when the dataset has no palette LUTs, and an explicit error for
// malformed descriptors or LUT data.
func PaletteFromObject(obj *object.Object) (*PaletteColorLUT, error) {
	if obj == nil {
		return nil, nil
	}

	red, err := paletteLUT(obj, tagRedPaletteDesc, tagRedPaletteData, tagSegRedPalette)
	if err != nil {
		return nil, err
	}
	green, err := paletteLUT(obj, tagGreenPaletteDesc, tagGreenPaletteData, tagSegGreenPalette)
	if err != nil {
		return nil, err
	}
	blue, err := paletteLUT(obj, tagBluePaletteDesc, tagBluePaletteData, tagSegBluePalette)
	if err != nil {
		return nil, err
	}
	if red == nil || green == nil || blue == nil {
		return nil, nil
	}
	return &PaletteColorLUT{Red: red, Green: green, Blue: blue}, nil
}

// paletteLUT reads one palette color LUT (descriptor + data). It returns
// (nil, nil) when the descriptor and data are absent.
func paletteLUT(obj *object.Object, descTag, dataTag, segmentedDataTag core.Tag) (*LUT, error) {
	elem, ok := obj.Get(descTag)
	if !ok {
		if _, hasData := obj.GetRaw(dataTag); hasData {
			return nil, ErrInvalidLUTDescriptor
		}
		if _, hasSegmentedData := obj.GetRaw(segmentedDataTag); hasSegmentedData {
			return nil, ErrInvalidLUTDescriptor
		}
		return nil, nil
	}
	descRaw, ok := elem.RawBytes()
	if !ok || len(descRaw) < 6 {
		return nil, ErrInvalidLUTDescriptor
	}
	order := obj.ValueByteOrder()
	numberOfEntries := int(order.Uint16(descRaw[0:2]))
	bitsPerEntry := int(order.Uint16(descRaw[4:6]))
	firstMapped := int(order.Uint16(descRaw[2:4]))
	if elem.VR() == core.VRSS {
		firstMapped = int(int16(order.Uint16(descRaw[2:4])))
	}

	entries, err := paletteEntriesFromObject(obj, dataTag, segmentedDataTag, bitsPerEntry, numberOfEntries, order)
	if err != nil {
		return nil, err
	}
	return NewLUT([]int{numberOfEntries, firstMapped, bitsPerEntry}, entries)
}

func paletteEntriesFromObject(obj *object.Object, dataTag, segmentedDataTag core.Tag, bits, numberOfEntries int, order binary.ByteOrder) ([]uint16, error) {
	if dataRaw, ok := obj.GetRaw(dataTag); ok && len(dataRaw) > 0 {
		return paletteEntries(dataRaw, bits, numberOfEntries, order), nil
	}
	segmentedRaw, ok := obj.GetRaw(segmentedDataTag)
	if !ok || len(segmentedRaw) == 0 {
		return nil, ErrInvalidLUTData
	}
	return segmentedPaletteEntries(segmentedRaw, numberOfEntries, order)
}

// paletteEntries decodes palette LUT data. 8-bit entries are one byte each
// according to the descriptor; odd entry counts may include a single padding
// byte to make the DICOM value length even. 16-bit entries use dataset words.
func paletteEntries(data []byte, bits, numberOfEntries int, order binary.ByteOrder) []uint16 {
	if order == nil {
		order = binary.LittleEndian
	}
	count := numberOfEntries
	if count == 0 {
		count = 1 << 16
	}
	if bits == 8 {
		entryCount := len(data)
		if count%2 == 1 && len(data) == count+1 {
			entryCount = count
		}
		out := make([]uint16, entryCount)
		for i, b := range data[:entryCount] {
			out[i] = uint16(b)
		}
		return out
	}
	out := make([]uint16, len(data)/2)
	for i := range out {
		out[i] = order.Uint16(data[i*2 : i*2+2])
	}
	return out
}

func segmentedPaletteEntries(data []byte, numberOfEntries int, order binary.ByteOrder) ([]uint16, error) {
	if len(data)%2 != 0 {
		return nil, ErrInvalidLUTData
	}
	if order == nil {
		order = binary.LittleEndian
	}
	count := numberOfEntries
	if count == 0 {
		count = 1 << 16
	}
	words := make([]uint16, len(data)/2)
	for i := range words {
		words[i] = order.Uint16(data[i*2 : i*2+2])
	}

	expander := segmentedPaletteExpander{words: words, count: count}
	entries := make([]uint16, 0, count)
	for i := 0; i < len(words); {
		next, err := expander.expandSegment(i, &entries, true)
		if err != nil {
			return nil, err
		}
		i = next
	}
	return entries, nil
}

type segmentedPaletteExpander struct {
	words []uint16
	count int
}

func (e segmentedPaletteExpander) expandSegment(index int, entries *[]uint16, allowIndirect bool) (int, error) {
	if index < 0 || index+1 >= len(e.words) {
		return 0, ErrInvalidLUTData
	}
	opcode := e.words[index]
	length := int(e.words[index+1])
	dataIndex := index + 2

	switch opcode {
	case 0:
		if len(*entries)+length > e.count || dataIndex+length > len(e.words) {
			return 0, ErrInvalidLUTData
		}
		*entries = append(*entries, e.words[dataIndex:dataIndex+length]...)
		return dataIndex + length, nil
	case 1:
		if len(*entries)+length > e.count || dataIndex >= len(e.words) {
			return 0, ErrInvalidLUTData
		}
		end := e.words[dataIndex]
		if length == 0 {
			return dataIndex + 1, nil
		}
		if len(*entries) == 0 {
			return 0, ErrInvalidLUTData
		}
		start := (*entries)[len(*entries)-1]
		for step := 1; step <= length; step++ {
			*entries = append(*entries, interpolateSegmentValue(start, end, step, length))
		}
		return dataIndex + 1, nil
	case 2:
		if !allowIndirect {
			return 0, fmt.Errorf("%w: indirect segment cannot copy another indirect segment", ErrUnsupportedSegmentedPalette)
		}
		if dataIndex+1 >= len(e.words) {
			return 0, ErrInvalidLUTData
		}
		offset := uint32(e.words[dataIndex]) | uint32(e.words[dataIndex+1])<<16
		if offset%2 != 0 {
			return 0, ErrInvalidLUTData
		}
		copyIndex := int(offset / 2)
		if copyIndex < 0 || copyIndex >= len(e.words) {
			return 0, ErrInvalidLUTData
		}
		for n := 0; n < length; n++ {
			next, err := e.expandSegment(copyIndex, entries, false)
			if err != nil {
				return 0, err
			}
			copyIndex = next
		}
		return dataIndex + 2, nil
	default:
		return 0, fmt.Errorf("%w: segmented palette opcode %d", ErrInvalidLUTData, opcode)
	}
}

func interpolateSegmentValue(start, end uint16, step, length int) uint16 {
	if length <= 0 {
		return start
	}
	value := float64(start) + (float64(end)-float64(start))*float64(step)/float64(length)
	return uint16(math.Round(value))
}

// rawUint16 reads a binary US attribute using the object's value byte order.
func rawUint16(obj *object.Object, tag core.Tag) (uint16, bool) {
	raw, ok := obj.GetRaw(tag)
	if !ok || len(raw) < 2 {
		return 0, false
	}
	return obj.ValueByteOrder().Uint16(raw[:2]), true
}
