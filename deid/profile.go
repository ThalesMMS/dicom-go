package deid

import (
	"context"
	"errors"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	ErrInvalidActionCode             = errors.New("deid: invalid PS3.15 action code")
	ErrInvalidProfileOptions         = errors.New("deid: invalid basic profile options")
	ErrProfileResourceLimit          = errors.New("deid: basic profile resource limit exceeded")
	ErrStaleProfilePlan              = errors.New("deid: basic profile plan no longer matches object")
	ErrCleanerRequired               = errors.New("deid: cleaner required for selected profile option")
	ErrDummyValueProviderRequired    = errors.New("deid: dummy value provider required for sequence attribute")
	ErrDateShiftPolicyRequired       = errors.New("deid: date shift policy required for modified dates")
	ErrSafePrivateRegistryRequired   = errors.New("deid: verified safe-private registry required")
	ErrPixelCleanerRequired          = errors.New("deid: pixel cleaner required for selected option")
	ErrVisualCleanerRequired         = errors.New("deid: visual-feature cleaner required for selected option")
	ErrProfileCallback               = errors.New("deid: profile callback failed")
	ErrDICOMDIRPolicyRequired        = errors.New("deid: DICOMDIR requires a media-set transaction policy")
	ErrUnrepresentableDateShift      = errors.New("deid: date shift cannot be represented at source precision")
	ErrDeferredValueUnavailable      = errors.New("deid: deferred value unavailable to profile callback")
	ErrStructuredContentValue        = errors.New("deid: structured content item value is missing or malformed")
	ErrUnclassifiedStructuredContent = errors.New("deid: structured content concept is not covered by the selected normative table")
)

// ActionCode is one action or conditional action from PS3.15 Table E.1-1a.
type ActionCode string

const (
	ActionCodeRemove            ActionCode = "X"
	ActionCodeZero              ActionCode = "Z"
	ActionCodeDummy             ActionCode = "D"
	ActionCodeClean             ActionCode = "C"
	ActionCodeUID               ActionCode = "U"
	ActionCodeKeep              ActionCode = "K"
	ActionCodeZeroOrDummy       ActionCode = "Z/D"
	ActionCodeRemoveOrZero      ActionCode = "X/Z"
	ActionCodeRemoveOrDummy     ActionCode = "X/D"
	ActionCodeRemoveZeroOrDummy ActionCode = "X/Z/D"
	ActionCodeRemoveZeroOrUID   ActionCode = "X/Z/U*"
)

// ProfileAction is the concrete operation selected for one encoded attribute.
type ProfileAction string

const (
	ProfileActionRemove   ProfileAction = "remove"
	ProfileActionZero     ProfileAction = "zero"
	ProfileActionDummy    ProfileAction = "dummy"
	ProfileActionClean    ProfileAction = "clean"
	ProfileActionRemapUID ProfileAction = "uid-remap"
	ProfileActionKeep     ProfileAction = "keep"
)

// AttributeRequirement describes the applicable IOD requirement for a
// conditional Table E.1-1 action. Conditional attributes whose condition is
// false use AttributeOptional.
type AttributeRequirement uint8

const (
	AttributeOptional AttributeRequirement = iota
	AttributeType2
	AttributeType1
)

func (requirement AttributeRequirement) String() string {
	switch requirement {
	case AttributeOptional:
		return "optional"
	case AttributeType2:
		return "type2"
	case AttributeType1:
		return "type1"
	default:
		return "invalid"
	}
}

// ResolveActionCode converts one normative code into a concrete operation for
// the applicable IOD requirement.
func ResolveActionCode(code ActionCode, requirement AttributeRequirement) (ProfileAction, error) {
	if requirement > AttributeType1 {
		return "", ErrInvalidActionCode
	}
	switch code {
	case ActionCodeRemove:
		return ProfileActionRemove, nil
	case ActionCodeZero:
		return ProfileActionZero, nil
	case ActionCodeDummy:
		return ProfileActionDummy, nil
	case ActionCodeClean:
		return ProfileActionClean, nil
	case ActionCodeUID:
		return ProfileActionRemapUID, nil
	case ActionCodeKeep:
		return ProfileActionKeep, nil
	case ActionCodeZeroOrDummy:
		if requirement == AttributeType1 {
			return ProfileActionDummy, nil
		}
		return ProfileActionZero, nil
	case ActionCodeRemoveOrZero:
		if requirement == AttributeType1 || requirement == AttributeType2 {
			return ProfileActionZero, nil
		}
		return ProfileActionRemove, nil
	case ActionCodeRemoveOrDummy:
		if requirement == AttributeType1 {
			return ProfileActionDummy, nil
		}
		return ProfileActionRemove, nil
	case ActionCodeRemoveZeroOrDummy:
		switch requirement {
		case AttributeType1:
			return ProfileActionDummy, nil
		case AttributeType2:
			return ProfileActionZero, nil
		default:
			return ProfileActionRemove, nil
		}
	case ActionCodeRemoveZeroOrUID:
		switch requirement {
		case AttributeType1:
			return ProfileActionRemapUID, nil
		case AttributeType2:
			return ProfileActionZero, nil
		default:
			return ProfileActionRemove, nil
		}
	default:
		return "", ErrInvalidActionCode
	}
}

