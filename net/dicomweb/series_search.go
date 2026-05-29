package dicomweb

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/dicomjson"
)

// SeriesSearchCriteria describes neutral QIDO-RS series search inputs.
type SeriesSearchCriteria struct {
	SeriesInstanceUID string
	Modality          string
	SeriesNumber      string
	SeriesDescription string

	SeriesDateFrom string
	SeriesDateTo   string
	SeriesTimeFrom string
	SeriesTimeTo   string

	CustomFieldKeyword string
	CustomFieldValue   string
	Limit              int
}

// SeriesMatch is a neutral QIDO-RS series result extracted from DICOM JSON.
type SeriesMatch struct {
	StudyInstanceUID  string
	SeriesInstanceUID string
	Modality          string
	SeriesNumber      string
	SeriesDescription string
	SeriesDate        string
	SeriesTime        string
	InstanceCount     string
}

var seriesSearchReturnFields = []string{
	"StudyInstanceUID",
	"SeriesInstanceUID",
	"Modality",
	"SeriesNumber",
	"SeriesDescription",
	"SeriesDate",
	"SeriesTime",
	"NumberOfSeriesRelatedInstances",
}

// SeriesSearchParams converts neutral series criteria into QIDO-RS query params.
func SeriesSearchParams(criteria SeriesSearchCriteria) (url.Values, error) {
	if strings.TrimSpace(criteria.CustomFieldValue) != "" && strings.TrimSpace(criteria.CustomFieldKeyword) == "" {
		return nil, fmt.Errorf("dicomweb: custom DICOM field keyword is required")
	}
	params := url.Values{}
	addSearchParam(params, "SeriesInstanceUID", criteria.SeriesInstanceUID)
	addSearchParam(params, "Modality", criteria.Modality)
	addSearchParam(params, "SeriesNumber", criteria.SeriesNumber)
	addSearchParam(params, "SeriesDescription", criteria.SeriesDescription)
	addSearchParam(params, "SeriesDate", studyRangeValue(criteria.SeriesDateFrom, criteria.SeriesDateTo))
	addSearchParam(params, "SeriesTime", studyRangeValue(criteria.SeriesTimeFrom, criteria.SeriesTimeTo))
	addSearchParam(params, criteria.CustomFieldKeyword, criteria.CustomFieldValue)
	if criteria.Limit > 0 {
		params.Set("limit", strconv.Itoa(criteria.Limit))
	}
	for _, field := range seriesSearchReturnFields {
		params.Add("includefield", field)
	}
	return params, nil
}

// SeriesSearchReturnFields returns the default QIDO-RS series return fields.
func SeriesSearchReturnFields() []string {
	return append([]string(nil), seriesSearchReturnFields...)
}

// SeriesMatchesFromDatasets extracts neutral series matches from raw DICOM JSON.
func SeriesMatchesFromDatasets(datasets []Dataset) []SeriesMatch {
	matches := make([]SeriesMatch, 0, len(datasets))
	for _, dataset := range datasets {
		matches = append(matches, SeriesMatch{
			StudyInstanceUID:  dicomjson.ElementString(dataset, "0020000D"),
			SeriesInstanceUID: dicomjson.ElementString(dataset, "0020000E"),
			Modality:          dicomjson.ElementString(dataset, "00080060"),
			SeriesNumber:      dicomjson.ElementString(dataset, "00200011"),
			SeriesDescription: dicomjson.ElementString(dataset, "0008103E"),
			SeriesDate:        dicomjson.ElementString(dataset, "00080021"),
			SeriesTime:        dicomjson.ElementString(dataset, "00080031"),
			InstanceCount:     dicomjson.ElementString(dataset, "00201209"),
		})
	}
	return matches
}
