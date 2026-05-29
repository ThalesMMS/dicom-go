package dimse

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomencoding "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/validation"
)

const ModalityWorklistFindSOPClassUID = "1.2.840.10008.5.1.4.31"

// ModalityWorklistFindPresentationContext returns the default MWL C-FIND
// presentation context proposal.
func ModalityWorklistFindPresentationContext() ul.PresentationContext {
	return presentationContextFor(ModalityWorklistFindSOPClassUID, DefaultFindTransferSyntaxes)
}

var (
	tagMWLSpecificCharacterSet              = core.NewTag(0x0008, 0x0005)
	tagMWLTimezoneOffsetFromUTC             = core.NewTag(0x0008, 0x0201)
	tagMWLAccessionNumber                   = core.NewTag(0x0008, 0x0050)
	tagMWLModality                          = core.NewTag(0x0008, 0x0060)
	tagMWLPatientName                       = core.NewTag(0x0010, 0x0010)
	tagMWLPatientID                         = core.NewTag(0x0010, 0x0020)
	tagMWLRequestedProcedureDescription     = core.NewTag(0x0032, 0x1060)
	tagMWLScheduledStationAETitle           = core.NewTag(0x0040, 0x0001)
	tagMWLScheduledProcedureStepStartDate   = core.NewTag(0x0040, 0x0002)
	tagMWLScheduledProcedureStepStartTime   = core.NewTag(0x0040, 0x0003)
	tagMWLScheduledPerformingPhysicianName  = core.NewTag(0x0040, 0x0006)
	tagMWLScheduledProcedureStepDescription = core.NewTag(0x0040, 0x0007)
	tagMWLScheduledProtocolCodeSequence     = core.NewTag(0x0040, 0x0008)
	tagMWLScheduledProcedureStepID          = core.NewTag(0x0040, 0x0009)
	tagMWLScheduledStationName              = core.NewTag(0x0040, 0x0010)
	tagMWLScheduledProcedureStepLocation    = core.NewTag(0x0040, 0x0011)
	tagMWLScheduledProcedureStepSequence    = core.NewTag(0x0040, 0x0100)
	tagMWLRequestedProcedureID              = core.NewTag(0x0040, 0x1001)
)

// MWLKey preserves the three states required by MWL matching: absent,
// present with an empty value (universal matching/return key), and present
// with one or more matching values.
type MWLKey struct {
	Present bool
	Values  []string
}

// MWLMatch constructs a present MWL matching key. Values are cloned.
func MWLMatch(values ...string) MWLKey {
	return MWLKey{Present: true, Values: append([]string(nil), values...)}
}

// MWLReturnKey constructs a present zero-length key for universal matching and
// return-key selection.
func MWLReturnKey() MWLKey {
	return MWLMatch("")
}

// ModalityWorklistScheduledProcedureStep contains supported keys inside the
// single Scheduled Procedure Step Sequence item used by an MWL C-FIND query.
type ModalityWorklistScheduledProcedureStep struct {
	ScheduledStationAETitle           MWLKey
	Modality                          MWLKey
	ScheduledProcedureStepStartDate   MWLKey
	ScheduledProcedureStepStartTime   MWLKey
	ScheduledPerformingPhysicianName  MWLKey
	ScheduledProcedureStepDescription MWLKey
	ScheduledProcedureStepID          MWLKey
	ScheduledStationName              MWLKey
	ScheduledProcedureStepLocation    MWLKey
}

// ModalityWorklistQuery is the typed Identifier model supported by the MWL
// helpers. The zero value omits every key. A non-nil ScheduledProcedureStep
// emits exactly one sequence item.
type ModalityWorklistQuery struct {
	SpecificCharacterSet          []string
	TimezoneOffsetFromUTC         string
	PatientName                   MWLKey
	PatientID                     MWLKey
	AccessionNumber               MWLKey
	RequestedProcedureID          MWLKey
	RequestedProcedureDescription MWLKey
	ScheduledProcedureStep        *ModalityWorklistScheduledProcedureStep
}

