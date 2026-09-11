package dimse

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/qrmatch"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// NonPatientModel identifies a single-entity Q/R model. It has no patient,
// study, series or Query/Retrieve Level. Its zero value is invalid.
type NonPatientModel uint8

const (
	HangingProtocolModel NonPatientModel = iota + 1
	ColorPaletteModel
	HangingProtocolStorageSOPClassUID = "1.2.840.10008.5.1.4.38.1"
	HangingProtocolFindSOPClassUID    = "1.2.840.10008.5.1.4.38.2"
	HangingProtocolMoveSOPClassUID    = "1.2.840.10008.5.1.4.38.3"
	HangingProtocolGetSOPClassUID     = "1.2.840.10008.5.1.4.38.4"
	ColorPaletteStorageSOPClassUID    = "1.2.840.10008.5.1.4.39.1"
	ColorPaletteFindSOPClassUID       = "1.2.840.10008.5.1.4.39.2"
	ColorPaletteMoveSOPClassUID       = "1.2.840.10008.5.1.4.39.3"
	ColorPaletteGetSOPClassUID        = "1.2.840.10008.5.1.4.39.4"
)

var ErrNonPatientIdentifier = errors.New("dicom dimse: invalid non-patient Identifier")
var ErrNonPatientProvider = errors.New("dicom dimse: invalid non-patient catalog result")

// SOPClasses returns storage, FIND, MOVE and GET abstract syntaxes.
func (model NonPatientModel) SOPClasses() (storage, find, move, get string, err error) {
	switch model {
	case HangingProtocolModel:
		return HangingProtocolStorageSOPClassUID, HangingProtocolFindSOPClassUID, HangingProtocolMoveSOPClassUID, HangingProtocolGetSOPClassUID, nil
	case ColorPaletteModel:
		return ColorPaletteStorageSOPClassUID, ColorPaletteFindSOPClassUID, ColorPaletteMoveSOPClassUID, ColorPaletteGetSOPClassUID, nil
	default:
		return "", "", "", "", ErrNonPatientIdentifier
	}
}

// PresentationContexts proposes the three Q/R contexts and the matching Storage
// context. C-GET additionally requires role negotiation for that Storage class.
func (model NonPatientModel) PresentationContexts() ([]ul.PresentationContext, error) {
	storage, find, move, get, err := model.SOPClasses()
	if err != nil {
		return nil, err
	}
	var contexts []ul.PresentationContext
	for _, uid := range []string{find, move, get, storage} {
		contexts = append(contexts, presentationContextFor(uid, []string{ul.ExplicitVRLittleEndian, ul.ImplicitVRLittleEndian}))
	}
	return contexts, nil
}

type nonPatientKey struct {
	vr       core.VR
	match    bool
	wildcard bool
	children map[core.Tag]nonPatientKey
}

func npTag(group, element uint16) core.Tag { return core.NewTag(group, element) }

func nonPatientKeys(model NonPatientModel) map[core.Tag]nonPatientKey {
	key := func(vr core.VR, match, wildcard bool) nonPatientKey {
		return nonPatientKey{vr: vr, match: match, wildcard: wildcard}
	}
	code := map[core.Tag]nonPatientKey{
		npTag(8, 0x100): key(core.VRSH, true, false), npTag(8, 0x102): key(core.VRSH, true, false),
		npTag(8, 0x103): key(core.VRSH, true, false), npTag(8, 0x104): key(core.VRLO, false, false),
	}
	sq := func(children map[core.Tag]nonPatientKey, match bool) nonPatientKey {
		return nonPatientKey{vr: core.VRSQ, match: match, children: children}
	}
	keys := map[core.Tag]nonPatientKey{
		npTag(8, 5): key(core.VRCS, false, false), npTag(8, 0x16): key(core.VRUI, true, false), npTag(8, 0x18): key(core.VRUI, true, false),
	}
	if model == ColorPaletteModel {
		keys[npTag(0x70, 0x80)] = key(core.VRCS, true, true)
		keys[npTag(0x70, 0x81)] = key(core.VRLO, false, false)
		keys[npTag(0x70, 0x84)] = key(core.VRPN, false, false)
		// Optional alternate descriptions are deliberately outside this profile.
		return keys
	}
	for tag, value := range map[core.Tag]nonPatientKey{
		npTag(0x72, 2): key(core.VRSH, true, true), npTag(0x72, 4): key(core.VRLO, false, false),
		npTag(0x72, 6): key(core.VRCS, true, false), npTag(0x72, 8): key(core.VRLO, false, false),
		npTag(0x72, 10): key(core.VRDT, false, false), npTag(0x72, 0x10): key(core.VRLO, true, true),
		npTag(0x72, 0x14): key(core.VRUS, true, false), npTag(0x72, 0x100): key(core.VRUS, true, false),
		npTag(0x72, 0x0e): sq(code, true),
		npTag(0x72, 0x0c): sq(map[core.Tag]nonPatientKey{
			npTag(8, 0x60): key(core.VRCS, true, false), npTag(0x20, 0x60): key(core.VRCS, true, false),
			npTag(8, 0x2218): sq(code, true), npTag(8, 0x1032): sq(code, true), npTag(0x40, 0x100a): sq(code, true),
		}, true),
		npTag(0x72, 0x102): sq(map[core.Tag]nonPatientKey{
			npTag(0x72, 0x104): key(core.VRUS, false, false), npTag(0x72, 0x106): key(core.VRUS, false, false),
			npTag(0x72, 0x108): key(core.VRFD, false, false), npTag(0x72, 0x10a): key(core.VRUS, false, false),
			npTag(0x72, 0x10c): key(core.VRUS, false, false), npTag(0x72, 0x10e): key(core.VRUS, false, false),
		}, false),
	} {
		keys[tag] = value
	}
	return keys
}

