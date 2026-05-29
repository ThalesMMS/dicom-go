package object

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

const DefaultSummaryMaxValueLength = 160

// SummaryOptions configures element inspection rows produced from an Object.
type SummaryOptions struct {
	// Dictionary resolves keyword and name metadata. When nil, the Object's
	// dictionary is used.
	Dictionary dictionary.DataDictionary
	// Source is copied onto every row so callers can distinguish file-meta and
	// data-set summaries after concatenating them.
	Source string
	// MaxValueLength limits preview values before appending "...". A zero value
	// uses DefaultSummaryMaxValueLength. Negative values disable truncation.
	MaxValueLength int
	// SkipPrivate omits private tags from the output.
	SkipPrivate bool
	// SkipPixelData omits Pixel Data from the output.
	SkipPixelData bool
}

// ElementSummary is a neutral display-oriented description of one DICOM
// element.
type ElementSummary struct {
	Source  string
	Tag     core.Tag
	VR      core.VR
	Keyword string
	Name    string
	Length  core.Length
	Value   string
	Private bool
}

func (s ElementSummary) TagString() string {
	return s.Tag.String()
}

func (s ElementSummary) VRString() string {
	return s.VR.String()
}

func (s ElementSummary) LengthString() string {
	return s.Length.String()
}

// SummarizeElements returns sorted inspection rows for obj. Nil objects return
// nil.
func SummarizeElements(obj *Object, opts SummaryOptions) []ElementSummary {
	return obj.SummarizeElements(opts)
}

// SummarizeElements returns sorted inspection rows for o. Nil receivers return
// nil.
func (o *Object) SummarizeElements(opts SummaryOptions) []ElementSummary {
	if o == nil {
		return nil
	}
	dict := opts.Dictionary
	if dict == nil {
		dict = o.dict
	}
	maxValueLength := summaryMaxValueLength(opts.MaxValueLength)

	elements := o.SortedElements()
	rows := make([]ElementSummary, 0, len(elements))
	for _, elem := range elements {
		tag := elem.Tag()
		if opts.SkipPrivate && tag.IsPrivate() {
			continue
		}
		if opts.SkipPixelData && tag == core.TagPixelData {
			continue
		}

		row := ElementSummary{
			Source:  opts.Source,
			Tag:     tag,
			VR:      elem.VR(),
			Length:  elem.Length(),
			Value:   truncateSummaryValue(o.summaryElementValue(elem), maxValueLength),
			Private: tag.IsPrivate(),
		}
		if dict != nil {
			if entry, ok := dict.ByTag(tag); ok {
				row.Keyword = entry.Keyword
				row.Name = entry.Name
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func summaryMaxValueLength(max int) int {
	if max == 0 {
		return DefaultSummaryMaxValueLength
	}
	return max
}

func (o *Object) summaryElementValue(elem core.Element) string {
	if elem.Value == nil {
		if elem.Tag() == core.TagPixelData {
			return "<pixel data skipped>"
		}
		return "<value skipped>"
	}

	switch value := elem.Value.(type) {
	case core.SequenceValue:
		return fmt.Sprintf("%d item(s)", len(value.Items))
	case core.FragmentSequence:
		return fmt.Sprintf("%d fragment(s)", len(value.Fragments))
	case core.BulkDataValue:
		if value.URI != "" {
			return value.URI
		}
		return "<bulk data>"
	}

	if elem.VR().IsStringLike() {
		values := o.summaryStringValues(elem)
		if len(values) == 0 {
			return ""
		}
		return strings.Join(values, `\`)
	}

	if raw, ok := elem.RawBytes(); ok {
		return fmt.Sprintf("<%d bytes>", len(raw))
	}
	return "<value>"
}

func (o *Object) summaryStringValues(elem core.Element) []string {
	if o != nil {
		if values, err := o.LookupStrings(elem.Tag()); err == nil {
			return values
		}
	}
	return elem.StringValues()
}

func truncateSummaryValue(value string, max int) string {
	if max < 0 {
		return value
	}
	if max == 0 {
		if value == "" {
			return ""
		}
		return "..."
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}
