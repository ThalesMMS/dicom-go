package derivedio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// ---------------------------------------------------------------------------
// File()
// ---------------------------------------------------------------------------

func Test_File_returns_error_for_empty_sop_class_uid(t *testing.T) {
	dataset := Object(UI(TagSOPInstanceUID, "1.2.3.instance"))
	_, err := File("", "1.2.3.instance", dataset)
	if !errors.Is(err, object.ErrMissingSOPClassUID) {
		t.Fatalf("File(empty SOP class) error = %v, want ErrMissingSOPClassUID", err)
	}
}

func Test_File_returns_error_for_whitespace_only_sop_class_uid(t *testing.T) {
	dataset := Object()
	_, err := File("   ", "1.2.3.instance", dataset)
	if !errors.Is(err, object.ErrMissingSOPClassUID) {
		t.Fatalf("File(whitespace SOP class) error = %v, want ErrMissingSOPClassUID", err)
	}
}

func Test_File_returns_error_for_empty_sop_instance_uid(t *testing.T) {
	dataset := Object(UI(TagSOPClassUID, "1.2.840.10008.5.1.4.1.1.2"))
	_, err := File("1.2.840.10008.5.1.4.1.1.2", "", dataset)
	if !errors.Is(err, object.ErrMissingSOPInstanceUID) {
		t.Fatalf("File(empty SOP instance) error = %v, want ErrMissingSOPInstanceUID", err)
	}
}

func Test_File_returns_error_for_nil_dataset(t *testing.T) {
	_, err := File("1.2.840.10008.5.1.4.1.1.2", "1.2.3.instance", nil)
	if err == nil {
		t.Fatal("File(nil dataset) expected error, got nil")
	}
}

func Test_File_sets_correct_transfer_syntax_in_meta(t *testing.T) {
	dataset := Object(
		UI(TagSOPClassUID, "1.2.840.10008.5.1.4.1.1.2"),
		UI(TagSOPInstanceUID, "1.2.3.instance.ts"),
	)
	file, err := File("1.2.840.10008.5.1.4.1.1.2", "1.2.3.instance.ts", dataset)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	// Transfer syntax should be ExplicitVRLittleEndian
	tsUID, ok := file.Meta.GetUID(TagTransferSyntaxUID)
	if !ok {
		t.Fatal("transfer syntax UID not found in file meta")
	}
	if tsUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax UID = %q, want %q", tsUID, transfer.ExplicitVRLittleEndian.UID)
	}
}

func TestFixedWidthIntegerConstructorsPreserveBoundaryValues(t *testing.T) {
	tag := core.NewTag(0x7777, 0x0001)
	unsigned, ok := US(tag, 0, 1<<16-1).RawBytes()
	if !ok || !bytes.Equal(unsigned, []byte{0, 0, 0xff, 0xff}) {
		t.Fatalf("US boundary bytes = % X, ok=%v", unsigned, ok)
	}
	signed, ok := SL(tag, -1<<31, 1<<31-1).RawBytes()
	if !ok || !bytes.Equal(signed, []byte{0, 0, 0, 0x80, 0xff, 0xff, 0xff, 0x7f}) {
		t.Fatalf("SL boundary bytes = % X, ok=%v", signed, ok)
	}
}

// ---------------------------------------------------------------------------
// CleanString()
// ---------------------------------------------------------------------------

func Test_CleanString_returns_empty_for_missing_tag(t *testing.T) {
	obj := Object()
	got := CleanString(obj, TagModality)
	if got != "" {
		t.Fatalf("CleanString missing tag = %q, want empty", got)
	}
}

func Test_CleanString_trims_whitespace_and_null(t *testing.T) {
	obj := Object(CS(TagModality, "CT \x00"))
	got := CleanString(obj, TagModality)
	if got != "CT" {
		t.Fatalf("CleanString = %q, want CT", got)
	}
}

func Test_CleanString_returns_plain_value(t *testing.T) {
	obj := Object(CS(TagModality, "MR"))
	got := CleanString(obj, TagModality)
	if got != "MR" {
		t.Fatalf("CleanString = %q, want MR", got)
	}
}

// ---------------------------------------------------------------------------
// CleanUID()
// ---------------------------------------------------------------------------

func Test_CleanUID_returns_empty_for_missing_tag(t *testing.T) {
	obj := Object()
	got := CleanUID(obj, TagSOPClassUID)
	if got != "" {
		t.Fatalf("CleanUID missing tag = %q, want empty", got)
	}
}

