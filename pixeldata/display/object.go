package display

import (
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagRescaleIntercept    = core.NewTag(0x0028, 0x1052)
	tagRescaleSlope        = core.NewTag(0x0028, 0x1053)
	tagModalityLUTSequence = core.NewTag(0x0028, 0x3000)
	tagLUTDescriptor       = core.NewTag(0x0028, 0x3002)
	tagLUTData             = core.NewTag(0x0028, 0x3006)
	tagWindowCenter        = core.NewTag(0x0028, 0x1050)
	tagWindowWidth         = core.NewTag(0x0028, 0x1051)
	tagVOILUTFunction      = core.NewTag(0x0028, 0x1056)
	tagVOILUTSequence      = core.NewTag(0x0028, 0x3010)
)

// ModalityLUTFromObject builds the modality transform from a dataset. A
// Modality LUT Sequence (0028,3000) takes precedence over Rescale
// Slope/Intercept; if a Modality LUT Sequence is present but malformed, the
// linear rescale is used and the second return value reports the LUT error so
// callers can surface an explicit limitation instead of rendering wrong pixels.
func ModalityLUTFromObject(obj *object.Object) (ModalityLUT, error) {
	out := ModalityLUT{Slope: 1}
	if obj == nil {
		return out, nil
	}
	if slope, err := obj.GetFloat(tagRescaleSlope); err == nil && slope != 0 {
		out.Slope = slope
	}
	if intercept, err := obj.GetFloat(tagRescaleIntercept); err == nil {
		out.Intercept = intercept
	}

	items, ok := obj.GetSequence(tagModalityLUTSequence)
	if !ok || len(items) == 0 {
		return out, nil
	}
	lut, err := lutFromItem(items[0])
	if err != nil {
		return out, err
	}
	out.LUT = lut
	return out, nil
}

// VOIFromObject builds the VOI transform from a dataset. A VOI LUT Sequence
// (0028,3010) takes precedence over Window Center/Width. If a VOI LUT Sequence
// is present but malformed, the window (if any) is used and the LUT error is
// returned so callers can surface an explicit limitation.
func VOIFromObject(obj *object.Object) (VOILUT, error) {
	out := VOILUT{Function: voiFunctionFromObject(obj)}
	if obj == nil {
		return out, nil
	}
	if centers, err := obj.GetFloats(tagWindowCenter); err == nil && len(centers) > 0 {
		if widths, err := obj.GetFloats(tagWindowWidth); err == nil && len(widths) > 0 && widths[0] > 0 {
			out.Center = centers[0]
			out.Width = widths[0]
		}
	}

	items, ok := obj.GetSequence(tagVOILUTSequence)
	if !ok || len(items) == 0 {
		return out, nil
	}
	lut, err := lutFromItem(items[0])
	if err != nil {
		return out, err
	}
	out.LUT = lut
	return out, nil
}

func voiFunctionFromObject(obj *object.Object) VOIFunction {
	if obj == nil {
		return VOILinear
	}
	value, ok := obj.GetString(tagVOILUTFunction)
	if !ok {
		return VOILinear
	}
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "SIGMOID":
		return VOISigmoid
	case "LINEAR_EXACT":
		return VOILinearExact
	default:
		return VOILinear
	}
}

// lutFromItem reads a LUT Descriptor (0028,3002) and LUT Data (0028,3006) from a
// LUT sequence item and validates them through NewLUT.
//
// LUT Descriptor and LUT Data are binary US/SS/OW values. The descriptor's
// middle value (the first stored value mapped) is sign-interpreted when the
// descriptor element VR is SS.
func lutFromItem(item *object.Object) (*LUT, error) {
	if item == nil {
		return nil, ErrInvalidLUTDescriptor
	}
	elem, ok := item.Get(tagLUTDescriptor)
	if !ok {
		return nil, ErrInvalidLUTDescriptor
	}
	descRaw, ok := elem.RawBytes()
	if !ok || len(descRaw) < 6 {
		return nil, ErrInvalidLUTDescriptor
	}
	order := item.ValueByteOrder()
	numberOfEntries := int(order.Uint16(descRaw[0:2]))
	bitsPerEntry := int(order.Uint16(descRaw[4:6]))
	firstMapped := int(order.Uint16(descRaw[2:4]))
	if elem.VR() == core.VRSS {
		firstMapped = int(int16(order.Uint16(descRaw[2:4])))
	}

	entries, err := lutEntriesFromItem(item)
	if err != nil {
		return nil, err
	}
	return NewLUT([]int{numberOfEntries, firstMapped, bitsPerEntry}, entries)
}

// lutEntriesFromItem reads LUT Data as 16-bit entries. LUT Data is encoded as
// 16-bit words regardless of the declared bits-per-entry (PS3.5).
func lutEntriesFromItem(item *object.Object) ([]uint16, error) {
	raw, ok := item.GetRaw(tagLUTData)
	if !ok || len(raw) < 2 {
		return nil, ErrInvalidLUTData
	}
	order := item.ValueByteOrder()
	count := len(raw) / 2
	entries := make([]uint16, count)
	for i := 0; i < count; i++ {
		entries[i] = order.Uint16(raw[i*2 : i*2+2])
	}
	return entries, nil
}
