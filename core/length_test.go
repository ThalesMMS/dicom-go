package core

import "testing"

func TestLengthIsUndefined(t *testing.T) {
	if !UndefinedLength.IsUndefined() {
		t.Fatalf("UndefinedLength.IsUndefined() = false, want true")
	}
	if Length(100).IsUndefined() {
		t.Fatalf("Length(100).IsUndefined() = true, want false")
	}
	if Length(0).IsUndefined() {
		t.Fatalf("Length(0).IsUndefined() = true, want false")
	}
}

func TestLengthIsDefinedComplementsIsUndefined(t *testing.T) {
	for _, length := range []Length{0, 1, 100, UndefinedLength} {
		if got, want := length.IsDefined(), !length.IsUndefined(); got != want {
			t.Fatalf("%v.IsDefined() = %v, want %v", length, got, want)
		}
	}
}

func TestLengthValue(t *testing.T) {
	if value, ok := Length(100).Value(); !ok || value != 100 {
		t.Fatalf("Length(100).Value() = (%d, %v), want (100, true)", value, ok)
	}
	if value, ok := Length(0).Value(); !ok || value != 0 {
		t.Fatalf("Length(0).Value() = (%d, %v), want (0, true)", value, ok)
	}
	if value, ok := UndefinedLength.Value(); ok || value != 0 {
		t.Fatalf("UndefinedLength.Value() = (%d, %v), want (0, false)", value, ok)
	}
}

func TestLengthString(t *testing.T) {
	if got := UndefinedLength.String(); got != "UNDEFINED" {
		t.Fatalf("UndefinedLength.String() = %q, want %q", got, "UNDEFINED")
	}
	if got := Length(100).String(); got != "100" {
		t.Fatalf("Length(100).String() = %q, want %q", got, "100")
	}
	if got := Length(0).String(); got != "0" {
		t.Fatalf("Length(0).String() = %q, want %q", got, "0")
	}
}
