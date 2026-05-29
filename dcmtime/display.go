package dcmtime

import "strings"

// FormatDate formats a DICOM DA value for display. Valid DICOM and legacy NEMA
// date strings are parsed and rendered as YYYY, YYYY-MM, or YYYY-MM-DD according
// to their precision. Invalid values are returned after display cleanup.
func FormatDate(value string) string {
	value = cleanDisplayValue(value)
	if value == "" {
		return ""
	}
	date, err := ParseDate(value)
	if err != nil {
		return value
	}
	return date.String()
}

// FormatTime formats a DICOM TM value for display. Valid values are parsed and
// rendered as HH, HH:MM, HH:MM:SS, or HH:MM:SS.ffffff according to their
// precision. Invalid values are returned after display cleanup.
func FormatTime(value string) string {
	value = cleanDisplayValue(value)
	if value == "" {
		return ""
	}
	tm, err := ParseTime(value)
	if err != nil {
		return value
	}
	return tm.String()
}

func cleanDisplayValue(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	return strings.TrimSpace(value)
}
