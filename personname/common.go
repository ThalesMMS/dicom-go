package personname

import "strings"

// renderWithSeps is a generalized function for rendering PN DCM() string with the
// correct level of null separators.
func renderWithSeps(sections []string, separator string, nullSepLevel uint) string {
	if len(sections) == 0 {
		return ""
	}
	last := len(sections) - 1
	if uint(last) > nullSepLevel {
		last = int(nullSepLevel)
	}
	for i := len(sections) - 1; i > last; i-- {
		if sections[i] != "" {
			last = i
			break
		}
	}
	return strings.Join(sections[:last+1], separator)
}