// ProfileOption selects one PS3.15 Basic Profile option. Options are explicit;
// the zero value selects only the Basic Application Confidentiality Profile.
type ProfileOption string

const (
	ProfileOptionCleanPixelData               ProfileOption = "clean-pixel-data"
	ProfileOptionCleanRecognizableFeatures    ProfileOption = "clean-recognizable-visual-features"
	ProfileOptionCleanGraphics                ProfileOption = "clean-graphics"
	ProfileOptionCleanStructuredContent       ProfileOption = "clean-structured-content"
	ProfileOptionCleanDescriptors             ProfileOption = "clean-descriptors"
	ProfileOptionRetainFullDates              ProfileOption = "retain-full-dates"
	ProfileOptionRetainModifiedDates          ProfileOption = "retain-modified-dates"
	ProfileOptionRetainPatientCharacteristics ProfileOption = "retain-patient-characteristics"
	ProfileOptionRetainDeviceIdentity         ProfileOption = "retain-device-identity"
	ProfileOptionRetainInstitutionIdentity    ProfileOption = "retain-institution-identity"
	ProfileOptionRetainUIDs                   ProfileOption = "retain-uids"
	ProfileOptionRetainSafePrivate            ProfileOption = "retain-safe-private"
)

// ProfileLimits bounds planning before any source mutation occurs.
type ProfileLimits struct {
	MaxDepth       int
	MaxElements    int
	MaxItems       int
	MaxValueBytes  int64
	MaxActions     int
	MaxPixelMasks  int
	MaxReportBytes int
}

// DefaultProfileLimits returns conservative offline limits.
func DefaultProfileLimits() ProfileLimits {
	return ProfileLimits{
		MaxDepth: 64, MaxElements: 100_000, MaxItems: 100_000, MaxActions: 100_000,
		MaxValueBytes: 2 << 30, MaxPixelMasks: 10_000, MaxReportBytes: 8 << 20,
	}
}

// AttributePathStep identifies a containing Sequence Item without carrying
// values. ItemIndex is zero-based.
type AttributePathStep struct {
	SequenceTag core.Tag `json:"sequence_tag"`
	ItemIndex   int      `json:"item_index"`
}

// AttributeContext is passed to an IOD requirement resolver.
type AttributeContext struct {
	Path        []AttributePathStep
	Tag         core.Tag
	VR          core.VR
	SOPClassUID string
	ActionCode  ActionCode
}

// AttributeRequirementResolver supplies Type 1/2/optional knowledge for
// conditional actions. It is trusted in-process code and must not log values.
type AttributeRequirementResolver func(context.Context, AttributeContext) (AttributeRequirement, error)

// CleanContext is passed to a caller cleaner for a C action. Element may carry
// PHI and is deliberately omitted from reports.
type CleanContext struct {
	AttributeContext
	Element core.Element
}

// ElementCleaner returns a value-safe replacement with the same Tag and VR.
type ElementCleaner func(context.Context, CleanContext) (core.Element, error)

// DummyContext is passed to an optional VR-aware dummy provider.
type DummyContext struct {
	AttributeContext
	Element core.Element
}

// DummyValueProvider returns a non-zero replacement with the same Tag and VR.
type DummyValueProvider func(context.Context, DummyContext) (core.Element, error)

// DateShiftContext is provided once per plan. Object is a detached clone and
// may contain PHI; the policy is trusted code. Returned days never enter the
// redacted report.
type DateShiftContext struct {
	Object *object.Object
}

