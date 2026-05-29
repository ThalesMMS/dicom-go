package deid

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagSOPClassUID                        = core.NewTag(0x0008, 0x0016)
	tagPatientIdentityRemoved             = core.NewTag(0x0012, 0x0062)
	tagDeidentificationMethod             = core.NewTag(0x0012, 0x0063)
	tagDeidentificationMethodCodeSequence = core.NewTag(0x0012, 0x0064)
	tagLongitudinalTemporalInformation    = core.NewTag(0x0028, 0x0303)
	tagRecognizableVisualFeatures         = core.NewTag(0x0028, 0x0302)
	tagCodeValue                          = core.NewTag(0x0008, 0x0100)
	tagCodingSchemeDesignator             = core.NewTag(0x0008, 0x0102)
	tagCodeMeaning                        = core.NewTag(0x0008, 0x0104)
	tagLongCodeValue                      = core.NewTag(0x0008, 0x0119)
	tagURNCodeValue                       = core.NewTag(0x0008, 0x0120)
	tagReferencedSOPSequence              = core.NewTag(0x0008, 0x1199)
	tagContentValueType                   = core.NewTag(0x0040, 0xA040)
	tagConceptNameCodeSequence            = core.NewTag(0x0040, 0xA043)
	tagStructuredDateTimeValue            = core.NewTag(0x0040, 0xA120)
	tagStructuredDateValue                = core.NewTag(0x0040, 0xA121)
	tagStructuredTimeValue                = core.NewTag(0x0040, 0xA122)
	tagStructuredPersonNameValue          = core.NewTag(0x0040, 0xA123)
	tagStructuredUIDValue                 = core.NewTag(0x0040, 0xA124)
	tagStructuredConceptCodeSequence      = core.NewTag(0x0040, 0xA168)
	tagStructuredTextValue                = core.NewTag(0x0040, 0xA160)
	tagStructuredMeasuredValueSequence    = core.NewTag(0x0040, 0xA300)
	tagAcquisitionContextSequence         = core.NewTag(0x0040, 0x0555)
	tagSpecimenPreparationSequence        = core.NewTag(0x0040, 0x0610)
	tagSpecimenPreparationContentSequence = core.NewTag(0x0040, 0x0612)
	tagContentSequence                    = core.NewTag(0x0040, 0xA730)
)

type compiledProfileTable struct {
	exact   map[core.Tag]generatedProfileRule
	curves  *generatedProfileRule
	overlay map[uint16]generatedProfileRule
	private *generatedProfileRule
}

type structuredContentKey struct {
	codeValue string
	scheme    string
	valueType string
}

type structuredContentCodeKey struct {
	codeValue string
	scheme    string
}

type compiledStructuredContentTable map[structuredContentKey]generatedStructuredContentRule

var (
	profileTableOnce sync.Once
	profileTableData compiledProfileTable
	profileTableErr  error

	structuredContentTableOnce sync.Once
	structuredContentTableData compiledStructuredContentTable
	structuredContentAliases   map[structuredContentCodeKey]structuredContentCodeKey
	structuredContentTableErr  error
)

func loadProfileTable() (compiledProfileTable, error) {
	profileTableOnce.Do(func() {
		profileTableData.exact = make(map[core.Tag]generatedProfileRule, len(generatedProfileRules))
		profileTableData.overlay = make(map[uint16]generatedProfileRule, 2)
		for _, rule := range generatedProfileRules {
			for _, code := range rule.Actions {
				if code == "" {
					continue
				}
				if _, err := ResolveActionCode(code, AttributeType1); err != nil {
					profileTableErr = ErrInvalidActionCode
					return
				}
			}
			switch rule.Pattern {
			case "(50xx,xxxx)":
				copy := rule
				profileTableData.curves = &copy
			case "(60xx,4000)":
				profileTableData.overlay[0x4000] = rule
			case "(60xx,3000)":
				profileTableData.overlay[0x3000] = rule
			case "(gggg,eeee) where gggg is odd":
				copy := rule
				profileTableData.private = &copy
			default:
				tag, err := core.ParseTag(rule.Pattern)
				if err != nil {
					profileTableErr = ErrInvalidActionCode
					return
				}
				if _, exists := profileTableData.exact[tag]; exists {
					profileTableErr = ErrInvalidActionCode
					return
				}
				profileTableData.exact[tag] = rule
			}
		}
	})
	return profileTableData, profileTableErr
}

func (table compiledProfileTable) lookup(tag core.Tag) (generatedProfileRule, bool) {
	if rule, ok := table.exact[tag]; ok {
		return rule, true
	}
	if tag.Group%2 == 0 && tag.Group&0xff00 == 0x5000 && table.curves != nil {
		return *table.curves, true
	}
	if tag.Group%2 == 0 && tag.Group&0xff00 == 0x6000 {
		if rule, ok := table.overlay[tag.Element]; ok {
			return rule, true
		}
	}
	if tag.IsPrivate() && table.private != nil {
		return *table.private, true
	}
	return generatedProfileRule{}, false
}

func loadStructuredContentTable() (compiledStructuredContentTable, error) {
	structuredContentTableOnce.Do(func() {
		structuredContentTableData = make(compiledStructuredContentTable, len(generatedStructuredContentRules))
		for _, rule := range generatedStructuredContentRules {
			key := structuredContentKey{
				codeValue: strings.TrimSpace(rule.CodeValue),
				scheme:    strings.TrimSpace(rule.CodingSchemeDesignator),
				valueType: strings.ToUpper(strings.TrimSpace(rule.ValueType)),
			}
			if key.codeValue == "" || key.scheme == "" || key.valueType == "" {
				structuredContentTableErr = ErrInvalidActionCode
				return
			}
			for _, code := range rule.Actions {
				if code == "" {
					continue
				}
				if _, err := ResolveActionCode(code, AttributeType1); err != nil {
					structuredContentTableErr = ErrInvalidActionCode
					return
				}
			}
			if _, exists := structuredContentTableData[key]; exists {
				structuredContentTableErr = ErrInvalidActionCode
				return
			}
			structuredContentTableData[key] = rule
		}
		structuredContentAliases = make(map[structuredContentCodeKey]structuredContentCodeKey, len(generatedRetiredCodeAliases))
		for _, alias := range generatedRetiredCodeAliases {
			oldKey := structuredContentCodeKey{
				codeValue: strings.TrimSpace(alias.OldCodeValue),
				scheme:    strings.TrimSpace(alias.OldCodingSchemeDesignator),
			}
			currentKey := structuredContentCodeKey{
				codeValue: strings.TrimSpace(alias.CurrentCodeValue),
				scheme:    strings.TrimSpace(alias.CurrentCodingSchemeDesignator),
			}
			if oldKey.codeValue == "" || oldKey.scheme == "" || currentKey.codeValue == "" || currentKey.scheme == "" {
				structuredContentTableErr = ErrInvalidActionCode
				return
			}
			if _, exists := structuredContentAliases[oldKey]; exists {
				structuredContentTableErr = ErrInvalidActionCode
				return
			}
			structuredContentAliases[oldKey] = currentKey
		}
	})
	return structuredContentTableData, structuredContentTableErr
}

type profilePlanChange struct {
	tag     core.Tag
	remove  bool
	element core.Element
}

// BasicProfilePlan is an immutable dry-run result. Apply succeeds only while
// the source Object still matches the state that was planned.
type BasicProfilePlan struct {
	mu              sync.Mutex
	sourceDigest    [sha256.Size]byte
	sourceByteOrder binary.ByteOrder
	limits          ProfileLimits
	changes         []profilePlanChange
	report          ProfileReport
	applied         bool
}

