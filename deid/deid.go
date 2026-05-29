// Package deid provides reusable, conservative metadata de-identification
// helpers for DICOM datasets. Callers remain responsible for assessing pixel
// data and application-specific attributes; use of this package alone is not a
// claim of conformance to every DICOM confidentiality profile option.
package deid

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

var (
	tagPatientName                 = core.NewTag(0x0010, 0x0010)
	tagPatientID                   = core.NewTag(0x0010, 0x0020)
	tagPatientBirthDate            = core.NewTag(0x0010, 0x0030)
	tagOtherPatientIDs             = core.NewTag(0x0010, 0x1000)
	tagPatientAddress              = core.NewTag(0x0010, 0x1040)
	tagInstitutionName             = core.NewTag(0x0008, 0x0080)
	tagReferringPhysician          = core.NewTag(0x0008, 0x0090)
	tagPhysiciansOfRecord          = core.NewTag(0x0008, 0x1048)
	tagPerformingPhysician         = core.NewTag(0x0008, 0x1050)
	tagOperatorsName               = core.NewTag(0x0008, 0x1070)
	tagAccessionNumber             = core.NewTag(0x0008, 0x0050)
	tagStudyDate                   = core.NewTag(0x0008, 0x0020)
	tagPatientBirthTime            = core.NewTag(0x0010, 0x0032)
	tagIssuerOfPatientID           = core.NewTag(0x0010, 0x0021)
	tagOtherPatientNames           = core.NewTag(0x0010, 0x1001)
	tagPatientBirthName            = core.NewTag(0x0010, 0x1005)
	tagMedicalRecordLocator        = core.NewTag(0x0010, 0x1090)
	tagMothersBirthName            = core.NewTag(0x0010, 0x1060)
	tagPatientTelephone            = core.NewTag(0x0010, 0x2154)
	tagPatientComments             = core.NewTag(0x0010, 0x4000)
	tagInstitutionAddress          = core.NewTag(0x0008, 0x0081)
	tagStationName                 = core.NewTag(0x0008, 0x1010)
	tagStudyDescription            = core.NewTag(0x0008, 0x1030)
	tagSeriesDescription           = core.NewTag(0x0008, 0x103E)
	tagReferringPhysicianAddress   = core.NewTag(0x0008, 0x0092)
	tagReferringPhysicianTelephone = core.NewTag(0x0008, 0x0094)
	tagRequestingPhysician         = core.NewTag(0x0032, 0x1032)
	tagDeviceSerialNumber          = core.NewTag(0x0018, 0x1000)
	tagProtocolName                = core.NewTag(0x0018, 0x1030)
	tagStudyID                     = core.NewTag(0x0020, 0x0010)
	tagImageComments               = core.NewTag(0x0020, 0x4000)

	tagInstanceCreationDate = core.NewTag(0x0008, 0x0012)
	tagInstanceCreationTime = core.NewTag(0x0008, 0x0013)
	tagSeriesDate           = core.NewTag(0x0008, 0x0021)
	tagAcquisitionDate      = core.NewTag(0x0008, 0x0022)
	tagContentDate          = core.NewTag(0x0008, 0x0023)
	tagOverlayDate          = core.NewTag(0x0008, 0x0024)
	tagCurveDate            = core.NewTag(0x0008, 0x0025)
	tagAcquisitionDateTime  = core.NewTag(0x0008, 0x002A)
	tagStudyTime            = core.NewTag(0x0008, 0x0030)
	tagSeriesTime           = core.NewTag(0x0008, 0x0031)
	tagAcquisitionTime      = core.NewTag(0x0008, 0x0032)
	tagContentTime          = core.NewTag(0x0008, 0x0033)
	tagOverlayTime          = core.NewTag(0x0008, 0x0034)
	tagCurveTime            = core.NewTag(0x0008, 0x0035)

	tagInstitutionCodeSequence                   = core.NewTag(0x0008, 0x0082)
	tagReferringPhysicianIdentificationSequence  = core.NewTag(0x0008, 0x0096)
	tagPhysiciansOfRecordIdentificationSequence  = core.NewTag(0x0008, 0x1049)
	tagPerformingPhysicianIdentificationSequence = core.NewTag(0x0008, 0x1052)
	tagOperatorIdentificationSequence            = core.NewTag(0x0008, 0x1072)
	tagIssuerOfPatientIDQualifiersSequence       = core.NewTag(0x0010, 0x0024)
	tagOtherPatientIDsSequence                   = core.NewTag(0x0010, 0x1002)
	tagRequestingPhysicianIdentificationSequence = core.NewTag(0x0032, 0x1031)

	tagStudyInstanceUID  = core.NewTag(0x0020, 0x000D)
	tagSeriesInstanceUID = core.NewTag(0x0020, 0x000E)
	tagSOPInstanceUID    = core.NewTag(0x0008, 0x0018)
	tagReferencedSOPUID  = core.NewTag(0x0008, 0x1155)

	tagSamplesPerPixel    = core.NewTag(0x0028, 0x0002)
	tagRows               = core.NewTag(0x0028, 0x0010)
	tagColumns            = core.NewTag(0x0028, 0x0011)
	tagBitsAllocated      = core.NewTag(0x0028, 0x0100)
	tagBurnedInAnnotation = core.NewTag(0x0028, 0x0301)
	tagPixelData          = core.NewTag(0x7FE0, 0x0010)
)

