package qrmatch

import (
	"fmt"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
)

func matchTemporal(vr core.VR, query, candidate string) (bool, error) {
	query = strings.TrimSpace(query)
	candidate = strings.TrimSpace(candidate)
	if query == "" {
		return true, nil
	}
	if strings.ContainsAny(query, "*?") {
		return false, fmt.Errorf("%w: wildcard is not allowed for %s matching", ErrInvalidQuery, vr)
	}
	candidateTime, candidatePrecision, err := parseTemporal(vr, candidate)
	if err != nil {
		return false, nil
	}
	candidateStart, candidateEnd := temporalWindow(candidateTime, candidatePrecision, vr)
	if !strings.Contains(query, "-") {
		queryTime, queryPrecision, parseErr := parseTemporal(vr, query)
		if parseErr != nil {
			return false, fmt.Errorf("%w: invalid %s matching value", ErrInvalidQuery, vr)
		}
		queryStart, queryEnd := temporalWindow(queryTime, queryPrecision, vr)
		return rangesOverlap(candidateStart, candidateEnd, queryStart, queryEnd), nil
	}
	if strings.Count(query, "-") != 1 {
		return false, fmt.Errorf("%w: invalid %s range", ErrInvalidQuery, vr)
	}
	bounds := strings.SplitN(query, "-", 2)
	if strings.TrimSpace(bounds[0]) == "" && strings.TrimSpace(bounds[1]) == "" {
		return false, fmt.Errorf("%w: invalid %s range", ErrInvalidQuery, vr)
	}
	lower := time.Time{}
	upper := time.Time{}
	lowerSet, upperSet := false, false
	if from := strings.TrimSpace(bounds[0]); from != "" {
		fromTime, fromPrecision, parseErr := parseTemporal(vr, from)
		if parseErr != nil {
			return false, fmt.Errorf("%w: invalid %s range", ErrInvalidQuery, vr)
		}
		lower, _ = temporalWindow(fromTime, fromPrecision, vr)
		lowerSet = true
	}
	if to := strings.TrimSpace(bounds[1]); to != "" {
		toTime, toPrecision, parseErr := parseTemporal(vr, to)
		if parseErr != nil {
			return false, fmt.Errorf("%w: invalid %s range", ErrInvalidQuery, vr)
		}
		_, upper = temporalWindow(toTime, toPrecision, vr)
		upperSet = true
	}
	if lowerSet && upperSet && lower.After(upper) {
		return false, fmt.Errorf("%w: invalid %s range", ErrInvalidQuery, vr)
	}
	if lowerSet && candidateEnd.Before(lower) {
		return false, nil
	}
	if upperSet && candidateStart.After(upper) {
		return false, nil
	}
	return true, nil
}

func parseTemporal(vr core.VR, value string) (time.Time, dcmtime.PrecisionLevel, error) {
	switch vr {
	case core.VRDA:
		parsed, err := dcmtime.ParseDate(value)
		if err != nil {
			return time.Time{}, 0, err
		}
		return parsed.Time, parsed.Precision, nil
	case core.VRTM:
		parsed, err := dcmtime.ParseTime(value)
		if err != nil {
			return time.Time{}, 0, err
		}
		return parsed.Time, parsed.Precision, nil
	case core.VRDT:
		parsed, err := dcmtime.ParseDatetime(value)
		if err != nil {
			return time.Time{}, 0, err
		}
		return parsed.Time, parsed.Precision, nil
	default:
		return time.Time{}, 0, fmt.Errorf("unsupported temporal VR %s", vr)
	}
}

func temporalWindow(value time.Time, precision dcmtime.PrecisionLevel, vr core.VR) (time.Time, time.Time) {
	loc := value.Location()
	if loc == nil {
		loc = time.UTC
	}
	switch vr {
	case core.VRDA:
		return dateWindow(value, precision, loc)
	case core.VRTM:
		return timeWindow(value, precision, loc)
	default:
		dateStart, dateEnd := dateWindow(value, precision, loc)
		if precision == dcmtime.PrecisionYear || precision == dcmtime.PrecisionMonth || precision == dcmtime.PrecisionDay {
			start := time.Date(dateStart.Year(), dateStart.Month(), dateStart.Day(), 0, 0, 0, 0, loc)
			end := time.Date(dateEnd.Year(), dateEnd.Month(), dateEnd.Day(), 23, 59, 59, 999999000, loc)
			return start, end
		}
		timeStart, timeEnd := timeWindow(value, precision, loc)
		start := time.Date(dateStart.Year(), dateStart.Month(), dateStart.Day(), timeStart.Hour(), timeStart.Minute(), timeStart.Second(), timeStart.Nanosecond(), loc)
		end := time.Date(dateEnd.Year(), dateEnd.Month(), dateEnd.Day(), timeEnd.Hour(), timeEnd.Minute(), timeEnd.Second(), timeEnd.Nanosecond(), loc)
		return start, end
	}
}

func dateWindow(value time.Time, precision dcmtime.PrecisionLevel, loc *time.Location) (time.Time, time.Time) {
	year, month, day := value.Date()
	switch precision {
	case dcmtime.PrecisionYear:
		return time.Date(year, 1, 1, 0, 0, 0, 0, loc), time.Date(year, 12, 31, 0, 0, 0, 0, loc)
	case dcmtime.PrecisionMonth:
		start := time.Date(year, month, 1, 0, 0, 0, 0, loc)
		end := start.AddDate(0, 1, -1)
		return start, end
	default:
		dayValue := time.Date(year, month, day, 0, 0, 0, 0, loc)
		return dayValue, dayValue
	}
}

func timeWindow(value time.Time, precision dcmtime.PrecisionLevel, loc *time.Location) (time.Time, time.Time) {
	base := time.Date(1, 1, 1, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), loc)
	switch precision {
	case dcmtime.PrecisionHours:
		return time.Date(1, 1, 1, value.Hour(), 0, 0, 0, loc), time.Date(1, 1, 1, value.Hour(), 59, 59, 999999000, loc)
	case dcmtime.PrecisionMinutes:
		return time.Date(1, 1, 1, value.Hour(), value.Minute(), 0, 0, loc), time.Date(1, 1, 1, value.Hour(), value.Minute(), 59, 999999000, loc)
	case dcmtime.PrecisionSeconds:
		return time.Date(1, 1, 1, value.Hour(), value.Minute(), value.Second(), 0, loc), time.Date(1, 1, 1, value.Hour(), value.Minute(), value.Second(), 999999000, loc)
	default:
		return base, base
	}
}

func rangesOverlap(aStart, aEnd, bStart, bEnd time.Time) bool {
	return !aEnd.Before(bStart) && !bEnd.Before(aStart)
}
