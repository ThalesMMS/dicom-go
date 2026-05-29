package dicomdir

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const mediaStorageDirectoryStorageUID = "1.2.840.10008.1.3.10"

var (
	tagFileSetID                                 = core.NewTag(0x0004, 0x1130)
	tagFileSetDescriptorFileID                   = core.NewTag(0x0004, 0x1141)
	tagDescriptorCharacterSet                    = core.NewTag(0x0004, 0x1142)
	tagOffsetLastRootDirectoryRecord             = core.NewTag(0x0004, 0x1202)
	tagFileSetConsistencyFlag                    = core.NewTag(0x0004, 0x1212)
	tagReferencedSOPClassUIDInFile               = core.NewTag(0x0004, 0x1510)
	tagReferencedSOPInstanceUIDInFile            = core.NewTag(0x0004, 0x1511)
	tagReferencedTransferSyntaxUIDInFile         = core.NewTag(0x0004, 0x1512)
	tagReferencedRelatedGeneralSOPClassUIDInFile = core.NewTag(0x0004, 0x151A)
	tagMediaStorageSOPClassUIDDirectory          = core.NewTag(0x0002, 0x0002)
	tagMediaStorageSOPInstanceUIDDirectory       = core.NewTag(0x0002, 0x0003)
	tagTransferSyntaxUIDDirectory                = core.NewTag(0x0002, 0x0010)
)

// WriteOptions bounds one two-pass DICOMDIR encoding.
type WriteOptions struct {
	// MaxBytes bounds each encoded DICOMDIR pass and the final write. Zero uses
	// 64 MiB, which is well above ordinary directories but finite.
	MaxBytes int64
}

// WriteReport describes the final encoded DICOMDIR bytes. CommitDICOMDIR may
// return a populated report with ErrTransaction when atomic replacement
// succeeded but the final directory durability sync failed.
type WriteReport struct {
	BytesWritten int64
	Records      int
	SHA256       [32]byte
	revision     uint64
}

type directoryNode struct {
	typ      RecordType
	record   FileRecord
	children []*directoryNode
	next     *directoryNode
	offset   uint32
}

// WriteDICOMDIR validates and writes a deterministic Basic Directory IOD.
// For atomic publication to the file-set root, use CommitDICOMDIR.
func WriteDICOMDIR(ctx context.Context, destination io.Writer, fileSet *FileSet, options WriteOptions) (WriteReport, error) {
	if fileSet == nil || destination == nil {
		return WriteReport{}, ErrInvalidOptions
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WriteReport{}, err
	}
	maxBytes, err := normalizeWriteOptions(options)
	if err != nil {
		return WriteReport{}, err
	}

	fileSet.mu.RLock()
	records := append([]sourceSnapshot(nil), fileSet.records...)
	uid := fileSet.uid
	id := fileSet.id
	descriptor := append(FileID(nil), fileSet.descriptor...)
	descriptorCharacterSet := fileSet.descriptorCharacterSet
	revision := fileSet.revision
	fileSet.mu.RUnlock()

	if err := fileSet.validateDescriptor(ctx); err != nil {
		return WriteReport{}, err
	}
	if _, err := fileSet.validateRecordsSnapshot(ctx, records); err != nil {
		return WriteReport{}, err
	}
	if err := fileSet.revalidateSources(ctx, records); err != nil {
		return WriteReport{}, err
	}
	roots, flat, err := buildDirectoryHierarchy(records)
	if err != nil {
		return WriteReport{}, err
	}
	encoded, err := encodeDirectoryTwoPass(ctx, roots, flat, uid, id, descriptor, descriptorCharacterSet, maxBytes)
	if err != nil {
		return WriteReport{}, err
	}
	if err := fileSet.revalidateSources(ctx, records); err != nil {
		return WriteReport{}, err
	}
	if !fileSet.revisionMatches(revision) {
		return WriteReport{}, redactedFileSetError{class: ErrSourceChanged}
	}
	written, err := writeAllContext(ctx, destination, encoded)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return WriteReport{}, ctxErr
		}
		if errors.Is(err, io.ErrShortWrite) {
			return WriteReport{}, errors.Join(ErrDICOMDIRWrite, io.ErrShortWrite)
		}
		return WriteReport{}, redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	return WriteReport{BytesWritten: written, Records: len(flat), SHA256: sha256.Sum256(encoded), revision: revision}, nil
}

