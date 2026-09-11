package clinicalrelation

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/gsps"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parametricmap"
	"github.com/ThalesMMS/dicom-go/rtdose"
	"github.com/ThalesMMS/dicom-go/rtstruct"
	"github.com/ThalesMMS/dicom-go/seg"
	"github.com/ThalesMMS/dicom-go/spatialreg"
	"github.com/ThalesMMS/dicom-go/sr"
	"github.com/ThalesMMS/dicom-go/vps"
)

// Resolve classifies dataset and extracts normalized, validated references.
// Parsing failures for supported objects are returned as *ResolutionError.
func Resolve(dataset *object.Object, identity Identity) (Resolution, error) {
	if dataset == nil {
		return Resolution{Kind: ObjectKindOther}, &ResolutionError{
			Code: DiagnosticNilDataset,
			Kind: ObjectKindOther,
			Err:  fmt.Errorf("dataset is nil"),
		}
	}

	resolver := relationResolver{resolution: Resolution{
		Kind:                ObjectKindImage,
		SOPClassUID:         firstNonEmpty(identity.SOPClassUID, datasetUID(dataset, tags.SOPClassUID)),
		SOPInstanceUID:      firstNonEmpty(identity.SOPInstanceUID, datasetUID(dataset, tags.SOPInstanceUID)),
		StudyInstanceUID:    firstNonEmpty(identity.StudyInstanceUID, datasetUID(dataset, tags.StudyInstanceUID)),
		SeriesInstanceUID:   firstNonEmpty(identity.SeriesInstanceUID, datasetUID(dataset, tags.SeriesInstanceUID)),
		FrameOfReferenceUID: datasetUID(dataset, tags.FrameOfReferenceUID),
	}}

	switch resolver.resolution.SOPClassUID {
	case seg.SegmentationStorage, seg.LabelMapSegmentationStorage:
		resolver.resolution.Kind = ObjectKindSEG
		resolver.resolution.Editable = true
		document, err := seg.Read(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRoleSourceImage)
			return resolver.resolution, malformed(ObjectKindSEG, err)
		}
		resolver.preferStudy(document.StudyInstanceUID)
		resolver.preferFrame(document.FrameOfReferenceUID)
		for _, reference := range document.ReferencedImages {
			resolver.addReference(Reference{
				Role:              ReferenceRoleSourceImage,
				SeriesInstanceUID: reference.SeriesInstanceUID,
				SOPClassUID:       reference.SOPClassUID,
				SOPInstanceUID:    reference.SOPInstanceUID,
				Frames:            reference.Frames,
			}, referenceRequirements{series: true})
		}
	case gsps.GrayscaleSoftcopyPresentationStateStorage:
		resolver.resolution.Kind = ObjectKindGSPS
		resolver.resolution.Editable = true
		state, err := gsps.Read(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRolePresentationImage)
			return resolver.resolution, malformed(ObjectKindGSPS, err)
		}
		resolver.preferStudy(state.StudyInstanceUID)
		for _, reference := range state.ReferencedImages {
			resolver.addReference(Reference{
				Role:              ReferenceRolePresentationImage,
				SeriesInstanceUID: reference.SeriesInstanceUID,
				SOPClassUID:       reference.SOPClassUID,
				SOPInstanceUID:    reference.SOPInstanceUID,
				Frames:            reference.Frames,
			}, referenceRequirements{series: true})
		}
	case sr.KeyObjectSelectionDocumentStorage:
		resolver.resolution.Kind = ObjectKindKOS
		resolver.resolution.Editable = true
		document, err := sr.ReadDocument(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRoleKeyObject)
			return resolver.resolution, malformed(ObjectKindKOS, err)
		}
		resolver.preferStudy(document.StudyInstanceUID)
		for _, reference := range sr.KeyObjectSelectionImages(document) {
			resolver.addReference(Reference{
				Role:              ReferenceRoleKeyObject,
				StudyInstanceUID:  reference.StudyInstanceUID,
				SeriesInstanceUID: reference.SeriesInstanceUID,
				SOPClassUID:       reference.SOPClassUID,
				SOPInstanceUID:    reference.SOPInstanceUID,
				Frames:            reference.Frames,
			}, referenceRequirements{study: true, series: true})
		}
	case rtstruct.RTStructureSetStorage:
		resolver.resolution.Kind = ObjectKindRTSTRUCT
		resolver.resolution.Editable = true
		structureSet, err := rtstruct.Read(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRoleContourImage)
			return resolver.resolution, malformed(ObjectKindRTSTRUCT, err)
		}
		resolver.preferStudy(structureSet.StudyInstanceUID)
		resolver.preferFrame(structureSet.FrameOfReferenceUID)
		for _, roi := range structureSet.ROIs {
			for _, contour := range roi.Contours {
				for _, reference := range contour.ReferencedImages {
					resolver.addReference(Reference{
						Role:              ReferenceRoleContourImage,
						StudyInstanceUID:  reference.StudyInstanceUID,
						SeriesInstanceUID: reference.SeriesInstanceUID,
						SOPClassUID:       reference.SOPClassUID,
						SOPInstanceUID:    reference.SOPInstanceUID,
					}, referenceRequirements{})
				}
			}
		}
	case rtdose.RTDoseStorage:
		resolver.resolution.Kind = ObjectKindRTDose
		dose, err := rtdose.ReadDataset(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRoleRTObject)
			return resolver.resolution, malformed(ObjectKindRTDose, err)
		}
		resolver.preferStudy(dose.StudyInstanceUID)
		resolver.preferFrame(dose.FrameOfReferenceUID)
		for _, reference := range dose.References {
			resolver.addReference(Reference{
				Role:           ReferenceRoleRTObject,
				SOPClassUID:    reference.SOPClassUID,
				SOPInstanceUID: reference.SOPInstanceUID,
			}, referenceRequirements{})
		}
		resolver.addMissingDirectReferences(dataset, ReferenceRoleRTObject,
			core.NewTag(0x300C, 0x0002),
			core.NewTag(0x300C, 0x0060),
		)
	case parametricmap.ParametricMapStorage:
		resolver.resolution.Kind = ObjectKindParametricMap
		parametricMap, err := parametricmap.ReadDataset(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRoleSourceImage)
			return resolver.resolution, malformed(ObjectKindParametricMap, err)
		}
		resolver.preferStudy(parametricMap.StudyInstanceUID)
		resolver.preferFrame(parametricMap.FrameOfReferenceUID)
		for _, reference := range parametricMap.References {
			resolver.addReference(Reference{
				Role:           ReferenceRoleSourceImage,
				SOPClassUID:    reference.SOPClassUID,
				SOPInstanceUID: reference.SOPInstanceUID,
			}, referenceRequirements{})
		}
		resolver.addMissingNestedReferences(dataset, ReferenceRoleSourceImage)
	case sr.EnhancedSRStorage, sr.ComprehensiveSRStorage, sr.Comprehensive3DSRStorage:
		resolver.resolution.Kind = ObjectKindSRMeasurement
		resolver.resolution.Editable = true
		report, err := sr.ReadMeasurementReport(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRoleMeasurementImage)
			return resolver.resolution, malformed(ObjectKindSRMeasurement, err)
		}
		for _, group := range report.Groups {
			resolver.addReference(Reference{
				Role:           ReferenceRoleSegment,
				SOPClassUID:    group.ReferencedSegment.SOPClassUID,
				SOPInstanceUID: group.ReferencedSegment.SOPInstanceUID,
			}, referenceRequirements{})
			for _, measurement := range group.Measurements {
				resolver.addReference(Reference{
					Role:              ReferenceRoleMeasurementImage,
					StudyInstanceUID:  measurement.Image.StudyInstanceUID,
					SeriesInstanceUID: measurement.Image.SeriesInstanceUID,
					SOPClassUID:       measurement.Image.SOPClassUID,
					SOPInstanceUID:    measurement.Image.SOPInstanceUID,
					Frames:            measurement.Image.Frames,
				}, referenceRequirements{})
			}
		}
	case vps.GrayscalePlanarMPRVolumetricPresentationStateStorage,
		vps.CompositingPlanarMPRVolumetricPresentationStateStorage,
		vps.VolumeRenderingVolumetricPresentationStateStorage,
		vps.SegmentedVolumeRenderingVolumetricPresentationStateStorage,
		vps.MultipleVolumeRenderingVolumetricPresentationStateStorage:
		resolver.resolution.Kind = ObjectKindVPS
		resolver.resolution.Editable = true
		state, err := vps.Read(dataset)
		if err != nil {
			resolver.addMalformedNestedReferences(dataset, ReferenceRolePresentationInput)
			return resolver.resolution, malformed(ObjectKindVPS, err)
		}
		resolver.preferStudy(state.StudyInstanceUID)
		for _, input := range state.Inputs {
			for _, reference := range input.ReferencedInstances {
				resolver.addReference(Reference{
					Role:           ReferenceRolePresentationInput,
					SOPClassUID:    reference.SOPClassUID,
					SOPInstanceUID: reference.SOPInstanceUID,
				}, referenceRequirements{})
			}
		}
	case spatialreg.SpatialRegistrationStorage:
		registration, err := spatialreg.ReadAffineDataset(dataset)
		if err != nil {
			return resolver.resolution, malformed(ObjectKindRegistration, err)
		}
		resolver.resolution.Kind = ObjectKindRegistration
		resolver.preferFrame(registration.RegisteredFrameOfReferenceUID)
		for _, item := range registration.Registrations {
			resolver.resolution.SourceFrameOfReferenceUIDs = appendUnique(resolver.resolution.SourceFrameOfReferenceUIDs, item.SourceFrameOfReferenceUID)
			resolver.resolution.RegistrationType = moreGeneralRegistrationType(resolver.resolution.RegistrationType, string(item.Type))
			for _, sopInstanceUID := range item.ReferencedSOPInstanceUIDs {
				resolver.addReference(Reference{Role: ReferenceRoleRegistrationSource, SOPInstanceUID: sopInstanceUID}, referenceRequirements{})
			}
		}
	case spatialreg.DeformableSpatialRegistrationStorage:
		registration, err := spatialreg.ReadDataset(dataset)
		if err != nil {
			return resolver.resolution, malformed(ObjectKindRegistration, err)
		}
		resolver.resolution.Kind = ObjectKindRegistration
		resolver.resolution.RegistrationType = "DEFORMABLE"
		resolver.preferFrame(registration.RegisteredFrameOfReferenceUID)
		for _, item := range registration.Registrations {
			resolver.resolution.SourceFrameOfReferenceUIDs = appendUnique(resolver.resolution.SourceFrameOfReferenceUIDs, item.SourceFrameOfReferenceUID)
			for _, sopInstanceUID := range item.ReferencedSOPInstanceUIDs {
				resolver.addReference(Reference{Role: ReferenceRoleRegistrationSource, SOPInstanceUID: sopInstanceUID}, referenceRequirements{})
			}
		}
	default:
		if resolver.resolution.SOPClassUID == "" {
			resolver.resolution.Kind = ObjectKindOther
		}
	}

	return resolver.resolution, nil
}