// DateShiftPolicy returns one deterministic day offset for the complete plan.
type DateShiftPolicy func(context.Context, DateShiftContext) (int, error)

// ProfilePixelCleaner applies the Clean Pixel Data Option to the detached
// planning clone and returns the explicit regions it changed.
type ProfilePixelCleaner func(context.Context, *object.Object) ([]PixelRegion, error)

// VisualFeaturesCleaner applies the Clean Recognizable Visual Features Option
// to the detached planning clone.
type VisualFeaturesCleaner func(context.Context, *object.Object) error

// BasicProfileOptions configures a bounded, auditable Basic Profile plan.
type BasicProfileOptions struct {
	SelectedOptions            []ProfileOption
	RequirementResolver        AttributeRequirementResolver
	RequireResolvedConditional bool
	Cleaner                    ElementCleaner
	DummyValueProvider         DummyValueProvider
	DateShiftPolicy            DateShiftPolicy
	SafePrivateRegistry        *SafePrivateRegistry
	PixelCleaner               ProfilePixelCleaner
	VisualFeaturesCleaner      VisualFeaturesCleaner
	Limits                     ProfileLimits
}

// DefaultBasicProfileOptions returns the Basic Profile with no retention or
// cleaning options and bounded conservative conditional resolution.
func DefaultBasicProfileOptions() BasicProfileOptions {
	return BasicProfileOptions{Limits: DefaultProfileLimits()}
}

// ProfileActionRecord is a redacted audit entry. It never includes original or
// replacement values, UIDs, dates, paths on disk, or private creator strings.
type ProfileActionRecord struct {
	Path       []AttributePathStep `json:"path,omitempty"`
	Tag        core.Tag            `json:"tag"`
	Action     ProfileAction       `json:"action"`
	ActionCode ActionCode          `json:"action_code"`
	Reason     string              `json:"reason"`
}

// ResidualRisk is a value-free warning that requires caller review.
type ResidualRisk struct {
	Code string   `json:"code"`
	Tag  core.Tag `json:"tag,omitempty"`
}

// ProfileSummary contains aggregate, value-free counters.
type ProfileSummary struct {
	Removed         int `json:"removed"`
	Zeroed          int `json:"zeroed"`
	Dummied         int `json:"dummied"`
	Cleaned         int `json:"cleaned"`
	UIDsRemapped    int `json:"uids_remapped"`
	UIDsRetained    int `json:"uids_retained"`
	UIDsUnresolved  int `json:"uids_unresolved"`
	UIDCollisions   int `json:"uid_collisions"`
	PrivateRemoved  int `json:"private_removed"`
	PrivateRetained int `json:"private_retained"`
	DatesShifted    int `json:"dates_shifted"`
	PixelMasks      int `json:"pixel_masks"`
}

// ProfileReport is the PHI-redacted result shared by dry-run and Apply.
type ProfileReport struct {
	StandardVersion           string                `json:"standard_version"`
	TableSource               string                `json:"table_source"`
	TableChecksum             string                `json:"table_checksum"`
	StructuredContentSource   string                `json:"structured_content_source,omitempty"`
	StructuredContentChecksum string                `json:"structured_content_checksum,omitempty"`
	RetiredCodeSource         string                `json:"retired_code_source,omitempty"`
	RetiredCodeChecksum       string                `json:"retired_code_checksum,omitempty"`
	SafePrivateVersion        string                `json:"safe_private_version,omitempty"`
	SafePrivateChecksum       string                `json:"safe_private_checksum,omitempty"`
	Complete                  bool                  `json:"complete"`
	Options                   []ProfileOption       `json:"options,omitempty"`
	Actions                   []ProfileActionRecord `json:"actions,omitempty"`
	ResidualRisks             []ResidualRisk        `json:"residual_risks,omitempty"`
	Summary                   ProfileSummary        `json:"summary"`
}

func (report ProfileReport) clone() ProfileReport {
	out := report
	out.Options = append([]ProfileOption(nil), report.Options...)
	out.Actions = append([]ProfileActionRecord(nil), report.Actions...)
	for index := range out.Actions {
		out.Actions[index].Path = append([]AttributePathStep(nil), report.Actions[index].Path...)
	}
	out.ResidualRisks = append([]ResidualRisk(nil), report.ResidualRisks...)
	return out
}

type generatedProfileRule struct {
	Pattern string
	Actions [11]ActionCode
}
