package dicominspect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicominspect"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomtags "github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestInspectReaderSummarizesPart10File(t *testing.T) {
	data := part10Fixture(t)

	summary, err := dicominspect.InspectReader("sample.dcm", bytes.NewReader(data), dicominspect.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	if summary.FileName != "sample.dcm" {
		t.Fatalf("FileName = %q, want sample.dcm", summary.FileName)
	}
	if summary.PatientName != "LIBRARY^PATIENT" || summary.PatientID != "LIB001" {
		t.Fatalf("patient = %q/%q, want LIBRARY^PATIENT/LIB001", summary.PatientName, summary.PatientID)
	}
	if summary.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntaxUID = %q, want %q", summary.TransferSyntaxUID, transfer.ExplicitVRLittleEndian.UID)
	}
	if !hasElement(summary.Elements, "(0010,0010)", "PatientName", "LIBRARY^PATIENT") {
		t.Fatal("missing summarized PatientName element")
	}
}

func TestInspectReaderContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	summary, err := dicominspect.InspectReaderContext(ctx, "sample.dcm", bytes.NewReader(part10Fixture(t)), dicominspect.DefaultOptions())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InspectReaderContext() error = %v, want context.Canceled", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeCanceled)
}

func TestSummarizeFileContextUsesRecursiveElementLimit(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	file := &object.File{
		Dataset: object.FromElements([]core.Element{{
			Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				dicomtest.StringElement(dicomtags.PatientName, core.VRPN, "NESTED^PATIENT"),
			}}}},
		}}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	opts := dicominspect.DefaultOptions()
	opts.MaxElements = 1

	_, err := dicominspect.SummarizeFileContext(context.Background(), "nested.dcm", file, opts)
	if !errors.Is(err, object.ErrWalkResourceLimit) {
		t.Fatalf("SummarizeFileContext() error = %v, want ErrWalkResourceLimit", err)
	}
	var limitErr *object.WalkLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != object.WalkLimitElements {
		t.Fatalf("SummarizeFileContext() error = %v, want element WalkLimitError", err)
	}
}

func TestSummarizeFileContextUsesRecursiveDepthLimit(t *testing.T) {
	outerSequenceTag := core.NewTag(0x0008, 0x1111)
	innerSequenceTag := core.NewTag(0x0008, 0x1115)
	file := &object.File{
		Dataset: object.FromElements([]core.Element{{
			Header: core.ElementHeader{Tag: outerSequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{{
				Header: core.ElementHeader{Tag: innerSequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
				Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
					dicomtest.StringElement(dicomtags.PatientName, core.VRPN, "DEEP^PATIENT"),
				}}}},
			}}}}},
		}}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	opts := dicominspect.DefaultOptions()
	opts.MaxSequenceDepth = 1

	_, err := dicominspect.SummarizeFileContext(context.Background(), "deep.dcm", file, opts)
	if !errors.Is(err, object.ErrWalkResourceLimit) {
		t.Fatalf("SummarizeFileContext() error = %v, want ErrWalkResourceLimit", err)
	}
	var limitErr *object.WalkLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != object.WalkLimitDepth {
		t.Fatalf("SummarizeFileContext() error = %v, want depth WalkLimitError", err)
	}
}