// Report returns a detached PHI-redacted copy of the dry-run report.
func (plan *BasicProfilePlan) Report() ProfileReport {
	if plan == nil {
		return ProfileReport{}
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return plan.report.clone()
}

// Apply atomically applies a previously validated plan to obj. No callback or
// fallible transformation occurs after the first mutation.
func (plan *BasicProfilePlan) Apply(ctx context.Context, obj *object.Object) (ProfileReport, error) {
	if plan == nil || obj == nil {
		return ProfileReport{}, ErrNilObject
	}
	if err := ctx.Err(); err != nil {
		return ProfileReport{}, err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	current := obj.ToDataSet()
	if err := validateProfileDataSetBudget(current, plan.limits); err != nil {
		return ProfileReport{}, err
	}
	if plan.applied || profileDigest(current) != plan.sourceDigest || !sameProfileByteOrder(obj.ValueByteOrder(), plan.sourceByteOrder) {
		return ProfileReport{}, ErrStaleProfilePlan
	}
	if err := ctx.Err(); err != nil {
		return ProfileReport{}, err
	}
	for _, change := range plan.changes {
		if change.remove {
			obj.Remove(change.tag)
			continue
		}
		obj.Put(profileCloneElement(change.element))
	}
	plan.applied = true
	return plan.report.clone(), nil
}

// ApplyBasicProfile plans and atomically applies the Basic Profile in one call.
func ApplyBasicProfile(ctx context.Context, obj *object.Object, options BasicProfileOptions, uids *UIDRemapper) (ProfileReport, error) {
	plan, err := PlanBasicProfile(ctx, obj, options, uids)
	if err != nil {
		return ProfileReport{}, err
	}
	return plan.Apply(ctx, obj)
}

// CloneFileWithBasicProfile returns a detached, de-identified Part 10 file.
// It replaces the source preamble, rebuilds File Meta from the transformed
// data set, and never mutates src.
func CloneFileWithBasicProfile(ctx context.Context, src *object.File, options BasicProfileOptions, uids *UIDRemapper) (*object.File, ProfileReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, ProfileReport{}, err
	}
	if src == nil || src.Dataset == nil {
		return nil, ProfileReport{}, object.ErrNilFile
	}
	normalized, _, err := normalizeProfileOptions(options)
	if err != nil {
		return nil, ProfileReport{}, err
	}
	if err := validateProfileDataSetBudget(src.Dataset.ToDataSet(), normalized.Limits); err != nil {
		return nil, ProfileReport{}, err
	}
	clone, err := object.CloneFile(src)
	if err != nil {
		return nil, ProfileReport{}, err
	}
	report, err := ApplyBasicProfile(ctx, clone.Dataset, normalized, uids)
	if err != nil {
		return nil, report, err
	}
	clone, err = rederiveFileMeta(clone)
	if err != nil {
		return nil, report, err
	}
	return clone, report, nil
}

// PlanBasicProfile creates a bounded dry-run without mutating obj.
func PlanBasicProfile(ctx context.Context, obj *object.Object, options BasicProfileOptions, uids *UIDRemapper) (*BasicProfilePlan, error) {
	if obj == nil {
		return nil, ErrNilObject
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, selected, err := normalizeProfileOptions(options)
	if err != nil {
		return nil, err
	}
	table, err := loadProfileTable()
	if err != nil {
		return nil, err
	}
	var structuredTable compiledStructuredContentTable
	if selected[ProfileOptionCleanStructuredContent] {
		structuredTable, err = loadStructuredContentTable()
		if err != nil {
			return nil, err
		}
	}
	if uids == nil {
		uids = NewUIDRemapper()
	}
	source := obj.ToDataSet()
	if err := validateProfileDataSetBudget(source, normalized.Limits); err != nil {
		return nil, err
	}
	state := profilePlanner{
		ctx:             ctx,
		options:         normalized,
		selected:        selected,
		table:           table,
		structuredTable: structuredTable,
		uids:            uids,
		report: ProfileReport{
			StandardVersion: generatedProfileStandardVersion,
			TableSource:     generatedProfileSourceURL,
			TableChecksum:   generatedProfileProjectionSHA256,
			Complete:        true,
			Options:         append([]ProfileOption(nil), normalized.SelectedOptions...),
		},
	}
	if selected[ProfileOptionCleanStructuredContent] {
		state.report.StructuredContentSource = GeneratedStructuredContentSourceURL
		state.report.StructuredContentChecksum = GeneratedStructuredContentProjectionSHA256
		state.report.RetiredCodeSource = GeneratedRetiredCodeSourceURL
		state.report.RetiredCodeChecksum = GeneratedRetiredCodeProjectionSHA256
	}
	if selected[ProfileOptionRetainSafePrivate] {
		provenance := normalized.SafePrivateRegistry.Provenance()
		state.report.SafePrivateVersion = provenance.Version
		state.report.SafePrivateChecksum = provenance.Checksum
	}
	if err := state.initializeReportBudget(); err != nil {
		return nil, err
	}
	state.setSOPClassUID(source)
	if state.sopClassUID == "1.2.840.10008.1.3.10" {
		return nil, ErrDICOMDIRPolicyRequired
	}
	if err := state.addSourceResidualRisks(obj); err != nil {
		return nil, err
	}
	if selected[ProfileOptionRetainModifiedDates] {
		shift, callErr := callDateShiftPolicy(ctx, normalized.DateShiftPolicy, object.FromDataSet(profileCloneDataSet(source), std.Dictionary))
		if callErr != nil {
			return nil, callErr
		}
		if shift == 0 {
			return nil, ErrUnrepresentableDateShift
		}
		state.dateShiftDays = shift
	}
	result, err := state.transformDataSet(source, nil, 0)
	if err != nil {
		return nil, err
	}
	planned := object.FromDataSet(result, std.Dictionary)
	planned.SetValueByteOrder(obj.ValueByteOrder())
	if selected[ProfileOptionCleanPixelData] {
		if profileHasDeferredValue(planned, isPixelCleanerTag) {
			return nil, ErrDeferredValueUnavailable
		}
		beforeCallback := planned.ToDataSet()
		regions, callErr := callPixelCleaner(ctx, normalized.PixelCleaner, planned)
		if callErr != nil {
			return nil, callErr
		}
		if profileHasDeferredValue(planned, isPixelCleanerTag) {
			return nil, ErrDeferredValueUnavailable
		}
		if len(regions) > normalized.Limits.MaxPixelMasks {
			return nil, ErrProfileResourceLimit
		}
		if !profileMutationsAllowed(beforeCallback, planned.ToDataSet(), isPixelCleanerTag) {
			return nil, ErrProfileCallback
		}
		state.report.Summary.PixelMasks = len(regions)
		planned.Put(core.NewRawElement(tagBurnedInAnnotation, core.VRCS, []byte("NO")))
	}
	if selected[ProfileOptionCleanRecognizableFeatures] {
		if profileHasDeferredValue(planned, isVisualCleanerTag) {
			return nil, ErrDeferredValueUnavailable
		}
		beforeCallback := planned.ToDataSet()
		if callErr := callVisualCleaner(ctx, normalized.VisualFeaturesCleaner, planned); callErr != nil {
			return nil, callErr
		}
		if profileHasDeferredValue(planned, isVisualCleanerTag) {
			return nil, ErrDeferredValueUnavailable
		}
		if !profileMutationsAllowed(beforeCallback, planned.ToDataSet(), isVisualCleanerTag) {
			return nil, ErrProfileCallback
		}
		planned.Put(core.NewRawElement(tagRecognizableVisualFeatures, core.VRCS, []byte("NO")))
	}
	state.addRequiredProfileAttributes(planned)
	result = planned.ToDataSet()
	if err := validateProfileDataSetBudget(result, normalized.Limits); err != nil {
		return nil, err
	}
	changes := profileChanges(source, result)
	if encoded, marshalErr := json.Marshal(state.report); marshalErr != nil || len(encoded) > normalized.Limits.MaxReportBytes {
		return nil, ErrProfileResourceLimit
	}
	return &BasicProfilePlan{
		sourceDigest:    profileDigest(source),
		sourceByteOrder: obj.ValueByteOrder(),
		limits:          normalized.Limits,
		changes:         changes,
		report:          state.report,
	}, nil
}

var profileOptionOrder = []ProfileOption{
	ProfileOptionCleanPixelData,
	ProfileOptionCleanRecognizableFeatures,
	ProfileOptionCleanGraphics,
	ProfileOptionCleanStructuredContent,
	ProfileOptionCleanDescriptors,
	ProfileOptionRetainFullDates,
	ProfileOptionRetainModifiedDates,
	ProfileOptionRetainPatientCharacteristics,
	ProfileOptionRetainDeviceIdentity,
	ProfileOptionRetainInstitutionIdentity,
	ProfileOptionRetainUIDs,
	ProfileOptionRetainSafePrivate,
}

var tableOptionOrder = []ProfileOption{
	ProfileOptionRetainSafePrivate,
	ProfileOptionRetainUIDs,
	ProfileOptionRetainDeviceIdentity,
	ProfileOptionRetainInstitutionIdentity,
	ProfileOptionRetainPatientCharacteristics,
	ProfileOptionRetainFullDates,
	ProfileOptionRetainModifiedDates,
	ProfileOptionCleanDescriptors,
	ProfileOptionCleanStructuredContent,
	ProfileOptionCleanGraphics,
}

var profileOptionColumn = map[ProfileOption]int{
	ProfileOptionRetainSafePrivate:            1,
	ProfileOptionRetainUIDs:                   2,
	ProfileOptionRetainDeviceIdentity:         3,
	ProfileOptionRetainInstitutionIdentity:    4,
	ProfileOptionRetainPatientCharacteristics: 5,
	ProfileOptionRetainFullDates:              6,
	ProfileOptionRetainModifiedDates:          7,
	ProfileOptionCleanDescriptors:             8,
	ProfileOptionCleanStructuredContent:       9,
	ProfileOptionCleanGraphics:                10,
}

func normalizeProfileOptions(options BasicProfileOptions) (BasicProfileOptions, map[ProfileOption]bool, error) {
	if options.Limits == (ProfileLimits{}) {
		options.Limits = DefaultProfileLimits()
	}
	limits := options.Limits
	if limits.MaxDepth <= 0 || limits.MaxElements <= 0 || limits.MaxItems <= 0 || limits.MaxValueBytes <= 0 || limits.MaxActions <= 0 || limits.MaxPixelMasks <= 0 || limits.MaxReportBytes <= 0 {
		return BasicProfileOptions{}, nil, ErrInvalidProfileOptions
	}
	requested := make(map[ProfileOption]bool, len(options.SelectedOptions))
	valid := make(map[ProfileOption]bool, len(profileOptionOrder))
	for _, option := range profileOptionOrder {
		valid[option] = true
	}
	for _, option := range options.SelectedOptions {
		if !valid[option] || requested[option] {
			return BasicProfileOptions{}, nil, ErrInvalidProfileOptions
		}
		requested[option] = true
	}
	if requested[ProfileOptionRetainFullDates] && requested[ProfileOptionRetainModifiedDates] {
		return BasicProfileOptions{}, nil, ErrInvalidProfileOptions
	}
	if requested[ProfileOptionRetainModifiedDates] && options.DateShiftPolicy == nil {
		return BasicProfileOptions{}, nil, ErrDateShiftPolicyRequired
	}
	if requested[ProfileOptionRetainSafePrivate] && options.SafePrivateRegistry == nil {
		return BasicProfileOptions{}, nil, ErrSafePrivateRegistryRequired
	}
	if requested[ProfileOptionCleanPixelData] && options.PixelCleaner == nil {
		return BasicProfileOptions{}, nil, ErrPixelCleanerRequired
	}
	if requested[ProfileOptionCleanRecognizableFeatures] && options.VisualFeaturesCleaner == nil {
		return BasicProfileOptions{}, nil, ErrVisualCleanerRequired
	}
	options.SelectedOptions = make([]ProfileOption, 0, len(requested))
	for _, option := range profileOptionOrder {
		if requested[option] {
			options.SelectedOptions = append(options.SelectedOptions, option)
		}
	}
	return options, requested, nil
}

type profilePlanner struct {
	ctx             context.Context
	options         BasicProfileOptions
	selected        map[ProfileOption]bool
	table           compiledProfileTable
	structuredTable compiledStructuredContentTable
	uids            *UIDRemapper
	report          ProfileReport
	elements        int
	items           int
	valueBytes      int64
	dateShiftDays   int
	sopClassUID     string
	reportBytes     int
}

func (state *profilePlanner) addSourceResidualRisks(source *object.Object) error {
	hasPixels := source.Has(tagPixelData) || source.Has(core.NewTag(0x7FE0, 0x0008)) || source.Has(core.NewTag(0x7FE0, 0x0009))
	if !state.selected[ProfileOptionCleanPixelData] && hasPixels {
		if err := state.addResidualRisk(ResidualRisk{Code: "pixel-cleaning-not-attested", Tag: tagPixelData}); err != nil {
			return err
		}
	}
	if !state.selected[ProfileOptionCleanPixelData] && (hasPixels || source.Has(tagBurnedInAnnotation)) {
		value, ok := singleCSValue(source, tagBurnedInAnnotation)
		if !ok || value != "NO" {
			if err := state.addResidualRisk(ResidualRisk{Code: "burned-in-annotation-not-cleared", Tag: tagBurnedInAnnotation}); err != nil {
				return err
			}
		}
	}
	if !state.selected[ProfileOptionCleanRecognizableFeatures] && (hasPixels || source.Has(tagRecognizableVisualFeatures)) {
		value, ok := singleCSValue(source, tagRecognizableVisualFeatures)
		if !ok || value != "NO" {
			if err := state.addResidualRisk(ResidualRisk{Code: "recognizable-visual-features-not-cleared", Tag: tagRecognizableVisualFeatures}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *profilePlanner) transformDataSet(source core.DataSet, path []AttributePathStep, depth int) (core.DataSet, error) {
	if err := state.ctx.Err(); err != nil {
		return core.DataSet{}, err
	}
	if depth > state.options.Limits.MaxDepth || len(source.Elements) > state.options.Limits.MaxElements-state.elements {
		return core.DataSet{}, ErrProfileResourceLimit
	}
	if depth == 0 {
		state.setSOPClassUID(source)
	}
	state.elements += len(source.Elements)
	creators := privateCreatorsFromElements(source.Elements)
	keepCreators := map[core.Tag]bool{}
	if state.selected[ProfileOptionRetainSafePrivate] {
		for _, element := range source.Elements {
			tag := element.Tag()
			if !tag.IsPrivate() || tag.Element < 0x1000 {
				continue
			}
			creatorTag := privateCreatorTagForDataTag(tag)
			creator := creators[creatorTag]
			if rule, ok := state.options.SafePrivateRegistry.lookup(tag, creator); ok && safePrivateRuleMatchesElement(rule, element) {
				keepCreators[creatorTag] = true
			}
		}
	}
	result := core.DataSet{ItemOffset: source.ItemOffset, ItemOffsetSet: source.ItemOffsetSet}
	result.Elements = make([]core.Element, 0, len(source.Elements))
	for _, sourceElement := range source.Elements {
		if err := state.ctx.Err(); err != nil {
			return core.DataSet{}, err
		}
		tag := sourceElement.Tag()
		valueBytes, ok := profileElementValueBytes(sourceElement)
		if !ok || valueBytes > state.options.Limits.MaxValueBytes-state.valueBytes {
			return core.DataSet{}, ErrProfileResourceLimit
		}
		state.valueBytes += valueBytes
		if tag.Group == 0x0004 {
			state.report.Summary.Removed++
			if err := state.record(path, tag, ActionCodeRemove, ProfileActionRemove, "file-set-only-attribute"); err != nil {
				return core.DataSet{}, err
			}
			continue
		}
		if isPrivateCreatorTag(tag) {
			if keepCreators[tag] {
				result.Elements = append(result.Elements, profileCloneElement(sourceElement))
				state.report.Summary.PrivateRetained++
			} else {
				state.report.Summary.PrivateRemoved++
				if err := state.record(path, tag, ActionCodeRemove, ProfileActionRemove, "private-creator-unused"); err != nil {
					return core.DataSet{}, err
				}
			}
			continue
		}
		if tag.IsPrivate() && tag.Element >= 0x1000 && state.selected[ProfileOptionRetainSafePrivate] {
			creator := creators[privateCreatorTagForDataTag(tag)]
			rule, registered := state.options.SafePrivateRegistry.lookup(tag, creator)
			if registered && safePrivateRuleMatchesElement(rule, sourceElement) {
				action := rule.Action
				if action == ProfileActionKeep {
					result.Elements = append(result.Elements, profileCloneElement(sourceElement))
					state.report.Summary.PrivateRetained++
					if err := state.record(path, tag, ActionCodeClean, action, "safe-private-verified"); err != nil {
						return core.DataSet{}, err
					}
					continue
				}
				cleaned, err := state.cleanElement(path, sourceElement, ActionCodeClean)
				if err != nil {
					return core.DataSet{}, err
				}
				if err := state.record(path, tag, ActionCodeClean, ProfileActionClean, "safe-private-verified"); err != nil {
					return core.DataSet{}, err
				}
				result.Elements = append(result.Elements, cleaned)
				state.report.Summary.PrivateRetained++
				continue
			}
			state.report.Summary.PrivateRemoved++
			reason := "safe-private-unverified"
			if registered {
				reason = "safe-private-shape-mismatch"
			}
			if err := state.record(path, tag, ActionCodeRemove, ProfileActionRemove, reason); err != nil {
				return core.DataSet{}, err
			}
			continue
		}
		rule, listed := state.table.lookup(tag)
		if !listed {
			if sequence, ok := sourceElement.Value.(core.SequenceValue); ok {
				var transformed core.Element
				var err error
				if state.selected[ProfileOptionCleanStructuredContent] && isStructuredContentItemSequence(tag) {
					transformed, err = state.transformStructuredContentSequence(sourceElement, sequence, path, depth)
				} else {
					transformed, err = state.transformSequence(sourceElement, sequence, path, depth)
				}
				if err != nil {
					return core.DataSet{}, err
				}
				result.Elements = append(result.Elements, transformed)
			} else {
				result.Elements = append(result.Elements, profileCloneElement(sourceElement))
			}
			continue
		}
		code := state.selectedActionCode(rule)
		requirement, reason, err := state.requirement(path, sourceElement, code)
		if err != nil {
			return core.DataSet{}, err
		}
		action, err := ResolveActionCode(code, requirement)
		if err != nil {
			return core.DataSet{}, err
		}
		transformed, keep, err := state.applyAction(path, sourceElement, code, action, reason, depth)
		if err != nil {
			return core.DataSet{}, err
		}
		if keep {
			result.Elements = append(result.Elements, transformed)
		}
	}
	return result, nil
}

func (state *profilePlanner) transformSequence(element core.Element, sequence core.SequenceValue, path []AttributePathStep, depth int) (core.Element, error) {
	if depth >= state.options.Limits.MaxDepth || len(sequence.Items) > state.options.Limits.MaxItems-state.items {
		return core.Element{}, ErrProfileResourceLimit
	}
	state.items += len(sequence.Items)
	items := make([]core.DataSet, len(sequence.Items))
	for index, item := range sequence.Items {
		itemPath := appendPath(path, AttributePathStep{SequenceTag: element.Tag(), ItemIndex: index})
		transformed, err := state.transformDataSet(item, itemPath, depth+1)
		if err != nil {
			return core.Element{}, err
		}
		items[index] = transformed
	}
	out := element
	out.Value = core.SequenceValue{Items: items}
	out.Header.Length = core.UndefinedLength
	out.Header.LengthSet = true
	return out, nil
}

var structuredContentOptionColumns = []struct {
	option ProfileOption
	column int
}{
	{ProfileOptionRetainUIDs, 1},
	{ProfileOptionRetainDeviceIdentity, 2},
	{ProfileOptionRetainInstitutionIdentity, 3},
	{ProfileOptionRetainPatientCharacteristics, 4},
	{ProfileOptionRetainFullDates, 5},
	{ProfileOptionRetainModifiedDates, 6},
	{ProfileOptionCleanDescriptors, 7},
}

func (state *profilePlanner) transformStructuredContentSequence(element core.Element, sequence core.SequenceValue, path []AttributePathStep, depth int) (core.Element, error) {
	if depth >= state.options.Limits.MaxDepth || len(sequence.Items) > state.options.Limits.MaxItems-state.items {
		return core.Element{}, ErrProfileResourceLimit
	}
	state.items += len(sequence.Items)
	items := make([]core.DataSet, 0, len(sequence.Items))
	for index, item := range sequence.Items {
		if err := state.ctx.Err(); err != nil {
			return core.Element{}, err
		}
		itemPath := appendPath(path, AttributePathStep{SequenceTag: element.Tag(), ItemIndex: index})
		rule, classified := state.structuredContentRule(item)
		if !classified {
			return core.Element{}, ErrUnclassifiedStructuredContent
		}

		code := state.selectedStructuredContentActionCode(rule)
		valueTag, hasValueTag := structuredContentValueTag(rule.ValueType)
		valueElement, hasValue := profileDataSetElement(item, valueTag)
		if !hasValueTag {
			hasValue = false
		}
		requirementElement := valueElement
		if !hasValue {
			requirementElement = core.Element{Header: core.ElementHeader{Tag: tagContentSequence, VR: core.VRSQ}}
		}
		requirement, _, err := state.requirement(itemPath, requirementElement, code)
		if err != nil {
			return core.Element{}, err
		}
		action, err := ResolveActionCode(code, requirement)
		if err != nil {
			return core.Element{}, err
		}
		switch action {
		case ProfileActionRemove:
			if err := state.consumeDataSetBudget(item, depth+1); err != nil {
				return core.Element{}, err
			}
			if err := state.record(itemPath, element.Tag(), code, action, "structured-content-table"); err != nil {
				return core.Element{}, err
			}
			state.report.Summary.Removed++
			continue
		case ProfileActionKeep:
			if err := state.record(itemPath, element.Tag(), code, action, "structured-content-table"); err != nil {
				return core.Element{}, err
			}
			if !hasValue {
				if strings.EqualFold(strings.TrimSpace(rule.ValueType), "CONTAINER") {
					transformed, err := state.transformDataSet(item, itemPath, depth+1)
					if err != nil {
						return core.Element{}, err
					}
					items = append(items, transformed)
					continue
				}
				return core.Element{}, ErrStructuredContentValue
			}
			transformed, err := state.transformStructuredContentItemKeep(item, valueElement, itemPath, depth+1)
			if err != nil {
				return core.Element{}, err
			}
			items = append(items, transformed)
		case ProfileActionDummy, ProfileActionClean:
			if !hasValue {
				return core.Element{}, ErrStructuredContentValue
			}
			transformed, err := state.transformStructuredContentItemValue(item, valueElement, itemPath, code, action, depth+1)
			if err != nil {
				return core.Element{}, err
			}
			items = append(items, transformed)
		default:
			return core.Element{}, ErrInvalidActionCode
		}
	}
	out := element
	out.Value = core.SequenceValue{Items: items}
	out.Header.Length = core.UndefinedLength
	out.Header.LengthSet = true
	return out, nil
}

func (state *profilePlanner) structuredContentRule(item core.DataSet) (generatedStructuredContentRule, bool) {
	valueType, ok := profileDataSetString(item, tagContentValueType)
	if !ok {
		return generatedStructuredContentRule{}, false
	}
	conceptElement, ok := profileDataSetElement(item, tagConceptNameCodeSequence)
	if !ok {
		return generatedStructuredContentRule{}, false
	}
	conceptSequence, ok := conceptElement.Value.(core.SequenceValue)
	if !ok || len(conceptSequence.Items) != 1 {
		return generatedStructuredContentRule{}, false
	}
	codeValue, ok := profileCodeValue(conceptSequence.Items[0])
	if !ok {
		return generatedStructuredContentRule{}, false
	}
	scheme, ok := profileDataSetString(conceptSequence.Items[0], tagCodingSchemeDesignator)
	if !ok {
		return generatedStructuredContentRule{}, false
	}
	key := structuredContentKey{
		codeValue: codeValue,
		scheme:    scheme,
		valueType: strings.ToUpper(valueType),
	}
	rule, ok := state.structuredTable[key]
	if !ok {
		if current, aliased := structuredContentAliases[structuredContentCodeKey{codeValue: key.codeValue, scheme: key.scheme}]; aliased {
			key.codeValue = current.codeValue
			key.scheme = current.scheme
			rule, ok = state.structuredTable[key]
		}
	}
	return rule, ok
}

func (state *profilePlanner) selectedStructuredContentActionCode(rule generatedStructuredContentRule) ActionCode {
	code := rule.Actions[0]
	for _, entry := range structuredContentOptionColumns {
		if state.selected[entry.option] && rule.Actions[entry.column] != "" {
			code = rule.Actions[entry.column]
		}
	}
	return code
}

func (state *profilePlanner) transformStructuredContentItemValue(item core.DataSet, valueElement core.Element, path []AttributePathStep, code ActionCode, action ProfileAction, depth int) (core.DataSet, error) {
	withoutValue := core.DataSet{ItemOffset: item.ItemOffset, ItemOffsetSet: item.ItemOffsetSet}
	withoutValue.Elements = make([]core.Element, 0, len(item.Elements)-1)
	for _, element := range item.Elements {
		if element.Tag() != valueElement.Tag() {
			withoutValue.Elements = append(withoutValue.Elements, element)
		}
	}
	transformed, err := state.transformDataSet(withoutValue, path, depth)
	if err != nil {
		return core.DataSet{}, err
	}
	if state.elements >= state.options.Limits.MaxElements {
		return core.DataSet{}, ErrProfileResourceLimit
	}
	state.elements++
	valueBytes, ok := profileElementValueBytes(valueElement)
	if !ok || valueBytes > state.options.Limits.MaxValueBytes-state.valueBytes {
		return core.DataSet{}, ErrProfileResourceLimit
	}
	state.valueBytes += valueBytes

	var replacement core.Element
	if action == ProfileActionDummy && valueElement.VR() == core.VRSQ {
		sequence, ok := valueElement.Value.(core.SequenceValue)
		if !ok || len(sequence.Items) == 0 {
			return core.DataSet{}, ErrStructuredContentValue
		}
		replacement, err = state.transformSequence(valueElement, sequence, path, depth)
		if err == nil {
			state.report.Summary.Dummied++
			err = state.record(path, valueElement.Tag(), code, action, "structured-content-table")
		}
	} else {
		replacement, _, err = state.applyAction(path, valueElement, code, action, "structured-content-table", depth)
	}
	if err != nil {
		return core.DataSet{}, err
	}
	transformed.Elements = append(transformed.Elements, replacement)
	return transformed, nil
}

func (state *profilePlanner) transformStructuredContentItemKeep(item core.DataSet, valueElement core.Element, path []AttributePathStep, depth int) (core.DataSet, error) {
	withoutValue := core.DataSet{ItemOffset: item.ItemOffset, ItemOffsetSet: item.ItemOffsetSet}
	withoutValue.Elements = make([]core.Element, 0, len(item.Elements)-1)
	for _, element := range item.Elements {
		if element.Tag() != valueElement.Tag() {
			withoutValue.Elements = append(withoutValue.Elements, element)
		}
	}
	transformed, err := state.transformDataSet(withoutValue, path, depth)
	if err != nil {
		return core.DataSet{}, err
	}
	if err := state.consumeElementBudget(valueElement, depth); err != nil {
		return core.DataSet{}, err
	}
	transformed.Elements = append(transformed.Elements, profileCloneElement(valueElement))
	return transformed, nil
}

func isStructuredContentItemSequence(tag core.Tag) bool {
	return tag == tagContentSequence || tag == tagAcquisitionContextSequence || tag == tagSpecimenPreparationContentSequence
}

func structuredContentValueTag(valueType string) (core.Tag, bool) {
	switch strings.ToUpper(strings.TrimSpace(valueType)) {
	case "TEXT":
		return tagStructuredTextValue, true
	case "PNAME":
		return tagStructuredPersonNameValue, true
	case "UIDREF":
		return tagStructuredUIDValue, true
	case "DATE":
		return tagStructuredDateValue, true
	case "TIME":
		return tagStructuredTimeValue, true
	case "DATETIME":
		return tagStructuredDateTimeValue, true
	case "CODE":
		return tagStructuredConceptCodeSequence, true
	case "NUM":
		return tagStructuredMeasuredValueSequence, true
	case "IMAGE", "COMPOSITE", "WAVEFORM":
		return tagReferencedSOPSequence, true
	default:
		return core.Tag{}, false
	}
}

func profileDataSetElement(dataset core.DataSet, tag core.Tag) (core.Element, bool) {
	for _, element := range dataset.Elements {
		if element.Tag() == tag {
			return element, true
		}
	}
	return core.Element{}, false
}

func profileDataSetString(dataset core.DataSet, tag core.Tag) (string, bool) {
	element, ok := profileDataSetElement(dataset, tag)
	if !ok {
		return "", false
	}
	value := strings.TrimSpace(element.StringValue())
	return value, value != ""
}

func profileCodeValue(dataset core.DataSet) (string, bool) {
	for _, tag := range []core.Tag{tagCodeValue, tagLongCodeValue, tagURNCodeValue} {
		if value, ok := profileDataSetString(dataset, tag); ok {
			return value, true
		}
	}
	return "", false
}

func (state *profilePlanner) consumeDataSetBudget(dataset core.DataSet, depth int) error {
	type frame struct {
		dataset core.DataSet
		depth   int
	}
	stack := []frame{{dataset: dataset, depth: depth}}
	for len(stack) > 0 {
		if err := state.ctx.Err(); err != nil {
			return err
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.depth > state.options.Limits.MaxDepth || len(current.dataset.Elements) > state.options.Limits.MaxElements-state.elements {
			return ErrProfileResourceLimit
		}
		state.elements += len(current.dataset.Elements)
		for _, element := range current.dataset.Elements {
			valueBytes, ok := profileElementValueBytes(element)
			if !ok || valueBytes > state.options.Limits.MaxValueBytes-state.valueBytes {
				return ErrProfileResourceLimit
			}
			state.valueBytes += valueBytes
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok {
				continue
			}
			if current.depth >= state.options.Limits.MaxDepth || len(sequence.Items) > state.options.Limits.MaxItems-state.items {
				return ErrProfileResourceLimit
			}
			state.items += len(sequence.Items)
			for index := len(sequence.Items) - 1; index >= 0; index-- {
				stack = append(stack, frame{dataset: sequence.Items[index], depth: current.depth + 1})
			}
		}
	}
	return nil
}

func (state *profilePlanner) consumeElementBudget(element core.Element, depth int) error {
	if state.elements >= state.options.Limits.MaxElements {
		return ErrProfileResourceLimit
	}
	state.elements++
	valueBytes, ok := profileElementValueBytes(element)
	if !ok || valueBytes > state.options.Limits.MaxValueBytes-state.valueBytes {
		return ErrProfileResourceLimit
	}
	state.valueBytes += valueBytes
	sequence, ok := element.Value.(core.SequenceValue)
	if !ok {
		return nil
	}
	if depth >= state.options.Limits.MaxDepth || len(sequence.Items) > state.options.Limits.MaxItems-state.items {
		return ErrProfileResourceLimit
	}
	state.items += len(sequence.Items)
	for _, item := range sequence.Items {
		if err := state.consumeDataSetBudget(item, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (state *profilePlanner) selectedActionCode(rule generatedProfileRule) ActionCode {
	code := rule.Actions[0]
	for _, option := range tableOptionOrder {
		if !state.selected[option] {
			continue
		}
		column, ok := profileOptionColumn[option]
		if ok && rule.Actions[column] != "" {
			code = rule.Actions[column]
		}
	}
	return code
}

func (state *profilePlanner) requirement(path []AttributePathStep, element core.Element, code ActionCode) (AttributeRequirement, string, error) {
	if code != ActionCodeZeroOrDummy && code != ActionCodeRemoveOrZero && code != ActionCodeRemoveOrDummy && code != ActionCodeRemoveZeroOrDummy && code != ActionCodeRemoveZeroOrUID {
		return AttributeOptional, "table", nil
	}
	context := AttributeContext{
		Path: append([]AttributePathStep(nil), path...), Tag: element.Tag(), VR: element.VR(),
		SOPClassUID: state.sopClassUID, ActionCode: code,
	}
	if state.options.RequirementResolver != nil {
		requirement, err := callRequirementResolver(state.ctx, state.options.RequirementResolver, context)
		if err != nil {
			return 0, "", err
		}
		if requirement > AttributeType1 {
			return 0, "", ErrInvalidProfileOptions
		}
		return requirement, "iod-resolver", nil
	}
	if state.options.RequireResolvedConditional {
		return 0, "", ErrInvalidProfileOptions
	}
	switch code {
	case ActionCodeRemoveOrZero:
		return AttributeType2, "conditional-conservative", nil
	default:
		return AttributeType1, "conditional-conservative", nil
	}
}

func (state *profilePlanner) applyAction(path []AttributePathStep, element core.Element, code ActionCode, action ProfileAction, reason string, depth int) (core.Element, bool, error) {
	tag := element.Tag()
	if element.Value == nil && element.Header.LengthSet && element.Header.Length > 0 &&
		(action == ProfileActionClean || action == ProfileActionRemapUID) {
		return core.Element{}, false, ErrDeferredValueUnavailable
	}
	if err := state.record(path, tag, code, action, reason); err != nil {
		return core.Element{}, false, err
	}
	switch action {
	case ProfileActionRemove:
		state.report.Summary.Removed++
		if tag.IsPrivate() {
			state.report.Summary.PrivateRemoved++
		}
		return core.Element{}, false, nil
	case ProfileActionZero:
		state.report.Summary.Zeroed++
		if element.VR() == core.VRSQ {
			return profileSequenceElement(tag, nil), true, nil
		}
		return core.NewRawElement(tag, element.VR(), nil), true, nil
	case ProfileActionDummy:
		state.report.Summary.Dummied++
		dummy, err := state.dummyElement(path, element, code)
		return dummy, err == nil, err
	case ProfileActionClean:
		if sequence, ok := element.Value.(core.SequenceValue); ok {
			var cleaned core.Element
			var err error
			if state.selected[ProfileOptionCleanStructuredContent] && isStructuredContentItemSequence(tag) {
				cleaned, err = state.transformStructuredContentSequence(element, sequence, path, depth)
			} else {
				cleaned, err = state.transformSequence(element, sequence, path, depth)
			}
			if err == nil {
				state.report.Summary.Cleaned++
			}
			return cleaned, err == nil, err
		}
		if state.selected[ProfileOptionRetainModifiedDates] && (element.VR() == core.VRDA || element.VR() == core.VRDT || element.VR() == core.VRTM) {
			if element.VR() == core.VRTM {
				return profileCloneElement(element), true, nil
			}
			shifted, err := shiftTemporalElement(element, state.dateShiftDays)
			if err != nil {
				return core.Element{}, false, err
			}
			state.report.Summary.DatesShifted++
			return shifted, true, nil
		}
		cleaned, err := state.cleanElement(path, element, code)
		return cleaned, err == nil, err
	case ProfileActionRemapUID:
		mapped, unresolved := state.remapUIDElement(element)
		state.report.Summary.UIDsUnresolved += unresolved
		state.report.Summary.UIDsRemapped += max(1, len(element.StringValues()))
		return mapped, true, nil
	case ProfileActionKeep:
		if element.VR() == core.VRUI {
			state.report.Summary.UIDsRetained += len(element.StringValues())
		}
		if sequence, ok := element.Value.(core.SequenceValue); ok {
			kept, err := state.transformSequence(element, sequence, path, depth)
			return kept, err == nil, err
		}
		return profileCloneElement(element), true, nil
	default:
		return core.Element{}, false, ErrInvalidActionCode
	}
}

// sopClassUID is kept on the planner separately so nested resolver calls do
// not depend on whether the root SOP Class element has already been visited.
func (state *profilePlanner) setSOPClassUID(source core.DataSet) {
	for _, element := range source.Elements {
		if element.Tag() == tagSOPClassUID {
			state.sopClassUID = strings.TrimSpace(element.StringValue())
			return
		}
	}
}

func (state *profilePlanner) record(path []AttributePathStep, tag core.Tag, code ActionCode, action ProfileAction, reason string) error {
	if len(state.report.Actions) >= state.options.Limits.MaxActions {
		return ErrProfileResourceLimit
	}
	record := ProfileActionRecord{
		Path:       append([]AttributePathStep(nil), path...),
		Tag:        tag,
		Action:     action,
		ActionCode: code,
		Reason:     reason,
	}
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded)+16 > state.options.Limits.MaxReportBytes-state.reportBytes {
		return ErrProfileResourceLimit
	}
	state.reportBytes += len(encoded) + 16
	state.report.Actions = append(state.report.Actions, record)
	return nil
}

func (state *profilePlanner) initializeReportBudget() error {
	encoded, err := json.Marshal(state.report)
	if err != nil || len(encoded) > state.options.Limits.MaxReportBytes {
		return ErrProfileResourceLimit
	}
	state.reportBytes = len(encoded)
	return nil
}

func (state *profilePlanner) addResidualRisk(risk ResidualRisk) error {
	encoded, err := json.Marshal(risk)
	if err != nil || len(encoded)+24 > state.options.Limits.MaxReportBytes-state.reportBytes {
		return ErrProfileResourceLimit
	}
	state.reportBytes += len(encoded) + 24
	state.report.Complete = false
	state.report.ResidualRisks = append(state.report.ResidualRisks, risk)
	return nil
}

func (state *profilePlanner) cleanElement(path []AttributePathStep, element core.Element, code ActionCode) (core.Element, error) {
	if state.options.Cleaner == nil {
		return core.Element{}, ErrCleanerRequired
	}
	context := CleanContext{AttributeContext: AttributeContext{
		Path: append([]AttributePathStep(nil), path...), Tag: element.Tag(), VR: element.VR(),
		SOPClassUID: state.sopClassUID, ActionCode: code,
	}, Element: profileCloneElement(element)}
	replacement, err := callElementCleaner(state.ctx, state.options.Cleaner, context)
	if err != nil {
		return core.Element{}, err
	}
	if replacement.Tag() != element.Tag() || replacement.VR() != element.VR() || replacement.Value == nil {
		return core.Element{}, ErrProfileCallback
	}
	state.report.Summary.Cleaned++
	return profileCloneElement(replacement), nil
}

func (state *profilePlanner) dummyElement(path []AttributePathStep, element core.Element, code ActionCode) (core.Element, error) {
	if element.VR() == core.VRSQ && state.options.DummyValueProvider == nil {
		return core.Element{}, ErrDummyValueProviderRequired
	}
	if state.options.DummyValueProvider != nil {
		context := DummyContext{AttributeContext: AttributeContext{
			Path: append([]AttributePathStep(nil), path...), Tag: element.Tag(), VR: element.VR(),
			SOPClassUID: state.sopClassUID, ActionCode: code,
		}, Element: profileCloneElement(element)}
		replacement, err := callDummyProvider(state.ctx, state.options.DummyValueProvider, context)
		if err != nil {
			return core.Element{}, err
		}
		if replacement.Tag() != element.Tag() || replacement.VR() != element.VR() || replacement.Value == nil || replacementIsEmpty(replacement) {
			return core.Element{}, ErrProfileCallback
		}
		return profileCloneElement(replacement), nil
	}
	return builtInDummy(element, state.uids), nil
}

func (state *profilePlanner) addRequiredProfileAttributes(obj *object.Object) {
	obj.Put(core.NewRawElement(tagPatientIdentityRemoved, core.VRCS, []byte("YES")))
	obj.Put(core.NewRawElement(tagDeidentificationMethod, core.VRLO, []byte("DICOM Basic Application Confidentiality Profile")))
	codes := []core.DataSet{profileMethodCode("113100", "Basic Application Confidentiality Profile")}
	for _, option := range state.options.SelectedOptions {
		if code, meaning, ok := profileOptionMethodCode(option); ok {
			codes = append(codes, profileMethodCode(code, meaning))
		}
	}
	obj.Put(profileSequenceElement(tagDeidentificationMethodCodeSequence, codes))
	status := "REMOVED"
	if state.selected[ProfileOptionRetainFullDates] {
		status = "UNMODIFIED"
	} else if state.selected[ProfileOptionRetainModifiedDates] {
		status = "MODIFIED"
	}
	obj.Put(core.NewRawElement(tagLongitudinalTemporalInformation, core.VRCS, []byte(status)))
}

func profileMethodCode(code, meaning string) core.DataSet {
	return core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagCodeValue, core.VRSH, []byte(code)),
		core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("DCM")),
		core.NewRawElement(tagCodeMeaning, core.VRLO, []byte(meaning)),
	}}
}

func profileOptionMethodCode(option ProfileOption) (string, string, bool) {
	switch option {
	case ProfileOptionCleanPixelData:
		return "113101", "Clean Pixel Data Option", true
	case ProfileOptionCleanRecognizableFeatures:
		return "113102", "Clean Recognizable Visual Features Option", true
	case ProfileOptionCleanGraphics:
		return "113103", "Clean Graphics Option", true
	case ProfileOptionCleanStructuredContent:
		return "113104", "Clean Structured Content Option", true
	case ProfileOptionCleanDescriptors:
		return "113105", "Clean Descriptors Option", true
	case ProfileOptionRetainFullDates:
		return "113106", "Retain Longitudinal Temporal Information Full Dates Option", true
	case ProfileOptionRetainModifiedDates:
		return "113107", "Retain Longitudinal Temporal Information Modified Dates Option", true
	case ProfileOptionRetainPatientCharacteristics:
		return "113108", "Retain Patient Characteristics Option", true
	case ProfileOptionRetainDeviceIdentity:
		return "113109", "Retain Device Identity Option", true
	case ProfileOptionRetainUIDs:
		return "113110", "Retain UIDs Option", true
	case ProfileOptionRetainSafePrivate:
		return "113111", "Retain Safe Private Option", true
	case ProfileOptionRetainInstitutionIdentity:
		return "113112", "Retain Institution Identity Option", true
	default:
		return "", "", false
	}
}

func profileSequenceElement(tag core.Tag, items []core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{Items: items},
	}
}

func sameProfileByteOrder(left, right binary.ByteOrder) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	probe := []byte{1, 2}
	return left.Uint16(probe) == right.Uint16(probe)
}

func appendPath(path []AttributePathStep, step AttributePathStep) []AttributePathStep {
	out := make([]AttributePathStep, len(path)+1)
	copy(out, path)
	out[len(path)] = step
	return out
}

func privateCreatorsFromElements(elements []core.Element) map[core.Tag]string {
	creators := make(map[core.Tag]string)
	for _, element := range elements {
		if isPrivateCreatorTag(element.Tag()) {
			creators[element.Tag()] = strings.TrimSpace(element.StringValue())
		}
	}
	return creators
}

func safePrivateRuleMatchesElement(rule SafePrivateRule, element core.Element) bool {
	return element.VR() == rule.VR && profileElementVM(element) == rule.VM
}

func profileElementVM(element core.Element) int {
	if element.Value == nil {
		return 0
	}
	switch value := element.Value.(type) {
	case core.SequenceValue, core.FragmentSequence, core.BulkDataValue:
		return 1
	case core.RawValue:
		if len(value) == 0 {
			return 0
		}
		if element.VR().IsStringLike() {
			return len(element.StringValues())
		}
		switch element.VR() {
		case core.VROB, core.VROD, core.VROF, core.VROL, core.VROV, core.VROW, core.VRUN:
			return 1
		}
		width := 1
		switch element.VR() {
		case core.VRUS, core.VRSS:
			width = 2
		case core.VRUL, core.VRSL, core.VRFL, core.VRAT:
			width = 4
		case core.VRUV, core.VRSV, core.VRFD:
			width = 8
		}
		if len(value) == 0 || len(value)%width != 0 {
			return 0
		}
		return len(value) / width
	case core.StringValue:
		return len(value)
	case core.Uint16Value:
		return len(value)
	case core.Int16Value:
		return len(value)
	case core.Uint32Value:
		return len(value)
	case core.Int32Value:
		return len(value)
	case core.Uint64Value:
		return len(value)
	case core.Int64Value:
		return len(value)
	case core.Float32Value:
		return len(value)
	case core.Float64Value:
		return len(value)
	case core.TagValue:
		return len(value)
	default:
		return 0
	}
}

func profileChanges(before, after core.DataSet) []profilePlanChange {
	beforeByTag := make(map[core.Tag]core.Element, len(before.Elements))
	afterByTag := make(map[core.Tag]core.Element, len(after.Elements))
	for _, element := range before.Elements {
		beforeByTag[element.Tag()] = element
	}
	for _, element := range after.Elements {
		afterByTag[element.Tag()] = element
	}
	changes := make([]profilePlanChange, 0)
	for tag, element := range beforeByTag {
		if replacement, ok := afterByTag[tag]; !ok {
			changes = append(changes, profilePlanChange{tag: tag, remove: true})
		} else if !reflect.DeepEqual(element, replacement) {
			changes = append(changes, profilePlanChange{tag: tag, element: profileCloneElement(replacement)})
		}
	}
	for tag, element := range afterByTag {
		if _, ok := beforeByTag[tag]; !ok {
			changes = append(changes, profilePlanChange{tag: tag, element: profileCloneElement(element)})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].tag.Less(changes[j].tag) })
	return changes
}

func profileCloneDataSet(source core.DataSet) core.DataSet {
	out := core.DataSet{ItemOffset: source.ItemOffset, ItemOffsetSet: source.ItemOffsetSet}
	out.Elements = make([]core.Element, len(source.Elements))
	for index := range source.Elements {
		out.Elements[index] = profileCloneElement(source.Elements[index])
	}
	return out
}

func profileCloneElement(source core.Element) core.Element {
	out := source
	switch value := source.Value.(type) {
	case nil:
	case core.RawValue:
		out.Value = core.RawValue(core.CloneBytes(value))
	case core.StringValue:
		out.Value = append(core.StringValue(nil), value...)
	case core.Uint16Value:
		out.Value = append(core.Uint16Value(nil), value...)
	case core.Int16Value:
		out.Value = append(core.Int16Value(nil), value...)
	case core.Uint32Value:
		out.Value = append(core.Uint32Value(nil), value...)
	case core.Int32Value:
		out.Value = append(core.Int32Value(nil), value...)
	case core.Uint64Value:
		out.Value = append(core.Uint64Value(nil), value...)
	case core.Int64Value:
		out.Value = append(core.Int64Value(nil), value...)
	case core.Float32Value:
		out.Value = append(core.Float32Value(nil), value...)
	case core.Float64Value:
		out.Value = append(core.Float64Value(nil), value...)
	case core.TagValue:
		out.Value = append(core.TagValue(nil), value...)
	case core.SequenceValue:
		items := make([]core.DataSet, len(value.Items))
		for index := range value.Items {
			items[index] = profileCloneDataSet(value.Items[index])
		}
		out.Value = core.SequenceValue{Items: items}
	case core.FragmentSequence:
		fragments := make([][]byte, len(value.Fragments))
		for index := range value.Fragments {
			fragments[index] = core.CloneBytes(value.Fragments[index])
		}
		out.Value = core.FragmentSequence{OffsetTable: core.CloneBytes(value.OffsetTable), Fragments: fragments}
	case core.BulkDataValue:
		out.Value = value
	default:
		out.Value = value
	}
	return out
}

func remapUIDElement(element core.Element, uids *UIDRemapper) core.Element {
	values := element.StringValues()
	if len(values) == 0 {
		return core.NewRawElement(element.Tag(), core.VRUI, nil)
	}
	for index := range values {
		values[index] = uids.Map(strings.TrimSpace(values[index]))
	}
	return core.NewRawElement(element.Tag(), core.VRUI, []byte(strings.Join(values, "\\")))
}

func (state *profilePlanner) remapUIDElement(element core.Element) (core.Element, int) {
	values := element.StringValues()
	if len(values) == 0 {
		uid, collisions := state.uids.mapMissingWithCollisionCount("profile:" + element.Tag().HexString())
		state.report.Summary.UIDCollisions += collisions
		return core.NewRawElement(element.Tag(), core.VRUI, []byte(uid)), 1
	}
	unresolved := 0
	for index := range values {
		original := core.NormalizeUID(values[index])
		if original == "" {
			unresolved++
			uid, collisions := state.uids.mapMissingWithCollisionCount("profile:" + element.Tag().HexString())
			state.report.Summary.UIDCollisions += collisions
			values[index] = uid
			continue
		}
		uid, collisions := state.uids.mapWithCollisionCount(original)
		state.report.Summary.UIDCollisions += collisions
		values[index] = uid
	}
	return core.NewRawElement(element.Tag(), core.VRUI, []byte(strings.Join(values, "\\"))), unresolved
}

func replacementIsEmpty(element core.Element) bool {
	if element.Value == nil {
		return true
	}
	if raw, ok := element.Value.(core.RawValue); ok {
		return len(raw) == 0
	}
	if stringsValue, ok := element.Value.(core.StringValue); ok {
		return len(stringsValue) == 0 || (len(stringsValue) == 1 && strings.TrimSpace(stringsValue[0]) == "")
	}
	return false
}

func builtInDummy(element core.Element, uids *UIDRemapper) core.Element {
	tag, vr := element.Tag(), element.VR()
	if vr == core.VRUI {
		return remapUIDElement(element, uids)
	}
	switch vr {
	case core.VRUS:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Uint16Value{1}}
	case core.VRSS:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Int16Value{1}}
	case core.VRUL:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Uint32Value{1}}
	case core.VRSL:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Int32Value{1}}
	case core.VRUV:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Uint64Value{1}}
	case core.VRSV:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Int64Value{1}}
	case core.VRFL:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Float32Value{1}}
	case core.VRFD:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.Float64Value{1}}
	case core.VRAT:
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.TagValue{core.NewTag(0x0008, 0x0008)}}
	case core.VROB, core.VROW, core.VROF, core.VROD, core.VROL, core.VROV, core.VRUN:
		return core.NewRawElement(tag, vr, []byte{1, 0})
	}
	var value string
	switch vr {
	case core.VRDA:
		value = "19000101"
	case core.VRTM:
		value = "000000"
	case core.VRDT:
		value = "19000101000000"
	case core.VRAS:
		value = "000Y"
	case core.VRDS, core.VRIS:
		value = "0"
	case core.VRPN:
		value = "ANONYMIZED"
	case core.VRAE, core.VRCS, core.VRSH:
		value = "ANON"
	default:
		value = "ANONYMIZED"
	}
	return core.NewRawElement(tag, vr, []byte(value))
}