// BuildModalityWorklistIdentifier builds an MWL C-FIND Identifier without
// collapsing absent and present-empty keys.
func BuildModalityWorklistIdentifier(query ModalityWorklistQuery) (*object.Object, error) {
	elements := make([]core.Element, 0, 8)
	if query.TimezoneOffsetFromUTC != "" && !modalityWorklistQueryHasTemporalKey(query) {
		return nil, fmt.Errorf("dicom dimse: MWL Timezone Offset From UTC requires a DA or TM key")
	}
	if len(query.SpecificCharacterSet) > 0 {
		if _, err := dicomencoding.ParseCharacterSet(query.SpecificCharacterSet...); err != nil {
			return nil, fmt.Errorf("dicom dimse: invalid MWL Specific Character Set")
		}
		element := core.Element{
			Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS},
			Value:  core.StringValue(append([]string(nil), query.SpecificCharacterSet...)),
		}
		if err := validateMWLQueryElement(element); err != nil {
			return nil, err
		}
		elements = append(elements, element)
	}
	if query.TimezoneOffsetFromUTC != "" {
		if err := validateMWLTimezoneOffset(query.TimezoneOffsetFromUTC); err != nil {
			return nil, err
		}
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: tagMWLTimezoneOffsetFromUTC, VR: core.VRSH},
			Value:  core.StringValue{query.TimezoneOffsetFromUTC},
		})
	}
	var err error
	elements, err = appendMWLKey(elements, tagMWLPatientName, core.VRPN, query.PatientName, false, mwlMatchWildcard)
	if err != nil {
		return nil, err
	}
	elements, err = appendMWLKey(elements, tagMWLPatientID, core.VRLO, query.PatientID, false, mwlMatchSingle)
	if err != nil {
		return nil, err
	}
	elements, err = appendMWLKey(elements, tagMWLAccessionNumber, core.VRSH, query.AccessionNumber, false, mwlMatchSingle)
	if err != nil {
		return nil, err
	}
	elements, err = appendMWLKey(elements, tagMWLRequestedProcedureID, core.VRSH, query.RequestedProcedureID, false, mwlMatchSingle)
	if err != nil {
		return nil, err
	}
	elements, err = appendMWLKey(elements, tagMWLRequestedProcedureDescription, core.VRLO, query.RequestedProcedureDescription, false, mwlMatchSingle)
	if err != nil {
		return nil, err
	}
	if query.ScheduledProcedureStep != nil {
		item, itemErr := buildModalityWorklistScheduledProcedureStep(*query.ScheduledProcedureStep)
		if itemErr != nil {
			return nil, itemErr
		}
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{Elements: item}}},
		})
	}
	return object.FromElements(elements, std.Dictionary), nil
}

func modalityWorklistQueryHasTemporalKey(query ModalityWorklistQuery) bool {
	return query.ScheduledProcedureStep != nil &&
		(query.ScheduledProcedureStep.ScheduledProcedureStepStartDate.Present ||
			query.ScheduledProcedureStep.ScheduledProcedureStepStartTime.Present)
}

func validateMWLTimezoneOffset(value string) error {
	if len(value) != 5 || (value[0] != '+' && value[0] != '-') {
		return fmt.Errorf("dicom dimse: invalid MWL Timezone Offset From UTC")
	}
	hours, hourErr := strconv.Atoi(value[1:3])
	minutes, minuteErr := strconv.Atoi(value[3:5])
	if hourErr != nil || minuteErr != nil || minutes > 59 {
		return fmt.Errorf("dicom dimse: invalid MWL Timezone Offset From UTC")
	}
	offset := hours*60 + minutes
	if value[0] == '-' {
		offset = -offset
	}
	if offset < -12*60 || offset > 14*60 || value == "-0000" {
		return fmt.Errorf("dicom dimse: invalid MWL Timezone Offset From UTC")
	}
	return nil
}

