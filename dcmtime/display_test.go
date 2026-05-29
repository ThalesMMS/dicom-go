package dcmtime_test

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/dcmtime"
)

func TestFormatDateForDisplay(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "full DA", value: "20260605", want: "2026-06-05"},
		{name: "month precision", value: "202606", want: "2026-06"},
		{name: "year precision", value: "2026", want: "2026"},
		{name: "NEMA full DA", value: "2026.06.05", want: "2026-06-05"},
		{name: "NUL and whitespace padded", value: " \x0020260605\x00 ", want: "2026-06-05"},
		{name: "empty", value: " \x00 ", want: ""},
		{name: "already display formatted", value: "2026-06-05", want: "2026-06-05"},
		{name: "malformed", value: "2026060", want: "2026060"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dcmtime.FormatDate(tt.value); got != tt.want {
				t.Fatalf("FormatDate(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestFormatTimeForDisplay(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "seconds precision", value: "134501", want: "13:45:01"},
		{name: "minutes precision", value: "1345", want: "13:45"},
		{name: "hours precision", value: "13", want: "13"},
		{name: "fractional precision", value: "134501.25", want: "13:45:01.25"},
		{name: "NUL and whitespace padded", value: " \x00134501\x00 ", want: "13:45:01"},
		{name: "empty", value: " \x00 ", want: ""},
		{name: "already display formatted", value: "13:45:01", want: "13:45:01"},
		{name: "malformed", value: "13450", want: "13450"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dcmtime.FormatTime(tt.value); got != tt.want {
				t.Fatalf("FormatTime(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
