package ups

import (
	"errors"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

const (
	maxSubscriptionMatchingKeys       = 16
	maxSubscriptionValuesPerKey       = 16
	maxSubscriptionMatchingValueBytes = 4 << 10
)

// SubscriptionMatchingKey is the canonical, persistable form of one filtered
// global subscription matching key.
type SubscriptionMatchingKey struct {
	Tag    core.Tag
	Values []string
}

// SubscriptionFilter is a bounded predicate that uses the UPS C-FIND matching
// rules. Store implementations must persist and deep-clone it with the global
// subscription so future UPS instances can inherit the subscription after a
// process restart.
type SubscriptionFilter struct {
	Keys []SubscriptionMatchingKey
}

// Matches reports whether step matches this validated filter using the UPS
// C-FIND matching rules. Store implementations use it while atomically
// materializing existing or newly created UPS instance subscriptions.
func (filter *SubscriptionFilter) Matches(step Step) bool {
	return subscriptionFilterMatches(filter, step)
}

type subscriptionFilterError struct {
	status uint16
	err    error
}

func (err *subscriptionFilterError) Error() string { return ErrInvalidDataSet.Error() }

func (err *subscriptionFilterError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.err
}

func newSubscriptionFilterError(status uint16, err error) error {
	return &subscriptionFilterError{status: status, err: err}
}

func subscriptionFilterStatus(err error) uint16 {
	var filterErr *subscriptionFilterError
	if errors.As(err, &filterErr) {
		return filterErr.status
	}
	return StatusInvalidArgumentValue
}

func compileSubscriptionFilter(matchingKeys map[string][]string) (*SubscriptionFilter, error) {
	if len(matchingKeys) == 0 {
		return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
	}
	if len(matchingKeys) > maxSubscriptionMatchingKeys {
		return nil, newSubscriptionFilterError(StatusResourceLimitation, ErrResourceLimit)
	}
	byTag := make(map[core.Tag][]string, len(matchingKeys))
	totalBytes := 0
	for encodedTag, values := range matchingKeys {
		tag, err := core.ParseTag(encodedTag)
		if err != nil {
			return nil, newSubscriptionFilterError(StatusNoSuchArgument, ErrInvalidDataSet)
		}
		if _, duplicate := byTag[tag]; duplicate {
			return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
		}
		entry, supported := queryEntry(tag)
		if !supported {
			return nil, newSubscriptionFilterError(StatusNoSuchArgument, ErrInvalidDataSet)
		}
		if entry.VR == core.VRSQ || len(values) == 0 {
			return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
		}
		if len(values) > maxSubscriptionValuesPerKey {
			return nil, newSubscriptionFilterError(StatusResourceLimitation, ErrResourceLimit)
		}
		cloned := make([]string, len(values))
		for index, value := range values {
			if value == "" {
				return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
			}
			totalBytes += len(value)
			if totalBytes > maxSubscriptionMatchingValueBytes {
				return nil, newSubscriptionFilterError(StatusResourceLimitation, ErrResourceLimit)
			}
			if !validSubscriptionMatchingValue(entry.VR, value) {
				return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
			}
			cloned[index] = strings.Clone(value)
		}
		byTag[tag] = cloned
	}
	if values, present := byTag[TagSpecificCharacterSet]; present && (len(values) != 1 || strings.TrimSpace(values[0]) == "") {
		return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
	}
	if values, present := byTag[TagTimezoneOffsetFromUTC]; present && (len(values) != 1 || !validUPSTimezoneOffset(values[0])) {
		return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
	}
	predicateCount := len(byTag)
	if _, present := byTag[TagSpecificCharacterSet]; present {
		predicateCount--
	}
	if _, present := byTag[TagTimezoneOffsetFromUTC]; present {
		predicateCount--
	}
	if predicateCount == 0 {
		return nil, newSubscriptionFilterError(StatusInvalidArgumentValue, ErrInvalidDataSet)
	}
	tags := make([]core.Tag, 0, len(byTag))
	for tag := range byTag {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(left, right int) bool { return tags[left].Less(tags[right]) })
	filter := &SubscriptionFilter{Keys: make([]SubscriptionMatchingKey, 0, len(tags))}
	for _, tag := range tags {
		filter.Keys = append(filter.Keys, SubscriptionMatchingKey{Tag: tag, Values: byTag[tag]})
	}
	return filter, nil
}

func validSubscriptionMatchingValue(vr core.VR, value string) bool {
	if vr == core.VRUI && strings.ContainsAny(value, "*?") {
		return false
	}
	if vr != core.VRDA && vr != core.VRDT && vr != core.VRTM {
		return true
	}
	if _, _, rangeOK := parseTemporalRange(vr, value, ""); rangeOK {
		return true
	}
	_, valueOK := parseTemporalValue(vr, value, "")
	return valueOK
}

func cloneSubscriptionFilter(filter *SubscriptionFilter) *SubscriptionFilter {
	if filter == nil {
		return nil
	}
	clone := &SubscriptionFilter{Keys: make([]SubscriptionMatchingKey, len(filter.Keys))}
	for index, key := range filter.Keys {
		clone.Keys[index] = SubscriptionMatchingKey{Tag: key.Tag, Values: append([]string(nil), key.Values...)}
	}
	return clone
}

func subscriptionFilterMatches(filter *SubscriptionFilter, step Step) bool {
	if filter == nil || len(filter.Keys) == 0 {
		return false
	}
	queryTimezone := ""
	for _, key := range filter.Keys {
		if key.Tag == TagTimezoneOffsetFromUTC && len(key.Values) == 1 {
			queryTimezone = key.Values[0]
			break
		}
	}
	candidateTimezone := ""
	if value, present := dataSetString(step.Attributes, TagTimezoneOffsetFromUTC); present {
		candidateTimezone = value
	}
	for _, key := range filter.Keys {
		if key.Tag == TagSpecificCharacterSet || key.Tag == TagTimezoneOffsetFromUTC {
			continue
		}
		element, present := dataSetElement(step.Attributes, key.Tag)
		if !present || !queryValuesMatch(element, key.Values, queryTimezone, candidateTimezone) {
			return false
		}
	}
	return true
}
