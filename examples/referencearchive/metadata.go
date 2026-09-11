package main

import (
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/object"
)

// Preflight only validates the incoming archive profile. Authoritative stored
// records always come from index.Read of the successfully published file.
func incomingRecord(ds *object.Object) (index.Record, error) {
	r := index.Record{Patient: &index.Patient{}}
	fields := map[string]*string{
		"PatientID": &r.Patient.ID, "PatientName": &r.Patient.Name, "PatientBirthDate": &r.Patient.BirthDate, "PatientSex": &r.Patient.Sex,
		"StudyInstanceUID": &r.Study.InstanceUID, "StudyDate": &r.Study.Date, "StudyTime": &r.Study.Time, "AccessionNumber": &r.Study.AccessionNumber, "StudyDescription": &r.Study.Description,
		"SeriesInstanceUID": &r.Series.InstanceUID, "Modality": &r.Series.Modality, "SeriesNumber": &r.Series.Number, "SeriesDate": &r.Series.Date, "SeriesTime": &r.Series.Time, "SeriesDescription": &r.Series.Description,
		"SOPClassUID": &r.Instance.SOPClassUID, "SOPInstanceUID": &r.Instance.SOPInstanceUID, "InstanceNumber": &r.Instance.Number,
	}
	for keyword, destination := range fields {
		definition, ok := std.Dictionary.ByKeyword(keyword)
		if !ok {
			return index.Record{}, errArchive
		}
		elem, present := ds.Get(definition.Tag)
		if !present {
			continue
		}
		if elem.VR() != definition.VR {
			return index.Record{}, errArchive
		}
		values, ok := ds.GetStrings(definition.Tag)
		if !ok || len(values) != 1 || len(values[0]) > 1024 {
			return index.Record{}, errArchive
		}
		*destination = values[0]
		if definition.VR == core.VRUI {
			*destination = core.NormalizeUID(*destination)
		}
	}
	if !validRecord(r) {
		return index.Record{}, errArchive
	}
	return r, nil
}
