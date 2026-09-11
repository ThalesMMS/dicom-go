package qrmatch

import (
	"strings"
	"unicode/utf8"

	"github.com/ThalesMMS/dicom-go/core"
)

// WildcardAllowed reports whether PS3.4 C.2.2.2.4 wild-card matching applies
// to vr. UI, DA, TM, DT and binary VRs are excluded.
func WildcardAllowed(vr core.VR) bool {
	switch vr {
	case core.VRAE, core.VRCS, core.VRLO, core.VRLT, core.VRPN, core.VRSH, core.VRST, core.VRUC, core.VRUR, core.VRUT:
		return true
	default:
		return false
	}
}

func matchWildcard(pattern, value string, limits Limits, steps *int) (bool, error) {
	return matchWildcardRunes([]rune(pattern), []rune(value), limits, steps)
}

func matchWildcardRunes(pattern, value []rune, limits Limits, steps *int) (bool, error) {
	patternIndex, valueIndex := 0, 0
	starIndex, starValueIndex := -1, 0
	for valueIndex < len(value) {
		if err := countWildcardStep(limits, steps); err != nil {
			return false, err
		}
		if patternIndex < len(pattern) && (pattern[patternIndex] == '?' || pattern[patternIndex] == value[valueIndex]) {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			starValueIndex = valueIndex
			patternIndex++
			continue
		}
		if starIndex == -1 {
			return false, nil
		}
		patternIndex = starIndex + 1
		starValueIndex++
		valueIndex = starValueIndex
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		if err := countWildcardStep(limits, steps); err != nil {
			return false, err
		}
		patternIndex++
	}
	return patternIndex == len(pattern), nil
}

func countWildcardStep(limits Limits, steps *int) error {
	if steps == nil {
		return nil
	}
	*steps++
	if *steps > limits.MaxWildcardSteps {
		return ErrResourceLimit
	}
	return nil
}

func valueByteLen(values []string) int {
	n := 0
	for _, value := range values {
		n += utf8.RuneCountInString(value)
	}
	return n
}

func allValuesEmpty(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}
