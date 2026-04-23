package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	uiddict "github.com/ThalesMMS/dicom-go/dictionary/uid"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const sectionSeparator = "----------------------------------------------------------"

type Formatter struct {
	maxValueLen int
	output      io.Writer
}

func NewFormatter(output io.Writer, maxValueLen int) *Formatter {
	if maxValueLen <= 0 {
		maxValueLen = defaultMaxValueLen
	}
	return &Formatter{
		maxValueLen: maxValueLen,
		output:      output,
	}
}

func (f *Formatter) DumpFile(file *object.File) error {
	if file == nil {
		return fmt.Errorf("dicom: nil file")
	}

	if _, err := fmt.Fprintf(f.output, "Transfer Syntax: %s (%s)\n\n# File Meta\n", file.TransferSyntax.Name, file.TransferSyntax.UID); err != nil {
		return err
	}
	if err := f.dump(file.Meta, transfer.ExplicitVRLittleEndian, 0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f.output, "\n%s\n# Dataset\n", sectionSeparator); err != nil {
		return err
	}
	return f.dump(file.Dataset, file.TransferSyntax, 0)
}

func (f *Formatter) dump(obj *object.Object, syntax transfer.Syntax, depth int) error {
	if obj == nil {
		return nil
	}
	for _, elem := range obj.Elements() {
		if err := f.dumpElement(elem, depth, syntax); err != nil {
			return err
		}
	}
	return nil
}

func (f *Formatter) dumpElement(elem core.Element, depth int, syntax transfer.Syntax) error {
	if _, err := fmt.Fprintln(f.output, f.formatLine(elem.Tag(), elem.VR().String(), elem.Length().String(), keywordForTag(elem.Tag()), f.formatValue(elem, syntax), depth)); err != nil {
		return err
	}

	seq, ok := elem.Value.(core.SequenceValue)
	if !ok {
		return nil
	}

	for _, item := range seq.Items {
		if _, err := fmt.Fprintln(f.output, f.formatLine(core.TagItem, "", core.UndefinedLength.String(), "Item", "", depth+1)); err != nil {
			return err
		}
		if err := f.dump(object.FromDataSet(item, std.Dictionary), syntax, depth+2); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(f.output, f.formatLine(core.TagItemDelimitationItem, "", "0", "ItemDelimitationItem", "", depth+1)); err != nil {
			return err
		}
	}
	return nil
}

func (f *Formatter) formatLine(tag core.Tag, vr, length, keyword, value string, depth int) string {
	indent := strings.Repeat("  ", depth)
	if vr == "" {
		vr = "--"
	}
	line := fmt.Sprintf("%s%-11s %-2s %9s %-32s", indent, tag.String(), vr, length, keyword)
	if value != "" {
		line += " " + value
	}
	return strings.TrimRight(line, " ")
}

func (f *Formatter) formatValue(elem core.Element, syntax transfer.Syntax) string {
	switch value := elem.Value.(type) {
	case nil:
		return ""
	case core.SequenceValue:
		return fmt.Sprintf("(%d item(s))", len(value.Items))
	case core.FragmentSequence:
		if elem.Tag() == core.TagPixelData {
			return fmt.Sprintf("(%d bytes)", fragmentSequenceBytes(value))
		}
		return fmt.Sprintf("(%d fragment(s))", len(value.Fragments))
	}

	if elem.VR() == core.VRUI {
		return truncateString(formatUIDValues(elem.StringValues()), f.maxValueLen)
	}
	if elem.VR().IsStringLike() {
		return truncateString(escapeStringValues(elem.StringValues()), f.maxValueLen)
	}

	raw, ok := elem.RawBytes()
	if !ok {
		return ""
	}
	if elem.Tag() == core.TagPixelData {
		return fmt.Sprintf("(%d bytes)", len(raw))
	}
	if decoded, ok := formatNumericValue(elem.VR(), raw, byteOrderForSyntax(syntax), f.maxValueLen); ok {
		return decoded
	}
	return formatBinaryValue(raw, f.maxValueLen)
}

func truncateString(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return strings.Repeat(".", max)
	}
	return string(runes[:max-3]) + "..."
}