type attributeToBlank struct {
	tag core.Tag
	vr  core.VR
}

var basicProfileTextAttributes = []attributeToBlank{
	{tagPatientBirthTime, core.VRTM},
	{tagIssuerOfPatientID, core.VRLO},
	{tagOtherPatientNames, core.VRPN},
	{tagPatientBirthName, core.VRPN},
	{tagMedicalRecordLocator, core.VRLO},
	{tagMothersBirthName, core.VRPN},
	{tagPatientTelephone, core.VRSH},
	{tagPatientComments, core.VRLT},
	{tagInstitutionAddress, core.VRST},
	{tagStationName, core.VRSH},
	{tagStudyDescription, core.VRLO},
	{tagSeriesDescription, core.VRLO},
	{tagReferringPhysicianAddress, core.VRST},
	{tagReferringPhysicianTelephone, core.VRSH},
	{tagRequestingPhysician, core.VRPN},
	{tagDeviceSerialNumber, core.VRLO},
	{tagProtocolName, core.VRLO},
	{tagStudyID, core.VRSH},
	{tagImageComments, core.VRLT},
}

var longitudinalDateTimeAttributes = []attributeToBlank{
	{tagInstanceCreationDate, core.VRDA},
	{tagInstanceCreationTime, core.VRTM},
	{tagStudyDate, core.VRDA},
	{tagSeriesDate, core.VRDA},
	{tagAcquisitionDate, core.VRDA},
	{tagContentDate, core.VRDA},
	{tagOverlayDate, core.VRDA},
	{tagCurveDate, core.VRDA},
	{tagAcquisitionDateTime, core.VRDT},
	{tagStudyTime, core.VRTM},
	{tagSeriesTime, core.VRTM},
	{tagAcquisitionTime, core.VRTM},
	{tagContentTime, core.VRTM},
	{tagOverlayTime, core.VRTM},
	{tagCurveTime, core.VRTM},
}

var identifyingSequenceAttributes = []core.Tag{
	tagInstitutionCodeSequence,
	tagReferringPhysicianIdentificationSequence,
	tagPhysiciansOfRecordIdentificationSequence,
	tagPerformingPhysicianIdentificationSequence,
	tagOperatorIdentificationSequence,
	tagIssuerOfPatientIDQualifiersSequence,
	tagOtherPatientIDsSequence,
	tagRequestingPhysicianIdentificationSequence,
}