func Test_CleanUID_trims_whitespace(t *testing.T) {
	obj := Object(UI(TagSOPClassUID, "  1.2.840.10008.5.1.4.1.1.2  "))
	got := CleanUID(obj, TagSOPClassUID)
	if got != "1.2.840.10008.5.1.4.1.1.2" {
		t.Fatalf("CleanUID = %q, want trimmed UID", got)
	}
}

// ---------------------------------------------------------------------------
// Int()
// ---------------------------------------------------------------------------

func Test_Int_returns_zero_for_missing_tag(t *testing.T) {
	obj := Object()
	if got := Int(obj, TagRows); got != 0 {
		t.Fatalf("Int missing tag = %d, want 0", got)
	}
}

func Test_Int_returns_correct_value(t *testing.T) {
	obj := Object(IS(TagRows, 512))
	if got := Int(obj, TagRows); got != 512 {
		t.Fatalf("Int = %d, want 512", got)
	}
}

// ---------------------------------------------------------------------------
// Floats()
// ---------------------------------------------------------------------------

func Test_Floats_returns_nil_for_missing_tag(t *testing.T) {
	obj := Object()
	if got := Floats(obj, core.NewTag(0x0070, 0x0022)); got != nil {
		t.Fatalf("Floats missing tag = %v, want nil", got)
	}
}

func Test_Floats_returns_correct_values(t *testing.T) {
	obj := Object(DS(core.NewTag(0x0070, 0x0022), 1.5, 2.5, 3.5))
	values := Floats(obj, core.NewTag(0x0070, 0x0022))
	if len(values) != 3 || values[0] != 1.5 || values[1] != 2.5 || values[2] != 3.5 {
		t.Fatalf("Floats = %v, want [1.5, 2.5, 3.5]", values)
	}
}

// ---------------------------------------------------------------------------
// Sequence()
// ---------------------------------------------------------------------------

func Test_Sequence_returns_nil_for_missing_tag(t *testing.T) {
	obj := Object()
	if got := Sequence(obj, core.NewTag(0x0008, 0x1115)); got != nil {
		t.Fatalf("Sequence missing tag = %v, want nil", got)
	}
}

func Test_Sequence_returns_items(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1115)
	inner := DataSet(UI(TagSOPClassUID, "1.2.3"))
	obj := Object(Seq(seqTag, inner))
	items := Sequence(obj, seqTag)
	if len(items) != 1 {
		t.Fatalf("Sequence len = %d, want 1", len(items))
	}
}

// ---------------------------------------------------------------------------
// Uint16Bytes / Uint16s round-trip
// ---------------------------------------------------------------------------

func Test_Uint16Bytes_Uint16s_round_trip(t *testing.T) {
	original := []uint16{0, 1, 255, 1024, 65535}
	encoded := Uint16Bytes(original)
	decoded := Uint16s(encoded)
	if len(decoded) != len(original) {
		t.Fatalf("decoded len = %d, want %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Fatalf("decoded[%d] = %d, want %d", i, decoded[i], original[i])
		}
	}
}

func Test_Uint16Bytes_empty_slice(t *testing.T) {
	result := Uint16Bytes(nil)
	if len(result) != 0 {
		t.Fatalf("Uint16Bytes(nil) = %v, want empty", result)
	}
}

func Test_Uint16s_empty_slice(t *testing.T) {
	result := Uint16s(nil)
	if len(result) != 0 {
		t.Fatalf("Uint16s(nil) = %v, want empty", result)
	}
}

// ---------------------------------------------------------------------------
// SamePosition()
// ---------------------------------------------------------------------------

func Test_SamePosition_returns_true_for_equal_values(t *testing.T) {
	if !SamePosition(1.0, 1.0) {
		t.Fatal("SamePosition(1.0, 1.0) = false, want true")
	}
}

func Test_SamePosition_returns_true_within_tolerance(t *testing.T) {
	if !SamePosition(1.0, 1.0009) {
		t.Fatal("SamePosition within tolerance = false, want true")
	}
}

func Test_SamePosition_returns_false_outside_tolerance(t *testing.T) {
	if SamePosition(1.0, 2.0) {
		t.Fatal("SamePosition(1.0, 2.0) = true, want false")
	}
}

func Test_SamePosition_returns_false_just_outside_tolerance(t *testing.T) {
	if SamePosition(0.0, 0.002) {
		t.Fatal("SamePosition just outside tolerance = true, want false")
	}
}

// ---------------------------------------------------------------------------
// IS / DS helpers
// ---------------------------------------------------------------------------