func buildModalityWorklistScheduledProcedureStep(step ModalityWorklistScheduledProcedureStep) ([]core.Element, error) {
	elements := make([]core.Element, 0, 9)
	keys := []struct {
		tag       core.Tag
		vr        core.VR
		key       MWLKey
		allowMany bool
		kind      mwlMatchingKind
	}{
		{tagMWLScheduledStationAETitle, core.VRAE, step.ScheduledStationAETitle, false, mwlMatchSingle},
		{tagMWLModality, core.VRCS, step.Modality, false, mwlMatchSingle},
		{tagMWLScheduledProcedureStepStartDate, core.VRDA, step.ScheduledProcedureStepStartDate, false, mwlMatchDateRange},
		{tagMWLScheduledProcedureStepStartTime, core.VRTM, step.ScheduledProcedureStepStartTime, false, mwlMatchTimeRange},
		{tagMWLScheduledPerformingPhysicianName, core.VRPN, step.ScheduledPerformingPhysicianName, false, mwlMatchWildcard},
		{tagMWLScheduledProcedureStepDescription, core.VRLO, step.ScheduledProcedureStepDescription, false, mwlMatchSingle},
		{tagMWLScheduledProcedureStepID, core.VRSH, step.ScheduledProcedureStepID, false, mwlMatchSingle},
		{tagMWLScheduledStationName, core.VRSH, step.ScheduledStationName, false, mwlMatchSingle},
		{tagMWLScheduledProcedureStepLocation, core.VRSH, step.ScheduledProcedureStepLocation, false, mwlMatchSingle},
	}
	for _, key := range keys {
		var err error
		elements, err = appendMWLKey(elements, key.tag, key.vr, key.key, key.allowMany, key.kind)
		if err != nil {
			return nil, err
		}
	}
	return elements, nil
}

func appendMWLKey(elements []core.Element, tag core.Tag, vr core.VR, key MWLKey, allowMany bool, kind mwlMatchingKind) ([]core.Element, error) {
	if !key.Present {
		return elements, nil
	}
	if len(key.Values) == 0 {
		return nil, fmt.Errorf("dicom dimse: MWL key %s is present without a value", tag)
	}
	if !allowMany && len(key.Values) != 1 {
		return nil, fmt.Errorf("dicom dimse: MWL key %s requires VM 1", tag)
	}
	for _, value := range key.Values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch kind {
		case mwlMatchSingle:
			if strings.ContainsAny(value, "*?") {
				return nil, fmt.Errorf("dicom dimse: wildcard is not supported for MWL key %s", tag)
			}
		case mwlMatchDateRange:
			if err := validateMWLTemporalQuery(value, core.VRDA); err != nil {
				return nil, err
			}
		case mwlMatchTimeRange:
			if err := validateMWLTemporalQuery(value, core.VRTM); err != nil {
				return nil, err
			}
		}
	}
	element := core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue(append([]string(nil), key.Values...)),
	}
	if kind != mwlMatchDateRange && kind != mwlMatchTimeRange {
		if err := validateMWLQueryElement(element); err != nil {
			return nil, err
		}
	}
	return append(elements, element), nil
}

func validateMWLQueryElement(element core.Element) error {
	if _, err := validation.ValidateElement(context.Background(), element, validation.Options{
		Mode:        validation.ModeStrict,
		Dictionary:  std.Dictionary,
		MaxFindings: 1,
		MaxDepth:    2,
		MaxElements: 4,
		StopFirst:   true,
	}); err != nil {
		return fmt.Errorf("dicom dimse: MWL key %s has invalid value", element.Tag())
	}
	return nil
}

type mwlMatchingKind uint8

const (
	mwlMatchSingle mwlMatchingKind = iota
	mwlMatchWildcard
	mwlMatchDateRange
	mwlMatchTimeRange
)

