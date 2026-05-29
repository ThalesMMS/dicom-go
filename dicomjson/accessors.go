package dicomjson

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Dataset is a raw DICOM JSON dataset keyed by uppercase tag hex.
type Dataset map[string]Element

// ElementString returns the first string value for the given DICOM tag.
func ElementString(dataset Dataset, tag string) string {
	values := ElementStrings(dataset, tag)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// ElementStrings extracts non-empty string values from a DICOM JSON element.
func ElementStrings(dataset Dataset, tag string) []string {
	elem, ok := dataset[strings.ToUpper(strings.TrimSpace(tag))]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(elem.Value))
	for _, value := range elem.Value {
		if s := valueString(value); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func valueString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return floatString(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		return strings.TrimSpace(v.String())
	case map[string]any:
		for _, key := range []string{"Alphabetic", "Ideographic", "Phonetic"} {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func floatString(v float64) string {
	if math.Trunc(v) == v {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
