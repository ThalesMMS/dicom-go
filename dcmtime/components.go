package dcmtime

import (
	"errors"
	"strings"
	"time"
)

var (
	errParseFullDA  = errors.New("date must be a full-precision DICOM DA value")
	errParseValidTM = errors.New("time must be a valid DICOM TM value")
)

// ParseDA returns the time value of a full-precision DICOM DA component.
func ParseDA(value string) (time.Time, error) {
	date, err := ParseDate(cleanComponentValue(value))
	if err != nil || date.IsNEMA || (date.Precision != PrecisionFull && date.Precision != PrecisionDay) {
		return time.Time{}, errParseFullDA
	}
	return date.Time, nil
}

// ParseTM returns the time value of a valid DICOM TM component at any legal
// precision. Omitted minute or second components are represented as zero.
func ParseTM(value string) (time.Time, error) {
	tm, err := ParseTime(cleanComponentValue(value))
	if err != nil {
		return time.Time{}, errParseValidTM
	}
	return tm.Time, nil
}

func cleanComponentValue(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
}