func profileElementValueBytes(element core.Element) (int64, bool) {
	if element.Value == nil {
		if element.Header.LengthSet && element.Header.Length != core.UndefinedLength {
			return int64(element.Header.Length), true
		}
		return 0, true
	}
	switch value := element.Value.(type) {
	case core.SequenceValue:
		return 0, true
	case core.FragmentSequence:
		total := int64(len(value.OffsetTable))
		for _, fragment := range value.Fragments {
			if int64(len(fragment)) > math.MaxInt64-total {
				return 0, false
			}
			total += int64(len(fragment))
		}
		return total, true
	case core.RawValue:
		return int64(len(value)), true
	case core.StringValue:
		if length, ok := element.CalculatedLength(); ok {
			return int64(length), true
		}
		return 0, false
	default:
		if length, ok := element.CalculatedLength(); ok {
			return int64(length), true
		}
		return 0, true
	}
}

func isPixelCleanerTag(tag core.Tag) bool {
	return tag == core.NewTag(0x7FE0, 0x0010) ||
		tag == core.NewTag(0x7FE0, 0x0008) ||
		tag == core.NewTag(0x7FE0, 0x0009) ||
		tag == core.NewTag(0x0088, 0x0200)
}

func isVisualCleanerTag(tag core.Tag) bool {
	return isPixelCleanerTag(tag) ||
		(tag.Group%2 == 0 && tag.Group&0xff00 == 0x6000) ||
		tag.Group == 0x0070
}

