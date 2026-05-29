package personname

import "strings"

// Display parses a DICOM PN value and returns a compact display string. The
// alphabetic group is preferred, with ideographic and phonetic groups used as
// fallbacks when earlier groups are empty.
func Display(value string) string {
	value = cleanDisplayValue(value)
	if value == "" {
		return ""
	}
	info, err := Parse(value)
	if err != nil {
		return displaySegments(strings.Split(strings.ReplaceAll(value, groupSep, segmentSep), segmentSep))
	}
	return info.Display()
}

// Display returns a compact display string for the first non-empty PN group,
// preferring alphabetic, then ideographic, then phonetic.
func (info Info) Display() string {
	for _, group := range []GroupInfo{info.Alphabetic, info.Ideographic, info.Phonetic} {
		if display := group.Display(); display != "" {
			return display
		}
	}
	return ""
}

// Display returns a compact display string for the group's non-empty
// family/given/middle/prefix/suffix segments in DICOM segment order.
func (group GroupInfo) Display() string {
	return displaySegments([]string{
		group.FamilyName,
		group.GivenName,
		group.MiddleName,
		group.NamePrefix,
		group.NameSuffix,
	})
}

func cleanDisplayValue(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	return strings.TrimSpace(value)
}

func displaySegments(segments []string) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		for _, field := range strings.Fields(segment) {
			parts = append(parts, field)
		}
	}
	return strings.Join(parts, " ")
}