func TestSummarizeFileContextHonorsWalkCancellation(t *testing.T) {
	file := &object.File{
		Dataset: object.FromElements([]core.Element{
			dicomtest.StringElement(dicomtags.PatientName, core.VRPN, "CANCELED^PATIENT"),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := dicominspect.SummarizeFileContext(ctx, "canceled.dcm", file, dicominspect.DefaultOptions())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SummarizeFileContext() error = %v, want context.Canceled", err)
	}
	var canceledErr *object.WalkCanceledError
	if !errors.As(err, &canceledErr) {
		t.Fatalf("SummarizeFileContext() error = %v, want WalkCanceledError", err)
	}
}

func TestSummarizeFileFlattensSequencePaths(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	file := &object.File{
		Dataset: object.FromElements([]core.Element{{
			Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				dicomtest.StringElement(dicomtags.PatientName, core.VRPN, "NESTED^PATIENT"),
			}}}},
		}}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	summary, err := dicominspect.SummarizeFile("nested.dcm", file, dicominspect.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if summary.ElementCount != 2 || len(summary.Elements) != 2 {
		t.Fatalf("element count = %d/%d, want sequence and nested element", summary.ElementCount, len(summary.Elements))
	}
	if summary.Elements[0].Tag != sequenceTag.String() || summary.Elements[0].Path != sequenceTag.String() || summary.Elements[0].Value != "1 item(s)" {
		t.Fatalf("sequence summary = %#v", summary.Elements[0])
	}
	wantPath := sequenceTag.String() + "[0]/" + dicomtags.PatientName.String()
	if summary.Elements[1].Path != wantPath || summary.Elements[1].Value != "NESTED^PATIENT" {
		t.Fatalf("nested summary = %#v, want path %q", summary.Elements[1], wantPath)
	}
}

func TestSummarizeFileReportsPreservedConformanceFindingsWithoutError(t *testing.T) {
	nestedSequence := core.NewTag(0x0008, 0x1111)
	referencedSOPInstanceUID := core.NewTag(0x0008, 0x1155)
	badUID := dicomtest.StringElement(referencedSOPInstanceUID, core.VRUI, "not-a-uid")
	file := &object.File{
		Dataset: object.FromElements([]core.Element{{
			Header: core.ElementHeader{Tag: nestedSequence, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
			Value:  core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{badUID}}}},
		}}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	summary, err := dicominspect.SummarizeFile("invalid.dcm", file, dicominspect.DefaultOptions())
	if err != nil {
		t.Fatalf("SummarizeFile() error = %v, want preserved findings", err)
	}
	wantPath := nestedSequence.String() + "[0]/" + referencedSOPInstanceUID.String()
	finding, ok := findFinding(summary.Findings, "vr.format")
	if !ok {
		t.Fatalf("findings = %#v, want vr.format", summary.Findings)
	}
	if finding.Severity != dicominspect.SeverityError || finding.Source != dicominspect.SourceDataset || finding.Path != wantPath || finding.Tag != referencedSOPInstanceUID.String() {
		t.Fatalf("finding = %#v, want dataset error at %q", finding, wantPath)
	}
	if strings.Contains(finding.Message, "not-a-uid") {
		t.Fatalf("finding leaked element value: %q", finding.Message)
	}
}

func TestSummarizeFileKeepsWarningsDistinctAndNonBlocking(t *testing.T) {
	file := &object.File{
		Dataset: object.FromElements([]core.Element{
			dicomtest.StringElement(dicomtags.PatientName, core.VRPN, "SAFE^VALUE"),
			dicomtest.StringElement(dicomtags.SOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	summary, err := dicominspect.SummarizeFile("warning.dcm", file, dicominspect.DefaultOptions())
	if err != nil {
		t.Fatalf("SummarizeFile() error = %v, want warnings to be non-blocking", err)
	}
	finding, ok := findFinding(summary.Findings, "dataset.order")
	if !ok || finding.Severity != dicominspect.SeverityWarning {
		t.Fatalf("findings = %#v, want warning dataset.order", summary.Findings)
	}
}

func TestSummarizeFileClassifiesFileMetaFindings(t *testing.T) {
	file, err := object.ReadFile(bytes.NewReader(part10Fixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	file.Meta.Put(dicomtest.StringElement(core.NewTag(0x0002, 0x0003), core.VRUI, "1.2.3.4.5"))

	summary, err := dicominspect.SummarizeFile("mismatch.dcm", file, dicominspect.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	finding, ok := findFinding(summary.Findings, "file_meta.mismatch")
	if !ok || finding.Source != dicominspect.SourceMeta || finding.Tag != "(0002,0003)" || finding.Path != "(0002,0003)" {
		t.Fatalf("findings = %#v, want navigable meta mismatch", summary.Findings)
	}
}

func TestSummarizeFileSourceDoesNotInferFromTagGroup(t *testing.T) {
	fileMetaTagInsideDataset := core.NewTag(0x0002, 0x0003)
	file := &object.File{
		Dataset: object.FromElements([]core.Element{
			dicomtest.StringElement(fileMetaTagInsideDataset, core.VRUI, "not-a-uid"),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	summary, err := dicominspect.SummarizeFile("dataset-group-0002.dcm", file, dicominspect.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	finding, ok := findFinding(summary.Findings, "vr.format")
	if !ok || finding.Source != dicominspect.SourceDataset || finding.Tag != fileMetaTagInsideDataset.String() {
		t.Fatalf("findings = %#v, want dataset source for group 0002 element", summary.Findings)
	}
}

func TestSummarizeFileBoundsFindings(t *testing.T) {
	elements := make([]core.Element, 8)
	for i := range elements {
		elements[i] = dicomtest.StringElement(core.NewTag(0x7776, uint16(i+1)), core.VRUI, "invalid")
	}
	file := &object.File{Dataset: object.FromElements(elements, std.Dictionary), TransferSyntax: transfer.ExplicitVRLittleEndian}
	opts := dicominspect.DefaultOptions()
	opts.MaxFindings = 2

	summary, err := dicominspect.SummarizeFile("bounded.dcm", file, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Findings) != 2 || summary.FindingCount != 2 || !summary.FindingsTruncated || summary.DroppedFindings == 0 {
		t.Fatalf("bounded findings = %#v", summary)
	}
}

func TestInspectReaderResourceLimitReturnsSafeDiagnosticAndError(t *testing.T) {
	data := part10Fixture(t)
	opts := dicominspect.DefaultOptions()
	opts.MaxTotalBytes = 132

	summary, err := dicominspect.InspectReader("large.dcm", bytes.NewReader(data), opts)
	if !errors.Is(err, dicominspect.ErrResourceLimit) {
		t.Fatalf("InspectReader() error = %v, want ErrResourceLimit", err)
	}
	if !errors.Is(err, parser.ErrMaxTotalBytesExceeded) {
		t.Fatalf("InspectReader() error = %v, want underlying parser limit", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeResourceLimit)
}

func TestSummarizeFileBoundsRetainedInMemoryValuesBeforeValidationClone(t *testing.T) {
	file := &object.File{
		Dataset: object.FromElements([]core.Element{
			core.NewRawElement(core.NewTag(0x7777, 0x0010), core.VROB, make([]byte, 64)),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	opts := dicominspect.DefaultOptions()
	opts.MaxElementBytes = 32

	summary, err := dicominspect.SummarizeFile("memory.dcm", file, opts)
	if !errors.Is(err, dicominspect.ErrResourceLimit) {
		t.Fatalf("SummarizeFile() error = %v, want ErrResourceLimit", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeResourceLimit)
}

func TestInspectReaderMalformedReturnsRedactedDiagnosticAndError(t *testing.T) {
	secret := "PATIENT^SECRET"
	summary, err := dicominspect.InspectReader("bad.dcm", strings.NewReader(secret), dicominspect.DefaultOptions())
	if !errors.Is(err, dicominspect.ErrMalformed) {
		t.Fatalf("InspectReader() error = %v, want ErrMalformed", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeMalformed)
	encoded, marshalErr := json.Marshal(summary.Findings)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("diagnostic leaked malformed input: %s", encoded)
	}
}

func TestInspectReaderTruncatedPart10ReturnsMalformedDiagnostic(t *testing.T) {
	data := part10Fixture(t)
	data = data[:len(data)-3]

	summary, err := dicominspect.InspectReader("truncated.dcm", bytes.NewReader(data), dicominspect.DefaultOptions())
	if !errors.Is(err, dicominspect.ErrMalformed) {
		t.Fatalf("InspectReader() error = %v, want ErrMalformed", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeMalformed)
}

func TestInspectReaderNilSourceReturnsMalformedDiagnosticWithoutPanic(t *testing.T) {
	summary, err := dicominspect.InspectReader("nil.dcm", nil, dicominspect.DefaultOptions())
	if !errors.Is(err, dicominspect.ErrMalformed) {
		t.Fatalf("InspectReader() error = %v, want ErrMalformed", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeMalformed)
}

func TestSummarizeFileRejectsMissingDataset(t *testing.T) {
	summary, err := dicominspect.SummarizeFile("missing.dcm", &object.File{}, dicominspect.DefaultOptions())
	if !errors.Is(err, dicominspect.ErrMalformed) {
		t.Fatalf("SummarizeFile() error = %v, want ErrMalformed", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeMalformed)
}

func TestInspectReaderTimeoutReturnsDiagnosticAndPreservesDeadline(t *testing.T) {
	opts := dicominspect.DefaultOptions()
	opts.Timeout = 3 * time.Millisecond
	reader := &slowChunkReader{data: part10Fixture(t), delay: time.Millisecond}

	summary, err := dicominspect.InspectReaderContext(context.Background(), "slow.dcm", reader, opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("InspectReaderContext() error = %v, want deadline exceeded", err)
	}
	assertOperationalFinding(t, summary, dicominspect.CodeTimeout)
}

func TestValidateOptionsRejectsNegativeBounds(t *testing.T) {
	opts := dicominspect.DefaultOptions()
	opts.MaxFindings = -1
	if err := dicominspect.ValidateOptions(opts); !errors.Is(err, dicominspect.ErrInvalidOptions) {
		t.Fatalf("ValidateOptions() error = %v, want ErrInvalidOptions", err)
	}
	if _, err := dicominspect.InspectReader("sample.dcm", bytes.NewReader(part10Fixture(t)), opts); !errors.Is(err, dicominspect.ErrInvalidOptions) {
		t.Fatalf("InspectReader() error = %v, want ErrInvalidOptions", err)
	}
}

func assertOperationalFinding(t *testing.T, summary dicominspect.Summary, code string) {
	t.Helper()
	if len(summary.Findings) != 1 || summary.Findings[0].Code != code || summary.Findings[0].Source != dicominspect.SourceFile || summary.Findings[0].Severity != dicominspect.SeverityError {
		t.Fatalf("operational findings = %#v, want one %q", summary.Findings, code)
	}
	if summary.Findings[0].Tag != "" || summary.Findings[0].Path != "" {
		t.Fatalf("operational finding fabricated location: %#v", summary.Findings[0])
	}
}

func findFinding(findings []dicominspect.Finding, code string) (dicominspect.Finding, bool) {
	for _, finding := range findings {
		if finding.Code == code {
			return finding, true
		}
	}
	return dicominspect.Finding{}, false
}

type slowChunkReader struct {
	data  []byte
	delay time.Duration
}

func (r *slowChunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("unexpected exhaustion")
	}
	time.Sleep(r.delay)
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func part10Fixture(t *testing.T) []byte {
	t.Helper()
	file := &object.File{
		Dataset: object.FromElements([]core.Element{
			dicomtest.StringElement(dicomtags.SOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
			dicomtest.StringElement(dicomtags.SOPInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.763.1"),
			dicomtest.StringElement(dicomtags.PatientName, core.VRPN, "LIBRARY^PATIENT"),
			dicomtest.StringElement(dicomtags.PatientID, core.VRLO, "LIB001"),
			dicomtest.StringElement(dicomtags.Modality, core.VRCS, "CT"),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func hasElement(elements []dicominspect.ElementSummary, tag, keyword, value string) bool {
	for _, elem := range elements {
		if elem.Tag == tag && elem.Keyword == keyword && elem.Value == value {
			return true
		}
	}
	return false
}