func profileHasDeferredValue(dataset *object.Object, allowed func(core.Tag) bool) bool {
	type frame struct {
		dataset         core.DataSet
		allowedAncestor bool
	}
	stack := []frame{{dataset: dataset.ToDataSet()}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, element := range current.dataset.Elements {
			allowedHere := current.allowedAncestor || allowed(element.Tag())
			if allowedHere && element.Value == nil {
				return true
			}
			if sequence, ok := element.Value.(core.SequenceValue); ok {
				for _, item := range sequence.Items {
					stack = append(stack, frame{dataset: item, allowedAncestor: allowedHere})
				}
			}
		}
	}
	return false
}

func profileMutationsAllowed(before, after core.DataSet, allowed func(core.Tag) bool) bool {
	beforeByTag := make(map[core.Tag][]core.Element, len(before.Elements))
	afterByTag := make(map[core.Tag][]core.Element, len(after.Elements))
	for _, element := range before.Elements {
		beforeByTag[element.Tag()] = append(beforeByTag[element.Tag()], element)
	}
	for _, element := range after.Elements {
		afterByTag[element.Tag()] = append(afterByTag[element.Tag()], element)
	}
	for tag, elements := range beforeByTag {
		replacements := afterByTag[tag]
		if len(elements) != len(replacements) {
			if !allowed(tag) {
				return false
			}
			for _, element := range append(append([]core.Element(nil), elements...), replacements...) {
				if _, sequence := element.Value.(core.SequenceValue); sequence {
					return false
				}
			}
		}
		for index := 0; index < min(len(elements), len(replacements)); index++ {
			element, replacement := elements[index], replacements[index]
			if reflect.DeepEqual(element, replacement) {
				continue
			}
			beforeSequence, beforeIsSequence := element.Value.(core.SequenceValue)
			afterSequence, afterIsSequence := replacement.Value.(core.SequenceValue)
			if beforeIsSequence || afterIsSequence {
				if !beforeIsSequence || !afterIsSequence || element.VR() != replacement.VR() || len(beforeSequence.Items) != len(afterSequence.Items) {
					return false
				}
				for itemIndex := range beforeSequence.Items {
					if !profileMutationsAllowed(beforeSequence.Items[itemIndex], afterSequence.Items[itemIndex], allowed) {
						return false
					}
				}
				continue
			}
			if !allowed(tag) {
				return false
			}
		}
	}
	for tag, elements := range afterByTag {
		if _, ok := beforeByTag[tag]; !ok {
			if !allowed(tag) {
				return false
			}
			for _, element := range elements {
				if _, sequence := element.Value.(core.SequenceValue); sequence {
					return false
				}
			}
		}
	}
	return true
}

