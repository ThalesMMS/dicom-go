package ups

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

var ErrQueryIdentifier = errors.New("dicom ups: query Identifier invalid")

const (
	maxQueryScannedSteps = 1_000_000
	maxQueryPageSize     = 4_096
)

// QueryKey preserves absent, universal/return, and matching-value states.
type QueryKey struct {
	Present bool
	Values  []string
}

func Match(values ...string) QueryKey {
	return QueryKey{Present: true, Values: append([]string(nil), values...)}
}

func ReturnKey() QueryKey { return Match() }

// Query contains the UPS keys selected by tag. Keys are cloned by builders.
// Sequence keys are currently supported as universal return keys only.
type Query struct {
	Keys map[core.Tag]QueryKey
}

type ParsedQuery struct {
	Keys  map[core.Tag]QueryKey
	order []core.Tag
}

func (query ParsedQuery) HasMatchingKey() bool {
	for tag, key := range query.Keys {
		if tag == TagSpecificCharacterSet || tag == TagTimezoneOffsetFromUTC {
			continue
		}
		if key.Present && len(key.Values) > 0 {
			return true
		}
	}
	return false
}

func BuildQueryIdentifier(query Query) (*object.Object, error) {
	tags := make([]core.Tag, 0, len(query.Keys))
	for tag, key := range query.Keys {
		if key.Present {
			tags = append(tags, tag)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Less(tags[j]) })
	elements := make([]core.Element, 0, len(tags))
	for _, tag := range tags {
		key := query.Keys[tag]
		entry, allowed := queryEntry(tag)
		if !allowed {
			return nil, ErrQueryIdentifier
		}
		if entry.VR == core.VRSQ {
			if len(key.Values) != 0 {
				return nil, ErrQueryIdentifier
			}
			elements = append(elements, EmptySequence(tag))
			continue
		}
		if tag == TagTimezoneOffsetFromUTC && (len(key.Values) != 1 || !validUPSTimezoneOffset(key.Values[0])) {
			return nil, ErrQueryIdentifier
		}
		if tag == TagSpecificCharacterSet && (len(key.Values) != 1 || strings.TrimSpace(key.Values[0]) == "") {
			return nil, ErrQueryIdentifier
		}
		if len(key.Values) == 0 {
			elements = append(elements, StringElement(tag, entry.VR, ""))
		} else {
			elements = append(elements, StringElement(tag, entry.VR, key.Values...))
		}
	}
	return NewDataSet(elements...), nil
}

func ParseQueryIdentifier(identifier *object.Object) (ParsedQuery, error) {
	if identifier == nil {
		return ParsedQuery{}, ErrQueryIdentifier
	}
	parsed := ParsedQuery{Keys: make(map[core.Tag]QueryKey), order: make([]core.Tag, 0, identifier.Len())}
	for _, element := range identifier.Elements() {
		if _, duplicate := parsed.Keys[element.Tag()]; duplicate {
			return ParsedQuery{}, ErrQueryIdentifier
		}
		entry, allowed := queryEntry(element.Tag())
		if !allowed || element.VR() != entry.VR {
			return ParsedQuery{}, ErrQueryIdentifier
		}
		key := QueryKey{Present: true}
		if entry.VR == core.VRSQ {
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok || len(sequence.Items) != 0 {
				return ParsedQuery{}, ErrQueryIdentifier
			}
		} else {
			key.Values = append([]string(nil), element.StringValues()...)
			if len(key.Values) == 1 && key.Values[0] == "" {
				key.Values = nil
			}
		}
		if element.Tag() == TagTimezoneOffsetFromUTC && (len(key.Values) != 1 || !validUPSTimezoneOffset(key.Values[0])) {
			return ParsedQuery{}, ErrQueryIdentifier
		}
		if element.Tag() == TagSpecificCharacterSet && (len(key.Values) != 1 || strings.TrimSpace(key.Values[0]) == "") {
			return ParsedQuery{}, ErrQueryIdentifier
		}
		parsed.Keys[element.Tag()] = key
		parsed.order = append(parsed.order, element.Tag())
	}
	return parsed, nil
}

