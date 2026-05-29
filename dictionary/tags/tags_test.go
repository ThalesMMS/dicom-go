package tags_test

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
)

func TestCommonTagsMatchStandardDictionary(t *testing.T) {
	tests := []struct {
		name    string
		got     core.Tag
		want    core.Tag
		keyword string
	}{
		{name: "MediaStorageSOPClassUID", got: tags.MediaStorageSOPClassUID, want: core.NewTag(0x0002, 0x0002), keyword: "MediaStorageSOPClassUID"},
		{name: "MediaStorageSOPInstanceUID", got: tags.MediaStorageSOPInstanceUID, want: core.NewTag(0x0002, 0x0003), keyword: "MediaStorageSOPInstanceUID"},
		{name: "TransferSyntaxUID", got: tags.TransferSyntaxUID, want: core.NewTag(0x0002, 0x0010), keyword: "TransferSyntaxUID"},
		{name: "SOPClassUID", got: tags.SOPClassUID, want: core.NewTag(0x0008, 0x0016), keyword: "SOPClassUID"},
		{name: "SOPInstanceUID", got: tags.SOPInstanceUID, want: core.NewTag(0x0008, 0x0018), keyword: "SOPInstanceUID"},
		{name: "StudyDate", got: tags.StudyDate, want: core.NewTag(0x0008, 0x0020), keyword: "StudyDate"},
		{name: "SeriesDate", got: tags.SeriesDate, want: core.NewTag(0x0008, 0x0021), keyword: "SeriesDate"},
		{name: "AcquisitionDate", got: tags.AcquisitionDate, want: core.NewTag(0x0008, 0x0022), keyword: "AcquisitionDate"},
		{name: "AcquisitionDateTime", got: tags.AcquisitionDateTime, want: core.NewTag(0x0008, 0x002A), keyword: "AcquisitionDateTime"},
		{name: "StudyTime", got: tags.StudyTime, want: core.NewTag(0x0008, 0x0030), keyword: "StudyTime"},
		{name: "SeriesTime", got: tags.SeriesTime, want: core.NewTag(0x0008, 0x0031), keyword: "SeriesTime"},
		{name: "AcquisitionTime", got: tags.AcquisitionTime, want: core.NewTag(0x0008, 0x0032), keyword: "AcquisitionTime"},
		{name: "AccessionNumber", got: tags.AccessionNumber, want: core.NewTag(0x0008, 0x0050), keyword: "AccessionNumber"},
		{name: "QueryRetrieveLevel", got: tags.QueryRetrieveLevel, want: core.NewTag(0x0008, 0x0052), keyword: "QueryRetrieveLevel"},
		{name: "RetrieveAETitle", got: tags.RetrieveAETitle, want: core.NewTag(0x0008, 0x0054), keyword: "RetrieveAETitle"},
		{name: "Modality", got: tags.Modality, want: core.NewTag(0x0008, 0x0060), keyword: "Modality"},
		{name: "ModalitiesInStudy", got: tags.ModalitiesInStudy, want: core.NewTag(0x0008, 0x0061), keyword: "ModalitiesInStudy"},
		{name: "PresentationIntentType", got: tags.PresentationIntentType, want: core.NewTag(0x0008, 0x0068), keyword: "PresentationIntentType"},
		{name: "InstitutionName", got: tags.InstitutionName, want: core.NewTag(0x0008, 0x0080), keyword: "InstitutionName"},
		{name: "ReferringPhysicianName", got: tags.ReferringPhysicianName, want: core.NewTag(0x0008, 0x0090), keyword: "ReferringPhysicianName"},
		{name: "TimezoneOffsetFromUTC", got: tags.TimezoneOffsetFromUTC, want: core.NewTag(0x0008, 0x0201), keyword: "TimezoneOffsetFromUTC"},
		{name: "StudyDescription", got: tags.StudyDescription, want: core.NewTag(0x0008, 0x1030), keyword: "StudyDescription"},
		{name: "SeriesDescription", got: tags.SeriesDescription, want: core.NewTag(0x0008, 0x103E), keyword: "SeriesDescription"},
		{name: "PerformingPhysicianName", got: tags.PerformingPhysicianName, want: core.NewTag(0x0008, 0x1050), keyword: "PerformingPhysicianName"},
		{name: "PatientName", got: tags.PatientName, want: core.NewTag(0x0010, 0x0010), keyword: "PatientName"},
		{name: "PatientID", got: tags.PatientID, want: core.NewTag(0x0010, 0x0020), keyword: "PatientID"},
		{name: "PatientBirthDate", got: tags.PatientBirthDate, want: core.NewTag(0x0010, 0x0030), keyword: "PatientBirthDate"},
		{name: "PatientSex", got: tags.PatientSex, want: core.NewTag(0x0010, 0x0040), keyword: "PatientSex"},
		{name: "PatientAge", got: tags.PatientAge, want: core.NewTag(0x0010, 0x1010), keyword: "PatientAge"},
		{name: "PatientWeight", got: tags.PatientWeight, want: core.NewTag(0x0010, 0x1030), keyword: "PatientWeight"},
		{name: "PatientComments", got: tags.PatientComments, want: core.NewTag(0x0010, 0x4000), keyword: "PatientComments"},
		{name: "BodyPartExamined", got: tags.BodyPartExamined, want: core.NewTag(0x0018, 0x0015), keyword: "BodyPartExamined"},
		{name: "SliceThickness", got: tags.SliceThickness, want: core.NewTag(0x0018, 0x0050), keyword: "SliceThickness"},
		{name: "RadiopharmaceuticalStartTime", got: tags.RadiopharmaceuticalStartTime, want: core.NewTag(0x0018, 0x1072), keyword: "RadiopharmaceuticalStartTime"},
		{name: "RadionuclideTotalDose", got: tags.RadionuclideTotalDose, want: core.NewTag(0x0018, 0x1074), keyword: "RadionuclideTotalDose"},
		{name: "RadionuclideHalfLife", got: tags.RadionuclideHalfLife, want: core.NewTag(0x0018, 0x1075), keyword: "RadionuclideHalfLife"},
		{name: "RadiopharmaceuticalStartDateTime", got: tags.RadiopharmaceuticalStartDateTime, want: core.NewTag(0x0018, 0x1078), keyword: "RadiopharmaceuticalStartDateTime"},
		{name: "StudyInstanceUID", got: tags.StudyInstanceUID, want: core.NewTag(0x0020, 0x000D), keyword: "StudyInstanceUID"},
		{name: "SeriesInstanceUID", got: tags.SeriesInstanceUID, want: core.NewTag(0x0020, 0x000E), keyword: "SeriesInstanceUID"},
		{name: "StudyID", got: tags.StudyID, want: core.NewTag(0x0020, 0x0010), keyword: "StudyID"},
		{name: "SeriesNumber", got: tags.SeriesNumber, want: core.NewTag(0x0020, 0x0011), keyword: "SeriesNumber"},
		{name: "InstanceNumber", got: tags.InstanceNumber, want: core.NewTag(0x0020, 0x0013), keyword: "InstanceNumber"},
		{name: "ImagePositionPatient", got: tags.ImagePositionPatient, want: core.NewTag(0x0020, 0x0032), keyword: "ImagePositionPatient"},
		{name: "ImageOrientationPatient", got: tags.ImageOrientationPatient, want: core.NewTag(0x0020, 0x0037), keyword: "ImageOrientationPatient"},
		{name: "FrameOfReferenceUID", got: tags.FrameOfReferenceUID, want: core.NewTag(0x0020, 0x0052), keyword: "FrameOfReferenceUID"},
		{name: "Laterality", got: tags.Laterality, want: core.NewTag(0x0020, 0x0060), keyword: "Laterality"},
		{name: "SliceLocation", got: tags.SliceLocation, want: core.NewTag(0x0020, 0x1041), keyword: "SliceLocation"},
		{name: "NumberOfStudyRelatedSeries", got: tags.NumberOfStudyRelatedSeries, want: core.NewTag(0x0020, 0x1206), keyword: "NumberOfStudyRelatedSeries"},
		{name: "NumberOfStudyRelatedInstances", got: tags.NumberOfStudyRelatedInstances, want: core.NewTag(0x0020, 0x1208), keyword: "NumberOfStudyRelatedInstances"},
		{name: "NumberOfSeriesRelatedInstances", got: tags.NumberOfSeriesRelatedInstances, want: core.NewTag(0x0020, 0x1209), keyword: "NumberOfSeriesRelatedInstances"},
		{name: "StudyStatusID", got: tags.StudyStatusID, want: core.NewTag(0x0032, 0x000A), keyword: "StudyStatusID"},
		{name: "SamplesPerPixel", got: tags.SamplesPerPixel, want: core.NewTag(0x0028, 0x0002), keyword: "SamplesPerPixel"},
		{name: "PhotometricInterpretation", got: tags.PhotometricInterpretation, want: core.NewTag(0x0028, 0x0004), keyword: "PhotometricInterpretation"},
		{name: "PlanarConfiguration", got: tags.PlanarConfiguration, want: core.NewTag(0x0028, 0x0006), keyword: "PlanarConfiguration"},
		{name: "NumberOfFrames", got: tags.NumberOfFrames, want: core.NewTag(0x0028, 0x0008), keyword: "NumberOfFrames"},
		{name: "Rows", got: tags.Rows, want: core.NewTag(0x0028, 0x0010), keyword: "Rows"},
		{name: "Columns", got: tags.Columns, want: core.NewTag(0x0028, 0x0011), keyword: "Columns"},
		{name: "PixelSpacing", got: tags.PixelSpacing, want: core.NewTag(0x0028, 0x0030), keyword: "PixelSpacing"},
		{name: "CorrectedImage", got: tags.CorrectedImage, want: core.NewTag(0x0028, 0x0051), keyword: "CorrectedImage"},
		{name: "BitsAllocated", got: tags.BitsAllocated, want: core.NewTag(0x0028, 0x0100), keyword: "BitsAllocated"},
		{name: "BitsStored", got: tags.BitsStored, want: core.NewTag(0x0028, 0x0101), keyword: "BitsStored"},
		{name: "HighBit", got: tags.HighBit, want: core.NewTag(0x0028, 0x0102), keyword: "HighBit"},
		{name: "PixelRepresentation", got: tags.PixelRepresentation, want: core.NewTag(0x0028, 0x0103), keyword: "PixelRepresentation"},
		{name: "WindowCenter", got: tags.WindowCenter, want: core.NewTag(0x0028, 0x1050), keyword: "WindowCenter"},
		{name: "WindowWidth", got: tags.WindowWidth, want: core.NewTag(0x0028, 0x1051), keyword: "WindowWidth"},
		{name: "RescaleIntercept", got: tags.RescaleIntercept, want: core.NewTag(0x0028, 0x1052), keyword: "RescaleIntercept"},
		{name: "RescaleSlope", got: tags.RescaleSlope, want: core.NewTag(0x0028, 0x1053), keyword: "RescaleSlope"},
		{name: "EncapsulatedDocument", got: tags.EncapsulatedDocument, want: core.NewTag(0x0042, 0x0011), keyword: "EncapsulatedDocument"},
		{name: "MIMETypeOfEncapsulatedDocument", got: tags.MIMETypeOfEncapsulatedDocument, want: core.NewTag(0x0042, 0x0012), keyword: "MIMETypeOfEncapsulatedDocument"},
		{name: "EncapsulatedDocumentLength", got: tags.EncapsulatedDocumentLength, want: core.NewTag(0x0042, 0x0015), keyword: "EncapsulatedDocumentLength"},
		{name: "RadiopharmaceuticalInformationSequence", got: tags.RadiopharmaceuticalInformationSequence, want: core.NewTag(0x0054, 0x0016), keyword: "RadiopharmaceuticalInformationSequence"},
		{name: "Units", got: tags.Units, want: core.NewTag(0x0054, 0x1001), keyword: "Units"},
		{name: "SUVType", got: tags.SUVType, want: core.NewTag(0x0054, 0x1006), keyword: "SUVType"},
		{name: "DecayCorrection", got: tags.DecayCorrection, want: core.NewTag(0x0054, 0x1102), keyword: "DecayCorrection"},
		{name: "PixelData", got: tags.PixelData, want: core.NewTag(0x7FE0, 0x0010), keyword: "PixelData"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("%s = %s, want %s", tt.name, tt.got, tt.want)
			}
			entry, ok := std.Dictionary.ByTag(tt.got)
			if !ok {
				t.Fatalf("std.Dictionary.ByTag(%s) did not find %s", tt.got, tt.name)
			}
			if entry.Keyword != tt.keyword {
				t.Fatalf("std keyword for %s = %q, want %q", tt.name, entry.Keyword, tt.keyword)
			}
		})
	}
}
