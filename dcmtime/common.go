package dcmtime

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// isIncluded returns whether `check` is included in `limit`. To be "included", the
// value must be of equal or lesser precision.
//
// For instance, if `check` is PrecisionSeconds, then minutes, hours, days, months and
// years would return true, but milliseconds would return false, since when printing a
// DT value with a seconds precision, all of said values would be included in the
// rendered DT value: '20210723121456' (YYYYMMDDHHMMSS).
//
// Example: to test whether seconds should be included, you would:
// isIncluded(PrecisionSeconds, [caller-passed-limit])
func isIncluded(check PrecisionLevel, limit PrecisionLevel) bool {
	return check >= limit
}

// truncateMilliseconds truncate nanosecond time.Time value to arbitrary precision.
func truncateMilliseconds(nanoSeconds int, precision PrecisionLevel) (millis string) {
	builder := strings.Builder{}
	builder.Grow(6)
	writeTruncatedMicroseconds(&builder, nanoSeconds, precision)
	return builder.String()
}

func writeZeroPaddedInt(builder *strings.Builder, value int, width int) {
	if width <= 0 {
		builder.WriteString(strconv.Itoa(value))
		return
	}
	value64 := int64(value)
	if value64 < 0 {
		builder.WriteByte('-')
		value64 = -value64
		width--
	}
	if width <= 0 {
		builder.WriteString(strconv.FormatInt(value64, 10))
		return
	}

	limit := int64(1)
	for i := 0; i < width; i++ {
		limit *= 10
	}
	if value64 >= limit {
		builder.WriteString(strconv.FormatInt(value64, 10))
		return
	}

	var buf [20]byte
	for i := width - 1; i >= 0; i-- {
		buf[i] = byte('0' + value64%10)
		value64 /= 10
	}
	builder.Write(buf[:width])
}

func writeTruncatedMicroseconds(builder *strings.Builder, nanoSeconds int, precision PrecisionLevel) {
	digits := 6 - int(PrecisionFull+precision)
	if digits <= 0 {
		return
	}

	microseconds := nanoSeconds / 1000
	divisor := 100000
	for i := 0; i < digits; i++ {
		digit := microseconds / divisor
		builder.WriteByte(byte('0' + digit))
		microseconds %= divisor
		divisor /= 10
	}
}

// Const for time.Time timezone. We don't want a timezone because we don't REALLY know
// that a TM or DA value is UTC, so we don't want UTC to be made explicit.
var zeroTimezone = time.FixedZone("", 0)

// durationInfo holds parsed piece of DA, TM, or DT info.
type durationInfo struct {
	// Value is the parsed int value.
	Value int
	// PresentInSource is whether it was present in the string.
	PresentInSource bool
	// FractalPrecision is only used when parsing Fractal seconds: the level of
	// precision used.
	FractalPrecision PrecisionLevel
}

// extractDurationInfo extracts a piece of DA, TM, or DT info from a parsed regex,
// handling all validation checks, empty values, etc.
func extractDurationInfo(subMatches []string, index int, isFractal bool) (durationInfo, error) {
	info := durationInfo{}

	// If the submatch array does not contain the group index we are looking for, then
	// we need to return a parsing error.
	if len(subMatches) <= index {
		return info, errors.New("not enough sub-matches")
	}

	// Get the value of our capture group.
	valueStr := subMatches[index]

	// If there was no match for the specific subgroup, this value is 0, and we leave
	// info.PresentInSource as false.
	if valueStr != "" {
		// Otherwise set the info.PresentInSource to true
		info.PresentInSource = true
	}

	// If this is a isFractal seconds value, we need to pad it out to nanoseconds for
	// go.
	if info.PresentInSource && isFractal {
		// The isFractal value can be any length, with truncation implying a loss of
		// precision, we need to add trailing zeros to the hundred-millionth place to
		// get our nano-seconds.
		missingPlaces := 9 - len(valueStr)
		valueStr = valueStr + strings.Repeat("0", missingPlaces)
		info.FractalPrecision = PrecisionFull + PrecisionLevel(missingPlaces-3)
	}

	// If our info is present, parse the value into an int.
	if info.PresentInSource {
		var err error
		info.Value, err = strconv.Atoi(valueStr)
		if err != nil {
			return info, fmt.Errorf("error parsing int: %w", err)
		}
	}

	return info, nil
}

// updatePrecision takes in our current precision and a piece of parsed info, then
// updates the precision based on whether the info was present.
//
// levelIsFull should be set to true if the level we are checking is "Full" for the
// type of value we are parsing. For instance, PrecisionHours would be the full
// precision of a TM value, and we'll use PrecisionFull instead.
func updatePrecision(info durationInfo, current, infoLevel PrecisionLevel, levelIsFull bool) PrecisionLevel {
	// If the info was not present, return our current level.
	if !info.PresentInSource {
		return current
	}

	if levelIsFull {
		return PrecisionFull
	}
	return infoLevel
}

func validDateComponents(year, month, day durationInfo) bool {
	if month.Value < 1 || month.Value > 12 || day.Value < 1 {
		return false
	}
	lastDay := time.Date(year.Value, time.Month(month.Value)+1, 0, 0, 0, 0, 0, zeroTimezone).Day()
	return day.Value <= lastDay
}

func validTimeComponents(hours, minutes, seconds, fraction durationInfo) bool {
	if hours.Value < 0 || hours.Value > 23 {
		return false
	}
	if minutes.Value < 0 || minutes.Value > 59 {
		return false
	}
	if seconds.Value < 0 || seconds.Value > 60 {
		return false
	}
	return !fraction.PresentInSource || seconds.PresentInSource
}

func validUTCOffset(sign string, hours, minutes durationInfo) bool {
	if !hours.PresentInSource {
		return !minutes.PresentInSource
	}
	if !minutes.PresentInSource || minutes.Value < 0 || minutes.Value > 59 {
		return false
	}
	switch sign {
	case "+":
		return hours.Value < 14 || hours.Value == 14 && minutes.Value == 0
	case "-":
		if hours.Value == 0 && minutes.Value == 0 {
			return false
		}
		return hours.Value < 12 || hours.Value == 12 && minutes.Value == 0
	default:
		return false
	}
}
