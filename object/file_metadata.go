package object

import (
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// FileMetadata contains common Part 10 metadata useful for inspection views.
type FileMetadata struct {
	TransferSyntaxUID       string
	TransferSyntaxName      string
	PatientName             string
	PatientID               string
	PatientBirthDate        string
	InstitutionName         string
	StudyDate               string
	StudyTime               string
	SeriesDate              string
	SeriesTime              string
	StudyDescription        string
	Modality                string
	AccessionNumber         string
	SeriesDescription       string
	StudyInstanceUID        string
	SeriesInstanceUID       string
	SeriesNumber            string
	SOPClassUID             string
	SOPInstanceUID          string
	InstanceNumber          string
	StudyID                 string
	BodyPartExamined        string
	ReferringPhysicianName  string
	PerformingPhysicianName string
}

// Metadata returns common metadata from file meta information and the data set.
// Missing elements are returned as empty strings.
func (f *File) Metadata() FileMetadata {
	var meta FileMetadata
	meta.TransferSyntaxUID = fileMetadataTransferSyntaxUID(f)
	meta.TransferSyntaxName = fileMetadataTransferSyntaxName(f, meta.TransferSyntaxUID)
	meta.PatientName = fileString(f, 0x0010, 0x0010)
	meta.PatientID = fileString(f, 0x0010, 0x0020)
	meta.PatientBirthDate = fileString(f, 0x0010, 0x0030)
	meta.InstitutionName = fileString(f, 0x0008, 0x0080)
	meta.StudyDate = fileString(f, 0x0008, 0x0020)
	meta.StudyTime = fileString(f, 0x0008, 0x0030)
	meta.SeriesDate = fileString(f, 0x0008, 0x0021)
	meta.SeriesTime = fileString(f, 0x0008, 0x0031)
	meta.StudyDescription = fileString(f, 0x0008, 0x1030)
	meta.Modality = fileString(f, 0x0008, 0x0060)
	meta.AccessionNumber = fileString(f, 0x0008, 0x0050)
	meta.SeriesDescription = fileString(f, 0x0008, 0x103E)
	meta.StudyInstanceUID = fileUID(f, 0x0020, 0x000D)
	meta.SeriesInstanceUID = fileUID(f, 0x0020, 0x000E)
	meta.SeriesNumber = fileString(f, 0x0020, 0x0011)
	meta.SOPClassUID = fileUID(f, 0x0008, 0x0016)
	meta.SOPInstanceUID = fileUID(f, 0x0008, 0x0018)
	meta.InstanceNumber = fileString(f, 0x0020, 0x0013)
	meta.StudyID = fileString(f, 0x0020, 0x0010)
	meta.BodyPartExamined = fileString(f, 0x0018, 0x0015)
	meta.ReferringPhysicianName = fileString(f, 0x0008, 0x0090)
	meta.PerformingPhysicianName = fileString(f, 0x0008, 0x1050)
	return meta
}

func fileMetadataTransferSyntaxUID(f *File) string {
	if f == nil {
		return ""
	}
	if f.TransferSyntax.UID != "" {
		return f.TransferSyntax.UID
	}
	return fileUID(f, 0x0002, 0x0010)
}

func fileMetadataTransferSyntaxName(f *File, uid string) string {
	if f != nil && f.TransferSyntax.Name != "" {
		return f.TransferSyntax.Name
	}
	if syntax, ok := transfer.DefaultRegistry.Get(uid); ok {
		return syntax.Name
	}
	return ""
}

func fileString(f *File, group, element uint16) string {
	value, _ := f.GetString(core.NewTag(group, element))
	return value
}

func fileUID(f *File, group, element uint16) string {
	value, _ := f.GetUID(core.NewTag(group, element))
	return value
}