// instanceUIDAttributes is the set assigned action U by the Basic Application
// Level Confidentiality Profile. Class, transfer-syntax, coding-scheme, and
// other semantic UIDs are deliberately absent.
var instanceUIDAttributes = []core.Tag{
	core.NewTag(0x0008, 0x0017), // Acquisition UID
	core.NewTag(0x0020, 0x9161), // Concatenation UID
	core.NewTag(0x3010, 0x0006), // Conceptual Volume UID
	core.NewTag(0x3010, 0x0013), // Constituent Conceptual Volume UID
	core.NewTag(0x0018, 0x1002), // Device UID
	core.NewTag(0x0400, 0x0100), // Digital Signature UID
	core.NewTag(0x0020, 0x9164), // Dimension Organization UID
	core.NewTag(0x300A, 0x0013), // Dose Reference UID
	core.NewTag(0x3010, 0x006E), // Dosimetric Objective UID
	core.NewTag(0x0008, 0x0058), // Failed SOP Instance UID List
	core.NewTag(0x0070, 0x031A), // Fiducial UID
	core.NewTag(0x0020, 0x0052), // Frame of Reference UID
	core.NewTag(0x0008, 0x0014), // Instance Creator UID
	core.NewTag(0x0008, 0x3010), // Irradiation Event UID
	core.NewTag(0x0028, 0x1214), // Large Palette Color Lookup Table UID
	core.NewTag(0x0018, 0x100B), // Manufacturer's Device Class UID
	core.NewTag(0x003A, 0x0310), // Multiplex Group UID
	core.NewTag(0x0040, 0xA402), // Observation Subject UID (Trial)
	core.NewTag(0x0040, 0xA171), // Observation UID
	core.NewTag(0x0028, 0x1199), // Palette Color Lookup Table UID
	core.NewTag(0x300A, 0x0650), // Patient Setup UID
	core.NewTag(0x0070, 0x1101), // Presentation Display Collection UID
	core.NewTag(0x0070, 0x1102), // Presentation Sequence Collection UID
	core.NewTag(0x0008, 0x0019), // Pyramid UID
	core.NewTag(0x3010, 0x000B), // Referenced Conceptual Volume UID
	core.NewTag(0x300A, 0x0083), // Referenced Dose Reference UID
	core.NewTag(0x3010, 0x006F), // Referenced Dosimetric Objective UID
	core.NewTag(0x3010, 0x0031), // Referenced Fiducials UID
	core.NewTag(0x3006, 0x0024), // Referenced Frame of Reference UID
	core.NewTag(0x0040, 0x4023), // Referenced General Purpose SPS Transaction UID
	core.NewTag(0x0040, 0xA172), // Referenced Observation UID (Trial)
	tagReferencedSOPUID,
	core.NewTag(0x0004, 0x1511), // Referenced SOP Instance UID in File
	core.NewTag(0x300A, 0x0785), // Referenced Treatment Position Group UID
	core.NewTag(0x3006, 0x00C2), // Related Frame of Reference UID
	core.NewTag(0x0000, 0x1001), // Requested SOP Instance UID
	core.NewTag(0x3010, 0x003B), // RT Treatment Phase UID
	tagSeriesInstanceUID,
	tagSOPInstanceUID,
	core.NewTag(0x3010, 0x0015), // Source Conceptual Volume UID
	core.NewTag(0x0064, 0x0003), // Source Frame of Reference UID
	core.NewTag(0x0040, 0x0554), // Specimen UID
	core.NewTag(0x0088, 0x0140), // Storage Media File-set UID
	tagStudyInstanceUID,
	core.NewTag(0x0020, 0x0200), // Synchronization Frame of Reference UID
	core.NewTag(0x300A, 0x0054), // Table Top Position Alignment UID
	core.NewTag(0x0018, 0x2042), // Target UID
	core.NewTag(0x0040, 0xDB0D), // Template Extension Creator UID
	core.NewTag(0x0040, 0xDB0C), // Template Extension Organization UID
	core.NewTag(0x0062, 0x0021), // Tracking UID
	core.NewTag(0x0008, 0x1195), // Transaction UID
	core.NewTag(0x300A, 0x0609), // Treatment Position Group UID
	core.NewTag(0x300A, 0x0700), // Treatment Session UID
	core.NewTag(0x0040, 0xA124), // UID content item value
}

var (
	randRead           = rand.Read
	fallbackUIDCounter atomic.Uint64
)

var ErrNilObject = errors.New("deid: nil object")

// ErrUnsupportedPixelRedaction reports that the requested explicit pixel mask
// cannot be applied to this in-memory pixel data representation.
var ErrUnsupportedPixelRedaction = errors.New("deid: unsupported pixel redaction")

// Report summarizes non-text de-identification policy outcomes.
type Report struct {
	BurnedInPixel              BurnedInPixelReport
	RecognizableVisualFeatures RecognizableVisualFeaturesReport
}

// BurnedInPixelRisk is the library's metadata-only assessment of possible
// burned-in pixel PHI.
type BurnedInPixelRisk int

const (
	BurnedInPixelRiskUnknown BurnedInPixelRisk = iota
	BurnedInPixelRiskAbsent
	BurnedInPixelRiskPresent
)

func (r BurnedInPixelRisk) String() string {
	switch r {
	case BurnedInPixelRiskAbsent:
		return "absent"
	case BurnedInPixelRiskPresent:
		return "present"
	default:
		return "unknown"
	}
}

// BurnedInPixelReport exposes the burned-in annotation metadata result and any
// explicit regions that a caller-selected policy redacted. MetadataValue is
// empty when the attribute is absent and otherwise is one of YES, NO, or OTHER;
// malformed source values are never echoed into reports.
type BurnedInPixelReport struct {
	Risk            BurnedInPixelRisk
	MetadataValue   string
	RedactedRegions []PixelRegion
}

// RecognizableVisualFeaturesReport exposes the metadata-only assessment of
// (0028,0302). Missing or malformed values are unknown; only NO is absent.
type RecognizableVisualFeaturesReport struct {
	Risk          BurnedInPixelRisk
	MetadataValue string
}

// PixelRegion is an inclusive-origin, width/height pixel rectangle. Frame is
// zero-based; the zero value targets the first frame.
type PixelRegion struct {
	Frame  int
	X      int
	Y      int
	Width  int
	Height int
}

// BurnedInPixelPolicyContext is passed to a caller's explicit redaction policy.
type BurnedInPixelPolicyContext struct {
	Report   BurnedInPixelReport
	Metadata *pixeldata.Metadata
	Rows     int
	Columns  int
}