// ValidateFindIdentifier validates the explicitly supported key profile before
// matching. Unsupported keys (including empty ones) and matching on return-only
// keys fail closed. Text supports ASCII and ISO_IR 192; coded entry matching
// currently supports the short Code Value form. See NON_PATIENT_QR.md.
func (model NonPatientModel) ValidateFindIdentifier(identifier *object.Object) error {
	if _, _, _, _, err := model.SOPClasses(); err != nil {
		return err
	}
	if identifier == nil {
		return ErrNonPatientIdentifier
	}
	if err := preflightStreamingCFindDataSet(identifier, transfer.ExplicitVRLittleEndian, 1<<20, 256, 8); err != nil {
		return err
	}
	charset, _ := identifier.GetString(npTag(8, 5))
	if charset != "" && charset != "ISO_IR 6" && charset != "ISO_IR 192" {
		return ErrNonPatientIdentifier
	}
	return validateNonPatientKeys(identifier, nonPatientKeys(model), charset == "ISO_IR 192", false)
}

func validateNonPatientKeys(identifier *object.Object, keys map[core.Tag]nonPatientKey, unicode, returnOnly bool) error {
	for _, element := range identifier.Elements() {
		key, ok := keys[element.Tag()]
		if !ok || element.VR() != key.vr {
			return ErrNonPatientIdentifier
		}
		if key.vr == core.VRSQ {
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok || len(sequence.Items) > 1 {
				return ErrNonPatientIdentifier
			}
			for _, item := range sequence.Items {
				if err := validateNonPatientKeys(object.FromElements(item.Elements, std.Dictionary), key.children, unicode, returnOnly || !key.match); err != nil {
					return err
				}
			}
			continue
		}
		values, err := nonPatientValues(identifier, element)
		if err != nil {
			return err
		}
		if len(values) > 1 {
			return ErrNonPatientIdentifier
		}
		for _, value := range values {
			if value == "" {
				continue
			}
			if len(value) > 1024 || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00\r\n\t") || (!unicode && !nonPatientASCII(value)) {
				return ErrNonPatientIdentifier
			}
			if element.Tag() == npTag(8, 5) {
				continue
			}
			if returnOnly || !key.match || (!key.wildcard && strings.ContainsAny(value, "*?")) {
				return ErrNonPatientIdentifier
			}
			if key.vr == core.VRUI && !core.IsValidUID(value) {
				return ErrNonPatientIdentifier
			}
		}
	}
	return nil
}

func nonPatientValues(owner *object.Object, element core.Element) ([]string, error) {
	if element.VR() == core.VRUS {
		switch value := element.Value.(type) {
		case core.Uint16Value:
			if len(value) > 1 {
				return nil, ErrNonPatientIdentifier
			}
			if len(value) == 0 {
				return nil, nil
			}
			return []string{strconv.Itoa(int(value[0]))}, nil
		case core.RawValue:
			if len(value) == 0 {
				return nil, nil
			}
			if len(value) != 2 {
				return nil, ErrNonPatientIdentifier
			}
			return []string{strconv.Itoa(int(owner.ValueByteOrder().Uint16(value)))}, nil
		default:
			return nil, ErrNonPatientIdentifier
		}
	}
	if element.VR() == core.VRFD {
		if value, ok := element.Value.(core.RawValue); ok && len(value) == 0 {
			return nil, nil
		}
		if value, ok := element.Value.(core.Float64Value); ok && len(value) == 0 {
			return nil, nil
		}
		return nil, ErrNonPatientIdentifier // return key only
	}
	switch element.Value.(type) {
	case core.StringValue, core.RawValue:
	default:
		return nil, ErrNonPatientIdentifier
	}
	return element.StringValues(), nil
}

