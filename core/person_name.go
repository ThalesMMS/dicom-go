package core

import "strings"

// PersonName represents a DICOM PN value using the five alphabetic components
// defined by the standard: family, given, middle, prefix, suffix.
type PersonName struct {
	FamilyName string
	GivenName  string
	MiddleName string
	NamePrefix string
	NameSuffix string

	componentGroups string
}

// ParsePersonName parses a single DICOM PN component group in the canonical
// family^given^middle^prefix^suffix layout.
func ParsePersonName(s string) PersonName {
	s = strings.TrimSpace(s)
	alphabetic, remainder, _ := strings.Cut(s, "=")
	parts := strings.Split(strings.TrimSpace(alphabetic), "^")
	pn := PersonName{}
	if len(parts) > 0 {
		pn.FamilyName = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		pn.GivenName = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		pn.MiddleName = strings.TrimSpace(parts[2])
	}
	if len(parts) > 3 {
		pn.NamePrefix = strings.TrimSpace(parts[3])
	}
	if len(parts) > 4 {
		pn.NameSuffix = strings.TrimSpace(parts[4])
	}
	if remainder != "" || strings.Contains(s, "=") {
		pn.componentGroups = "=" + remainder
	}
	return pn
}

// IsEmpty reports whether all five PN components are empty.
func (pn PersonName) IsEmpty() bool {
	return pn.FamilyName == "" &&
		pn.GivenName == "" &&
		pn.MiddleName == "" &&
		pn.NamePrefix == "" &&
		pn.NameSuffix == "" &&
		pn.componentGroups == ""
}

// String renders the name in display order: prefix, given, middle, family,
// suffix. Empty components are skipped.
func (pn PersonName) String() string {
	parts := make([]string, 0, 5)
	for _, part := range []string{
		pn.NamePrefix,
		pn.GivenName,
		pn.MiddleName,
		pn.FamilyName,
		pn.NameSuffix,
	} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// ToDICOMString serializes the PN value back to family^given^middle^prefix^suffix,
// omitting trailing empty components while preserving internal empty slots.
func (pn PersonName) ToDICOMString() string {
	parts := []string{
		pn.FamilyName,
		pn.GivenName,
		pn.MiddleName,
		pn.NamePrefix,
		pn.NameSuffix,
	}
	last := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			last = i
			break
		}
	}
	if last < 0 {
		return pn.componentGroups
	}
	return strings.Join(parts[:last+1], "^") + pn.componentGroups
}
