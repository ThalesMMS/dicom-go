package personname_test

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/personname"
)

func TestDisplay(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "alphabetic components", value: "DOE^JANE", want: "DOE JANE"},
		{name: "collapses empty and whitespace components", value: " DOE ^ JANE ^^ Dr ^ PhD ", want: "DOE JANE Dr PhD"},
		{name: "uses alphabetic group without appending ideographic group", value: "DOE^JANE=山田^太郎", want: "DOE JANE"},
		{name: "falls back to ideographic group", value: "=山田^太郎", want: "山田 太郎"},
		{name: "falls back to phonetic group", value: "==YAMADA^TARO", want: "YAMADA TARO"},
		{name: "removes NULs", value: "\x00DOE^JANE \x00", want: "DOE JANE"},
		{name: "empty", value: " \x00 ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := personname.Display(tt.value); got != tt.want {
				t.Fatalf("Display(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestDisplayMethods(t *testing.T) {
	info, err := personname.Parse("DOE^JANE=山田^太郎")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Display(); got != "DOE JANE" {
		t.Fatalf("Info.Display() = %q, want DOE JANE", got)
	}
	if got := info.Ideographic.Display(); got != "山田 太郎" {
		t.Fatalf("GroupInfo.Display() = %q, want 山田 太郎", got)
	}
}