// qrmatch's text/UID/sequence rules apply here, but binary US keys need a
// decimal matching representation; raw binary is never interpreted as text.
func nonPatientMatchingObject(source *object.Object) *object.Object {
	result := object.New(std.Dictionary)
	for _, e := range source.Elements() {
		if e.VR() == core.VRUS {
			values, err := nonPatientValues(source, e)
			if err == nil {
				e.Value = core.StringValue(values)
			}
		}
		if sequence, ok := e.Value.(core.SequenceValue); ok {
			items := make([]core.DataSet, len(sequence.Items))
			for i, item := range sequence.Items {
				nested := object.FromElements(item.Elements, std.Dictionary)
				nested.SetValueByteOrder(source.ValueByteOrder())
				items[i] = core.DataSet{Elements: nonPatientMatchingObject(nested).Elements()}
			}
			e.Value = core.SequenceValue{Items: items}
		}
		result.Put(e)
	}
	return result
}

func nonPatientASCII(value string) bool {
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// RetrieveUIDs validates a C-MOVE/C-GET Identifier. Only a nonempty unique UID
// or UID list is accepted; at most 128 instances can be selected per request.
func (model NonPatientModel) RetrieveUIDs(identifier *object.Object) ([]string, error) {
	if _, _, _, _, err := model.SOPClasses(); err != nil {
		return nil, err
	}
	if identifier == nil || len(identifier.Elements()) != 1 {
		return nil, ErrNonPatientIdentifier
	}
	element, ok := identifier.Get(npTag(8, 0x18))
	if !ok || element.VR() != core.VRUI {
		return nil, ErrNonPatientIdentifier
	}
	values := element.StringValues()
	if len(values) == 0 || len(values) > 128 {
		return nil, ErrNonPatientIdentifier
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !core.IsValidUID(value) || seen[value] {
			return nil, ErrNonPatientIdentifier
		}
		seen[value] = true
	}
	return append([]string(nil), values...), nil
}

// Match validates and matches a query against borrowed catalog metadata. A
// match yields a projection containing only requested keys and both SOP UIDs.
// The result borrows candidate values until the synchronous yield completes.
func (model NonPatientModel) Match(ctx context.Context, query, candidate *object.Object) (*object.Object, bool, error) {
	if err := model.ValidateFindIdentifier(query); err != nil {
		return nil, false, err
	}
	storage, _, _, _, _ := model.SOPClasses()
	if candidate == nil {
		return nil, false, ErrNonPatientProvider
	}
	if err := preflightStreamingCFindDataSet(candidate, transfer.ExplicitVRLittleEndian, 1<<20, 1024, 8); err != nil {
		return nil, false, err
	}
	class, _ := candidate.GetString(npTag(8, 0x16))
	instance, _ := candidate.GetString(npTag(8, 0x18))
	if class != storage || !core.IsValidUID(instance) {
		return nil, false, ErrNonPatientProvider
	}
	charset, _ := candidate.GetString(npTag(8, 5))
	if charset != "" && charset != "ISO_IR 6" && charset != "ISO_IR 192" {
		return nil, false, ErrNonPatientProvider
	}
	result, err := qrmatch.Match(nonPatientMatchingObject(query), nonPatientMatchingObject(candidate), qrmatch.Options{Context: ctx})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, false, err
	}
	if !result.Match {
		return nil, false, nil
	}
	projection, err := projectNonPatient(ctx, query, candidate)
	if err != nil {
		return nil, false, err
	}
	for _, tag := range []core.Tag{npTag(8, 0x16), npTag(8, 0x18), npTag(8, 5)} {
		if e, ok := candidate.Get(tag); ok {
			projection.Put(e)
		}
	}
	return projection, true, nil
}