func validUPSTimezoneOffset(value string) bool {
	if len(value) != 5 || value[0] != '+' && value[0] != '-' {
		return false
	}
	if value == "-0000" {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	hours := int(value[1]-'0')*10 + int(value[2]-'0')
	minutes := int(value[3]-'0')*10 + int(value[4]-'0')
	return minutes < 60 && (hours < 14 || hours == 14 && minutes == 0)
}

type QuerySCPOptions struct {
	Limits          dimse.StreamingCFindLimits
	MaxScannedSteps int
	PageSize        int
}

func (options QuerySCPOptions) normalized() (QuerySCPOptions, error) {
	if options.MaxScannedSteps < 0 || options.PageSize < 0 || options.MaxScannedSteps > maxQueryScannedSteps || options.PageSize > maxQueryPageSize {
		return QuerySCPOptions{}, ErrResourceLimit
	}
	if options.MaxScannedSteps == 0 {
		options.MaxScannedSteps = 100_000
	}
	if options.PageSize == 0 {
		options.PageSize = 128
	}
	if options.PageSize > options.MaxScannedSteps {
		options.PageSize = options.MaxScannedSteps
	}
	return options, nil
}

// QueryRoutes returns exact Pull, Watch, and Query C-FIND routes. State and
// subscription mutation never occur through these handlers.
func (service *Service) QueryRoutes(options QuerySCPOptions) ([]dimse.StreamingCFindRoute, error) {
	if service == nil {
		return nil, ErrConflict
	}
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	handler := &queryHandler{service: service, options: options}
	routes := make([]dimse.StreamingCFindRoute, 0, 3)
	for _, uid := range []string{PullSOPClassUID, WatchSOPClassUID, QuerySOPClassUID} {
		routes = append(routes, dimse.StreamingCFindRoute{
			SOPClassUID: uid, ResponseSOPClassUID: uid, Handler: handler, Limits: options.Limits,
		})
	}
	return routes, nil
}

type queryHandler struct {
	service *Service
	options QuerySCPOptions
}

func (handler *queryHandler) Find(ctx context.Context, request dimse.StreamingCFindRequest, yield dimse.StreamingCFindYield) error {
	query, err := ParseQueryIdentifier(request.Identifier)
	if err != nil {
		return dimse.ErrStreamingCFindIdentifier
	}
	if !query.HasMatchingKey() {
		return nil
	}
	var after uint64
	scanned := 0
	for scanned < handler.options.MaxScannedSteps {
		limit := handler.options.PageSize
		if remaining := handler.options.MaxScannedSteps - scanned; limit > remaining {
			limit = remaining
		}
		probeLimit := limit
		if scanned+limit == handler.options.MaxScannedSteps {
			probeLimit++
		}
		steps, err := handler.service.store.ListSteps(ctx, StepQuery{AfterSequence: after, Limit: probeLimit})
		if err != nil {
			return dimse.ErrStreamingCFindProvider
		}
		if len(steps) == 0 {
			return nil
		}
		hasMore := len(steps) > limit
		if hasMore {
			steps = steps[:limit]
		}
		for _, step := range steps {
			if err := ctx.Err(); err != nil {
				return err
			}
			scanned++
			after = step.Sequence
			if !queryMatchesStep(query, step) {
				continue
			}
			projected, warning := projectQueryResult(query, step)
			status := dimse.StatusPending
			if warning {
				status = dimse.StatusPendingWarning
			}
			if err := yield(status, projected); err != nil {
				return err
			}
		}
		if hasMore {
			return dimse.ErrStreamingCFindResourceLimit
		}
		if len(steps) < probeLimit {
			return nil
		}
	}
	return dimse.ErrStreamingCFindResourceLimit
}

func queryMatchesStep(query ParsedQuery, step Step) bool {
	queryTimezone := queryTimezoneOffset(query)
	candidateTimezone := ""
	if value, present := dataSetString(step.Attributes, TagTimezoneOffsetFromUTC); present {
		candidateTimezone = value
	}
	for tag, key := range query.Keys {
		if tag == TagSpecificCharacterSet || tag == TagTimezoneOffsetFromUTC {
			continue
		}
		if len(key.Values) == 0 {
			continue
		}
		element, found := dataSetElement(step.Attributes, tag)
		if !found || !queryValuesMatch(element, key.Values, queryTimezone, candidateTimezone) {
			return false
		}
	}
	return true
}

func queryTimezoneOffset(query ParsedQuery) string {
	key, present := query.Keys[TagTimezoneOffsetFromUTC]
	if present && len(key.Values) == 1 {
		return key.Values[0]
	}
	return ""
}

func queryValuesMatch(element core.Element, requested []string, queryTimezone, candidateTimezone string) bool {
	candidates := element.StringValues()
	if len(candidates) == 0 {
		return false
	}
	for _, request := range requested {
		for _, candidate := range candidates {
			if queryValueMatchWithTimezone(element.VR(), request, candidate, queryTimezone, candidateTimezone) {
				return true
			}
		}
	}
	return false
}

func queryValueMatch(vr core.VR, request, candidate string) bool {
	return queryValueMatchWithTimezone(vr, request, candidate, "", "")
}

func queryValueMatchWithTimezone(vr core.VR, request, candidate, queryTimezone, candidateTimezone string) bool {
	if vr == core.VRDA || vr == core.VRDT || vr == core.VRTM {
		candidateValue, candidateOK := parseTemporalValue(vr, candidate, candidateTimezone)
		if !candidateOK {
			return false
		}
		if lower, upper, ok := parseTemporalRange(vr, request, queryTimezone); ok {
			return (lower == nil || !candidateValue.end.Before(lower.start)) &&
				(upper == nil || !candidateValue.start.After(upper.end))
		}
		requestValue, requestOK := parseTemporalValue(vr, request, queryTimezone)
		if !requestOK {
			return false
		}
		return !candidateValue.end.Before(requestValue.start) && !candidateValue.start.After(requestValue.end)
	}
	if strings.ContainsAny(request, "*?") && vr != core.VRUI {
		return wildcardMatch(request, candidate)
	}
	if vr == core.VRCS {
		return strings.EqualFold(request, candidate)
	}
	return request == candidate
}

type temporalValue struct {
	start time.Time
	end   time.Time
}

func parseTemporalValue(vr core.VR, value, defaultTimezone string) (temporalValue, bool) {
	var start time.Time
	var precision dcmtime.PrecisionLevel
	switch vr {
	case core.VRDA:
		parsed, err := dcmtime.ParseDate(value)
		if err != nil || parsed.IsNEMA {
			return temporalValue{}, false
		}
		start, precision = parsed.Time, parsed.Precision
	case core.VRTM:
		parsed, err := dcmtime.ParseTime(value)
		if err != nil {
			return temporalValue{}, false
		}
		start, precision = parsed.Time, parsed.Precision
		if defaultTimezone != "" {
			offset, ok := timezoneOffsetSeconds(defaultTimezone)
			if !ok {
				return temporalValue{}, false
			}
			start = start.Add(-time.Duration(offset) * time.Second)
		}
	case core.VRDT:
		parsed, err := dcmtime.ParseDatetime(value)
		if err != nil {
			return temporalValue{}, false
		}
		start, precision = parsed.Time, parsed.Precision
		if parsed.NoOffset && defaultTimezone != "" {
			offset, ok := timezoneOffsetSeconds(defaultTimezone)
			if !ok {
				return temporalValue{}, false
			}
			start = time.Date(start.Year(), start.Month(), start.Day(), start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), time.FixedZone("", offset)).UTC()
		} else {
			start = start.UTC()
		}
	default:
		return temporalValue{}, false
	}
	end := temporalPrecisionEnd(start, precision, vr)
	return temporalValue{start: start, end: end}, true
}