// BurnedInPixelPolicyResult is the caller's explicit instruction to mask pixel
// regions. Empty Regions means metadata-only reporting.
type BurnedInPixelPolicyResult struct {
	Regions []PixelRegion
	// Fill is the raw bytes for one output pixel. Nil means zero-fill.
	Fill []byte
}

// BurnedInPixelPolicy can opt into masking explicit pixel regions. The default
// nil policy never mutates pixels.
type BurnedInPixelPolicy func(BurnedInPixelPolicyContext) BurnedInPixelPolicyResult

// PrivateTagAction controls whether a private element is retained or removed.
type PrivateTagAction int

const (
	PrivateTagActionRemove PrivateTagAction = iota
	PrivateTagActionRetain
)

// PrivateTagContext is passed to a private-tag policy.
type PrivateTagContext struct {
	Tag        core.Tag
	Element    core.Element
	CreatorTag core.Tag
	Creator    string
}

// PrivateTagPolicy decides how each private tag is handled. A nil policy removes
// all private tags.
type PrivateTagPolicy func(PrivateTagContext) PrivateTagAction

// RetainPrivateTags is a PrivateTagPolicy that preserves every private tag.
func RetainPrivateTags(PrivateTagContext) PrivateTagAction {
	return PrivateTagActionRetain
}

// RemovePrivateTags is a PrivateTagPolicy that removes every private tag.
func RemovePrivateTags(PrivateTagContext) PrivateTagAction {
	return PrivateTagActionRemove
}

// PrivateTagAllow identifies one retained private tag. Creator is optional for
// private creator tags and required to match private data tags safely.
type PrivateTagAllow struct {
	Tag     core.Tag
	Creator string
}

// AllowPrivateTags returns a policy that retains only the listed private tags.
// Private data tags automatically retain their associated private creator tag
// when the creator value matches.
func AllowPrivateTags(allowlist ...PrivateTagAllow) PrivateTagPolicy {
	allowedCreators := map[core.Tag]struct{}{}
	allowedData := map[privateTagAllowKey]struct{}{}
	for _, allowed := range allowlist {
		if isPrivateCreatorTag(allowed.Tag) {
			allowedCreators[allowed.Tag] = struct{}{}
			continue
		}
		creator := privateCreatorTagForDataTag(allowed.Tag)
		if creator == (core.Tag{}) {
			allowedCreators[allowed.Tag] = struct{}{}
			continue
		}
		key := privateTagAllowKey{Tag: allowed.Tag, Creator: strings.TrimSpace(allowed.Creator)}
		allowedData[key] = struct{}{}
		allowedCreators[creator] = struct{}{}
	}
	return func(ctx PrivateTagContext) PrivateTagAction {
		if _, ok := allowedCreators[ctx.Tag]; ok {
			return PrivateTagActionRetain
		}
		if ctx.Creator != "" {
			key := privateTagAllowKey{Tag: ctx.Tag, Creator: strings.TrimSpace(ctx.Creator)}
			if _, ok := allowedData[key]; ok {
				return PrivateTagActionRetain
			}
		}
		return PrivateTagActionRemove
	}
}

type privateTagAllowKey struct {
	Tag     core.Tag
	Creator string
}

func privateCreatorTagForDataTag(tag core.Tag) core.Tag {
	if !tag.IsPrivate() || tag.Element < 0x1000 {
		return core.Tag{}
	}
	return core.NewTag(tag.Group, tag.Element>>8)
}

func isPrivateCreatorTag(tag core.Tag) bool {
	return tag.IsPrivate() && tag.Element >= 0x0010 && tag.Element <= 0x00ff
}

// Options controls replacements and explicit retention policies applied by
// AnonymizeObject. KeepDates retains the longitudinal date/time attributes that
// are blanked by default.
type Options struct {
	PatientName string
	PatientID   string
	KeepDates   bool

	BurnedInPixelPolicy BurnedInPixelPolicy
	PrivateTagPolicy    PrivateTagPolicy
}

// UIDRemapper assigns one stable replacement UID per original UID.
type UIDRemapper struct {
	mu      sync.Mutex
	m       map[string]string
	reverse map[string]string
	missing uint64
}

// NewUIDRemapper builds an empty UID remapper.
func NewUIDRemapper() *UIDRemapper {
	return &UIDRemapper{m: map[string]string{}, reverse: map[string]string{}}
}

// Map returns the stable replacement UID for old, minting one on first use.
func (r *UIDRemapper) Map(old string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mapLocked(old)
}

func (r *UIDRemapper) mapLocked(old string) string {
	uid, _ := r.mapLockedWithCollisions(old)
	return uid
}

func (r *UIDRemapper) mapWithCollisionCount(old string) (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mapLockedWithCollisions(old)
}

