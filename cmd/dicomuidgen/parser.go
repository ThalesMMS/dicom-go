package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	uiddict "github.com/ThalesMMS/dicom-go/dictionary/uid"
)

type parsedEntry struct {
	UID      string
	Name     string
	Keyword  string
	Type     uiddict.Type
	TypeExpr string
	Retired  bool
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

	entries, err = parseUIDTable(bufio.NewScanner(file))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return entries, nil
}

func parseUIDTable(scanner *bufio.Scanner) ([]parsedEntry, error) {
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var entries []parsedEntry
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		entry, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func parseLine(line string) (parsedEntry, error) {
	parts := strings.Split(line, "\t")
	if len(parts) != 5 {
		return parsedEntry{}, fmt.Errorf("expected 5 tab-separated fields, got %d", len(parts))
	}

	uid := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	keyword := strings.TrimSpace(parts[2])
	typeLabel := strings.TrimSpace(parts[3])
	retiredField := strings.TrimSpace(parts[4])

	if uid = uiddict.NormalizeUID(uid); uid == "" {
		return parsedEntry{}, fmt.Errorf("missing UID")
	}
	if name == "" {
		return parsedEntry{}, fmt.Errorf("missing name")
	}
	if keyword == "" {
		return parsedEntry{}, fmt.Errorf("missing keyword")
	}

	typ, err := uiddict.ParseType(typeLabel)
	if err != nil {
		return parsedEntry{}, err
	}

	retired, err := parseBool(retiredField)
	if err != nil {
		return parsedEntry{}, err
	}

	return parsedEntry{
		UID:      uid,
		Name:     name,
		Keyword:  keyword,
		Type:     typ,
		TypeExpr: typeExpr(typ),
		Retired:  retired,
	}, nil
}

func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid retired flag %q", value)
	}
}

func typeExpr(typ uiddict.Type) string {
	switch typ {
	case uiddict.SOPClass:
		return "SOPClass"
	case uiddict.MetaSOPClass:
		return "MetaSOPClass"
	case uiddict.TransferSyntax:
		return "TransferSyntax"
	case uiddict.WellKnownSOPInstance:
		return "WellKnownSOPInstance"
	case uiddict.DICOMUIDAsCodingScheme:
		return "DICOMUIDAsCodingScheme"
	case uiddict.CodingScheme:
		return "CodingScheme"
	case uiddict.ApplicationContextName:
		return "ApplicationContextName"
	case uiddict.ServiceClass:
		return "ServiceClass"
	case uiddict.ApplicationHostingModel:
		return "ApplicationHostingModel"
	case uiddict.MappingResource:
		return "MappingResource"
	case uiddict.LDAPOID:
		return "LDAPOID"
	case uiddict.SynchronizationFrameOfReference:
		return "SynchronizationFrameOfReference"
	default:
		panic(fmt.Sprintf("unknown uiddict.Type %d", typ))
	}
}
