// Package clinicalrelation extracts normalized relationships from clinical
// DICOM objects without depending on an application catalog or UI model.
package clinicalrelation

import (
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/gsps"
	"github.com/ThalesMMS/dicom-go/rtstruct"
	"github.com/ThalesMMS/dicom-go/seg"
	"github.com/ThalesMMS/dicom-go/sr"
	"github.com/ThalesMMS/dicom-go/vps"
)

type ObjectKind string

const (
	ObjectKindImage         ObjectKind = "image"
	ObjectKindSEG           ObjectKind = "seg"
	ObjectKindSRMeasurement ObjectKind = "sr_measurement"
	ObjectKindKOS           ObjectKind = "kos"
	ObjectKindGSPS          ObjectKind = "gsps"
	ObjectKindRTSTRUCT      ObjectKind = "rtstruct"
	ObjectKindRTDose        ObjectKind = "rtdose"
	ObjectKindParametricMap ObjectKind = "parametric_map"
	ObjectKindVPS           ObjectKind = "vps"
	ObjectKindRegistration  ObjectKind = "registration"
	ObjectKindOther         ObjectKind = "other"
)

type ReferenceRole string

const (
	ReferenceRoleSourceImage        ReferenceRole = "source_image"
	ReferenceRolePresentationImage  ReferenceRole = "presentation_image"
	ReferenceRoleKeyObject          ReferenceRole = "key_object"
	ReferenceRoleContourImage       ReferenceRole = "contour_image"
	ReferenceRoleRTObject           ReferenceRole = "rt_object"
	ReferenceRoleSegment            ReferenceRole = "segment"
	ReferenceRoleMeasurementImage   ReferenceRole = "measurement_image"
	ReferenceRolePresentationInput  ReferenceRole = "presentation_input"
	ReferenceRoleRegistrationSource ReferenceRole = "registration_source"
)

// Identity supplies catalog-known identity fields. Resolve uses these values
// before falling back to the corresponding data-set attributes.
type Identity struct {
	SOPClassUID       string
	SOPInstanceUID    string
	StudyInstanceUID  string
	SeriesInstanceUID string
}

// Reference is one validated relationship extracted from a clinical object.
type Reference struct {
	Role              ReferenceRole
	StudyInstanceUID  string
	SeriesInstanceUID string
	SOPClassUID       string
	SOPInstanceUID    string
	Frames            []int
}

type DiagnosticCode string

const (
	DiagnosticMissingStudyInstanceUID  DiagnosticCode = "missing_study_instance_uid"
	DiagnosticInvalidStudyInstanceUID  DiagnosticCode = "invalid_study_instance_uid"
	DiagnosticMissingSeriesInstanceUID DiagnosticCode = "missing_series_instance_uid"
	DiagnosticInvalidSeriesInstanceUID DiagnosticCode = "invalid_series_instance_uid"
	DiagnosticMissingSOPClassUID       DiagnosticCode = "missing_sop_class_uid"
	DiagnosticInvalidSOPClassUID       DiagnosticCode = "invalid_sop_class_uid"
	DiagnosticMissingSOPInstanceUID    DiagnosticCode = "missing_sop_instance_uid"
	DiagnosticInvalidSOPInstanceUID    DiagnosticCode = "invalid_sop_instance_uid"
	DiagnosticMalformedObject          DiagnosticCode = "malformed_object"
	DiagnosticNilDataset               DiagnosticCode = "nil_dataset"
)

// Diagnostic identifies an unusable reference without copying the invalid UID
// value into logs. ReferenceIndex is zero-based within the extracted candidates.
type Diagnostic struct {
	Code           DiagnosticCode
	Role           ReferenceRole
	ReferenceIndex int
}

// Resolution describes the object and every validated outgoing relationship.
// Diagnostics contains rejected candidates; rejected candidates are never
// included in References.
type Resolution struct {
	Kind                       ObjectKind
	SOPClassUID                string
	SOPInstanceUID             string
	StudyInstanceUID           string
	SeriesInstanceUID          string
	FrameOfReferenceUID        string
	SourceFrameOfReferenceUIDs []string
	RegistrationType           string
	Editable                   bool
	References                 []Reference
	Diagnostics                []Diagnostic
}

// ReferencedSOPInstanceUIDs returns stable, first-occurrence SOP UID order.
func (resolution Resolution) ReferencedSOPInstanceUIDs() []string {
	var out []string
	for _, reference := range resolution.References {
		out = appendUnique(out, reference.SOPInstanceUID)
	}
	return out
}

// ReferencedSeriesInstanceUIDs returns stable, first-occurrence series UID
// order from validated references.
func (resolution Resolution) ReferencedSeriesInstanceUIDs() []string {
	var out []string
	for _, reference := range resolution.References {
		out = appendUnique(out, reference.SeriesInstanceUID)
	}
	return out
}

var ErrResolution = errors.New("dicom/clinicalrelation: resolution failed")

// ResolutionError reports a typed failure to decode a supported clinical
// object. It preserves the underlying package error for errors.Is/errors.As.
type ResolutionError struct {
	Code DiagnosticCode
	Kind ObjectKind
	Err  error
}

func (err *ResolutionError) Error() string {
	if err == nil {
		return ErrResolution.Error()
	}
	if err.Err == nil {
		return fmt.Sprintf("%s: %s (%s)", ErrResolution, err.Code, err.Kind)
	}
	return fmt.Sprintf("%s: %s (%s): %v", ErrResolution, err.Code, err.Kind, err.Err)
}

func (err *ResolutionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

func (err *ResolutionError) Is(target error) bool {
	return target == ErrResolution
}

// IsEditableSOPClass reports whether the library currently supports rewriting
// the clinical object represented by sopClassUID.
func IsEditableSOPClass(sopClassUID string) bool {
	switch sopClassUID {
	case seg.SegmentationStorage,
		seg.LabelMapSegmentationStorage,
		gsps.GrayscaleSoftcopyPresentationStateStorage,
		sr.KeyObjectSelectionDocumentStorage,
		rtstruct.RTStructureSetStorage,
		sr.EnhancedSRStorage,
		sr.ComprehensiveSRStorage,
		sr.Comprehensive3DSRStorage,
		vps.GrayscalePlanarMPRVolumetricPresentationStateStorage,
		vps.CompositingPlanarMPRVolumetricPresentationStateStorage,
		vps.VolumeRenderingVolumetricPresentationStateStorage,
		vps.SegmentedVolumeRenderingVolumetricPresentationStateStorage,
		vps.MultipleVolumeRenderingVolumetricPresentationStateStorage:
		return true
	default:
		return false
	}
}