func (r *UIDRemapper) mapLockedWithCollisions(old string) (string, int) {
	if r.m == nil {
		r.m = map[string]string{}
	}
	if uid, ok := r.m[old]; ok {
		return uid, 0
	}
	if r.reverse == nil {
		r.reverse = make(map[string]string, len(r.m))
		for source, uid := range r.m {
			r.reverse[uid] = source
		}
	}
	collisions := 0
	for {
		uid := mintUID()
		if existing, used := r.reverse[uid]; !used || existing == old {
			r.m[old] = uid
			r.reverse[uid] = old
			return uid, collisions
		}
		collisions++
		// A broken entropy source can repeat forever. After the first collision,
		// switch to the process-unique fallback rather than retrying random input.
		uid = fallbackUID()
		if _, used := r.reverse[uid]; !used {
			r.m[old] = uid
			r.reverse[uid] = old
			return uid, collisions
		}
	}
}

func (r *UIDRemapper) mapMissing(scope string) string {
	uid, _ := r.mapMissingWithCollisionCount(scope)
	return uid
}

func (r *UIDRemapper) mapMissingWithCollisionCount(scope string) (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missing++
	return r.mapLockedWithCollisions(fmt.Sprintf("%s:%d", scope, r.missing))
}

// AnonymizeObject de-identifies obj in place by replacing patient name/ID,
// blanking common PHI tags, and remapping hierarchy UIDs through uids.
func AnonymizeObject(obj *object.Object, opts Options, uids *UIDRemapper) error {
	_, err := AnonymizeObjectWithReport(obj, opts, uids)
	return err
}

// CloneAnonymizedFile returns an anonymized copy of src without mutating src.
func CloneAnonymizedFile(src *object.File, opts Options, uids *UIDRemapper) (*object.File, Report, error) {
	clone, err := object.CloneFile(src)
	if err != nil {
		return nil, Report{}, err
	}
	report, err := AnonymizeObjectWithReport(clone.Dataset, opts, uids)
	if err != nil {
		return nil, report, err
	}
	clone, err = rederiveFileMeta(clone)
	if err != nil {
		return nil, report, err
	}
	return clone, report, nil
}

// AnonymizeObjectWithReport de-identifies obj in place and returns policy
// outcomes that callers may surface to users. Burned-in annotation handling is
// metadata-only unless opts.BurnedInPixelPolicy returns explicit mask regions.
func AnonymizeObjectWithReport(obj *object.Object, opts Options, uids *UIDRemapper) (Report, error) {
	if obj == nil {
		return Report{}, ErrNilObject
	}
	report := Report{
		BurnedInPixel:              assessBurnedInPixel(obj),
		RecognizableVisualFeatures: assessRecognizableVisualFeatures(obj),
	}
	if opts.BurnedInPixelPolicy != nil {
		metadata, rows, columns := pixelPolicyMetadata(obj)
		result := opts.BurnedInPixelPolicy(BurnedInPixelPolicyContext{
			Report:   report.BurnedInPixel,
			Metadata: metadata,
			Rows:     rows,
			Columns:  columns,
		})
		if len(result.Regions) > 0 {
			regions, err := MaskPixelRegions(obj, result.Regions, result.Fill)
			if err != nil {
				return report, err
			}
			report.BurnedInPixel.RedactedRegions = regions
		}
	}
	if uids == nil {
		uids = NewUIDRemapper()
	}
	anonymizeObject(obj, opts, uids, true)
	return report, nil
}

// RemapHierarchyUIDs remaps Study, Series, and SOP Instance UIDs in place.
func RemapHierarchyUIDs(obj *object.Object, uids *UIDRemapper) error {
	if obj == nil {
		return ErrNilObject
	}
	if uids == nil {
		uids = NewUIDRemapper()
	}
	remapUID(obj, uids, tagStudyInstanceUID, "\x00study", true)
	remapUID(obj, uids, tagSeriesInstanceUID, "\x00series", true)
	remapUID(obj, uids, tagSOPInstanceUID, "\x00sop", true)
	return nil
}

// CloneWithRemappedHierarchyUIDs returns a copy of src with Study, Series, and
// SOP Instance UIDs remapped without mutating src.
func CloneWithRemappedHierarchyUIDs(src *object.File, uids *UIDRemapper) (*object.File, error) {
	clone, err := object.CloneFile(src)
	if err != nil {
		return nil, err
	}
	if err := RemapHierarchyUIDs(clone.Dataset, uids); err != nil {
		return nil, err
	}
	return rederiveFileMeta(clone)
}

// rederiveFileMeta discards source-specific optional meta elements and rebuilds
// the canonical Part 10 metadata around the already-cloned dataset.
func rederiveFileMeta(file *object.File) (*object.File, error) {
	file.Preamble = nil
	file.Meta = nil
	if err := file.RebuildFileMeta(); err != nil {
		return nil, err
	}
	return file, nil
}

