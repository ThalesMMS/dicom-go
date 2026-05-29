package personname

import (
	"strings"
	"testing"
)

func TestRenderWithSeps(t *testing.T) {
	tests := []struct {
		name         string
		sections     []string
		separator    string
		nullSepLevel uint
		want         string
	}{
		{name: "no sections", separator: "^", want: ""},
		{name: "all empty no trailing", sections: []string{"", "", "", "", ""}, separator: "^", want: ""},
		{name: "all empty requested trailing", sections: []string{"", "", "", "", ""}, separator: "^", nullSepLevel: 3, want: "^^^"},
		{name: "interior nulls", sections: []string{"family", "", "middle", "", ""}, separator: "^", want: "family^^middle"},
		{name: "content and requested trailing", sections: []string{"family", "", "middle", "", ""}, separator: "^", nullSepLevel: 4, want: "family^^middle^^"},
		{name: "later content wins", sections: []string{"a", "", "c"}, separator: "=", nullSepLevel: 1, want: "a==c"},
		{name: "multi-character separator", sections: []string{"a", "", "c"}, separator: "::", want: "a::::c"},
		{name: "level beyond sections", sections: []string{"a", "", ""}, separator: "^", nullSepLevel: 100, want: "a^^"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderWithSeps(tt.sections, tt.separator, tt.nullSepLevel); got != tt.want {
				t.Fatalf("renderWithSeps() = %q, want %q", got, tt.want)
			}
		})
	}
}

func BenchmarkRenderWithSeps(b *testing.B) {
	sections := []string{
		strings.Repeat("family", 16),
		strings.Repeat("given", 16),
		"",
		strings.Repeat("prefix", 16),
		strings.Repeat("suffix", 16),
	}
	b.Run("prepend", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkRenderedPN = renderWithSepsPrepend(sections, "^", 4)
		}
	})
	b.Run("join", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkRenderedPN = renderWithSeps(sections, "^", 4)
		}
	})
}

func renderWithSepsPrepend(sections []string, separator string, nullSepLevel uint) string {
	nonZeroFound := false
	result := ""
	for i := len(sections) - 1; i >= 0; i-- {
		section := sections[i]
		result = section + result
		if section != "" {
			nonZeroFound = true
		}
		if i > 0 && (nonZeroFound || i <= int(nullSepLevel)) {
			result = separator + result
		}
	}
	return result
}

var benchmarkRenderedPN string
