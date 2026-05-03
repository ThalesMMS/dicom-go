package dimse

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
)

// Study Root Query/Retrieve levels supported by this minimal implementation.
const (
	QueryRetrieveLevelStudy  = "STUDY"
	QueryRetrieveLevelSeries = "SERIES"
)

// NewQueryRetrieveLevelElement creates (0008,0052) QueryRetrieveLevel element.
func NewQueryRetrieveLevelElement(level string) (core.Element, error) {
	level = strings.ToUpper(strings.TrimSpace(level))
	if level != QueryRetrieveLevelStudy && level != QueryRetrieveLevelSeries {
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

func buildStudyRootFindKeys(level string, keys map[string]string, returnKeys ...string) ([]core.Element, error) {
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