// MatchModalityWorklist reports whether candidate satisfies every present,
// non-universal key in query. Non-PN single-value matching is case-sensitive;
// supported PN keys use a documented case-insensitive literal/wildcard policy;
// DA and TM keys support closed and open ranges.
func MatchModalityWorklist(query ModalityWorklistQuery, candidate *object.Object) (bool, error) {
	if candidate == nil {
		return false, fmt.Errorf("dicom dimse: nil MWL candidate")
	}
	if candidateOffset, ok := candidate.GetString(tagMWLTimezoneOffsetFromUTC); ok {
		if err := validateMWLTimezoneOffset(candidateOffset); err != nil {
			return false, fmt.Errorf("dicom dimse: invalid MWL candidate Timezone Offset From UTC")
		}
		if query.TimezoneOffsetFromUTC != "" && candidateOffset != query.TimezoneOffsetFromUTC {
			return false, nil
		}
	}
	topLevel := []struct {
		key  MWLKey
		tag  core.Tag
		kind mwlMatchingKind
	}{
		{query.PatientName, tagMWLPatientName, mwlMatchWildcard},
		{query.PatientID, tagMWLPatientID, mwlMatchSingle},
		{query.AccessionNumber, tagMWLAccessionNumber, mwlMatchSingle},
		{query.RequestedProcedureID, tagMWLRequestedProcedureID, mwlMatchSingle},
		{query.RequestedProcedureDescription, tagMWLRequestedProcedureDescription, mwlMatchSingle},
	}
	for _, key := range topLevel {
		matched, err := matchMWLKey(key.key, candidate, key.tag, key.kind)
		if err != nil || !matched {
			return matched, err
		}
	}
	if query.ScheduledProcedureStep == nil {
		return true, nil
	}
	items, ok := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
	if !ok || len(items) == 0 {
		return false, nil
	}
	if len(items) != 1 {
		return false, fmt.Errorf("dicom dimse: MWL candidate requires exactly one Scheduled Procedure Step item")
	}
	return matchModalityWorklistScheduledProcedureStep(*query.ScheduledProcedureStep, items[0])
}

func matchModalityWorklistScheduledProcedureStep(query ModalityWorklistScheduledProcedureStep, candidate *object.Object) (bool, error) {
	combineTemporal := mwlKeyUsesRangeMatching(query.ScheduledProcedureStepStartDate) && mwlKeyUsesRangeMatching(query.ScheduledProcedureStepStartTime)
	if combineTemporal {
		matched, err := matchMWLCombinedDateTime(query.ScheduledProcedureStepStartDate, query.ScheduledProcedureStepStartTime, candidate)
		if err != nil || !matched {
			return matched, err
		}
	}
	keys := []struct {
		key  MWLKey
		tag  core.Tag
		kind mwlMatchingKind
	}{
		{query.ScheduledStationAETitle, tagMWLScheduledStationAETitle, mwlMatchSingle},
		{query.Modality, tagMWLModality, mwlMatchSingle},
		{query.ScheduledPerformingPhysicianName, tagMWLScheduledPerformingPhysicianName, mwlMatchWildcard},
		{query.ScheduledProcedureStepDescription, tagMWLScheduledProcedureStepDescription, mwlMatchSingle},
		{query.ScheduledProcedureStepID, tagMWLScheduledProcedureStepID, mwlMatchSingle},
		{query.ScheduledStationName, tagMWLScheduledStationName, mwlMatchSingle},
		{query.ScheduledProcedureStepLocation, tagMWLScheduledProcedureStepLocation, mwlMatchSingle},
	}
	if !combineTemporal {
		keys = append(keys,
			struct {
				key  MWLKey
				tag  core.Tag
				kind mwlMatchingKind
			}{query.ScheduledProcedureStepStartDate, tagMWLScheduledProcedureStepStartDate, mwlMatchDateRange},
			struct {
				key  MWLKey
				tag  core.Tag
				kind mwlMatchingKind
			}{query.ScheduledProcedureStepStartTime, tagMWLScheduledProcedureStepStartTime, mwlMatchTimeRange},
		)
	}
	for _, key := range keys {
		matched, err := matchMWLKey(key.key, candidate, key.tag, key.kind)
		if err != nil || !matched {
			return matched, err
		}
	}
	return true, nil
}

func mwlKeyHasMatchingValue(key MWLKey) bool {
	return key.Present && len(key.Values) == 1 && strings.TrimSpace(key.Values[0]) != ""
}

func mwlKeyUsesRangeMatching(key MWLKey) bool {
	return mwlKeyHasMatchingValue(key) && strings.Contains(key.Values[0], "-")
}