func validateProfileDataSetBudget(dataset core.DataSet, limits ProfileLimits) error {
	type frame struct {
		dataset core.DataSet
		depth   int
	}
	stack := []frame{{dataset: dataset}}
	elements, items := 0, 0
	var valueBytes int64
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.depth > limits.MaxDepth || len(current.dataset.Elements) > limits.MaxElements-elements {
			return ErrProfileResourceLimit
		}
		elements += len(current.dataset.Elements)
		for _, element := range current.dataset.Elements {
			bytes, ok := profileElementValueBytes(element)
			if !ok || bytes > limits.MaxValueBytes-valueBytes {
				return ErrProfileResourceLimit
			}
			valueBytes += bytes
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok {
				continue
			}
			if current.depth >= limits.MaxDepth || len(sequence.Items) > limits.MaxItems-items {
				return ErrProfileResourceLimit
			}
			items += len(sequence.Items)
			for index := len(sequence.Items) - 1; index >= 0; index-- {
				stack = append(stack, frame{dataset: sequence.Items[index], depth: current.depth + 1})
			}
		}
	}
	return nil
}

func shiftTemporalElement(element core.Element, days int) (core.Element, error) {
	if element.Value == nil && element.Header.LengthSet && element.Header.Length > 0 {
		return core.Element{}, ErrDeferredValueUnavailable
	}
	values := element.StringValues()
	if len(values) == 0 {
		return core.NewRawElement(element.Tag(), element.VR(), nil), nil
	}
	shifted := make([]string, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch element.VR() {
		case core.VRDA:
			parsed, err := dcmtime.ParseDate(value)
			if err != nil {
				return core.Element{}, ErrProfileCallback
			}
			parsed.Time = parsed.Time.AddDate(0, 0, days)
			shifted[index] = parsed.DCM()
		case core.VRDT:
			parsed, err := dcmtime.ParseDatetime(value)
			if err != nil {
				return core.Element{}, ErrProfileCallback
			}
			parsed.Time = parsed.Time.AddDate(0, 0, days)
			shifted[index] = parsed.DCM()
		default:
			return core.Element{}, ErrProfileCallback
		}
	}
	if strings.Join(shifted, "\\") == strings.Join(values, "\\") {
		return core.Element{}, ErrUnrepresentableDateShift
	}
	return core.NewRawElement(element.Tag(), element.VR(), []byte(strings.Join(shifted, "\\"))), nil
}