func Test_IS_encodes_multiple_integers(t *testing.T) {
	el := IS(TagRows, 1, 2, 3)
	obj := Object(el)
	// Reading back as integers via Int only returns the first
	if got := Int(obj, TagRows); got != 1 {
		t.Fatalf("IS first value = %d, want 1", got)
	}
}

func Test_DS_encodes_single_float(t *testing.T) {
	tag := core.NewTag(0x0028, 0x1050) // Window Center
	el := DS(tag, 40.0)
	obj := Object(el)
	vals := Floats(obj, tag)
	if len(vals) != 1 || vals[0] != 40.0 {
		t.Fatalf("DS float = %v, want [40.0]", vals)
	}
}

func Test_DS_limits_each_value_to_16_bytes(t *testing.T) {
	longValue := 1.0 / 300000.0
	if unbounded := strconv.FormatFloat(longValue, 'g', -1, 64); len(unbounded) <= 16 {
		t.Fatalf("test value formats as %q; want more than 16 bytes", unbounded)
	}

	inputs := []float64{longValue, -longValue, math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64}
	values := DS(core.NewTag(0x0040, 0xA30A), inputs...).StringValues()
	if len(values) != len(inputs) {
		t.Fatalf("DS value count = %d, want %d", len(values), len(inputs))
	}
	for i, value := range values {
		if len(value) > 16 {
			t.Errorf("DS value %d = %q (%d bytes), want at most 16", i, value, len(value))
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Errorf("ParseFloat(DS value %q): %v", value, err)
			continue
		}
		relativeError := math.Abs((parsed - inputs[i]) / inputs[i])
		if relativeError > 1e-7 {
			t.Errorf("DS value %q relative error = %g, want <= 1e-7", value, relativeError)
		}
	}
}

func Test_Binary_numeric_helpers_round_trip(t *testing.T) {
	usTag := core.NewTag(0x0028, 0x0010)
	slTag := core.NewTag(0x0070, 0x0052)
	flTag := core.NewTag(0x0070, 0x0022)
	obj := Object(
		US(usTag, 1, 65535),
		SL(slTag, -10, 20),
		FL(flTag, 1.5, -2.25),
	)

	if got := Ints(obj, usTag); len(got) != 2 || got[0] != 1 || got[1] != 65535 {
		t.Fatalf("US values = %v, want [1 65535]", got)
	}
	if got := Ints(obj, slTag); len(got) != 2 || got[0] != -10 || got[1] != 20 {
		t.Fatalf("SL values = %v, want [-10 20]", got)
	}
	if got := Floats(obj, flTag); len(got) != 2 || got[0] != 1.5 || got[1] != -2.25 {
		t.Fatalf("FL values = %v, want [1.5 -2.25]", got)
	}
}

func Test_Binary_numeric_readers_honor_object_byte_order(t *testing.T) {
	usTag := core.NewTag(0x0028, 0x0010)
	flTag := core.NewTag(0x0070, 0x0022)
	usRaw := make([]byte, 2)
	flRaw := make([]byte, 4)
	binary.BigEndian.PutUint16(usRaw, 512)
	binary.BigEndian.PutUint32(flRaw, math.Float32bits(3.5))
	obj := Object(
		Raw(usTag, core.VRUS, usRaw),
		Raw(flTag, core.VRFL, flRaw),
	)
	obj.SetValueByteOrder(binary.BigEndian)

	if got := Int(obj, usTag); got != 512 {
		t.Fatalf("big-endian US = %d, want 512", got)
	}
	if got := Floats(obj, flTag); len(got) != 1 || got[0] != 3.5 {
		t.Fatalf("big-endian FL = %v, want [3.5]", got)
	}
}

func Test_Strict_numeric_readers_report_malformed_binary_lengths(t *testing.T) {
	usTag := core.NewTag(0x0028, 0x0010)
	flTag := core.NewTag(0x0070, 0x0022)
	obj := Object(
		Raw(usTag, core.VRUS, []byte{1}),
		Raw(flTag, core.VRFL, []byte{1, 2, 3}),
	)

	if _, err := LookupInts(obj, usTag); err == nil {
		t.Fatal("LookupInts() error = nil, want malformed US length error")
	}
	if _, err := LookupFloats(obj, flTag); err == nil {
		t.Fatal("LookupFloats() error = nil, want malformed FL length error")
	}
	if got := Ints(obj, usTag); got != nil {
		t.Fatalf("compatibility Ints() = %v, want nil for malformed value", got)
	}
	if got := Floats(obj, flTag); got != nil {
		t.Fatalf("compatibility Floats() = %v, want nil for malformed value", got)
	}
}
