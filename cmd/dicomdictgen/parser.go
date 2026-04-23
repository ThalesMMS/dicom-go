package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ThalesMMS/dicom-go/core"
)

type parsedEntry struct {
	Tag     core.Tag
	VRExpr  string
	Keyword string
	Name    string
	VM      string
	Retired bool
}

type parsedEntryCandidate struct {
	entry       parsedEntry
	specificity int
	lineNo      int
}

func parseFile(path string) (entries []parsedEntry, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			cerr = fmt.Errorf("close %s: %w", path, cerr)
			if err == nil {
				err = cerr
				return
			}
			err = errors.Join(err, cerr)
		}
	}()

	entries, err = parseDictionary(bufio.NewScanner(file))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return entries, nil
}

func parseDictionary(scanner *bufio.Scanner) ([]parsedEntry, error) {
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	byTag := make(map[core.Tag]parsedEntryCandidate)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		candidates, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		for _, candidate := range candidates {
			candidate.lineNo = lineNo
			current, exists := byTag[candidate.entry.Tag]
			if !exists || preferCandidate(candidate, current) {
				byTag[candidate.entry.Tag] = candidate
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	entries := make([]parsedEntry, 0, len(byTag))
	for _, candidate := range byTag {
		entries = append(entries, candidate.entry)
	}

	slices.SortFunc(entries, func(a, b parsedEntry) int {
		if cmp := a.Tag.Compare(b.Tag); cmp != 0 {
			return cmp
		}
		if a.Keyword < b.Keyword {
			return -1
		}
		if a.Keyword > b.Keyword {
			return 1
		}
		return 0
	})

	return entries, nil
}

func preferCandidate(candidate, current parsedEntryCandidate) bool {
	if candidate.specificity != current.specificity {
		return candidate.specificity > current.specificity
	}
	if candidate.entry.Retired != current.entry.Retired {
		return !candidate.entry.Retired
	}
	return candidate.lineNo > current.lineNo
}

func parseLine(line string) ([]parsedEntryCandidate, error) {
	parts := strings.Split(line, "\t")
	if len(parts) != 5 {
		return nil, fmt.Errorf("expected 5 tab-separated fields, got %d", len(parts))
	}

	tagField := strings.TrimSpace(parts[0])
	vrField := strings.TrimSpace(parts[1])
	keywordField := strings.TrimSpace(parts[2])
	vmField := strings.TrimSpace(parts[3])
	observationField := strings.TrimSpace(parts[4])

	switch observationField {
	case "PRIVATE", "ILLEGAL", "GENERIC":
		return nil, nil
	}

	if vrField == "na" {
		return nil, nil
	}

	keyword := strings.TrimPrefix(keywordField, "RETIRED_")
	retired := keyword != keywordField || strings.Contains(strings.ToLower(observationField), "retired")

	vrExpr, err := vrExpression(vrField)
	if err != nil {
		return nil, err
	}

	tags, specificity, err := expandTagSpec(tagField)
	if err != nil {
		return nil, err
	}

	entries := make([]parsedEntryCandidate, 0, len(tags))
	for _, tag := range tags {
		entries = append(entries, parsedEntryCandidate{
			entry: parsedEntry{
				Tag:     tag,
				VRExpr:  vrExpr,
				Keyword: keyword,
				Name:    keywordToName(keyword),
				VM:      vmField,
				Retired: retired,
			},
			specificity: specificity,
		})
	}
	return entries, nil
}

func vrExpression(vr string) (string, error) {
	switch vr {
	case "AE":
		return "core.VRAE", nil
	case "AS":
		return "core.VRAS", nil
	case "AT":
		return "core.VRAT", nil
	case "CS":
		return "core.VRCS", nil
	case "DA":
		return "core.VRDA", nil
	case "DS":
		return "core.VRDS", nil
	case "DT":
		return "core.VRDT", nil
	case "FL":
		return "core.VRFL", nil
	case "FD":
		return "core.VRFD", nil
	case "IS":
		return "core.VRIS", nil
	case "LO":
		return "core.VRLO", nil
	case "LT":
		return "core.VRLT", nil
	case "OB":
		return "core.VROB", nil
	case "OD":
		return "core.VROD", nil
	case "OF":
		return "core.VROF", nil
	case "OL":
		return "core.VROL", nil
	case "OW":
		return "core.VROW", nil
	case "OV":
		return "core.VROV", nil
	case "PN":
		return "core.VRPN", nil
	case "SH":
		return "core.VRSH", nil
	case "SL":
		return "core.VRSL", nil
	case "SQ":
		return "core.VRSQ", nil
	case "SS":
		return "core.VRSS", nil
	case "ST":
		return "core.VRST", nil
	case "SV":
		return "core.VRSV", nil
	case "TM":
		return "core.VRTM", nil
	case "UC":
		return "core.VRUC", nil
	case "UI":
		return "core.VRUI", nil
	case "UL":
		return "core.VRUL", nil
	case "UN":
		return "core.VRUN", nil
	case "UR":
		return "core.VRUR", nil
	case "US":
		return "core.VRUS", nil
	case "UT":
		return "core.VRUT", nil
	case "UV":
		return "core.VRUV", nil
	case "lt":
		return "core.VROW", nil
	case "ox":
		return "core.VROW", nil
	case "px":
		return "core.VROW", nil
	case "up":
		return "core.VRUL", nil
	case "xs":
		return "core.VRUS", nil
	default:
		return "", fmt.Errorf("unsupported VR %q", vr)
	}
}

func expandTagSpec(spec string) ([]core.Tag, int, error) {
	if !strings.HasPrefix(spec, "(") || !strings.HasSuffix(spec, ")") {
		return nil, 0, fmt.Errorf("invalid tag %q", spec)
	}

	body := strings.TrimSuffix(strings.TrimPrefix(spec, "("), ")")
	parts := strings.Split(body, ",")
	if len(parts) != 2 {
		return nil, 0, fmt.Errorf("invalid tag %q", spec)
	}

	groups, groupIsRange, err := expandTagComponent(parts[0])
	if err != nil {
		return nil, 0, fmt.Errorf("invalid group component %q: %w", parts[0], err)
	}
	elements, elementIsRange, err := expandTagComponent(parts[1])
	if err != nil {
		return nil, 0, fmt.Errorf("invalid element component %q: %w", parts[1], err)
	}

	tags := make([]core.Tag, 0, len(groups)*len(elements))
	for _, group := range groups {
		for _, element := range elements {
			tags = append(tags, core.NewTag(group, element))
		}
	}

	specificity := 2
	if groupIsRange {
		specificity--
	}
	if elementIsRange {
		specificity--
	}

	return tags, specificity, nil
}

func expandTagComponent(spec string) ([]uint16, bool, error) {
	spec = strings.ToUpper(strings.TrimSpace(spec))

	switch {
	case strings.Contains(spec, "-O-"):
		values, err := expandRangeSpec(spec, "-O-", 2)
		return values, true, err
	case strings.Contains(spec, "-U-"):
		values, err := expandRangeSpec(spec, "-U-", 1)
		return values, true, err
	case strings.Count(spec, "-") == 1:
		values, err := expandRangeSpec(spec, "-", 2)
		return values, true, err
	default:
		value, err := parseHex16(spec)
		if err != nil {
			return nil, false, err
		}
		return []uint16{value}, false, nil
	}
}

func expandRangeSpec(spec, sep string, step uint16) ([]uint16, error) {
	parts := strings.Split(spec, sep)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range %q", spec)
	}

	start, err := parseHex16(parts[0])
	if err != nil {
		return nil, err
	}
	end, err := parseHex16(parts[1])
	if err != nil {
		return nil, err
	}
	if start > end {
		return nil, fmt.Errorf("range start %q is greater than end %q", parts[0], parts[1])
	}

	values := make([]uint16, 0, int((end-start)/step)+1)
	for value := start; value <= end; value += step {
		values = append(values, value)
		if end-value < step {
			break
		}
	}
	return values, nil
}

func parseHex16(s string) (uint16, error) {
	if len(s) != 4 {
		return 0, fmt.Errorf("component %q must have 4 hex digits", s)
	}
	value, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0, err
	}
	return uint16(value), nil
}

func keywordToName(keyword string) string {
	var out strings.Builder
	runes := []rune(keyword)

	for i, r := range runes {
		if i > 0 && shouldInsertSpace(runes, i) {
			out.WriteByte(' ')
		}
		out.WriteRune(r)
	}

	return out.String()
}

func shouldInsertSpace(runes []rune, i int) bool {
	current := runes[i]
	previous := runes[i-1]

	if !unicode.IsUpper(current) {
		return false
	}
	if unicode.IsLower(previous) || unicode.IsDigit(previous) {
		return true
	}
	if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
		return true
	}
	return false
}
