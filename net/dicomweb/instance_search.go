package dicomweb

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/dicomjson"
)

// InstanceSearchCriteria describes neutral QIDO-RS instance search inputs.
type InstanceSearchCriteria struct {
	SOPInstanceUID string
	SOPClassUID    string
	InstanceNumber string
	Modality       string

	CustomFieldKeyword string
	CustomFieldValue   string
	Limit              int
}

// InstanceMatch is a neutral QIDO-RS instance result extracted from DICOM JSON.
type InstanceMatch struct {
	SOPInstanceUID    string
	SOPClassUID       string
	InstanceNumber    string
	Modality          string
	PatientID         string
	PatientName       string
	StudyInstanceUID  string
	SeriesInstanceUID string
}

var instanceSearchReturnFields = []string{
	"SOPInstanceUID",
	"SOPClassUID",
	"InstanceNumber",
	"Modality",
	"PatientID",
	"PatientName",
	"StudyInstanceUID",
	"SeriesInstanceUID",
}

// InstanceSearchParams converts neutral instance criteria into QIDO-RS query params.
func InstanceSearchParams(criteria InstanceSearchCriteria) (url.Values, error) {
	if strings.TrimSpace(criteria.CustomFieldValue) != "" && strings.TrimSpace(criteria.CustomFieldKeyword) == "" {
		return nil, fmt.Errorf("dicomweb: custom DICOM field keyword is required")
	}
	params := url.Values{}
	addSearchParam(params, "SOPInstanceUID", criteria.SOPInstanceUID)
	addSearchParam(params, "SOPClassUID", criteria.SOPClassUID)
	addSearchParam(params, "InstanceNumber", criteria.InstanceNumber)
	addSearchParam(params, "Modality", criteria.Modality)
	addSearchParam(params, criteria.CustomFieldKeyword, criteria.CustomFieldValue)
	if criteria.Limit > 0 {
		params.Set("limit", strconv.Itoa(criteria.Limit))
	}
	for _, field := range instanceSearchReturnFields {
		params.Add("includefield", field)
	}
	return params, nil
}

// InstanceSearchReturnFields returns the default QIDO-RS instance return fields.
func InstanceSearchReturnFields() []string {
	return append([]string(nil), instanceSearchReturnFields...)
}

// InstanceMatchesFromDatasets extracts neutral instance matches from raw DICOM JSON.
func InstanceMatchesFromDatasets(datasets []Dataset) []InstanceMatch {
	matches := make([]InstanceMatch, 0, len(datasets))
	for _, dataset := range datasets {
		matches = append(matches, InstanceMatch{
			SOPInstanceUID:    dicomjson.ElementString(dataset, "00080018"),
			SOPClassUID:       dicomjson.ElementString(dataset, "00080016"),
			InstanceNumber:    dicomjson.ElementString(dataset, "00200013"),
			Modality:          dicomjson.ElementString(dataset, "00080060"),
			PatientID:         dicomjson.ElementString(dataset, "00100020"),
			PatientName:       dicomjson.ElementString(dataset, "00100010"),
			StudyInstanceUID:  dicomjson.ElementString(dataset, "0020000D"),
			SeriesInstanceUID: dicomjson.ElementString(dataset, "0020000E"),
		})
	}
	return matches
}
