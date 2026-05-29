// Package dsa reads the DICOM Mask Module (PS3.3 C.7.6.7): the Mask
// Subtraction Sequence (0028,6100) that XA/XRF multi-frame objects use to
// record how live frames should be paired with mask frames for digital
// subtraction angiography, plus the Recommended Viewing Mode (0028,1090).
//
// This package only reads the module's declared frame pairing and optional
// sub-pixel registration shift; it does not implement the acquisition-time
// TID/REV_TID temporal-subtraction algorithms (PS3.3 C.7.6.7.1.1) — those are
// historical acquisition-side processing modes, not something a viewer needs
// to reproduce to display a simple live-minus-mask subtraction. A caller that
// only wants "which frame is the mask" can use MaskFrameForContrastFrame
// without caring about the operation code.
package dsa

import (
	"math"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

// Mask Operation (0028,6101) defined terms (PS3.3 C.7.6.7.1.1).
const (
	// MaskOperationAverageSubtraction is a simple average-mask subtraction:
	// exactly what this package's callers care about.
	MaskOperationAverageSubtraction = "AVG_SUB"
	// MaskOperationTID is time-interval differencing.
	MaskOperationTID = "TID"
	// MaskOperationReviewedTID is reviewed time-interval differencing.
	MaskOperationReviewedTID = "REV_TID"
)

var (
	tagMaskSubtractionSequence  = core.NewTag(0x0028, 0x6100)
	tagMaskOperation            = core.NewTag(0x0028, 0x6101)
	tagApplicableFrameRange     = core.NewTag(0x0028, 0x6102)
	tagMaskFrameNumbers         = core.NewTag(0x0028, 0x6110)
	tagContrastFrameAveraging   = core.NewTag(0x0028, 0x6112)
	tagMaskSubPixelShift        = core.NewTag(0x0028, 0x6114)
	tagMaskOperationExplanation = core.NewTag(0x0028, 0x6190)
	tagRecommendedViewingMode   = core.NewTag(0x0028, 0x1090)
)

// MaskSubtractionItem is one item of the Mask Subtraction Sequence: the mask
// operation and frame pairing that applies to a range of contrast frames.
type MaskSubtractionItem struct {
	// MaskOperation is the Mask Operation (0028,6101) defined term (e.g.
	// MaskOperationAverageSubtraction), or "" if absent.
	MaskOperation string
	// ApplicableFrameRange is Applicable Frame Range (0028,6102): zero or more
	// (start, end) 1-based frame-number pairs this item covers. Empty means
	// the item applies to every frame.
	ApplicableFrameRange []int
	// MaskFrameNumbers is Mask Frame Numbers (0028,6110): the 1-based frame
	// number(s) to use as the mask. A single value is a shared mask for every
	// contrast frame in ApplicableFrameRange; multiple values pair
	// one-to-one with the contrast frames in that range, in order.
	MaskFrameNumbers []int
	// ContrastFrameAveraging is Contrast Frame Averaging (0028,6112): the
	// number of contrast frames averaged together before subtraction, or 0 if
	// absent (meaning no averaging).
	ContrastFrameAveraging int
	// MaskSubPixelShift is Mask Sub-Pixel Shift (0028,6114): the (row,
	// column) sub-pixel registration shift, in pixels, to apply to the mask
	// frame before subtracting it from the contrast frame. ShiftPresent is
	// false when the element is absent (most objects don't carry one; the
	// acquisition was assumed registered).
	MaskSubPixelShift [2]float64
	ShiftPresent      bool
	// Explanation is Mask Operation Explanation (0028,6190), or "" if absent.
	Explanation string
}

// AppliesToFrame reports whether the 1-based frameNumber falls within one of
// item's ApplicableFrameRange pairs. An empty range means "every frame".
func (item MaskSubtractionItem) AppliesToFrame(frameNumber int) bool {
	if len(item.ApplicableFrameRange) == 0 {
		return true
	}
	for i := 0; i+1 < len(item.ApplicableFrameRange); i += 2 {
		start, end := item.ApplicableFrameRange[i], item.ApplicableFrameRange[i+1]
		if frameNumber >= start && frameNumber <= end {
			return true
		}
	}
	return false
}

// MaskFrameForContrastFrame resolves the 1-based mask frame number for a
// 1-based contrastFrame that AppliesToFrame(contrastFrame). When
// MaskFrameNumbers has one entry, that single mask frame is shared by every
// contrast frame in range; when it has one entry per applicable contrast
// frame, contrastIndex (the contrast frame's 0-based position within the
// applicable range, in acquisition order) selects the paired entry. ok is
// false if there is no mask frame number to resolve.
func (item MaskSubtractionItem) MaskFrameForContrastFrame(contrastIndex int) (int, bool) {
	switch {
	case len(item.MaskFrameNumbers) == 0:
		return 0, false
	case len(item.MaskFrameNumbers) == 1:
		return item.MaskFrameNumbers[0], true
	case contrastIndex >= 0 && contrastIndex < len(item.MaskFrameNumbers):
		return item.MaskFrameNumbers[contrastIndex], true
	default:
		return 0, false
	}
}

// ReadMaskSubtraction reads the Mask Subtraction Sequence (0028,6100) from
// obj. ok is false when the object has no Mask Module.
func ReadMaskSubtraction(obj *object.Object) (items []MaskSubtractionItem, ok bool) {
	if obj == nil {
		return nil, false
	}
	seqItems, present := obj.GetSequence(tagMaskSubtractionSequence)
	if !present {
		return nil, false
	}
	items = make([]MaskSubtractionItem, 0, len(seqItems))
	for _, seqItem := range seqItems {
		if seqItem == nil {
			continue
		}
		item := MaskSubtractionItem{}
		if op, ok := seqItem.GetString(tagMaskOperation); ok {
			item.MaskOperation = op
		}
		if explanation, ok := seqItem.GetString(tagMaskOperationExplanation); ok {
			item.Explanation = explanation
		}
		if frameRange, ok := getUint16s(seqItem, tagApplicableFrameRange); ok {
			item.ApplicableFrameRange = uint16sToInts(frameRange)
		}
		if frameNumbers, ok := getUint16s(seqItem, tagMaskFrameNumbers); ok {
			item.MaskFrameNumbers = uint16sToInts(frameNumbers)
		}
		if averaging, ok := getUint16s(seqItem, tagContrastFrameAveraging); ok && len(averaging) > 0 {
			item.ContrastFrameAveraging = int(averaging[0])
		}
		if shift, ok := getFloat32s(seqItem, tagMaskSubPixelShift); ok && len(shift) >= 2 {
			item.MaskSubPixelShift = [2]float64{float64(shift[0]), float64(shift[1])}
			item.ShiftPresent = true
		}
		items = append(items, item)
	}
	return items, len(items) > 0
}

// RecommendedViewingMode returns Recommended Viewing Mode (0028,1090), or ""
// if absent.
func RecommendedViewingMode(obj *object.Object) string {
	if obj == nil {
		return ""
	}
	value, _ := obj.GetString(tagRecommendedViewingMode)
	return value
}

func getUint16s(obj *object.Object, tag core.Tag) ([]uint16, bool) {
	raw, ok := obj.GetRaw(tag)
	if !ok || len(raw) < 2 {
		return nil, false
	}
	order := obj.ValueByteOrder()
	n := len(raw) / 2
	out := make([]uint16, n)
	for i := 0; i < n; i++ {
		out[i] = order.Uint16(raw[i*2 : i*2+2])
	}
	return out, true
}

func getFloat32s(obj *object.Object, tag core.Tag) ([]float32, bool) {
	raw, ok := obj.GetRaw(tag)
	if !ok || len(raw) < 4 {
		return nil, false
	}
	order := obj.ValueByteOrder()
	n := len(raw) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(order.Uint32(raw[i*4 : i*4+4]))
	}
	return out, true
}

func uint16sToInts(values []uint16) []int {
	out := make([]int, len(values))
	for i, v := range values {
		out[i] = int(v)
	}
	return out
}
