package dicomdir

import (
	"math"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

type encodedReference struct {
	fileID                     FileID
	recordType                 RecordType
	selection                  FileRecord
	sopClassUID                string
	sopInstanceUID             string
	transferSyntaxUID          string
	relatedGeneralSOPClassUIDs []string
}

type directoryTraversalFrame struct {
	offset     uint32
	parentType RecordType
	root       bool
	selection  FileRecord
}

// validateDirectoryDataSet validates the offset graph without using the
// compatibility flat-order fallback exposed by References.
func validateDirectoryDataSet(dataset *object.Object) ([]encodedReference, error) {
	if dataset == nil {
		return nil, ErrInvalidRecord
	}
	first, ok := uint32Value(dataset, tagOffsetFirstRootDirectoryRecord)
	if !ok {
		return nil, ErrInvalidRecord
	}
	last, ok := uint32Value(dataset, tagOffsetLastRootDirectoryRecord)
	if !ok {
		return nil, ErrInvalidRecord
	}
	consistency, ok := uint16Value(dataset, tagFileSetConsistencyFlag)
	if !ok || consistency != 0 {
		return nil, ErrInvalidRecord
	}
	items, ok := dataset.GetSequence(tagDirectoryRecordSequence)
	if !ok {
		return nil, ErrInvalidRecord
	}
	if len(items) == 0 {
		if first != 0 || last != 0 {
			return nil, ErrInvalidRecord
		}
		return nil, nil
	}
	if first == 0 || last == 0 {
		return nil, ErrInvalidRecord
	}
	byOffset := make(map[uint32]*object.Object, len(items))
	for _, item := range items {
		if item == nil {
			return nil, ErrInvalidRecord
		}
		offset, set := item.ItemOffset()
		if !set || offset <= 0 || offset > math.MaxUint32 || byOffset[uint32(offset)] != nil {
			return nil, ErrInvalidRecord
		}
		byOffset[uint32(offset)] = item
	}

	rootLast := uint32(0)
	for offset, seen := first, map[uint32]bool{}; offset != 0; {
		if seen[offset] || byOffset[offset] == nil {
			return nil, ErrInvalidRecord
		}
		seen[offset] = true
		rootLast = offset
		next, ok := uint32Value(byOffset[offset], tagOffsetNextDirectoryRecord)
		if !ok {
			return nil, ErrInvalidRecord
		}
		offset = next
	}
	if rootLast != last {
		return nil, ErrInvalidRecord
	}

	visited := make(map[uint32]bool, len(items))
	pending := []directoryTraversalFrame{{offset: first, root: true}}
	references := make([]encodedReference, 0)
	fileIDs := make(map[string]bool)
	sopInstances := make(map[string]bool)
	for len(pending) != 0 {
		frame := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if frame.offset == 0 {
			continue
		}
		if visited[frame.offset] {
			return nil, ErrInvalidRecord
		}
		item := byOffset[frame.offset]
		if item == nil {
			return nil, ErrInvalidRecord
		}
		visited[frame.offset] = true
		if characterSet, present := item.Get(tagSpecificCharacterSetFS); present && characterSet.VR() != core.VRCS {
			return nil, ErrInvalidRecord
		}
		typeValue, ok := stringWithVR(item, tagDirectoryRecordType, core.VRCS)
		if !ok {
			return nil, ErrInvalidRecord
		}
		recordType := RecordType(typeValue)
		if !validHierarchyChild(frame.parentType, recordType, frame.root) {
			return nil, ErrInvalidRecord
		}
		next, nextOK := uint32Value(item, tagOffsetNextDirectoryRecord)
		lower, lowerOK := uint32Value(item, tagOffsetLowerLevelRecord)
		inUse, inUseOK := uint16Value(item, tagRecordInUseFlag)
		if !nextOK || !lowerOK || !inUseOK || inUse == 0 {
			return nil, ErrInvalidRecord
		}
		if next != 0 {
			pending = append(pending, directoryTraversalFrame{
				offset: next, parentType: frame.parentType, root: frame.root,
				selection: frame.selection,
			})
		}
		childFrame := directoryTraversalFrame{offset: lower, parentType: recordType, selection: frame.selection}
		switch recordType {
		case RecordTypePatient:
			patientID, patientOK := requiredStringWithVR(item, tagPatientIDFS, core.VRLO)
			patientName, patientNameOK := typeTwoStringWithVR(item, tagPatientNameFS, core.VRPN)
			if !patientOK || !patientNameOK {
				return nil, ErrInvalidRecord
			}
			childFrame.selection.PatientName = patientName
			childFrame.selection.PatientID = patientID
		case RecordTypeStudy:
			studyUID, studyOK := requiredUID(item, tagStudyInstanceUIDFS)
			if !studyOK || !hasRequiredStudyKeys(item) {
				return nil, ErrInvalidRecord
			}
			childFrame.selection.StudyDate, _ = item.GetString(tagStudyDateFS)
			childFrame.selection.StudyTime, _ = item.GetString(tagStudyTimeFS)
			childFrame.selection.StudyDescription, _ = item.GetString(tagStudyDescriptionFS)
			childFrame.selection.StudyInstanceUID = studyUID
			childFrame.selection.StudyID, _ = item.GetString(tagStudyIDFS)
			childFrame.selection.AccessionNumber, _ = item.GetString(tagAccessionNumberFS)
		case RecordTypeSeries:
			seriesUID, seriesOK := requiredUID(item, tagSeriesInstanceUIDFS)
			if !seriesOK || !hasRequiredSeriesKeys(item) {
				return nil, ErrInvalidRecord
			}
			childFrame.selection.Modality, _ = item.GetString(tagModalityFS)
			childFrame.selection.SeriesInstanceUID = seriesUID
			childFrame.selection.SeriesNumber, _ = item.GetString(tagSeriesNumberFS)
		default:
			if recordType == RecordTypeImage {
				instanceNumber, instanceOK := requiredStringWithVR(item, tagInstanceNumberFS, core.VRIS)
				if !instanceOK {
					return nil, ErrInvalidRecord
				}
				childFrame.selection.InstanceNumber = instanceNumber
			}
		}
		if lower != 0 {
			pending = append(pending, childFrame)
		} else if recordType != RecordTypePatient && recordType != RecordTypeStudy && recordType != RecordTypeSeries {
			// Leaf records have no lower entity by definition.
		}
		fileIDValues, hasFileID := stringsWithVR(item, tagReferencedFileID, core.VRCS)
		if !hasFileID {
			if recordType != RecordTypePatient && recordType != RecordTypeStudy && recordType != RecordTypeSeries {
				return nil, ErrInvalidRecord
			}
			continue
		}
		if recordType == RecordTypePatient || recordType == RecordTypeStudy || recordType == RecordTypeSeries {
			return nil, ErrInvalidRecord
		}
		fileID, err := NewFileID(fileIDValues...)
		if err != nil {
			return nil, err
		}
		classUID, classOK := requiredUID(item, tagReferencedSOPClassUIDInFile)
		instanceUID, instanceOK := requiredUID(item, tagReferencedSOPInstanceUIDInFile)
		syntaxUID, syntaxOK := requiredUID(item, tagReferencedTransferSyntaxUIDInFile)
		if !classOK || !instanceOK || !syntaxOK {
			return nil, ErrInvalidRecord
		}
		relatedUIDs, hasRelatedUIDs := stringsWithVR(item, tagReferencedRelatedGeneralSOPClassUIDInFile, core.VRUI)
		if hasRelatedUIDs {
			if len(relatedUIDs) == 0 {
				return nil, ErrInvalidRecord
			}
			for i, uid := range relatedUIDs {
				relatedUIDs[i] = core.NormalizeUID(uid)
				if !validUID(relatedUIDs[i]) {
					return nil, ErrInvalidRecord
				}
			}
		}
		if fileIDs[fileID.String()] || sopInstances[instanceUID] {
			return nil, ErrInvalidRecord
		}
		fileIDs[fileID.String()] = true
		sopInstances[instanceUID] = true
		references = append(references, encodedReference{
			fileID: fileID, recordType: recordType, selection: childFrame.selection, sopClassUID: classUID,
			sopInstanceUID: instanceUID, transferSyntaxUID: syntaxUID,
			relatedGeneralSOPClassUIDs: append([]string(nil), relatedUIDs...),
		})
	}
	if len(visited) != len(items) {
		return nil, ErrInvalidRecord
	}
	return references, nil
}

func stringWithVR(item *object.Object, tag core.Tag, vr core.VR) (string, bool) {
	element, ok := item.Get(tag)
	if !ok || element.VR() != vr {
		return "", false
	}
	values, ok := item.GetStrings(tag)
	if !ok {
		return "", false
	}
	if len(values) == 0 {
		length, lengthSet := element.CalculatedLength()
		return "", lengthSet && length == 0
	}
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func stringsWithVR(item *object.Object, tag core.Tag, vr core.VR) ([]string, bool) {
	element, ok := item.Get(tag)
	if !ok || element.VR() != vr {
		return nil, false
	}
	return item.GetStrings(tag)
}

func requiredStringWithVR(item *object.Object, tag core.Tag, vr core.VR) (string, bool) {
	value, ok := stringWithVR(item, tag, vr)
	return value, ok && value != ""
}

func typeTwoStringWithVR(item *object.Object, tag core.Tag, vr core.VR) (string, bool) {
	return stringWithVR(item, tag, vr)
}

func requiredUID(item *object.Object, tag core.Tag) (string, bool) {
	value, ok := stringWithVR(item, tag, core.VRUI)
	value = core.NormalizeUID(value)
	return value, ok && validUID(value)
}

func hasRequiredStudyKeys(item *object.Object) bool {
	_, dateOK := requiredStringWithVR(item, tagStudyDateFS, core.VRDA)
	_, timeOK := requiredStringWithVR(item, tagStudyTimeFS, core.VRTM)
	_, idOK := requiredStringWithVR(item, tagStudyIDFS, core.VRSH)
	_, descriptionOK := typeTwoStringWithVR(item, tagStudyDescriptionFS, core.VRLO)
	_, accessionOK := typeTwoStringWithVR(item, tagAccessionNumberFS, core.VRSH)
	return dateOK && timeOK && idOK && descriptionOK && accessionOK
}

func hasRequiredSeriesKeys(item *object.Object) bool {
	_, modalityOK := requiredStringWithVR(item, tagModalityFS, core.VRCS)
	_, numberOK := requiredStringWithVR(item, tagSeriesNumberFS, core.VRIS)
	return modalityOK && numberOK
}

func validHierarchyChild(parent, child RecordType, root bool) bool {
	if root {
		return child == RecordTypePatient
	}
	switch parent {
	case RecordTypePatient:
		return child == RecordTypeStudy
	case RecordTypeStudy:
		return child == RecordTypeSeries
	case RecordTypeSeries:
		return child == RecordTypeImage
	default:
		return false
	}
}

func uint16Value(obj *object.Object, tag core.Tag) (uint16, bool) {
	element, ok := obj.Get(tag)
	if !ok || element.VR() != core.VRUS {
		return 0, false
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw) != 2 {
		return 0, false
	}
	return obj.ValueByteOrder().Uint16(raw), true
}
