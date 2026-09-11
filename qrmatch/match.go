package qrmatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
)

// Result is the outcome of matching one Identifier against one candidate.
type Result struct {
	Match               bool
	UnsupportedOptional []core.Tag
}

type matchState struct {
	ctx           context.Context
	limits        Limits
	supported     map[core.Tag]struct{}
	wildcardSteps int
	unsupported   []core.Tag
}

// Match reports whether candidate satisfies every supported matching key in
// query. Present empty values are universal matches and return keys. Sequence
// keys match only when one candidate item satisfies every attribute in the
// corresponding query item.
func Match(query, candidate *object.Object, options Options) (Result, error) {
	if query == nil {
		return Result{}, fmt.Errorf("%w: nil query", ErrInvalidQuery)
	}
	if candidate == nil {
		return Result{Match: false}, nil
	}
	state := matchState{
		ctx:    options.Context,
		limits: options.Limits.normalized(),
	}
	if state.ctx == nil {
		state.ctx = context.Background()
	}
	if len(options.SupportedMatching) > 0 {
		state.supported = make(map[core.Tag]struct{}, len(options.SupportedMatching))
		for _, tag := range options.SupportedMatching {
			state.supported[tag] = struct{}{}
		}
	}
	if err := state.errIfCanceled(); err != nil {
		return Result{}, err
	}
	ok, err := state.matchObject(query, candidate, 0)
	if err != nil {
		return Result{}, err
	}
	return Result{Match: ok, UnsupportedOptional: append([]core.Tag(nil), state.unsupported...)}, nil
}

func (state *matchState) matchObject(query, candidate *object.Object, depth int) (bool, error) {
	if depth > state.limits.MaxSequenceDepth {
		return false, ErrResourceLimit
	}
	if err := state.errIfCanceled(); err != nil {
		return false, err
	}
	matched := true
	for _, element := range query.Elements() {
		if err := state.errIfCanceled(); err != nil {
			return false, err
		}
		if isControlTag(element.Tag()) {
			continue
		}
		ok, err := state.matchQueryElement(element, candidate, depth)
		if err != nil {
			return false, err
		}
		if !ok {
			matched = false
		}
	}
	return matched, nil
}

func (state *matchState) matchQueryElement(element core.Element, candidate *object.Object, depth int) (bool, error) {
	if element.VR() == core.VRSQ {
		return state.matchSequence(element, candidate, depth)
	}
	queryValues := stringValues(element)
	if err := state.checkValues(queryValues); err != nil {
		return false, err
	}
	if allValuesEmpty(queryValues) {
		return true, nil
	}
	if state.skipUnsupported(element.Tag()) {
		return true, nil
	}
	candidateElem, ok := candidate.Get(element.Tag())
	if !ok {
		return false, nil
	}
	candidateValues := stringValues(candidateElem)
	if err := state.checkValues(candidateValues); err != nil {
		return false, err
	}
	return state.matchValues(element.VR(), queryValues, candidateValues)
}

func (state *matchState) matchSequence(element core.Element, candidate *object.Object, depth int) (bool, error) {
	sequence, ok := element.Value.(core.SequenceValue)
	if !ok || len(sequence.Items) == 0 {
		return true, nil
	}
	if len(sequence.Items) > state.limits.MaxSequenceItems {
		return false, ErrResourceLimit
	}
	if allSequenceItemsUniversal(sequence.Items) {
		return true, nil
	}
	if state.skipUnsupported(element.Tag()) {
		return true, nil
	}
	candidateItems, ok := candidate.GetSequence(element.Tag())
	if !ok || len(candidateItems) == 0 {
		return false, nil
	}
	if len(candidateItems) > state.limits.MaxSequenceItems {
		return false, ErrResourceLimit
	}
	for _, queryItem := range sequence.Items {
		queryObject := object.FromElements(queryItem.Elements, std.Dictionary)
		itemMatched := false
		for _, candidateItem := range candidateItems {
			matched, err := state.matchObject(queryObject, candidateItem, depth+1)
			if err != nil {
				return false, err
			}
			if matched {
				itemMatched = true
				break
			}
		}
		if !itemMatched {
			return false, nil
		}
	}
	return true, nil
}

func (state *matchState) matchValues(vr core.VR, queryValues, candidateValues []string) (bool, error) {
	if vr == core.VRUI && len(queryValues) > 1 {
		wanted := make(map[string]struct{}, len(queryValues))
		for _, query := range queryValues {
			wanted[core.NormalizeUID(query)] = struct{}{}
		}
		for _, candidate := range candidateValues {
			if _, ok := wanted[core.NormalizeUID(candidate)]; ok {
				return true, nil
			}
		}
		return false, nil
	}
	for _, query := range queryValues {
		query = normalizeQueryValue(vr, query)
		if query == "" {
			continue
		}
		for _, candidate := range candidateValues {
			matched, err := state.matchOneValue(vr, query, normalizeQueryValue(vr, candidate))
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
	}
	return false, nil
}

func (state *matchState) matchOneValue(vr core.VR, query, candidate string) (bool, error) {
	switch vr {
	case core.VRDA, core.VRTM, core.VRDT:
		return matchTemporal(vr, query, candidate)
	}
	if WildcardAllowed(vr) && strings.ContainsAny(query, "*?") {
		if vr == core.VRPN {
			return matchWildcard(strings.ToUpper(query), strings.ToUpper(candidate), state.limits, &state.wildcardSteps)
		}
		return matchWildcard(query, candidate, state.limits, &state.wildcardSteps)
	}
	if vr == core.VRPN {
		return strings.EqualFold(query, candidate), nil
	}
	return query == candidate, nil
}

func (state *matchState) skipUnsupported(tag core.Tag) bool {
	if state.supported == nil {
		return false
	}
	if _, ok := state.supported[tag]; ok {
		return false
	}
	for _, existing := range state.unsupported {
		if existing == tag {
			return true
		}
	}
	state.unsupported = append(state.unsupported, tag)
	return true
}

func (state *matchState) checkValues(values []string) error {
	if len(values) > state.limits.MaxValues {
		return ErrResourceLimit
	}
	if valueByteLen(values) > state.limits.MaxValueBytes {
		return ErrResourceLimit
	}
	return nil
}

func (state *matchState) errIfCanceled() error {
	if state.ctx == nil {
		return nil
	}
	if err := state.ctx.Err(); err != nil {
		return ErrCanceled
	}
	return nil
}

func isControlTag(tag core.Tag) bool {
	switch tag {
	case tags.QueryRetrieveLevel, tags.RetrieveAETitle, core.NewTag(0x0008, 0x0005):
		return true
	default:
		return false
	}
}

func stringValues(element core.Element) []string {
	values := element.StringValues()
	if len(values) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.Contains(value, `\`) {
			out = append(out, strings.Split(value, `\`)...)
			continue
		}
		out = append(out, value)
	}
	return out
}

func normalizeQueryValue(vr core.VR, value string) string {
	switch vr {
	case core.VRUI:
		return core.NormalizeUID(value)
	case core.VRPN:
		return strings.TrimRight(strings.TrimSpace(value), "^ ")
	default:
		return strings.TrimRight(strings.TrimSpace(value), " ")
	}
}

func allSequenceItemsUniversal(items []core.DataSet) bool {
	for _, item := range items {
		for _, element := range item.Elements {
			if element.VR() == core.VRSQ {
				seq, ok := element.Value.(core.SequenceValue)
				if !ok || allSequenceItemsUniversal(seq.Items) {
					continue
				}
				return false
			}
			if !allValuesEmpty(stringValues(element)) {
				return false
			}
		}
	}
	return true
}
