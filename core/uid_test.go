package core

import (
	"strings"
	"testing"
)

func TestIsValidUID(t *testing.T) {
	valid64 := "2." + strings.Repeat("1", 62)
	tests := []struct {
		name string
		uid  string
		want bool
	}{
		{name: "root zero", uid: "0.0", want: true},
		{name: "root one maximum second arc", uid: "1.39.0", want: true},
		{name: "root two unrestricted second arc", uid: "2.999.1", want: true},
		{name: "standard storage UID", uid: "1.2.840.10008.5.1.4.1.1.2", want: true},
		{name: "maximum length", uid: valid64, want: true},
		{name: "empty", uid: "", want: false},
		{name: "one component", uid: "1", want: false},
		{name: "invalid root arc", uid: "3.1", want: false},
		{name: "root zero second arc too large", uid: "0.40", want: false},
		{name: "root one second arc too large", uid: "1.40", want: false},
		{name: "leading zero root", uid: "01.2", want: false},
		{name: "leading zero component", uid: "1.02", want: false},
		{name: "empty component", uid: "1..2", want: false},
		{name: "leading dot", uid: ".1.2", want: false},
		{name: "trailing dot", uid: "1.2.", want: false},
		{name: "non-digit", uid: "1.a.2", want: false},
		{name: "space padding is not syntax", uid: "1.2 ", want: false},
		{name: "NUL padding is not syntax", uid: "1.2\x00", want: false},
		{name: "over maximum length", uid: valid64 + "1", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsValidUID(test.uid); got != test.want {
				t.Fatalf("IsValidUID(%q) = %v, want %v", test.uid, got, test.want)
			}
		})
	}
}

func TestIsValidUIDKeepsPaddingNormalizationExplicit(t *testing.T) {
	const padded = "1.2.840.10008 \x00"
	if IsValidUID(padded) {
		t.Fatal("IsValidUID accepted encoded padding")
	}
	if normalized := NormalizeUID(padded); normalized != "1.2.840.10008" || !IsValidUID(normalized) {
		t.Fatalf("NormalizeUID(%q) = %q, want valid canonical UID", padded, normalized)
	}
}
