package dicomweb

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/dicomjson"
)

// StudySearchCriteria describes neutral QIDO-RS study search inputs.
type StudySearchCriteria struct {
	PatientName      string
	PatientID        string
	PatientBirthDate string

	StudyDateFrom string
	StudyDateTo   string
	StudyTimeFrom string
	StudyTimeTo   string

	StudyDescription       string
	AccessionNumber        string
	ReferringPhysicianName string
	InstitutionName        string
	PatientComments        string
	StudyStatusID          string
	BodyPartExamined       string
	WorkListStatus         string
	Modality               string
	StudyInstanceUID       string

	CustomFieldKeyword string
	CustomFieldValue   string
	Limit              int
}

// StudyMatch is a neutral QIDO-RS study result extracted from DICOM JSON.
type StudyMatch struct {
	PatientName      string
	PatientID        string
	PatientBirthDate string

	StudyDate  string
	StudyTime  string
	ImageCount string

	StudyDescription       string
	AccessionNumber        string
	ReferringPhysicianName string
	InstitutionName        string
	PatientComments        string
	StudyStatusID          string
	BodyPartExamined       string
	WorkListStatus         string
	StudyInstanceUID       string
	Modalities             string
}

var studySearchReturnFields = []string{
	"PatientName",
	"PatientID",
	"PatientBirthDate",
	"StudyDate",
	"StudyTime",
	"NumberOfStudyRelatedInstances",
	"StudyDescription",
	"AccessionNumber",
	"ReferringPhysicianName",
	"InstitutionName",
	"PatientComments",
	"StudyStatusID",
	"BodyPartExamined",
	"ScheduledProcedureStepStatus",
	"StudyInstanceUID",
	"ModalitiesInStudy",
}

// StudySearchParams converts neutral study criteria into QIDO-RS query params.
func StudySearchParams(criteria StudySearchCriteria) (url.Values, error) {
	if strings.TrimSpace(criteria.CustomFieldValue) != "" && strings.TrimSpace(criteria.CustomFieldKeyword) == "" {
		return nil, fmt.Errorf("dicomweb: custom DICOM field keyword is required")
	}
	params := url.Values{}
	addSearchParam(params, "PatientName", criteria.PatientName)
	addSearchParam(params, "PatientID", criteria.PatientID)
	addSearchParam(params, "PatientBirthDate", criteria.PatientBirthDate)
	addSearchParam(params, "StudyDate", studyRangeValue(criteria.StudyDateFrom, criteria.StudyDateTo))
	addSearchParam(params, "StudyTime", studyRangeValue(criteria.StudyTimeFrom, criteria.StudyTimeTo))
	addSearchParam(params, "StudyDescription", criteria.StudyDescription)
	addSearchParam(params, "AccessionNumber", criteria.AccessionNumber)
	addSearchParam(params, "ReferringPhysicianName", criteria.ReferringPhysicianName)
	addSearchParam(params, "InstitutionName", criteria.InstitutionName)
	addSearchParam(params, "PatientComments", criteria.PatientComments)
	addSearchParam(params, "StudyStatusID", criteria.StudyStatusID)
	addSearchParam(params, "BodyPartExamined", criteria.BodyPartExamined)
	addSearchParam(params, "ScheduledProcedureStepStatus", criteria.WorkListStatus)
	addSearchParam(params, "ModalitiesInStudy", criteria.Modality)
	addSearchParam(params, "StudyInstanceUID", criteria.StudyInstanceUID)
	addSearchParam(params, criteria.CustomFieldKeyword, criteria.CustomFieldValue)
	if criteria.Limit > 0 {
		params.Set("limit", strconv.Itoa(criteria.Limit))
	}
	for _, field := range studySearchReturnFields {
		params.Add("includefield", field)
	}
	return params, nil
}

// StudySearchReturnFields returns the default QIDO-RS study return fields.
func StudySearchReturnFields() []string {
	return append([]string(nil), studySearchReturnFields...)
}

// StudyMatchesFromDatasets extracts neutral study matches from raw DICOM JSON.
func StudyMatchesFromDatasets(datasets []Dataset) []StudyMatch {
	matches := make([]StudyMatch, 0, len(datasets))
	for _, dataset := range datasets {
		matches = append(matches, StudyMatch{
			PatientName:            dicomjson.ElementString(dataset, "00100010"),
			PatientID:              dicomjson.ElementString(dataset, "00100020"),
			PatientBirthDate:       dicomjson.ElementString(dataset, "00100030"),
			StudyDate:              dicomjson.ElementString(dataset, "00080020"),
			StudyTime:              dicomjson.ElementString(dataset, "00080030"),
			ImageCount:             dicomjson.ElementString(dataset, "00201208"),
			StudyDescription:       dicomjson.ElementString(dataset, "00081030"),
			AccessionNumber:        dicomjson.ElementString(dataset, "00080050"),
			ReferringPhysicianName: dicomjson.ElementString(dataset, "00080090"),
			InstitutionName:        dicomjson.ElementString(dataset, "00080080"),
			PatientComments:        dicomjson.ElementString(dataset, "00104000"),
			StudyStatusID:          dicomjson.ElementString(dataset, "0032000A"),
			BodyPartExamined:       dicomjson.ElementString(dataset, "00180015"),
			WorkListStatus:         dicomjson.ElementString(dataset, "00400020"),
			StudyInstanceUID:       dicomjson.ElementString(dataset, "0020000D"),
			Modalities:             strings.Join(dicomjson.ElementStrings(dataset, "00080061"), "\\"),
		})
	}
	return matches
}

func addSearchParam(params url.Values, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	params.Set(key, value)
}

func studyRangeValue(from, to string) string {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	switch {
	case from == "" && to == "":
		return ""
	case from == to:
		return from
	default:
		return from + "-" + to
	}
}