func escapeStringValues(values []string) string {
	if len(values) == 0 {
		return ""
	}

	var builder strings.Builder
	for i, value := range values {
		if i > 0 {
			builder.WriteString(`\`)
		}
		builder.WriteString(escapeStringValue(value))
	}
	return builder.String()
}

func escapeStringValue(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				_, _ = fmt.Fprintf(&builder, `\x%02X`, r)
				continue
			}
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func formatBinaryValue(data []byte, max int) string {
	if len(data) == 0 || max <= 0 {
		return ""
	}

	var builder strings.Builder
	currentLen := 0
	for i, b := range data {
		segment := fmt.Sprintf("%02X", b)
		if i > 0 {
			segment = " " + segment
		}
		result := appendWithLimit(&builder, &currentLen, segment, max)
		if result.stop {
			appendTruncationMarker(&builder, max)
			break
		}
		if currentLen == max && i < len(data)-1 {
			appendTruncationMarker(&builder, max)
			break
		}
	}
	return builder.String()
}

func formatNumericValue(vr core.VR, raw []byte, order binary.ByteOrder, max int) (string, bool) {
	switch vr {
	case core.VRUS:
		return formatFixedWidthValues(raw, 2, func(chunk []byte) string {
			return strconv.FormatUint(uint64(order.Uint16(chunk)), 10)
		}, max)
	case core.VRSS:
		return formatFixedWidthValues(raw, 2, func(chunk []byte) string {
			return strconv.FormatInt(int64(int16(order.Uint16(chunk))), 10)
		}, max)
	case core.VRUL:
		return formatFixedWidthValues(raw, 4, func(chunk []byte) string {
			return strconv.FormatUint(uint64(order.Uint32(chunk)), 10)
		}, max)
	case core.VRSL:
		return formatFixedWidthValues(raw, 4, func(chunk []byte) string {
			return strconv.FormatInt(int64(int32(order.Uint32(chunk))), 10)
		}, max)
	case core.VRUV:
		return formatFixedWidthValues(raw, 8, func(chunk []byte) string {
			return strconv.FormatUint(order.Uint64(chunk), 10)
		}, max)
	case core.VRSV:
		return formatFixedWidthValues(raw, 8, func(chunk []byte) string {
			return strconv.FormatInt(int64(order.Uint64(chunk)), 10)
		}, max)
	case core.VRFL:
		return formatFixedWidthValues(raw, 4, func(chunk []byte) string {
			return strconv.FormatFloat(float64(math.Float32frombits(order.Uint32(chunk))), 'g', -1, 32)
		}, max)
	case core.VRFD:
		return formatFixedWidthValues(raw, 8, func(chunk []byte) string {
			return strconv.FormatFloat(math.Float64frombits(order.Uint64(chunk)), 'g', -1, 64)
		}, max)
	case core.VRAT:
		return formatFixedWidthValues(raw, 4, func(chunk []byte) string {
			return core.NewTag(order.Uint16(chunk[:2]), order.Uint16(chunk[2:])).String()
		}, max)
	default:
		return "", false
	}
}

func formatFixedWidthValues(raw []byte, width int, decode func([]byte) string, max int) (string, bool) {
	if len(raw) == 0 || len(raw)%width != 0 || max <= 0 {
		return "", false
	}

	var builder strings.Builder
	currentLen := 0
	for i := 0; i < len(raw); i += width {
		segment := decode(raw[i : i+width])
		if i > 0 {
			segment = `\` + segment
		}
		result := appendWithLimit(&builder, &currentLen, segment, max)
		if result.stop {
			if result.truncated || i+width < len(raw) {
				appendTruncationMarker(&builder, max)
			}
			break
		}
		if currentLen == max && i+width < len(raw) {
			appendTruncationMarker(&builder, max)
			break
		}
	}
	return builder.String(), true
}

func keywordForTag(tag core.Tag) string {
	if entry, ok := std.Dictionary.ByTag(tag); ok {
		return entry.Keyword
	}
	return ""
}

func byteOrderForSyntax(syntax transfer.Syntax) binary.ByteOrder {
	if syntax.ByteOrder != nil {
		return syntax.ByteOrder
	}
	return binary.LittleEndian
}

func fragmentSequenceBytes(value core.FragmentSequence) int {
	total := len(value.OffsetTable)
	for _, fragment := range value.Fragments {
		total += len(fragment)
	}
	return total
}

func formatUIDValues(values []string) string {
	if len(values) == 0 {
		return ""
	}

	var builder strings.Builder
	for i, value := range values {
		if i > 0 {
			builder.WriteString(`\`)
		}
		builder.WriteString(formatUIDValue(value))
	}
	return builder.String()
}

func formatUIDValue(value string) string {
	escaped := escapeStringValue(value)
	if name := nameForUID(value); name != "" {
		return escaped + ` = "` + escapeStringValue(name) + `"`
	}
	return escaped
}

func nameForUID(uid string) string {
	entry, ok := uiddict.Dictionary.ByUID(uid)
	if !ok {
		return ""
	}
	return entry.Name
}

type appendResult struct {
	stop      bool
	truncated bool
}

func appendWithLimit(builder *strings.Builder, currentLen *int, segment string, max int) appendResult {
	if *currentLen >= max {
		return appendResult{stop: true}
	}
	segmentLen := utf8.RuneCountInString(segment)
	if *currentLen+segmentLen <= max {
		builder.WriteString(segment)
		*currentLen += segmentLen
		return appendResult{}
	}
	remaining := max - *currentLen
	if remaining > 0 {
		builder.WriteString(truncateString(segment, remaining))
		*currentLen = max
	}
	return appendResult{stop: true, truncated: true}
}

func appendTruncationMarker(builder *strings.Builder, max int) {
	if max <= 0 {
		return
	}
	runes := []rune(builder.String())
	if len(runes) > max {
		runes = runes[:max]
	}
	switch {
	case max <= 3:
		builder.Reset()
		builder.WriteString(strings.Repeat(".", max))
	default:
		keep := max - 3
		if len(runes) > keep {
			runes = runes[:keep]
		}
		builder.Reset()
		builder.WriteString(string(runes))
		builder.WriteString("...")
	}
}
