package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
)

// Query/Retrieve levels supported by helper metadata in this package.
const (
	QueryRetrieveLevelPatient = "PATIENT"
	QueryRetrieveLevelStudy   = "STUDY"
	QueryRetrieveLevelSeries  = "SERIES"
	QueryRetrieveLevelImage   = "IMAGE"
)

// NewQueryRetrieveLevelElement creates (0008,0052) QueryRetrieveLevel element.
func NewQueryRetrieveLevelElement(level string) (core.Element, error) {
	level = normalizeQueryRetrieveLevel(level)
	switch level {
	case QueryRetrieveLevelPatient, QueryRetrieveLevelStudy, QueryRetrieveLevelSeries, QueryRetrieveLevelImage:
	default:
		return core.Element{}, fmt.Errorf("dicom dimse: unsupported QueryRetrieveLevel %q", level)
	}
	return core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS},
		Value:  core.StringValue{level},
	}, nil
}

// BuildStudyRootStudyFindKeys builds an Identifier dataset for a Study Root C-FIND at STUDY level.
//
// keys maps DICOM keyword -> value. Empty values are allowed and are treated as return keys.
// Keywords are looked up in the standard dictionary; unknown keywords return an error.
func BuildStudyRootStudyFindKeys(keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	return buildStudyRootFindKeys(QueryRetrieveLevelStudy, keys, returnKeys...)
}

// BuildStudyRootSeriesFindKeys builds an Identifier dataset for a Study Root C-FIND at SERIES level.
//
// keys maps DICOM keyword -> value. Empty values are allowed and are treated as return keys.
// Keywords are looked up in the standard dictionary; unknown keywords return an error.
func BuildStudyRootSeriesFindKeys(keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	return buildStudyRootFindKeys(QueryRetrieveLevelSeries, keys, returnKeys...)
}

// BuildStudyRootImageFindKeys builds an Identifier dataset for a Study Root C-FIND at IMAGE level.
//
// keys maps DICOM keyword -> value. Empty values are allowed and are treated as return keys.
// Keywords are looked up in the standard dictionary; unknown keywords return an error.
func BuildStudyRootImageFindKeys(keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	return buildStudyRootFindKeys(QueryRetrieveLevelImage, keys, returnKeys...)
}

// BuildPatientRootPatientFindKeys builds an Identifier dataset for a Patient
// Root C-FIND at PATIENT level.
//
// keys maps DICOM keyword -> value. Empty values are allowed and are treated as
// return keys. Keywords are looked up in the standard dictionary; unknown
// keywords return an error.
func BuildPatientRootPatientFindKeys(keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	return buildPatientRootFindKeys(QueryRetrieveLevelPatient, keys, returnKeys...)
}

// BuildPatientRootStudyFindKeys builds an Identifier dataset for a Patient Root
// C-FIND at STUDY level.
//
// keys maps DICOM keyword -> value. Empty values are allowed and are treated as
// return keys. Keywords are looked up in the standard dictionary; unknown
// keywords return an error.
func BuildPatientRootStudyFindKeys(keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	return buildPatientRootFindKeys(QueryRetrieveLevelStudy, keys, returnKeys...)
}

// BuildPatientRootSeriesFindKeys builds an Identifier dataset for a Patient Root
// C-FIND at SERIES level.
//
// keys maps DICOM keyword -> value. Empty values are allowed and are treated as
// return keys. Keywords are looked up in the standard dictionary; unknown
// keywords return an error.
func BuildPatientRootSeriesFindKeys(keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	return buildPatientRootFindKeys(QueryRetrieveLevelSeries, keys, returnKeys...)
}

// BuildPatientRootImageFindKeys builds an Identifier dataset for a Patient Root
// C-FIND at IMAGE level.
//
// keys maps DICOM keyword -> value. Empty values are allowed and are treated as
// return keys. Keywords are looked up in the standard dictionary; unknown
// keywords return an error.
func BuildPatientRootImageFindKeys(keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	return buildPatientRootFindKeys(QueryRetrieveLevelImage, keys, returnKeys...)
}

func buildStudyRootFindKeys(level string, keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	if _, err := QueryRetrieveRequiredKeys(QueryRetrieveModelStudyRoot, level); err != nil {
		return nil, err
	}
	return buildFindKeys(level, keys, returnKeys...)
}

func buildPatientRootFindKeys(level string, keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	if _, err := QueryRetrieveRequiredKeys(QueryRetrieveModelPatientRoot, level); err != nil {
		return nil, err
	}
	return buildFindKeys(level, keys, returnKeys...)
}

func buildFindKeys(level string, keys map[string]string, returnKeys ...string) ([]core.Element, error) {
	base, err := NewQueryRetrieveLevelElement(level)
	if err != nil {
		return nil, err
	}
	elems := []core.Element{base}
	k, err := buildKeywordElements(keys)
	if err != nil {
		return nil, err
	}
	elems = append(elems, k...)
	ret, err := buildReturnKeyElements(returnKeys)
	if err != nil {
		return nil, err
	}
	elems = append(elems, ret...)
	return elems, nil
}

func buildReturnKeyElements(returnKeys []string) ([]core.Element, error) {
	if len(returnKeys) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(returnKeys))
	for _, k := range returnKeys {
		m[k] = ""
	}
	return buildKeywordElements(m)
}

func buildKeywordElements(keys map[string]string) ([]core.Element, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	elems := make([]core.Element, 0, len(keys))
	for keyword, value := range keys {
		entry, ok := std.Dictionary.ByKeyword(keyword)
		if !ok {
			return nil, fmt.Errorf("dicom dimse: unknown keyword %q", keyword)
		}
		vr := entry.VR
		// Minimal support: treat all as string-like and encode using core.StringValue.
		// This is appropriate for common Q/R keys like UI/CS/DA/LO/SH.
		elems = append(elems, core.Element{
			Header: core.ElementHeader{Tag: entry.Tag, VR: vr},
			Value:  core.StringValue{value},
		})
	}
	return elems, nil
}
