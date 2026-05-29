// Package dicomdir reads DICOMDIR references and builds, validates, queries,
// and transactionally writes bounded DICOM file-sets.
package dicomdir

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagOffsetFirstRootDirectoryRecord = core.NewTag(0x0004, 0x1200)
	tagDirectoryRecordSequence        = core.NewTag(0x0004, 0x1220)
	tagOffsetNextDirectoryRecord      = core.NewTag(0x0004, 0x1400)
	tagRecordInUseFlag                = core.NewTag(0x0004, 0x1410)
	tagOffsetLowerLevelRecord         = core.NewTag(0x0004, 0x1420)
	tagDirectoryRecordType            = core.NewTag(0x0004, 0x1430)
	tagReferencedFileID               = core.NewTag(0x0004, 0x1500)
)

// Reference is one DICOMDIR directory record reference.
type Reference struct {
	FileID []string
	Type   string
}

// References returns usable active directory-record file references in
// DICOMDIR hierarchy order. Objects constructed without encoded item offsets
// retain the historical physical-order behavior as a compatibility fallback.
func References(obj *object.Object) []Reference {
	if obj == nil {
		return nil
	}
	records, ok := obj.GetSequence(tagDirectoryRecordSequence)
	if !ok {
		return nil
	}
	if _, hasRootOffset := obj.Get(tagOffsetFirstRootDirectoryRecord); !hasRootOffset {
		return flatReferences(records)
	}
	rootOffset, validRootOffset := uint32Value(obj, tagOffsetFirstRootDirectoryRecord)
	if !validRootOffset {
		return nil
	}
	if rootOffset == 0 {
		return nil
	}

	recordsByOffset := make(map[uint32]*object.Object, len(records))
	ambiguousOffsets := make(map[uint32]bool)
	for _, record := range records {
		if record == nil {
			continue
		}
		offset, ok := record.ItemOffset()
		if !ok || offset < 0 || offset > int64(math.MaxUint32) {
			continue
		}
		encodedOffset := uint32(offset)
		if _, exists := recordsByOffset[encodedOffset]; exists {
			ambiguousOffsets[encodedOffset] = true
			delete(recordsByOffset, encodedOffset)
			continue
		}
		if !ambiguousOffsets[encodedOffset] {
			recordsByOffset[encodedOffset] = record
		}
	}
	if len(recordsByOffset) == 0 {
		return flatReferences(records)
	}

	refs := make([]Reference, 0, len(records))
	visited := make(map[uint32]bool, len(records))
	pending := []uint32{rootOffset}
	for len(pending) > 0 {
		offset := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if offset == 0 || visited[offset] {
			continue
		}
		visited[offset] = true
		record, ok := recordsByOffset[offset]
		if !ok {
			continue
		}

		if next, ok := uint32Value(record, tagOffsetNextDirectoryRecord); ok && next != 0 {
			pending = append(pending, next)
		}
		if lower, ok := uint32Value(record, tagOffsetLowerLevelRecord); ok && lower != 0 {
			pending = append(pending, lower)
		}
		if !recordInUse(record) {
			continue
		}
		if ref, ok := referenceFromRecord(record); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func flatReferences(records []*object.Object) []Reference {
	refs := make([]Reference, 0, len(records))
	for _, record := range records {
		if record == nil || !recordInUse(record) {
			continue
		}
		if ref, ok := referenceFromRecord(record); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func referenceFromRecord(record *object.Object) (Reference, bool) {
	fileID, ok := record.GetStrings(tagReferencedFileID)
	if !ok || !hasUsablePart(fileID) {
		return Reference{}, false
	}
	recordType, _ := record.GetString(tagDirectoryRecordType)
	return Reference{FileID: fileID, Type: recordType}, true
}

func recordInUse(record *object.Object) bool {
	elem, ok := record.Get(tagRecordInUseFlag)
	if !ok {
		return true
	}
	raw, ok := elem.RawBytes()
	return ok && elem.VR() == core.VRUS && len(raw) == 2 && record.ValueByteOrder().Uint16(raw) == 0xFFFF
}

func uint32Value(obj *object.Object, tag core.Tag) (uint32, bool) {
	elem, ok := obj.Get(tag)
	if !ok {
		return 0, false
	}
	raw, ok := elem.RawBytes()
	if !ok || elem.VR() != core.VRUL || len(raw) != 4 {
		return 0, false
	}
	return obj.ValueByteOrder().Uint32(raw), true
}

// Resolve turns a DICOMDIR reference into a path under root. Backslash-separated
// components inside any FileID value are split according to PS3.10 media rules.
func Resolve(root string, ref Reference) string {
	parts := make([]string, 0, len(ref.FileID))
	for _, component := range ref.FileID {
		if !safeFileIDComponent(component) {
			return ""
		}
		for _, part := range strings.Split(component, `\`) {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	joined := filepath.Join(append([]string{root}, parts...)...)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	pathAbs, err := filepath.Abs(joined)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if unsafe, err := hasRedirectComponent(rootAbs, rel); err != nil || unsafe {
		return ""
	}
	return filepath.Clean(joined)
}

func hasRedirectComponent(rootAbs, relative string) (bool, error) {
	current := rootAbs
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// Go reports Windows junctions as ModeIrregular rather than
		// ModeSymlink. Both can redirect path resolution outside root.
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return true, nil
		}
	}
	return false, nil
}

func safeFileIDComponent(component string) bool {
	component = strings.TrimSpace(component)
	if component == "" {
		return true
	}

	parts := strings.Split(component, `\`)
	hasPart := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hasPart = true
		if part == "." || part == ".." ||
			isWindowsReservedFileName(part) ||
			strings.ContainsAny(part, "/:\x00") ||
			filepath.IsAbs(part) ||
			filepath.VolumeName(part) != "" {
			return false
		}
	}
	if !hasPart {
		return true
	}

	// File IDs use backslashes only as separators. A leading separator is a
	// rooted, UNC, or device path on Windows, even when validation runs on a
	// different operating system.
	return !strings.HasPrefix(component, `\`) &&
		!strings.Contains(component, "/") &&
		!filepath.IsAbs(component) &&
		filepath.VolumeName(component) == ""
}

func isWindowsReservedFileName(part string) bool {
	name := strings.TrimRight(part, " .")
	if extension := strings.IndexByte(name, '.'); extension >= 0 {
		name = strings.TrimRight(name[:extension], " .")
	}
	name = strings.ToUpper(name)
	switch name {
	case "NUL", "CON", "PRN", "AUX":
		return true
	}
	return len(name) == 4 &&
		(name[:3] == "COM" || name[:3] == "LPT") &&
		name[3] >= '1' && name[3] <= '9'
}

// ReferencedPaths opens dicomdirPath and returns unique absolute referenced
// paths, resolved relative to the DICOMDIR file's directory. It only enumerates
// names; callers opening untrusted media contents should use OpenReferencedFile
// so path components cannot be replaced between validation and open.
func ReferencedPaths(dicomdirPath string) ([]string, error) {
	dicomdirPath, err := filepath.Abs(dicomdirPath)
	if err != nil {
		return nil, err
	}
	file, err := object.OpenFile(dicomdirPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	root := filepath.Dir(dicomdirPath)
	refs := References(file.Dataset)
	paths := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		path := Resolve(root, ref)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths, nil
}

// OpenReferencedFile opens ref relative to root without following symbolic
// links or other path-redirection components. The returned path is absolute and
// is suitable for display or diagnostics; callers that need to use the file
// contents later should retain the parsed data instead of reopening that path.
func OpenReferencedFile(root string, ref Reference) (*os.File, string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("dicomdir: invalid media root")
	}
	resolved := Resolve(rootAbs, ref)
	if resolved == "" {
		return nil, "", errors.New("dicomdir: invalid referenced file ID")
	}
	relative, err := filepath.Rel(rootAbs, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		if err != nil {
			return nil, "", err
		}
		return nil, "", errors.New("dicomdir: referenced file is outside the media root")
	}
	file, err := openRelativeFile(rootAbs, relative)
	if err != nil {
		return nil, "", fmt.Errorf("dicomdir: open referenced file %q: %w", relative, err)
	}
	return file, resolved, nil
}

func hasUsablePart(values []string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, `\`) {
			if strings.TrimSpace(part) != "" {
				return true
			}
		}
	}
	return false
}
