package core

import "testing"

func TestRawValueKindAndEncodedLength(t *testing.T) {
	value := RawValue([]byte{0x01, 0x02, 0x03})
	if got := value.Kind(); got != ValueRaw {
		t.Fatalf("Kind() = %v, want %v", got, ValueRaw)
	}
	if length, ok := value.EncodedLength(); !ok || length != 3 {
		t.Fatalf("EncodedLength() = (%v, %v), want (3, true)", length, ok)
	}
}

func TestRawValueTextAndStrings(t *testing.T) {
	value := RawValue([]byte("TEST\\PATIENT \x00"))
	if got := value.Text(); got != "TEST\\PATIENT" {
		t.Fatalf("Text() = %q, want TEST\\\\PATIENT", got)
	}

	parts := value.Strings()
	if len(parts) != 2 || parts[0] != "TEST" || parts[1] != "PATIENT" {
		t.Fatalf("Strings() = %v", parts)
	}
}

func TestStringValueKindAndEncodedLength(t *testing.T) {
	value := StringValue{"ONE", "TWO"}
	if got := value.Kind(); got != ValueStrings {
		t.Fatalf("Kind() = %v, want %v", got, ValueStrings)
	}

	length, ok := value.EncodedLength()
	if !ok || length != 8 {
		t.Fatalf("EncodedLength() = (%v, %v), want (8, true)", length, ok)
	}

	length, ok = StringValue{"ABC"}.EncodedLength()
	if !ok || length != 4 {
		t.Fatalf("odd EncodedLength() = (%v, %v), want (4, true)", length, ok)
	}
}

func TestUndefinedLengthValues(t *testing.T) {
	sequenceLength, sequenceOK := SequenceValue{}.EncodedLength()
	if sequenceOK || sequenceLength != UndefinedLength {
		t.Fatalf("SequenceValue.EncodedLength() = (%v, %v), want (%v, false)", sequenceLength, sequenceOK, UndefinedLength)
	}

	fragmentsLength, fragmentsOK := FragmentSequence{}.EncodedLength()
	if fragmentsOK || fragmentsLength != UndefinedLength {
		t.Fatalf("FragmentSequence.EncodedLength() = (%v, %v), want (%v, false)", fragmentsLength, fragmentsOK, UndefinedLength)
	}

	bulkLength, bulkOK := BulkDataValue{URI: "https://example.test/bulk"}.EncodedLength()
	if bulkOK || bulkLength != UndefinedLength {
		t.Fatalf("BulkDataValue.EncodedLength() = (%v, %v), want (%v, false)", bulkLength, bulkOK, UndefinedLength)
	}
	if got := (BulkDataValue{}).Kind(); got != ValueBulkData {
		t.Fatalf("BulkDataValue.Kind() = %v, want %v", got, ValueBulkData)
	}
}

func TestCloneBytes(t *testing.T) {
	if CloneBytes(nil) != nil {
		t.Fatalf("CloneBytes(nil) should return nil")
	}

	original := []byte{0x01, 0x02, 0x03}
	cloned := CloneBytes(original)
	if len(cloned) != len(original) {
		t.Fatalf("CloneBytes length = %d, want %d", len(cloned), len(original))
	}
	for i := range original {
		if cloned[i] != original[i] {
			t.Fatalf("CloneBytes mismatch at %d: got %d, want %d", i, cloned[i], original[i])
		}
	}

	cloned[0] = 0xFF
	if original[0] != 0x01 {
		t.Fatalf("CloneBytes should return a distinct slice")
	}
}