func callRequirementResolver(ctx context.Context, callback AttributeRequirementResolver, input AttributeContext) (output AttributeRequirement, err error) {
	defer func() {
		if recover() != nil {
			err = ErrProfileCallback
		}
	}()
	output, callbackErr := callback(ctx, input)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if callbackErr != nil {
		return 0, ErrProfileCallback
	}
	return output, nil
}

func callElementCleaner(ctx context.Context, callback ElementCleaner, input CleanContext) (output core.Element, err error) {
	defer func() {
		if recover() != nil {
			err = ErrProfileCallback
		}
	}()
	output, callbackErr := callback(ctx, input)
	if err := ctx.Err(); err != nil {
		return core.Element{}, err
	}
	if callbackErr != nil {
		return core.Element{}, ErrProfileCallback
	}
	return output, nil
}

func callDummyProvider(ctx context.Context, callback DummyValueProvider, input DummyContext) (output core.Element, err error) {
	defer func() {
		if recover() != nil {
			err = ErrProfileCallback
		}
	}()
	output, callbackErr := callback(ctx, input)
	if err := ctx.Err(); err != nil {
		return core.Element{}, err
	}
	if callbackErr != nil {
		return core.Element{}, ErrProfileCallback
	}
	return output, nil
}

func callDateShiftPolicy(ctx context.Context, callback DateShiftPolicy, input *object.Object) (days int, err error) {
	defer func() {
		if recover() != nil {
			err = ErrProfileCallback
		}
	}()
	days, callbackErr := callback(ctx, DateShiftContext{Object: input})
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if callbackErr != nil {
		return 0, ErrProfileCallback
	}
	return days, nil
}