func anonymizeObject(obj *object.Object, opts Options, uids *UIDRemapper, root bool) {
	rewriteSequencesAndPrivateTags(obj, opts, uids)
	applyDirectIdentifiers(obj, opts, uids, root)
}

func rewriteSequencesAndPrivateTags(obj *object.Object, opts Options, uids *UIDRemapper) {
	creators := privateCreators(obj.Elements())
	for _, elem := range obj.Elements() {
		tag := elem.Tag()
		if isOverlayTag(tag) {
			obj.Remove(tag)
			continue
		}
		if tag.IsPrivate() {
			if privateTagAction(opts, elem, creators) != PrivateTagActionRetain {
				obj.Remove(tag)
				continue
			}
			if seq, ok := elem.Value.(core.SequenceValue); ok {
				elem.Value = anonymizedSequence(seq, opts, uids)
				obj.Put(elem)
			}
			continue
		}
		seq, ok := elem.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		elem.Value = anonymizedSequence(seq, opts, uids)
		obj.Put(elem)
	}
}

func isOverlayTag(tag core.Tag) bool {
	return tag.Group >= 0x6000 && tag.Group <= 0x60ff && tag.Group%2 == 0
}

func anonymizedSequence(seq core.SequenceValue, opts Options, uids *UIDRemapper) core.SequenceValue {
	seq = cloneSequenceValue(seq)
	for i := range seq.Items {
		item := object.FromDataSet(seq.Items[i], nil)
		anonymizeObject(item, opts, uids, false)
		seq.Items[i] = item.ToDataSet()
	}
	return seq
}

func privateTagAction(opts Options, elem core.Element, creators map[core.Tag]string) PrivateTagAction {
	if opts.PrivateTagPolicy == nil {
		return PrivateTagActionRemove
	}
	creatorTag := privateCreatorTagForDataTag(elem.Tag())
	return opts.PrivateTagPolicy(PrivateTagContext{
		Tag:        elem.Tag(),
		Element:    elem,
		CreatorTag: creatorTag,
		Creator:    creators[creatorTag],
	})
}

func privateCreators(elements []core.Element) map[core.Tag]string {
	creators := map[core.Tag]string{}
	for _, elem := range elements {
		tag := elem.Tag()
		if !isPrivateCreatorTag(tag) {
			continue
		}
		value := strings.TrimSpace(elem.StringValue())
		if value != "" {
			creators[tag] = value
		}
	}
	return creators
}

func applyDirectIdentifiers(obj *object.Object, opts Options, uids *UIDRemapper, root bool) {
	name := opts.PatientName
	if name == "" {
		name = "Anonymous"
	}
	id := opts.PatientID
	if id == "" {
		id = "ANON"
	}

	if root || obj.Has(tagPatientName) {
		obj.Put(core.NewRawElement(tagPatientName, core.VRPN, padText(name)))
	}
	if root || obj.Has(tagPatientID) {
		obj.Put(core.NewRawElement(tagPatientID, core.VRLO, padText(id)))
	}

	blank := func(tag core.Tag, vr core.VR) {
		if !root && !obj.Has(tag) {
			return
		}
		obj.Put(core.NewRawElement(tag, vr, nil))
	}
	blank(tagPatientBirthDate, core.VRDA)
	blank(tagOtherPatientIDs, core.VRLO)
	blank(tagPatientAddress, core.VRLO)
	blank(tagInstitutionName, core.VRLO)
	blank(tagReferringPhysician, core.VRPN)
	blank(tagPhysiciansOfRecord, core.VRPN)
	blank(tagPerformingPhysician, core.VRPN)
	blank(tagOperatorsName, core.VRPN)
	blank(tagAccessionNumber, core.VRSH)
	blankIfPresent := func(attribute attributeToBlank) {
		if obj.Has(attribute.tag) {
			obj.Put(core.NewRawElement(attribute.tag, attribute.vr, nil))
		}
	}
	for _, attribute := range basicProfileTextAttributes {
		blankIfPresent(attribute)
	}
	for _, tag := range identifyingSequenceAttributes {
		obj.Remove(tag)
	}
	if !opts.KeepDates {
		for _, attribute := range longitudinalDateTimeAttributes {
			blankIfPresent(attribute)
		}
	}

	for _, tag := range instanceUIDAttributes {
		force := root && isHierarchyUID(tag)
		remapUID(obj, uids, tag, "\x00uid:"+tag.String(), force)
	}
}

func isHierarchyUID(tag core.Tag) bool {
	return tag == tagStudyInstanceUID || tag == tagSeriesInstanceUID || tag == tagSOPInstanceUID
}

