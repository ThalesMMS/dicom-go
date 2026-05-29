package core

import "testing"

func TestCleanTextValue(t *testing.T) {
	tests := []struct {
		name  string
		vr    VR
		value string
		want  string
	}{
		{name: "removes NUL bytes and trims whitespace", vr: VRLO, value: " \x00 General Hospital \x00 ", want: "General Hospital"},
		{name: "keeps interior spacing", vr: VRLO, value: "LEFT  RIGHT  ", want: "LEFT  RIGHT"},
		{name: "cleans person name without changing separators", vr: VRPN, value: "\x00DOE^JANE \x00", want: "DOE^JANE"},
		{name: "uses UID padding rules", vr: VRUI, value: " 1.2.3\x00 ", want: "1.2.3"},
		{name: "empty", vr: VRLO, value: " \x00 ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanTextValue(tt.vr, tt.value); got != tt.want {
				t.Fatalf("CleanTextValue(%s, %q) = %q, want %q", tt.vr, tt.value, got, tt.want)
			}
		})
	}
}

func TestNormalizeUID(t *testing.T) {
	tests := []struct {
		name string
		uid  string
		want string
	}{
		{name: "trailing space and NUL padding", uid: "1.2.3 \x00", want: "1.2.3"},
		{name: "mixed trailing padding", uid: "1.2.3\x00 \x00 ", want: "1.2.3"},
		{name: "blank padding only", uid: " \x00", want: ""},
		{name: "leading padding is preserved", uid: " 1.2.3", want: " 1.2.3"},
		{name: "interior padding bytes are preserved", uid: "1.2\x003 4", want: "1.2\x003 4"},
		{name: "empty", uid: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeUID(tt.uid); got != tt.want {
				t.Fatalf("NormalizeUID(%q) = %q, want %q", tt.uid, got, tt.want)
			}
		})
	}
}
