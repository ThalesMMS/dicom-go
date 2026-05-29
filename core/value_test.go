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

func TestTypedNumericValueKindsAndEncodedLengths(t *testing.T) {
	tests := []struct {
		value Value
		kind  ValueKind
		want  Length
	}{
		{Uint16Value{1, 2}, ValueUint16, 4},
		{Int16Value{-1, 2}, ValueInt16, 4},
		{Uint32Value{1, 2}, ValueUint32, 8},
		{Int32Value{-1, 2}, ValueInt32, 8},
		{Uint64Value{1, 2}, ValueUint64, 16},
		{Int64Value{-1, 2}, ValueInt64, 16},
		{Float32Value{1, 2}, ValueFloat32, 8},
		{Float64Value{1, 2}, ValueFloat64, 16},
		{TagValue{NewTag(0x0010, 0x0020), NewTag(0x0020, 0x000D)}, ValueTag, 8},
	}
	for _, tt := range tests {
		if got := tt.value.Kind(); got != tt.kind {
			t.Errorf("%T.Kind() = %v, want %v", tt.value, got, tt.kind)
		}
		if got, ok := tt.value.EncodedLength(); !ok || got != tt.want {
			t.Errorf("%T.EncodedLength() = %v,%v, want %v,true", tt.value, got, ok, tt.want)
		}
	}
}

func BenchmarkStringValueEncodedLengthManyComponents(b *testing.B) {
	value := make(StringValue, 256)
	for i := range value {
		value[i] = "ABCDE"
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		length, ok := value.EncodedLength()
		if !ok || length == 0 {
			b.Fatalf("EncodedLength() = (%v, %v), want nonzero defined length", length, ok)
		}
	}
}

func TestUndefinedLengthValues(t *testing.T) {
	if got := (SequenceValue{}).Kind(); got != ValueSequence {
		t.Fatalf("SequenceValue.Kind() = %v, want %v", got, ValueSequence)
	}
	sequenceLength, sequenceOK := SequenceValue{}.EncodedLength()
	if sequenceOK || sequenceLength != UndefinedLength {
		t.Fatalf("SequenceValue.EncodedLength() = (%v, %v), want (%v, false)", sequenceLength, sequenceOK, UndefinedLength)
	}

	if got := (FragmentSequence{}).Kind(); got != ValueFragments {
		t.Fatalf("FragmentSequence.Kind() = %v, want %v", got, ValueFragments)
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