func (f *FileSet) revisionMatches(revision uint64) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.revision == revision
}

func normalizeWriteOptions(options WriteOptions) (int64, error) {
	if options.MaxBytes < 0 {
		return 0, ErrInvalidOptions
	}
	if options.MaxBytes == 0 {
		return 64 << 20, nil
	}
	return options.MaxBytes, nil
}

func writeAllContext(ctx context.Context, destination io.Writer, data []byte) (int64, error) {
	var written int64
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, err := destination.Write(data)
		written += int64(n)
		data = data[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func buildDirectoryHierarchy(records []sourceSnapshot) ([]*directoryNode, []*directoryNode, error) {
	records = sortedFileRecords(records)
	patients := make(map[string]*directoryNode)
	studies := make(map[string]*directoryNode)
	series := make(map[string]*directoryNode)
	root := make([]*directoryNode, 0)
	for _, stored := range records {
		r := stored.record
		patient := patients[r.PatientID]
		if patient == nil {
			patient = &directoryNode{typ: RecordTypePatient, record: r}
			patients[r.PatientID] = patient
			root = append(root, patient)
		} else if patient.record.PatientName != r.PatientName {
			return nil, nil, ErrInvalidRecord
		}
		study := studies[r.StudyInstanceUID]
		if study == nil {
			study = &directoryNode{typ: RecordTypeStudy, record: r}
			studies[r.StudyInstanceUID] = study
			patient.children = append(patient.children, study)
		} else if study.record.PatientID != r.PatientID || !sameStudyKeys(study.record, r) {
			return nil, nil, ErrInvalidRecord
		}
		seriesNode := series[r.SeriesInstanceUID]
		if seriesNode == nil {
			seriesNode = &directoryNode{typ: RecordTypeSeries, record: r}
			series[r.SeriesInstanceUID] = seriesNode
			study.children = append(study.children, seriesNode)
		} else if seriesNode.record.StudyInstanceUID != r.StudyInstanceUID || !sameSeriesKeys(seriesNode.record, r) {
			return nil, nil, ErrInvalidRecord
		}
		seriesNode.children = append(seriesNode.children, &directoryNode{typ: r.RecordType, record: r})
	}
	flat := make([]*directoryNode, 0, len(records)*4)
	var visit func([]*directoryNode)
	visit = func(nodes []*directoryNode) {
		for _, node := range nodes {
			flat = append(flat, node)
			visit(node.children)
		}
	}
	visit(root)
	groups := [][]*directoryNode{root}
	for _, node := range flat {
		if len(node.children) != 0 {
			groups = append(groups, node.children)
		}
	}
	for _, group := range groups {
		for i := 0; i+1 < len(group); i++ {
			group[i].next = group[i+1]
		}
	}
	return root, flat, nil
}

func sameStudyKeys(a, b FileRecord) bool {
	return a.StudyDate == b.StudyDate && a.StudyTime == b.StudyTime && a.StudyDescription == b.StudyDescription &&
		a.StudyID == b.StudyID && a.AccessionNumber == b.AccessionNumber
}

func sameSeriesKeys(a, b FileRecord) bool {
	return a.Modality == b.Modality && a.SeriesNumber == b.SeriesNumber
}

func encodeDirectoryTwoPass(ctx context.Context, roots, flat []*directoryNode, uid, id string, descriptor FileID, descriptorCharacterSet string, maxBytes int64) ([]byte, error) {
	passOne, err := encodeDirectory(ctx, roots, flat, uid, id, descriptor, descriptorCharacterSet, maxBytes)
	if err != nil {
		return nil, err
	}
	offsets, err := encodedDirectoryOffsets(passOne, len(flat))
	if err != nil {
		return nil, err
	}
	for i, offset := range offsets {
		flat[i].offset = offset
	}
	passTwo, err := encodeDirectory(ctx, roots, flat, uid, id, descriptor, descriptorCharacterSet, maxBytes)
	if err != nil {
		return nil, err
	}
	secondOffsets, err := encodedDirectoryOffsets(passTwo, len(flat))
	if err != nil || len(secondOffsets) != len(offsets) {
		return nil, redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	for i := range offsets {
		if offsets[i] != secondOffsets[i] {
			return nil, redactedFileSetError{class: ErrDICOMDIRWrite}
		}
	}
	if err := validateEncodedDirectory(passTwo, roots, flat); err != nil {
		return nil, err
	}
	return passTwo, nil
}

func encodeDirectory(ctx context.Context, roots, flat []*directoryNode, uid, id string, descriptor FileID, descriptorCharacterSet string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	elements := []core.Element{
		textElement(tagFileSetID, core.VRCS, id),
		ulElement(tagOffsetFirstRootDirectoryRecord, nodeOffset(firstNode(roots))),
		ulElement(tagOffsetLastRootDirectoryRecord, nodeOffset(lastNode(roots))),
		usElement(tagFileSetConsistencyFlag, 0),
	}
	if len(descriptor) != 0 {
		elements = append(elements, textValuesElement(tagFileSetDescriptorFileID, core.VRCS, []string(descriptor)))
		if descriptorCharacterSet != "" {
			elements = append(elements, textElement(tagDescriptorCharacterSet, core.VRCS, descriptorCharacterSet))
		}
	}
	items := make([]core.DataSet, len(flat))
	for i, node := range flat {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items[i] = directoryRecordDataSet(node, nodeOffset(node.next), firstChildOffset(node))
	}
	elements = append(elements, core.Element{
		Header: core.ElementHeader{Tag: tagDirectoryRecordSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	})
	dataset := object.FromElements(elements, std.Dictionary)
	meta := object.FromElements([]core.Element{
		uiElementValue(tagMediaStorageSOPClassUIDDirectory, mediaStorageDirectoryStorageUID),
		uiElementValue(tagMediaStorageSOPInstanceUIDDirectory, uid),
		uiElementValue(tagTransferSyntaxUIDDirectory, transfer.ExplicitVRLittleEndian.UID),
	}, std.Dictionary)
	file := &object.File{Meta: meta, Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}
	var buffer limitedBuffer
	buffer.max = maxBytes
	if err := object.WriteFileWithOptions(&buffer, file, object.WriteFileOptions{OmitReconciledDataSetSOPUIDs: true}); err != nil {
		if errors.Is(err, ErrResourceLimit) {
			return nil, err
		}
		return nil, redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	return append([]byte(nil), buffer.Bytes()...), nil
}

type limitedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if int64(len(data)) > b.max-int64(b.Len()) {
		return 0, ErrResourceLimit
	}
	return b.Buffer.Write(data)
}

func firstNode(nodes []*directoryNode) *directoryNode {
	if len(nodes) == 0 {
		return nil
	}
	return nodes[0]
}

func lastNode(nodes []*directoryNode) *directoryNode {
	if len(nodes) == 0 {
		return nil
	}
	return nodes[len(nodes)-1]
}

func nodeOffset(node *directoryNode) uint32 {
	if node == nil {
		return 0
	}
	return node.offset
}

func firstChildOffset(node *directoryNode) uint32 {
	return nodeOffset(firstNode(node.children))
}

func directoryRecordDataSet(node *directoryNode, next, lower uint32) core.DataSet {
	elements := []core.Element{
		ulElement(tagOffsetNextDirectoryRecord, next), usElement(tagRecordInUseFlag, 0xFFFF),
		ulElement(tagOffsetLowerLevelRecord, lower), textElement(tagDirectoryRecordType, core.VRCS, node.typ.String()),
	}
	if node.record.SpecificCharacterSet != "" {
		elements = append(elements, textValuesElement(tagSpecificCharacterSetFS, core.VRCS, strings.Split(node.record.SpecificCharacterSet, `\`)))
	}
	switch node.typ {
	case RecordTypePatient:
		elements = append(elements, textElement(tagPatientNameFS, core.VRPN, node.record.PatientName), textElement(tagPatientIDFS, core.VRLO, node.record.PatientID))
	case RecordTypeStudy:
		elements = append(elements,
			textElement(tagStudyDateFS, core.VRDA, node.record.StudyDate), textElement(tagStudyTimeFS, core.VRTM, node.record.StudyTime),
			textElement(tagStudyDescriptionFS, core.VRLO, node.record.StudyDescription), uiElementValue(tagStudyInstanceUIDFS, node.record.StudyInstanceUID),
			textElement(tagStudyIDFS, core.VRSH, node.record.StudyID), textElement(tagAccessionNumberFS, core.VRSH, node.record.AccessionNumber))
	case RecordTypeSeries:
		elements = append(elements, textElement(tagModalityFS, core.VRCS, node.record.Modality), uiElementValue(tagSeriesInstanceUIDFS, node.record.SeriesInstanceUID), textElement(tagSeriesNumberFS, core.VRIS, node.record.SeriesNumber))
	default:
		elements = append(elements,
			textValuesElement(tagReferencedFileID, core.VRCS, []string(node.record.FileID)),
			uiElementValue(tagReferencedSOPClassUIDInFile, node.record.SOPClassUID), uiElementValue(tagReferencedSOPInstanceUIDInFile, node.record.SOPInstanceUID),
			uiElementValue(tagReferencedTransferSyntaxUIDInFile, node.record.TransferSyntaxUID))
		if node.record.InstanceNumber != "" {
			elements = append(elements, textElement(tagInstanceNumberFS, core.VRIS, node.record.InstanceNumber))
		}
		if len(node.record.RelatedGeneralSOPClassUIDs) != 0 {
			elements = append(elements, textValuesElement(tagReferencedRelatedGeneralSOPClassUIDInFile, core.VRUI, node.record.RelatedGeneralSOPClassUIDs))
		}
	}
	sort.SliceStable(elements, func(i, j int) bool { return elements[i].Tag().Less(elements[j].Tag()) })
	return core.DataSet{Elements: elements}
}

func textElement(tag core.Tag, vr core.VR, value string) core.Element {
	return textValuesElement(tag, vr, []string{value})
}

func textValuesElement(tag core.Tag, vr core.VR, values []string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue(append([]string(nil), values...)),
	}
}

func uiElementValue(tag core.Tag, value string) core.Element {
	raw := []byte(core.NormalizeUID(value))
	if len(raw)%2 != 0 {
		raw = append(raw, 0)
	}
	return core.NewRawElement(tag, core.VRUI, raw)
}

func ulElement(tag core.Tag, value uint32) core.Element {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, value)
	return core.NewRawElement(tag, core.VRUL, raw)
}

func usElement(tag core.Tag, value uint16) core.Element {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	return core.NewRawElement(tag, core.VRUS, raw)
}

func encodedDirectoryOffsets(encoded []byte, want int) ([]uint32, error) {
	file, err := object.ReadFile(bytes.NewReader(encoded))
	if err != nil {
		return nil, redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	defer file.Close()
	items, ok := file.Dataset.GetSequence(tagDirectoryRecordSequence)
	if !ok || len(items) != want {
		return nil, redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	offsets := make([]uint32, len(items))
	seen := make(map[uint32]bool, len(items))
	for i, item := range items {
		offset, ok := item.ItemOffset()
		if !ok || offset <= 0 || offset > math.MaxUint32 || seen[uint32(offset)] {
			return nil, redactedFileSetError{class: ErrDICOMDIRWrite}
		}
		offsets[i] = uint32(offset)
		seen[offsets[i]] = true
	}
	return offsets, nil
}

func validateEncodedDirectory(encoded []byte, roots, flat []*directoryNode) error {
	file, err := object.ReadFile(bytes.NewReader(encoded))
	if err != nil {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	defer file.Close()
	if file.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID || file.Dataset.Has(tagSOPClassUIDFS) || file.Dataset.Has(tagSOPInstanceUIDFS) {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	metaClass, _ := file.Meta.GetUID(tagMediaStorageSOPClassUIDDirectory)
	if metaClass != mediaStorageDirectoryStorageUID {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	first, okFirst := uint32Value(file.Dataset, tagOffsetFirstRootDirectoryRecord)
	last, okLast := uint32Value(file.Dataset, tagOffsetLastRootDirectoryRecord)
	if !okFirst || !okLast || first != nodeOffset(firstNode(roots)) || last != nodeOffset(lastNode(roots)) {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	items, ok := file.Dataset.GetSequence(tagDirectoryRecordSequence)
	if !ok || len(items) != len(flat) {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	byOffset := make(map[uint32]*object.Object, len(items))
	for _, item := range items {
		offset, ok := item.ItemOffset()
		if !ok {
			return redactedFileSetError{class: ErrDICOMDIRWrite}
		}
		byOffset[uint32(offset)] = item
	}
	visited := map[uint32]bool{}
	pending := []uint32{first}
	for len(pending) != 0 {
		offset := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if offset == 0 || visited[offset] {
			if offset != 0 && visited[offset] {
				return redactedFileSetError{class: ErrDICOMDIRWrite}
			}
			continue
		}
		item := byOffset[offset]
		if item == nil {
			return redactedFileSetError{class: ErrDICOMDIRWrite}
		}
		visited[offset] = true
		next, ok := uint32Value(item, tagOffsetNextDirectoryRecord)
		if !ok {
			return redactedFileSetError{class: ErrDICOMDIRWrite}
		}
		lower, ok := uint32Value(item, tagOffsetLowerLevelRecord)
		if !ok {
			return redactedFileSetError{class: ErrDICOMDIRWrite}
		}
		if next != 0 {
			pending = append(pending, next)
		}
		if lower != 0 {
			pending = append(pending, lower)
		}
	}
	if len(visited) != len(flat) {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	references, err := validateDirectoryDataSet(file.Dataset)
	if err != nil {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	expected := make([]FileRecord, 0)
	for _, node := range flat {
		if node.typ != RecordTypePatient && node.typ != RecordTypeStudy && node.typ != RecordTypeSeries {
			expected = append(expected, node.record)
		}
	}
	if len(references) != len(expected) {
		return redactedFileSetError{class: ErrDICOMDIRWrite}
	}
	for i := range references {
		got, want := references[i], expected[i]
		if !equalFileID(got.fileID, want.FileID) || got.recordType != want.RecordType ||
			!encodedSelectionMatchesRecord(got.selection, want) || got.sopClassUID != want.SOPClassUID ||
			got.sopInstanceUID != want.SOPInstanceUID || got.transferSyntaxUID != want.TransferSyntaxUID ||
			!equalStrings(got.relatedGeneralSOPClassUIDs, want.RelatedGeneralSOPClassUIDs) {
			return redactedFileSetError{class: ErrDICOMDIRWrite}
		}
	}
	return nil
}

func (f *FileSet) revalidateSources(ctx context.Context, records []sourceSnapshot) error {
	rootInfo, err := os.Lstat(f.root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(f.rootInfo, rootInfo) {
		return redactedFileSetError{class: ErrSourceChanged}
	}
	fileIDs := make([]FileID, len(records))
	for i := range records {
		fileIDs[i] = records[i].record.FileID
	}
	if sourceIndex, spellingErr := validateExactFileIDSpellings(ctx, f.root, fileIDs); spellingErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if sourceIndex >= 0 {
			return sourceValidationError{sourceIndex: sourceIndex}
		}
		return redactedFileSetError{class: ErrSourceChanged}
	}
	for sourceIndex, stored := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, _, err := OpenReferencedFile(f.root, Reference{FileID: []string(stored.record.FileID)})
		if err != nil {
			return sourceValidationError{sourceIndex: sourceIndex}
		}
		info, statErr := file.Stat()
		if statErr != nil || !os.SameFile(stored.info, info) || info.Size() != stored.size || !info.ModTime().Equal(stored.modTime) {
			file.Close()
			return sourceValidationError{sourceIndex: sourceIndex}
		}
		result, readErr := index.Read(ctx, index.ReaderAtSource("", file, info.Size()), f.metadataIndexOptions())
		closeErr := file.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if readErr != nil || closeErr != nil {
			return sourceValidationError{sourceIndex: sourceIndex}
		}
		current, currentErr := fileRecordFromIndex(stored.record.FileID, result)
		if currentErr != nil || !sameReferencedIdentity(stored.record, current) {
			return sourceValidationError{sourceIndex: sourceIndex}
		}
	}
	return nil
}

func sameReferencedIdentity(a, b FileRecord) bool {
	return a.RecordType == b.RecordType && a.SpecificCharacterSet == b.SpecificCharacterSet &&
		encodedSelectionMatchesRecord(a, b) && a.SOPClassUID == b.SOPClassUID &&
		a.SOPInstanceUID == b.SOPInstanceUID && a.TransferSyntaxUID == b.TransferSyntaxUID &&
		equalStrings(a.RelatedGeneralSOPClassUIDs, b.RelatedGeneralSOPClassUIDs)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CommitDICOMDIR transactionally publishes DICOMDIR in the FileSet root.
func CommitDICOMDIR(ctx context.Context, fileSet *FileSet, options WriteOptions) (report WriteReport, err error) {
	if fileSet == nil {
		return WriteReport{}, ErrInvalidOptions
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WriteReport{}, err
	}
	maxBytes, optionErr := normalizeWriteOptions(options)
	if optionErr != nil {
		return WriteReport{}, optionErr
	}
	rootInfo, rootErr := os.Lstat(fileSet.root)
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(fileSet.rootInfo, rootInfo) {
		return WriteReport{}, redactedFileSetError{class: ErrSourceChanged}
	}
	destination := filepath.Join(fileSet.root, "DICOMDIR")
	var previous fsSnapshot
	if info, statErr := os.Lstat(destination); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return WriteReport{}, redactedFileSetError{class: ErrTransaction}
		}
		previous = fsSnapshot{exists: true, info: info}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return WriteReport{}, redactedFileSetError{class: ErrTransaction}
	}
	temp, createErr := os.CreateTemp(fileSet.root, ".DICOMDIR.tmp-")
	if createErr != nil {
		return WriteReport{}, redactedFileSetError{class: ErrTransaction}
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if chmodErr := temp.Chmod(0o600); chmodErr != nil {
		temp.Close()
		return WriteReport{}, redactedFileSetError{class: ErrTransaction}
	}
	report, err = WriteDICOMDIR(ctx, temp, fileSet, options)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return WriteReport{}, err
	}
	fileSet.mu.RLock()
	if fileSet.revision != report.revision {
		fileSet.mu.RUnlock()
		return WriteReport{}, redactedFileSetError{class: ErrSourceChanged}
	}
	records := append([]sourceSnapshot(nil), fileSet.records...)
	fileSetOptions := fileSet.options
	fileSet.mu.RUnlock()
	readOptions, limitErr := directoryReadOptions(maxBytes, directoryRecordCount(records))
	if limitErr != nil {
		return WriteReport{}, limitErr
	}
	if _, parseErr := parseFileSet(ctx, tempPath, readOptions, fileSetOptions); parseErr != nil {
		if errors.Is(parseErr, context.Canceled) || errors.Is(parseErr, context.DeadlineExceeded) || errors.Is(parseErr, ErrResourceLimit) {
			return WriteReport{}, parseErr
		}
		return WriteReport{}, redactedFileSetError{class: ErrTransaction}
	}
	if err := fileSet.revalidateSources(ctx, records); err != nil {
		return WriteReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return WriteReport{}, err
	}
	fileSet.mu.RLock()
	defer fileSet.mu.RUnlock()
	if fileSet.revision != report.revision {
		return WriteReport{}, redactedFileSetError{class: ErrSourceChanged}
	}
	if !sameDestinationSnapshot(destination, previous) {
		return WriteReport{}, redactedFileSetError{class: ErrTransaction}
	}
	if err := replaceFileAtomically(tempPath, destination); err != nil {
		return WriteReport{}, redactedFileSetError{class: ErrTransaction}
	}
	if err := syncFileSetDirectory(fileSet.root); err != nil {
		// The atomic replacement has already committed; preserve the report so
		// callers can distinguish a post-publication durability failure from a
		// pre-publication rejection.
		return report, redactedFileSetError{class: ErrTransaction}
	}
	return report, nil
}

type fsSnapshot struct {
	exists bool
	info   os.FileInfo
}

func sameDestinationSnapshot(path string, snapshot fsSnapshot) bool {
	info, err := os.Lstat(path)
	if !snapshot.exists {
		return errors.Is(err, os.ErrNotExist)
	}
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && os.SameFile(snapshot.info, info)
}

// ParseFileSet strictly imports a DICOMDIR authored within the supported IMAGE
// hierarchy and revalidates every active reference against its source file.
func ParseFileSet(path string) (*FileSet, error) {
	absPath, err := filepath.Abs(path)
	if err != nil || filepath.Base(absPath) != "DICOMDIR" {
		return nil, redactedFileSetError{class: ErrInvalidRecord}
	}
	if err := exactFileIDSpelling(context.Background(), filepath.Dir(absPath), FileID{"DICOMDIR"}); err != nil {
		return nil, redactedFileSetError{class: ErrInvalidRecord}
	}
	readOptions, err := directoryReadOptions(64<<20, defaultMaxFileSetFiles*4)
	if err != nil {
		return nil, err
	}
	return parseFileSet(context.Background(), absPath, readOptions, Options{})
}

func directoryReadOptions(maxBytes int64, maxRecords int) (object.ReadFileOptions, error) {
	if maxBytes <= 0 || maxRecords < 0 || maxRecords > (math.MaxInt-32)/16 {
		return object.ReadFileOptions{}, ErrResourceLimit
	}
	return object.ReadFileOptions{
		MaxElementBytes:  16 << 20,
		MaxTotalBytes:    maxBytes,
		MaxSequenceDepth: 64,
		MaxElements:      maxRecords*16 + 32,
		MaxFragments:     200_000,
		SkipPixelData:    true,
	}, nil
}

func parseFileSet(ctx context.Context, path string, readOptions object.ReadFileOptions, sourceOptions Options) (*FileSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, redactedFileSetError{class: ErrInvalidRecord}
	}
	root := filepath.Dir(absPath)
	handle, _, err := OpenReferencedFile(root, Reference{FileID: []string{filepath.Base(absPath)}})
	if err != nil {
		return nil, redactedFileSetError{class: ErrInvalidRecord}
	}
	file, readErr := object.ReadFileWithOptions(handle, readOptions)
	closeErr := handle.Close()
	if readErr != nil {
		if isParserResourceLimit(readErr) {
			return nil, redactedFileSetError{class: ErrResourceLimit}
		}
		return nil, redactedFileSetError{class: ErrInvalidRecord}
	}
	if closeErr != nil {
		return nil, redactedFileSetError{class: ErrInvalidRecord}
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	classUID, classOK := requiredUID(file.Meta, tagMediaStorageSOPClassUIDDirectory)
	uid, uidOK := requiredUID(file.Meta, tagMediaStorageSOPInstanceUIDDirectory)
	transferSyntaxUID, syntaxOK := requiredUID(file.Meta, tagTransferSyntaxUIDDirectory)
	if !classOK || !uidOK || !syntaxOK || classUID != mediaStorageDirectoryStorageUID || !validUID(uid) ||
		transferSyntaxUID != transfer.ExplicitVRLittleEndian.UID || file.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		return nil, ErrInvalidRecord
	}
	id, idOK := typeTwoStringWithVR(file.Dataset, tagFileSetID, core.VRCS)
	if !idOK {
		return nil, ErrInvalidRecord
	}
	descriptorValues, descriptorPresent := stringsWithVR(file.Dataset, tagFileSetDescriptorFileID, core.VRCS)
	if file.Dataset.Has(tagFileSetDescriptorFileID) && !descriptorPresent {
		return nil, ErrInvalidRecord
	}
	var descriptor FileID
	if len(descriptorValues) != 0 {
		descriptor, err = NewFileID(descriptorValues...)
		if err != nil {
			return nil, err
		}
	}
	descriptorCharacterSet, descriptorCharacterSetOK := stringWithVR(file.Dataset, tagDescriptorCharacterSet, core.VRCS)
	if file.Dataset.Has(tagDescriptorCharacterSet) && (!descriptorCharacterSetOK || descriptorCharacterSet == "") {
		return nil, ErrInvalidRecord
	}
	if len(descriptor) == 0 && file.Dataset.Has(tagDescriptorCharacterSet) {
		return nil, ErrInvalidRecord
	}
	declared, err := validateDirectoryDataSet(file.Dataset)
	if err != nil {
		return nil, err
	}
	sourceOptions.FileSetUID = uid
	sourceOptions.FileSetID = id
	sourceOptions.DescriptorFileID = descriptor
	sourceOptions.DescriptorCharacterSet = descriptorCharacterSet
	parsed, err := NewFileSet(root, sourceOptions)
	if err != nil {
		return nil, err
	}
	fileIDs := make([]FileID, len(declared))
	for i := range declared {
		fileIDs[i] = declared[i].fileID
	}
	if _, err := validateExactFileIDSpellings(ctx, root, fileIDs); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, redactedFileSetError{class: ErrInvalidRecord}
	}
	for _, reference := range declared {
		if err := parsed.add(ctx, reference.fileID, false); err != nil {
			return nil, err
		}
		parsed.mu.RLock()
		added := parsed.records[len(parsed.records)-1].record
		parsed.mu.RUnlock()
		if added.RecordType != reference.recordType || added.SOPClassUID != reference.sopClassUID ||
			added.SOPInstanceUID != reference.sopInstanceUID || added.TransferSyntaxUID != reference.transferSyntaxUID ||
			!encodedSelectionMatchesRecord(reference.selection, added) ||
			!equalStrings(added.RelatedGeneralSOPClassUIDs, reference.relatedGeneralSOPClassUIDs) {
			return nil, ErrInvalidRecord
		}
	}
	if err := parsed.validateDescriptor(ctx); err != nil {
		return nil, err
	}
	return parsed, nil
}

func isParserResourceLimit(err error) bool {
	for _, target := range []error{
		parser.ErrMaxDepthExceeded, parser.ErrMaxElementBytesExceeded, parser.ErrMaxElementsExceeded,
		parser.ErrMaxFragmentsExceeded, parser.ErrMaxTotalBytesExceeded,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func encodedSelectionMatchesRecord(encoded, record FileRecord) bool {
	return encoded.PatientName == record.PatientName && encoded.PatientID == record.PatientID &&
		encoded.StudyDate == record.StudyDate && encoded.StudyTime == record.StudyTime &&
		encoded.StudyDescription == record.StudyDescription && encoded.StudyInstanceUID == record.StudyInstanceUID &&
		encoded.StudyID == record.StudyID && encoded.AccessionNumber == record.AccessionNumber &&
		encoded.Modality == record.Modality && encoded.SeriesInstanceUID == record.SeriesInstanceUID &&
		encoded.SeriesNumber == record.SeriesNumber && encoded.InstanceNumber == record.InstanceNumber
}

func (f *FileSet) sortedRecordsSnapshot() []sourceSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return sortedFileRecords(f.records)
}

func directoryRecordCount(records []sourceSnapshot) int {
	patients, studies, series := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, stored := range records {
		patients[stored.record.PatientID] = true
		studies[stored.record.StudyInstanceUID] = true
		series[stored.record.SeriesInstanceUID] = true
	}
	return len(patients) + len(studies) + len(series) + len(records)
}