func parseTemporalRange(vr core.VR, value, defaultTimezone string) (*temporalValue, *temporalValue, bool) {
	for index := 0; index < len(value); index++ {
		if value[index] != '-' {
			continue
		}
		left, right := value[:index], value[index+1:]
		var lower, upper *temporalValue
		if left != "" {
			parsed, ok := parseTemporalValue(vr, left, defaultTimezone)
			if !ok {
				continue
			}
			lower = &parsed
		}
		if right != "" {
			parsed, ok := parseTemporalValue(vr, right, defaultTimezone)
			if !ok {
				continue
			}
			upper = &parsed
		}
		if lower != nil || upper != nil {
			return lower, upper, true
		}
	}
	return nil, nil, false
}

func timezoneOffsetSeconds(value string) (int, bool) {
	if !validUPSTimezoneOffset(value) {
		return 0, false
	}
	offset := (int(value[1]-'0')*10+int(value[2]-'0'))*60*60 + (int(value[3]-'0')*10+int(value[4]-'0'))*60
	if value[0] == '-' {
		offset = -offset
	}
	return offset, true
}

func temporalPrecisionEnd(start time.Time, precision dcmtime.PrecisionLevel, vr core.VR) time.Time {
	if vr == core.VRDA && (precision == dcmtime.PrecisionDay || precision == dcmtime.PrecisionFull) {
		return start.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	var exclusive time.Time
	switch precision {
	case dcmtime.PrecisionYear:
		exclusive = start.AddDate(1, 0, 0)
	case dcmtime.PrecisionMonth:
		exclusive = start.AddDate(0, 1, 0)
	case dcmtime.PrecisionDay:
		exclusive = start.AddDate(0, 0, 1)
	case dcmtime.PrecisionHours:
		exclusive = start.Add(time.Hour)
	case dcmtime.PrecisionMinutes:
		exclusive = start.Add(time.Minute)
	case dcmtime.PrecisionSeconds:
		exclusive = start.Add(time.Second)
	case dcmtime.PrecisionMS1:
		exclusive = start.Add(100 * time.Millisecond)
	case dcmtime.PrecisionMS2:
		exclusive = start.Add(10 * time.Millisecond)
	case dcmtime.PrecisionMS3:
		exclusive = start.Add(time.Millisecond)
	case dcmtime.PrecisionMS4:
		exclusive = start.Add(100 * time.Microsecond)
	case dcmtime.PrecisionMS5:
		exclusive = start.Add(10 * time.Microsecond)
	default:
		exclusive = start.Add(time.Microsecond)
	}
	return exclusive.Add(-time.Nanosecond)
}

func wildcardMatch(pattern, value string) bool {
	patternRunes := []rune(pattern)
	valueRunes := []rune(value)
	previous := make([]bool, len(valueRunes)+1)
	previous[0] = true
	for _, token := range patternRunes {
		next := make([]bool, len(valueRunes)+1)
		if token == '*' {
			next[0] = previous[0]
			for index := range valueRunes {
				next[index+1] = next[index] || previous[index+1]
			}
		} else {
			for index, candidate := range valueRunes {
				next[index+1] = previous[index] && (token == '?' || token == candidate)
			}
		}
		previous = next
	}
	return previous[len(valueRunes)]
}

func projectQueryResult(query ParsedQuery, step Step) (*object.Object, bool) {
	elements := make([]core.Element, 0, len(query.order)+2)
	warning := false
	hasTemporal := false
	for _, tag := range query.order {
		// These attributes declare how the request is encoded/interpreted. They
		// are not return keys and therefore cannot make a response FF01.
		if tag == TagSpecificCharacterSet || tag == TagTimezoneOffsetFromUTC {
			continue
		}
		element, found := dataSetElement(step.Attributes, tag)
		if !found {
			warning = true
			continue
		}
		elements = append(elements, element)
		if element.VR() == core.VRDA || element.VR() == core.VRDT || element.VR() == core.VRTM {
			hasTemporal = true
		}
	}
	if queryElementsNeedCharacterSet(elements) {
		if characterSet, found := dataSetElement(step.Attributes, TagSpecificCharacterSet); found && !containsTag(elements, TagSpecificCharacterSet) {
			elements = append([]core.Element{characterSet}, elements...)
		}
	}
	if hasTemporal {
		if timezone, found := dataSetElement(step.Attributes, TagTimezoneOffsetFromUTC); found && !containsTag(elements, TagTimezoneOffsetFromUTC) {
			elements = append([]core.Element{timezone}, elements...)
		}
	}
	return NewDataSet(elements...), warning
}

func queryElementsNeedCharacterSet(elements []core.Element) bool {
	for _, element := range elements {
		if raw, ok := element.RawBytes(); ok {
			for _, value := range raw {
				if value >= utf8.RuneSelf || value == 0x1b {
					return true
				}
			}
		}
		for _, value := range element.StringValues() {
			if !utf8.ValidString(value) || len(value) != len([]rune(value)) {
				return true
			}
		}
	}
	return false
}

func containsTag(elements []core.Element, tag core.Tag) bool {
	for _, element := range elements {
		if element.Tag() == tag {
			return true
		}
	}
	return false
}

func queryEntry(tag core.Tag) (entry struct{ VR core.VR }, ok bool) {
	if !queryTags[tag] {
		return entry, false
	}
	dictionaryEntry, found := std.Dictionary.ByTag(tag)
	if !found {
		return entry, false
	}
	entry.VR = dictionaryEntry.VR
	return entry, true
}

var queryTags = map[core.Tag]bool{
	TagSpecificCharacterSet: true, TagTimezoneOffsetFromUTC: true,
	TagSOPClassUID: true, TagSOPInstanceUID: true, TagStudyInstanceUID: true,
	TagPatientName: true, TagPatientID: true, TagIssuerOfPatientID: true,
	TagPatientBirthDate: true, TagPatientSex: true, TagAdmissionID: true,
	TagScheduledProcedureStepPriority: true, TagProcedureStepLabel: true, TagWorklistLabel: true,
	TagScheduledStationNameCodeSequence: true, TagScheduledStationClassCodeSequence: true,
	TagScheduledStationGeographicLocationCodeSequence: true, TagScheduledHumanPerformersSequence: true,
	TagScheduledProcedureStepStartDateTime: true, TagExpectedCompletionDateTime: true,
	TagScheduledWorkitemCodeSequence: true, TagInputReadinessState: true,
	TagScheduledProcedureStepModificationDateTime: true, TagProcedureStepState: true,
	TagProcedureStepProgressInformationSequence: true, TagUnifiedProcedureStepPerformedProcedureSequence: true,
}

type QueryMatch struct {
	Status     uint16
	Identifier *object.Object
}

type QueryFindOptions struct {
	DIMSE dimse.StreamingCFindClientOptions
}

type QueryClient struct {
	client *dimse.StreamingCFindClient
}

func NewQueryClient(association *ul.Association, sopClassUID string) (*QueryClient, error) {
	if !oneOf(sopClassUID, PullSOPClassUID, WatchSOPClassUID, QuerySOPClassUID) {
		return nil, ErrQueryIdentifier
	}
	client, err := dimse.NewStreamingCFindClient(association, sopClassUID, sopClassUID)
	if err != nil {
		return nil, err
	}
	return &QueryClient{client: client}, nil
}

func (client *QueryClient) Find(ctx context.Context, query Query, yield func(QueryMatch) error) (dimse.StreamingCFindResult, error) {
	return client.FindWithOptions(QueryFindOptions{DIMSE: dimse.StreamingCFindClientOptions{Operation: dimse.OperationOptions{Context: ctx}}}, query, yield)
}

func (client *QueryClient) FindWithOptions(options QueryFindOptions, query Query, yield func(QueryMatch) error) (dimse.StreamingCFindResult, error) {
	if client == nil || client.client == nil || yield == nil {
		return dimse.StreamingCFindResult{}, ErrQueryIdentifier
	}
	identifier, err := BuildQueryIdentifier(query)
	if err != nil {
		return dimse.StreamingCFindResult{}, err
	}
	return client.client.FindWithOptions(options.DIMSE, identifier, func(status uint16, identifier *object.Object) error {
		return yield(QueryMatch{Status: status, Identifier: identifier})
	})
}