func moreGeneralRegistrationType(current, candidate string) string {
	rank := func(value string) int {
		switch value {
		case "RIGID":
			return 1
		case "RIGID_SCALE":
			return 2
		case "AFFINE":
			return 3
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return candidate
	}
	return current
}

type relationResolver struct {
	resolution     Resolution
	referenceIndex int
}

type referenceRequirements struct {
	study  bool
	series bool
}

func (resolver *relationResolver) addReference(reference Reference, requirements referenceRequirements) {
	reference.Role = ReferenceRole(strings.TrimSpace(string(reference.Role)))
	reference.StudyInstanceUID = cleanUID(reference.StudyInstanceUID)
	reference.SeriesInstanceUID = cleanUID(reference.SeriesInstanceUID)
	reference.SOPClassUID = cleanUID(reference.SOPClassUID)
	reference.SOPInstanceUID = cleanUID(reference.SOPInstanceUID)
	reference.Frames = append([]int(nil), reference.Frames...)
	index := resolver.referenceIndex
	resolver.referenceIndex++

	valid := true
	valid = resolver.validateUID(reference.Role, index, reference.SOPClassUID, true, DiagnosticMissingSOPClassUID, DiagnosticInvalidSOPClassUID) && valid
	valid = resolver.validateUID(reference.Role, index, reference.SOPInstanceUID, true, DiagnosticMissingSOPInstanceUID, DiagnosticInvalidSOPInstanceUID) && valid
	valid = resolver.validateUID(reference.Role, index, reference.StudyInstanceUID, requirements.study, DiagnosticMissingStudyInstanceUID, DiagnosticInvalidStudyInstanceUID) && valid
	valid = resolver.validateUID(reference.Role, index, reference.SeriesInstanceUID, requirements.series, DiagnosticMissingSeriesInstanceUID, DiagnosticInvalidSeriesInstanceUID) && valid
	if valid {
		resolver.resolution.References = append(resolver.resolution.References, reference)
	}
}

func (resolver *relationResolver) validateUID(role ReferenceRole, index int, value string, required bool, missingCode, invalidCode DiagnosticCode) bool {
	if value == "" {
		if required {
			resolver.resolution.Diagnostics = append(resolver.resolution.Diagnostics, Diagnostic{Code: missingCode, Role: role, ReferenceIndex: index})
			return false
		}
		return true
	}
	if !core.IsValidUID(value) {
		resolver.resolution.Diagnostics = append(resolver.resolution.Diagnostics, Diagnostic{Code: invalidCode, Role: role, ReferenceIndex: index})
		return false
	}
	return true
}

func (resolver *relationResolver) addMissingDirectReferences(dataset *object.Object, role ReferenceRole, sequenceTags ...core.Tag) {
	for _, sequenceTag := range sequenceTags {
		items, _ := dataset.GetSequence(sequenceTag)
		for _, item := range items {
			if missingReferencedSOPInstanceUID(item) {
				resolver.addReference(referenceFromObject(item, role), referenceRequirements{})
			}
		}
	}
}

func (resolver *relationResolver) addMissingNestedReferences(dataset *object.Object, role ReferenceRole) {
	resolver.visitNestedReferences(dataset, func(item *object.Object) {
		if missingReferencedSOPInstanceUID(item) {
			resolver.addReference(referenceFromObject(item, role), referenceRequirements{})
		}
	})
}

func (resolver *relationResolver) addMalformedNestedReferences(dataset *object.Object, role ReferenceRole) {
	resolver.visitNestedReferences(dataset, func(item *object.Object) {
		classTag := core.NewTag(0x0008, 0x1150)
		instanceTag := core.NewTag(0x0008, 0x1155)
		if !item.Has(classTag) && !item.Has(instanceTag) {
			return
		}
		classUID := objectUID(item, classTag)
		instanceUID := objectUID(item, instanceTag)
		if core.IsValidUID(classUID) && core.IsValidUID(instanceUID) {
			return
		}
		resolver.addReference(Reference{
			Role:           role,
			SOPClassUID:    classUID,
			SOPInstanceUID: instanceUID,
		}, referenceRequirements{})
	})
}

func (resolver *relationResolver) visitNestedReferences(dataset *object.Object, visitReference func(*object.Object)) {
	var visit func(*object.Object)
	visit = func(current *object.Object) {
		if current == nil {
			return
		}
		visitReference(current)
		for _, element := range current.Elements() {
			if element.VR() != core.VRSQ {
				continue
			}
			items, _ := current.GetSequence(element.Tag())
			for _, item := range items {
				visit(item)
			}
		}
	}
	visit(dataset)
}

func missingReferencedSOPInstanceUID(item *object.Object) bool {
	if item == nil || !item.Has(core.NewTag(0x0008, 0x1150)) {
		return false
	}
	return objectUID(item, core.NewTag(0x0008, 0x1155)) == ""
}

func referenceFromObject(item *object.Object, role ReferenceRole) Reference {
	return Reference{
		Role:           role,
		SOPClassUID:    objectUID(item, core.NewTag(0x0008, 0x1150)),
		SOPInstanceUID: objectUID(item, core.NewTag(0x0008, 0x1155)),
	}
}

func (resolver *relationResolver) preferStudy(value string) {
	resolver.resolution.StudyInstanceUID = firstNonEmpty(value, resolver.resolution.StudyInstanceUID)
}

func (resolver *relationResolver) preferFrame(value string) {
	resolver.resolution.FrameOfReferenceUID = firstNonEmpty(value, resolver.resolution.FrameOfReferenceUID)
}

func malformed(kind ObjectKind, err error) error {
	return &ResolutionError{Code: DiagnosticMalformedObject, Kind: kind, Err: err}
}

func datasetUID(dataset *object.Object, tag core.Tag) string {
	if dataset == nil {
		return ""
	}
	value, ok := dataset.GetUID(tag)
	if !ok {
		return ""
	}
	return cleanUID(value)
}

func objectUID(dataset *object.Object, tag core.Tag) string {
	if dataset == nil {
		return ""
	}
	value, ok := dataset.GetString(tag)
	if !ok {
		return ""
	}
	return cleanUID(value)
}

func cleanUID(value string) string {
	return strings.TrimSpace(core.NormalizeUID(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if cleaned := cleanUID(value); cleaned != "" {
			return cleaned
		}
	}
	return ""
}

func appendUnique(values []string, value string) []string {
	value = cleanUID(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
