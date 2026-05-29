package dimse

import (
	"fmt"
	"strings"
)

// QueryRetrieveModel names a Query/Retrieve information model supported by
// helper metadata in this package.
type QueryRetrieveModel string

const (
	QueryRetrieveModelStudyRoot   QueryRetrieveModel = "STUDY_ROOT"
	QueryRetrieveModelPatientRoot QueryRetrieveModel = "PATIENT_ROOT"
)

type queryRetrieveModelDefinition struct {
	levels       []string
	requiredKeys map[string][]string
	optionalKeys map[string][]string
}

var queryRetrieveModels = map[QueryRetrieveModel]queryRetrieveModelDefinition{
	QueryRetrieveModelStudyRoot: {
		levels: []string{QueryRetrieveLevelStudy, QueryRetrieveLevelSeries, QueryRetrieveLevelImage},
		requiredKeys: map[string][]string{
			QueryRetrieveLevelStudy:  {"StudyInstanceUID"},
			QueryRetrieveLevelSeries: {"StudyInstanceUID", "SeriesInstanceUID"},
			QueryRetrieveLevelImage:  {"StudyInstanceUID", "SeriesInstanceUID", "SOPInstanceUID"},
		},
		optionalKeys: map[string][]string{
			QueryRetrieveLevelStudy:  {"PatientID", "PatientName", "StudyDate", "AccessionNumber", "StudyDescription"},
			QueryRetrieveLevelSeries: {"PatientID", "PatientName", "StudyDate", "Modality", "SeriesNumber", "SeriesDescription"},
			QueryRetrieveLevelImage:  {"PatientID", "PatientName", "StudyDate", "Modality", "SOPClassUID", "InstanceNumber"},
		},
	},
	QueryRetrieveModelPatientRoot: {
		levels: []string{QueryRetrieveLevelPatient, QueryRetrieveLevelStudy, QueryRetrieveLevelSeries, QueryRetrieveLevelImage},
		requiredKeys: map[string][]string{
			QueryRetrieveLevelPatient: {"PatientID"},
			QueryRetrieveLevelStudy:   {"PatientID", "StudyInstanceUID"},
			QueryRetrieveLevelSeries:  {"PatientID", "StudyInstanceUID", "SeriesInstanceUID"},
			QueryRetrieveLevelImage:   {"PatientID", "StudyInstanceUID", "SeriesInstanceUID", "SOPInstanceUID"},
		},
		optionalKeys: map[string][]string{
			QueryRetrieveLevelPatient: {"PatientName", "PatientBirthDate", "PatientSex"},
			QueryRetrieveLevelStudy:   {"PatientName", "StudyDate", "StudyTime", "AccessionNumber", "StudyID", "StudyDescription"},
			QueryRetrieveLevelSeries:  {"PatientName", "StudyDate", "Modality", "SeriesNumber", "SeriesDescription"},
			QueryRetrieveLevelImage:   {"PatientName", "StudyDate", "Modality", "SOPClassUID", "InstanceNumber"},
		},
	},
}

// QueryRetrieveLevels returns the supported levels for the model.
func QueryRetrieveLevels(model QueryRetrieveModel) ([]string, error) {
	def, err := queryRetrieveModel(model)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), def.levels...), nil
}

// QueryRetrieveRequiredKeys returns the identifying key path for the model and
// level used by the convenience identifier builders.
func QueryRetrieveRequiredKeys(model QueryRetrieveModel, level string) ([]string, error) {
	def, err := queryRetrieveModel(model)
	if err != nil {
		return nil, err
	}
	level = normalizeQueryRetrieveLevel(level)
	keys, ok := def.requiredKeys[level]
	if !ok {
		return nil, fmt.Errorf("dicom dimse: model %s does not support QueryRetrieveLevel %q", model, level)
	}
	return append([]string(nil), keys...), nil
}

// QueryRetrieveOptionalKeys returns common optional matching/return keys for
// the model and level. The list is intentionally not exhaustive.
func QueryRetrieveOptionalKeys(model QueryRetrieveModel, level string) ([]string, error) {
	def, err := queryRetrieveModel(model)
	if err != nil {
		return nil, err
	}
	level = normalizeQueryRetrieveLevel(level)
	keys, ok := def.optionalKeys[level]
	if !ok {
		return nil, fmt.Errorf("dicom dimse: model %s does not support QueryRetrieveLevel %q", model, level)
	}
	return append([]string(nil), keys...), nil
}

func queryRetrieveModel(model QueryRetrieveModel) (queryRetrieveModelDefinition, error) {
	model = QueryRetrieveModel(strings.ToUpper(strings.TrimSpace(string(model))))
	def, ok := queryRetrieveModels[model]
	if !ok {
		return queryRetrieveModelDefinition{}, fmt.Errorf("dicom dimse: unsupported Query/Retrieve model %q", model)
	}
	return def, nil
}

func normalizeQueryRetrieveLevel(level string) string {
	return strings.ToUpper(strings.TrimSpace(level))
}