func projectNonPatient(ctx context.Context, query, candidate *object.Object) (*object.Object, error) {
	result := object.New(std.Dictionary)
	result.SetValueByteOrder(candidate.ValueByteOrder())
	for _, requested := range query.Elements() {
		e, ok := candidate.Get(requested.Tag())
		if !ok {
			e = core.Element{Header: core.ElementHeader{Tag: requested.Tag(), VR: requested.VR()}, Value: core.RawValue{}}
			if requested.VR() == core.VRSQ {
				e.Value = core.SequenceValue{}
			}
		}
		if q, yes := requested.Value.(core.SequenceValue); yes && len(q.Items) == 1 {
			if c, yes := e.Value.(core.SequenceValue); yes {
				items := make([]core.DataSet, 0, len(c.Items))
				for _, item := range c.Items {
					queryItem := object.FromElements(q.Items[0].Elements, std.Dictionary)
					candidateItem := object.FromElements(item.Elements, std.Dictionary)
					queryItem.SetValueByteOrder(query.ValueByteOrder())
					candidateItem.SetValueByteOrder(candidate.ValueByteOrder())
					matched, err := qrmatch.Match(nonPatientMatchingObject(queryItem), nonPatientMatchingObject(candidateItem), qrmatch.Options{Context: ctx})
					if err != nil {
						return nil, err
					}
					if !matched.Match {
						continue
					}
					p, err := projectNonPatient(ctx, queryItem, candidateItem)
					if err != nil {
						return nil, err
					}
					items = append(items, core.DataSet{Elements: p.Elements()})
				}
				e.Value = core.SequenceValue{Items: items}
			}
		}
		result.Put(e)
	}
	return result, nil
}

// NonPatientCatalog enumerates bounded, borrowed metadata objects. It must stop
// on context cancellation or the first yield error and must not retain yield.
// Persistence, indexing, IOD validation and access policy belong to the caller.
type NonPatientCatalog func(context.Context, func(*object.Object) error) error

// FindRoute applies this profile to an application-supplied catalog and reuses
// the existing streaming C-FIND dispatcher, limits, status and cancel handling.
func (model NonPatientModel) FindRoute(catalog NonPatientCatalog, limits StreamingCFindLimits) (StreamingCFindRoute, error) {
	_, find, _, _, err := model.SOPClasses()
	if err != nil || catalog == nil {
		return StreamingCFindRoute{}, ErrNonPatientProvider
	}
	limits, err = limits.normalized()
	if err != nil {
		return StreamingCFindRoute{}, err
	}
	return StreamingCFindRoute{SOPClassUID: find, Limits: limits, Handler: StreamingCFindHandlerFunc(func(ctx context.Context, request StreamingCFindRequest, yield StreamingCFindYield) error {
		if err := model.ValidateFindIdentifier(request.Identifier); err != nil {
			if errors.Is(err, ErrStreamingCFindResourceLimit) {
				return err
			}
			return fmt.Errorf("%w: %w", ErrStreamingCFindIdentifier, err)
		}
		count := 0
		err := catalog(ctx, func(candidate *object.Object) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			count++
			if count > 10000 {
				return ErrStreamingCFindResourceLimit
			}
			projection, match, err := model.Match(ctx, request.Identifier, candidate)
			if err != nil {
				return err
			}
			if !match {
				return nil
			}
			return yield(StatusPending, projection)
		})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStreamingCFindProvider, err)
		}
		return nil
	})}, nil
}

// StartCFind validates the profile, then invokes the existing AsyncSession.
func (model NonPatientModel) StartCFind(ctx context.Context, session *AsyncSession, pcID byte, request CFindRequest, identifier *object.Object) (*AsyncOperation, error) {
	if err := model.ValidateFindIdentifier(identifier); err != nil {
		return nil, err
	}
	_, request.AffectedSOPClassUID, _, _, _ = model.SOPClasses()
	if session == nil {
		return nil, ErrNonPatientProvider
	}
	return session.StartCFind(ctx, pcID, request, identifier)
}

func (model NonPatientModel) StartCMove(ctx context.Context, session *AsyncSession, pcID byte, request CMoveRequest, identifier *object.Object) (*AsyncOperation, error) {
	if _, err := model.RetrieveUIDs(identifier); err != nil {
		return nil, err
	}
	_, _, request.AffectedSOPClassUID, _, _ = model.SOPClasses()
	if session == nil {
		return nil, ErrNonPatientProvider
	}
	return session.StartCMove(ctx, pcID, request, identifier)
}

func (model NonPatientModel) StartCGet(ctx context.Context, session *AsyncSession, pcID byte, request CGetRequest, identifier *object.Object) (*AsyncOperation, error) {
	if _, err := model.RetrieveUIDs(identifier); err != nil {
		return nil, err
	}
	storage, _, _, get, _ := model.SOPClasses()
	request.AffectedSOPClassUID = get
	if session == nil || session.assoc == nil {
		return nil, ErrNonPatientProvider
	}
	pc, err := AcceptedContextForSOPClass(session.assoc, storage)
	if err != nil || !session.localMayPerform(pc.ID) {
		return nil, ErrCGetStorageRoleNotAccepted
	}
	return session.StartCGet(ctx, pcID, request, identifier)
}