func remapUID(obj *object.Object, uids *UIDRemapper, tag core.Tag, emptyKey string, force bool) {
	if !force && !obj.Has(tag) {
		return
	}
	oldValues, ok := obj.GetStrings(tag)
	if !ok || len(oldValues) == 0 {
		obj.Put(core.NewRawElement(tag, core.VRUI, padUID(uids.mapMissing(emptyKey))))
		return
	}
	mapped := make([]string, len(oldValues))
	for i, old := range oldValues {
		old = core.NormalizeUID(old)
		if old == "" {
			mapped[i] = uids.mapMissing(fmt.Sprintf("%s:%d", emptyKey, i))
			continue
		}
		mapped[i] = uids.Map(old)
	}
	obj.Put(core.NewRawElement(tag, core.VRUI, padUID(strings.Join(mapped, "\\"))))
}

func cloneSequenceValue(seq core.SequenceValue) core.SequenceValue {
	out := core.SequenceValue{Items: make([]core.DataSet, len(seq.Items))}
	for i := range seq.Items {
		out.Items[i] = cloneDataSet(seq.Items[i])
	}
	return out
}

func cloneDataSet(ds core.DataSet) core.DataSet {
	out := core.DataSet{Elements: make([]core.Element, len(ds.Elements))}
	for i := range ds.Elements {
		out.Elements[i] = cloneElement(ds.Elements[i])
	}
	return out
}

func cloneElement(elem core.Element) core.Element {
	switch value := elem.Value.(type) {
	case core.RawValue:
		elem.Value = core.RawValue(core.CloneBytes(value.Bytes()))
	case core.StringValue:
		elem.Value = append(core.StringValue(nil), value...)
	case core.SequenceValue:
		elem.Value = cloneSequenceValue(value)
	case core.FragmentSequence:
		clone := core.FragmentSequence{
			OffsetTable: core.CloneBytes(value.OffsetTable),
			Fragments:   make([][]byte, len(value.Fragments)),
		}
		for i := range value.Fragments {
			clone.Fragments[i] = core.CloneBytes(value.Fragments[i])
		}
		elem.Value = clone
	}
	return elem
}

func assessBurnedInPixel(obj *object.Object) BurnedInPixelReport {
	value, ok := singleCSValue(obj, tagBurnedInAnnotation)
	if !ok {
		if obj.Has(tagBurnedInAnnotation) {
			return BurnedInPixelReport{Risk: BurnedInPixelRiskUnknown, MetadataValue: "OTHER"}
		}
		return BurnedInPixelReport{Risk: BurnedInPixelRiskUnknown}
	}
	switch value {
	case "YES":
		return BurnedInPixelReport{Risk: BurnedInPixelRiskPresent, MetadataValue: value}
	case "NO":
		return BurnedInPixelReport{Risk: BurnedInPixelRiskAbsent, MetadataValue: value}
	default:
		return BurnedInPixelReport{Risk: BurnedInPixelRiskUnknown, MetadataValue: "OTHER"}
	}
}

func assessRecognizableVisualFeatures(obj *object.Object) RecognizableVisualFeaturesReport {
	value, ok := singleCSValue(obj, tagRecognizableVisualFeatures)
	if !ok {
		if obj.Has(tagRecognizableVisualFeatures) {
			return RecognizableVisualFeaturesReport{Risk: BurnedInPixelRiskUnknown, MetadataValue: "OTHER"}
		}
		return RecognizableVisualFeaturesReport{Risk: BurnedInPixelRiskUnknown}
	}
	switch value {
	case "YES":
		return RecognizableVisualFeaturesReport{Risk: BurnedInPixelRiskPresent, MetadataValue: value}
	case "NO":
		return RecognizableVisualFeaturesReport{Risk: BurnedInPixelRiskAbsent, MetadataValue: value}
	default:
		return RecognizableVisualFeaturesReport{Risk: BurnedInPixelRiskUnknown, MetadataValue: "OTHER"}
	}
}

func singleCSValue(obj *object.Object, tag core.Tag) (string, bool) {
	element, ok := obj.Get(tag)
	if !ok || element.VR() != core.VRCS {
		return "", false
	}
	values := element.StringValues()
	if len(values) != 1 {
		return "", false
	}
	value := strings.ToUpper(strings.TrimSpace(values[0]))
	return value, value != ""
}

func pixelPolicyMetadata(obj *object.Object) (*pixeldata.Metadata, int, int) {
	metadata, err := pixeldata.ExtractMetadata(obj)
	if err == nil {
		return &metadata, int(metadata.Rows), int(metadata.Columns)
	}
	rows, _ := pixeldataDimension(obj, tagRows)
	columns, _ := pixeldataDimension(obj, tagColumns)
	return nil, rows, columns
}