func matchMWLCombinedDateTime(dateKey, timeKey MWLKey, candidate *object.Object) (bool, error) {
	if len(dateKey.Values) != 1 || len(timeKey.Values) != 1 {
		return false, fmt.Errorf("dicom dimse: MWL date/time matching requires VM 1")
	}
	dateQuery := strings.TrimSpace(dateKey.Values[0])
	timeQuery := strings.TrimSpace(timeKey.Values[0])
	if err := validateMWLTemporalQuery(dateQuery, core.VRDA); err != nil {
		return false, err
	}
	if err := validateMWLTemporalQuery(timeQuery, core.VRTM); err != nil {
		return false, err
	}
	candidateDateValue, dateOK := candidate.GetString(tagMWLScheduledProcedureStepStartDate)
	candidateTimeValue, timeOK := candidate.GetString(tagMWLScheduledProcedureStepStartTime)
	if !dateOK || !timeOK {
		return false, nil
	}
	candidateDate, err := parseMWLTemporal(strings.TrimSpace(candidateDateValue), core.VRDA)
	if err != nil {
		return false, fmt.Errorf("dicom dimse: invalid MWL candidate DA value")
	}
	candidateTime, err := parseMWLTemporal(strings.TrimSpace(candidateTimeValue), core.VRTM)
	if err != nil {
		return false, fmt.Errorf("dicom dimse: invalid MWL candidate TM value")
	}
	candidateDateTime := combineMWLDateAndTime(candidateDate, candidateTime)

	dateLower, dateUpper := splitMWLRange(dateQuery)
	timeLower, timeUpper := splitMWLRange(timeQuery)
	if dateLower != "" {
		lowerDate, _ := parseMWLTemporal(dateLower, core.VRDA)
		lowerTime := time.Time{}
		if timeLower != "" {
			lowerTime, _ = parseMWLTemporal(timeLower, core.VRTM)
		}
		if candidateDateTime.Before(combineMWLDateAndTime(lowerDate, lowerTime)) {
			return false, nil
		}
	}
	if dateUpper != "" {
		upperDate, _ := parseMWLTemporal(dateUpper, core.VRDA)
		upperTime := time.Date(1, 1, 1, 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC)
		if timeUpper != "" {
			upperTime, _ = parseMWLTemporal(timeUpper, core.VRTM)
		}
		if candidateDateTime.After(combineMWLDateAndTime(upperDate, upperTime)) {
			return false, nil
		}
	}
	return true, nil
}

func splitMWLRange(value string) (string, string) {
	if !strings.Contains(value, "-") {
		return value, value
	}
	bounds := strings.SplitN(value, "-", 2)
	return bounds[0], bounds[1]
}

func combineMWLDateAndTime(dateValue, timeValue time.Time) time.Time {
	return time.Date(dateValue.Year(), dateValue.Month(), dateValue.Day(), timeValue.Hour(), timeValue.Minute(), timeValue.Second(), timeValue.Nanosecond(), time.UTC)
}

