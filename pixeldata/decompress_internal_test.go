package pixeldata

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestTranscodeElementsToLittleEndianPreservesNestedInput(t *testing.T) {
	textTag := core.NewTag(0x0010, 0x0010)
	numberTag := core.NewTag(0x0028, 0x0010)
	sequenceTag := core.NewTag(0x0008, 0x1111)
	var bigEndianNumber [2]byte
	binary.BigEndian.PutUint16(bigEndianNumber[:], 0x1234)
	nested := []core.Element{
		core.NewRawElement(textTag, core.VRPN, []byte("DOE^JANE")),
		core.NewRawElement(numberTag, core.VRUS, bigEndianNumber[:]),
	}
	original := []core.Element{{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{
			Elements: nested, ItemOffset: 1234, ItemOffsetSet: true,
		}}},
	}}
	owned := append([]core.Element(nil), original...)

	got := transcodeElementsToLittleEndian(owned)

	gotRaw := nestedRawValue(t, got, 0, 1)
	if !bytes.Equal(gotRaw, []byte{0x34, 0x12}) {
		t.Fatalf("transcoded nested US = % X, want 34 12", gotRaw)
	}
	originalRaw := nestedRawValue(t, original, 0, 1)
	if !bytes.Equal(originalRaw, []byte{0x12, 0x34}) {
		t.Fatalf("source nested US mutated to % X", originalRaw)
	}
	if gotText := nestedRawValue(t, got, 0, 0); !bytes.Equal(gotText, []byte("DOE^JANE")) {
		t.Fatalf("nested text changed to %q", gotText)
	}
	sequence := got[0].Value.(core.SequenceValue)
	if item := sequence.Items[0]; item.ItemOffset != 1234 || !item.ItemOffsetSet {
		t.Fatalf("transcoded item metadata = offset %d, set %v; want 1234, true", item.ItemOffset, item.ItemOffsetSet)
	}
}

func nestedRawValue(t *testing.T, elements []core.Element, itemIndex, elementIndex int) []byte {
	t.Helper()
	sequence, ok := elements[0].Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("value type = %T, want SequenceValue", elements[0].Value)
	}
	raw, ok := sequence.Items[itemIndex].Elements[elementIndex].Value.(core.RawValue)
	if !ok {
		t.Fatalf("nested value type = %T, want RawValue", sequence.Items[itemIndex].Elements[elementIndex].Value)
	}
	return raw.Bytes()
}

func BenchmarkTranscodeElementsToLittleEndian(b *testing.B) {
	base := make([]core.Element, 0, 1001)
	for i := 0; i < 1000; i++ {
		tag := core.NewTag(0x0010, uint16(i+1))
		base = append(base, core.NewRawElement(tag, core.VRLO, []byte("unchanged text value")))
	}
	var number [2]byte
	binary.BigEndian.PutUint16(number[:], 0x1234)
	base = append(base, core.NewRawElement(core.NewTag(0x0028, 0x0010), core.VRUS, number[:]))

	b.Run("clone_all", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			elements := append([]core.Element(nil), base...)
			benchmarkTranscodedElements = transcodeElementsToLittleEndianLegacy(elements)
		}
	})
	b.Run("owned_copy_on_write", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			elements := append([]core.Element(nil), base...)
			benchmarkTranscodedElements = transcodeElementsToLittleEndian(elements)
		}
	})
}

func transcodeElementsToLittleEndianLegacy(elements []core.Element) []core.Element {
	out := make([]core.Element, len(elements))
	for i, element := range elements {
		out[i] = element
		value, changed := transcodeValueToLittleEndian(element.VR(), element.Value)
		if changed {
			out[i].Value = value
		}
	}
	return out
}

var benchmarkTranscodedElements []core.Element