// MaskPixelRegions masks explicit native pixel regions and writes the modified
// Pixel Data back to obj. It rejects unsupported pixel representations rather
// than clipping, guessing, or silently skipping regions.
func MaskPixelRegions(obj *object.Object, regions []PixelRegion, fill []byte) ([]PixelRegion, error) {
	native, err := pixeldata.ExtractNativeFrames(obj)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedPixelRedaction, err)
	}
	elem, ok := obj.Get(tagPixelData)
	if !ok {
		return nil, fmt.Errorf("%w: missing pixel data", ErrUnsupportedPixelRedaction)
	}
	metadata := native.Metadata
	if metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration > 1 {
		return nil, fmt.Errorf("%w: PlanarConfiguration=%d", ErrUnsupportedPixelRedaction, metadata.PlanarConfiguration)
	}
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	switch photometric {
	case "YBR_FULL_422", "YBR_PARTIAL_422", "YBR_PARTIAL_420":
		return nil, fmt.Errorf("%w: subsampled PhotometricInterpretation=%s is not supported", ErrUnsupportedPixelRedaction, photometric)
	}
	if metadata.BitsAllocated < 8 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d packed pixels are not supported", ErrUnsupportedPixelRedaction, metadata.BitsAllocated)
	}
	rows := int(metadata.Rows)
	columns := int(metadata.Columns)
	samplesPerPixel := int(metadata.SamplesPerPixel)
	bytesPerSample := metadata.BytesPerSample()
	bytesPerPixel := samplesPerPixel * bytesPerSample
	if bytesPerPixel <= 0 {
		return nil, fmt.Errorf("%w: invalid pixel format", ErrUnsupportedPixelRedaction)
	}
	fillPixel := core.CloneBytes(fill)
	if len(fillPixel) == 0 {
		fillPixel = make([]byte, bytesPerPixel)
	}
	if len(fillPixel) != bytesPerPixel {
		return nil, fmt.Errorf("%w: fill has %d byte(s), want %d", ErrUnsupportedPixelRedaction, len(fillPixel), bytesPerPixel)
	}
	rowStride := columns * bytesPerPixel
	planeStride := rows * columns * bytesPerSample
	planar := metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration == 1

	applied := make([]PixelRegion, 0, len(regions))
	for _, region := range regions {
		if region.Width <= 0 || region.Height <= 0 {
			return nil, fmt.Errorf("%w: invalid region %#v", ErrUnsupportedPixelRedaction, region)
		}
		if region.Frame < 0 || region.Frame >= len(native.Data) {
			return nil, fmt.Errorf("%w: frame %d outside %d frame(s)", ErrUnsupportedPixelRedaction, region.Frame, len(native.Data))
		}
		if region.X < 0 || region.Y < 0 ||
			region.X >= columns || region.Y >= rows ||
			region.Width > columns-region.X || region.Height > rows-region.Y {
			return nil, fmt.Errorf("%w: region %#v outside %dx%d pixels", ErrUnsupportedPixelRedaction, region, columns, rows)
		}
		frame := native.Data[region.Frame]
		for y := region.Y; y < region.Y+region.Height; y++ {
			rowOffset := y * rowStride
			for x := region.X; x < region.X+region.Width; x++ {
				if planar {
					pixelIndex := y*columns + x
					for sample := 0; sample < samplesPerPixel; sample++ {
						offset := sample*planeStride + pixelIndex*bytesPerSample
						fillOffset := sample * bytesPerSample
						copy(frame[offset:offset+bytesPerSample], fillPixel[fillOffset:fillOffset+bytesPerSample])
					}
					continue
				}
				offset := rowOffset + x*bytesPerPixel
				copy(frame[offset:offset+bytesPerPixel], fillPixel)
			}
		}
		applied = append(applied, region)
	}

	masked := make([]byte, 0, int(metadata.TotalSize()))
	for _, frame := range native.Data {
		masked = append(masked, frame...)
	}
	obj.Put(core.NewRawElement(tagPixelData, elem.VR(), masked))
	return applied, nil
}

func pixeldataDimension(obj *object.Object, tag core.Tag) (int, bool) {
	raw, ok := obj.GetRaw(tag)
	if !ok || len(raw) < 2 {
		return 0, false
	}
	return int(obj.ValueByteOrder().Uint16(raw[:2])), true
}

func mintUID() string {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		return fallbackUID()
	}
	return "2.25." + new(big.Int).SetBytes(b[:]).String()
}

func fallbackUID() string {
	return fmt.Sprintf("2.25.%d%d", time.Now().UnixNano(), fallbackUIDCounter.Add(1))
}

func padText(s string) []byte {
	b := []byte(s)
	if len(b)%2 != 0 {
		b = append(b, ' ')
	}
	return b
}

func padUID(s string) []byte {
	b := []byte(s)
	if len(b)%2 != 0 {
		b = append(b, 0)
	}
	return b
}