func matchMWLKey(key MWLKey, candidate *object.Object, tag core.Tag, kind mwlMatchingKind) (bool, error) {
	if !key.Present {
		return true, nil
	}
	if len(key.Values) != 1 {
		return false, fmt.Errorf("dicom dimse: MWL key %s requires VM 1", tag)
	}
	queryValue := strings.TrimSpace(key.Values[0])
	if queryValue == "" {
		return true, nil
	}
	candidateValues, ok := candidate.GetStrings(tag)
	if !ok || len(candidateValues) == 0 {
		return false, nil
	}
	for _, candidateValue := range candidateValues {
		matched, err := matchMWLValue(queryValue, strings.TrimSpace(candidateValue), kind)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func matchMWLValue(query, candidate string, kind mwlMatchingKind) (bool, error) {
	switch kind {
	case mwlMatchSingle:
		if strings.ContainsAny(query, "*?") {
			return false, fmt.Errorf("dicom dimse: wildcard is not supported for this MWL key")
		}
		return query == candidate, nil
	case mwlMatchWildcard:
		return matchMWLWildcard(query, candidate), nil
	case mwlMatchDateRange:
		return matchMWLTemporalRange(query, candidate, core.VRDA)
	case mwlMatchTimeRange:
		return matchMWLTemporalRange(query, candidate, core.VRTM)
	default:
		return false, fmt.Errorf("dicom dimse: unsupported MWL matching kind")
	}
}

func matchMWLWildcard(pattern, value string) bool {
	patternRunes := []rune(strings.ToUpper(pattern))
	valueRunes := []rune(strings.ToUpper(value))
	patternIndex, valueIndex := 0, 0
	starIndex, starValueIndex := -1, 0
	for valueIndex < len(valueRunes) {
		if patternIndex < len(patternRunes) && (patternRunes[patternIndex] == '?' || patternRunes[patternIndex] == valueRunes[valueIndex]) {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(patternRunes) && patternRunes[patternIndex] == '*' {
			starIndex = patternIndex
			patternIndex++
			starValueIndex = valueIndex
			continue
		}
		if starIndex >= 0 {
			patternIndex = starIndex + 1
			starValueIndex++
			valueIndex = starValueIndex
			continue
		}
		return false
	}
	for patternIndex < len(patternRunes) && patternRunes[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(patternRunes)
}

func matchMWLTemporalRange(query, candidate string, vr core.VR) (bool, error) {
	if err := validateMWLTemporalQuery(query, vr); err != nil {
		return false, err
	}
	candidateTime, err := parseMWLTemporal(candidate, vr)
	if err != nil {
		return false, fmt.Errorf("dicom dimse: invalid MWL candidate %s value", vr)
	}
	if !strings.Contains(query, "-") {
		queryTime, parseErr := parseMWLTemporal(query, vr)
		if parseErr != nil {
			return false, fmt.Errorf("dicom dimse: invalid MWL %s matching value", vr)
		}
		return queryTime.Equal(candidateTime), nil
	}
	if strings.Count(query, "-") != 1 {
		return false, fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
	}
	bounds := strings.SplitN(query, "-", 2)
	if bounds[0] != "" {
		lower, parseErr := parseMWLTemporal(bounds[0], vr)
		if parseErr != nil {
			return false, fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
		}
		if candidateTime.Before(lower) {
			return false, nil
		}
	}
	if bounds[1] != "" {
		upper, parseErr := parseMWLTemporal(bounds[1], vr)
		if parseErr != nil {
			return false, fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
		}
		if candidateTime.After(upper) {
			return false, nil
		}
	}
	return true, nil
}

func validateMWLTemporalQuery(query string, vr core.VR) error {
	if strings.ContainsAny(query, "*?") {
		return fmt.Errorf("dicom dimse: wildcard is not supported for MWL %s matching", vr)
	}
	if !strings.Contains(query, "-") {
		if _, err := parseMWLTemporal(query, vr); err != nil {
			return fmt.Errorf("dicom dimse: invalid MWL %s matching value", vr)
		}
		return nil
	}
	if strings.Count(query, "-") != 1 {
		return fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
	}
	bounds := strings.SplitN(query, "-", 2)
	if bounds[0] == "" && bounds[1] == "" {
		return fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
	}
	var lower, upper time.Time
	var lowerSet, upperSet bool
	if bounds[0] != "" {
		value, err := parseMWLTemporal(bounds[0], vr)
		if err != nil {
			return fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
		}
		lower, lowerSet = value, true
	}
	if bounds[1] != "" {
		value, err := parseMWLTemporal(bounds[1], vr)
		if err != nil {
			return fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
		}
		upper, upperSet = value, true
	}
	if lowerSet && upperSet && lower.After(upper) {
		return fmt.Errorf("dicom dimse: invalid MWL %s range", vr)
	}
	return nil
}

func parseMWLTemporal(value string, vr core.VR) (time.Time, error) {
	switch vr {
	case core.VRDA:
		date, err := dcmtime.ParseDate(value)
		return date.Time, err
	case core.VRTM:
		timeValue, err := dcmtime.ParseTime(value)
		return timeValue.Time, err
	default:
		return time.Time{}, fmt.Errorf("dicom dimse: unsupported MWL temporal VR %s", vr)
	}
}