func callPixelCleaner(ctx context.Context, callback ProfilePixelCleaner, input *object.Object) (regions []PixelRegion, err error) {
	defer func() {
		if recover() != nil {
			err = ErrProfileCallback
		}
	}()
	regions, callbackErr := callback(ctx, input)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if callbackErr != nil {
		return nil, ErrProfileCallback
	}
	return append([]PixelRegion(nil), regions...), nil
}

func callVisualCleaner(ctx context.Context, callback VisualFeaturesCleaner, input *object.Object) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrProfileCallback
		}
	}()
	callbackErr := callback(ctx, input)
	if err := ctx.Err(); err != nil {
		return err
	}
	if callbackErr != nil {
		return ErrProfileCallback
	}
	return nil
}

func profileDigest(dataset core.DataSet) [sha256.Size]byte {
	digest := sha256.New()
	hashDataSet(digest, dataset)
	var sum [sha256.Size]byte
	copy(sum[:], digest.Sum(nil))
	return sum
}

func hashDataSet(digest hash.Hash, dataset core.DataSet) {
	writeUint64(digest, uint64(len(dataset.Elements)))
	for _, element := range dataset.Elements {
		writeUint16(digest, element.Tag().Group)
		writeUint16(digest, element.Tag().Element)
		digest.Write([]byte(element.VR()))
		writeUint32(digest, uint32(element.Header.Length))
		if element.Header.LengthSet {
			digest.Write([]byte{1})
		} else {
			digest.Write([]byte{0})
		}
		hashValue(digest, element.Value)
	}
}

func hashValue(digest hash.Hash, value core.Value) {
	if value == nil {
		digest.Write([]byte{0xff})
		return
	}
	digest.Write([]byte{byte(value.Kind())})
	switch typed := value.(type) {
	case core.RawValue:
		hashBytes(digest, typed)
	case core.StringValue:
		for _, value := range typed {
			hashBytes(digest, []byte(value))
		}
	case core.Uint16Value:
		for _, value := range typed {
			writeUint16(digest, value)
		}
	case core.Int16Value:
		for _, value := range typed {
			writeUint16(digest, uint16(value))
		}
	case core.Uint32Value:
		for _, value := range typed {
			writeUint32(digest, value)
		}
	case core.Int32Value:
		for _, value := range typed {
			writeUint32(digest, uint32(value))
		}
	case core.Uint64Value:
		for _, value := range typed {
			writeUint64(digest, value)
		}
	case core.Int64Value:
		for _, value := range typed {
			writeUint64(digest, uint64(value))
		}
	case core.Float32Value:
		for _, value := range typed {
			writeUint32(digest, math.Float32bits(value))
		}
	case core.Float64Value:
		for _, value := range typed {
			writeUint64(digest, math.Float64bits(value))
		}
	case core.TagValue:
		for _, value := range typed {
			writeUint16(digest, value.Group)
			writeUint16(digest, value.Element)
		}
	case core.SequenceValue:
		writeUint64(digest, uint64(len(typed.Items)))
		for _, item := range typed.Items {
			hashDataSet(digest, item)
		}
	case core.FragmentSequence:
		hashBytes(digest, typed.OffsetTable)
		for _, fragment := range typed.Fragments {
			hashBytes(digest, fragment)
		}
	case core.BulkDataValue:
		hashBytes(digest, []byte(typed.URI))
	default:
		hashBytes(digest, []byte(fmt.Sprintf("%T", value)))
	}
}

func hashBytes(digest hash.Hash, value []byte) {
	writeUint64(digest, uint64(len(value)))
	digest.Write(value)
}
func writeUint16(digest hash.Hash, value uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], value)
	digest.Write(b[:])
}
func writeUint32(digest hash.Hash, value uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], value)
	digest.Write(b[:])
}
func writeUint64(digest hash.Hash, value uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], value)
	digest.Write(b[:])
}
