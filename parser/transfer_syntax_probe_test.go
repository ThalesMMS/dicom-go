package parser

import (
	"bytes"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestProbeTransferSyntaxSelectsNativeSyntaxes(t *testing.T) {
	t.Parallel()

	for _, syntax := range []transfer.Syntax{
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	} {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()
			encoded := encodeProbeDataSet(t, syntax, probeElements())
			report, err := ProbeTransferSyntax(encoded, TransferSyntaxProbeOptions{})
			if err != nil {
				t.Fatalf("ProbeTransferSyntax() error = %v; report = %s", err, report.String())
			}
			if report.Outcome != TransferSyntaxProbeSelected {
				t.Fatalf("Outcome = %q, want selected", report.Outcome)
			}
			if report.Selected.UID != syntax.UID {
				t.Fatalf("Selected UID = %q, want %q", report.Selected.UID, syntax.UID)
			}
			if report.Confidence < 0.75 {
				t.Fatalf("Confidence = %.3f, want >= 0.75", report.Confidence)
			}
			if len(report.Candidates) != 3 {
				t.Fatalf("Candidates = %d, want 3", len(report.Candidates))
			}
			selected, ok := report.SelectedCandidate()
			if !ok || selected.DictionaryVMMatches == 0 {
				t.Fatalf("selected candidate = %+v, want dictionary VM evidence", selected)
			}
		})
	}
}

func TestProbeTransferSyntaxRequiresEnoughEvidenceAndBoundsCandidates(t *testing.T) {
	t.Parallel()

	single := encodeProbeDataSet(t, transfer.ExplicitVRLittleEndian, probeElements()[:1])
	report, err := ProbeTransferSyntax(single, TransferSyntaxProbeOptions{})
	if !errors.Is(err, ErrTransferSyntaxProbeAmbiguous) {
		t.Fatalf("single-element error = %v, want ErrTransferSyntaxProbeAmbiguous; report=%s", err, report.String())
	}
	if report.Selected.UID != "" {
		t.Fatalf("single-element Selected UID = %q, want empty", report.Selected.UID)
	}

	_, err = ProbeTransferSyntax(single, TransferSyntaxProbeOptions{
		Candidates: []transfer.Syntax{
			transfer.ExplicitVRLittleEndian,
			transfer.ExplicitVRBigEndian,
		},
		MaxCandidates: 1,
	})
	if !errors.Is(err, ErrTransferSyntaxProbePolicy) {
		t.Fatalf("candidate amplification error = %v, want ErrTransferSyntaxProbePolicy", err)
	}
}

func TestProbeTransferSyntaxReportsAmbiguousWithoutChoosingByOrder(t *testing.T) {
	t.Parallel()

	// Both tag components and the zero VL are byte-order symmetric, so the two
	// explicit syntaxes have exactly the same evidence.
	data := []byte{0x09, 0x09, 0x10, 0x10, 'L', 'O', 0x00, 0x00}
	report, err := ProbeTransferSyntax(data, TransferSyntaxProbeOptions{
		Candidates: []transfer.Syntax{
			transfer.ExplicitVRLittleEndian,
			transfer.ExplicitVRBigEndian,
		},
	})
	if !errors.Is(err, ErrTransferSyntaxProbeAmbiguous) {
		t.Fatalf("error = %v, want ErrTransferSyntaxProbeAmbiguous", err)
	}
	if report.Outcome != TransferSyntaxProbeAmbiguous {
		t.Fatalf("Outcome = %q, want ambiguous", report.Outcome)
	}
	if report.Selected.UID != "" {
		t.Fatalf("Selected UID = %q, want empty", report.Selected.UID)
	}
	if len(report.Candidates) != 2 || report.Candidates[0].Score != report.Candidates[1].Score {
		t.Fatalf("candidate scores = %+v, want an exact tie", report.Candidates)
	}
}

func TestProbeTransferSyntaxHonorsConfidenceThreshold(t *testing.T) {
	t.Parallel()

	data := encodeProbeDataSet(t, transfer.ExplicitVRLittleEndian, probeElements())
	report, err := ProbeTransferSyntax(data, TransferSyntaxProbeOptions{MinimumConfidence: 1.01})
	if !errors.Is(err, ErrTransferSyntaxProbeAmbiguous) {
		t.Fatalf("error = %v, want ErrTransferSyntaxProbeAmbiguous", err)
	}
	if report.Outcome != TransferSyntaxProbeAmbiguous || report.Selected.UID != "" {
		t.Fatalf("report = %+v, want ambiguous without selection", report)
	}
}

func TestProbeTransferSyntaxRejectsImpossibleAndUnsafeCandidates(t *testing.T) {
	t.Parallel()

	t.Run("random input", func(t *testing.T) {
		report, err := ProbeTransferSyntax([]byte{0xff, 0x01, 0x37, 0x91, 0x42, 0x00, 0xff}, TransferSyntaxProbeOptions{})
		if !errors.Is(err, ErrTransferSyntaxProbeImpossible) {
			t.Fatalf("error = %v, want ErrTransferSyntaxProbeImpossible", err)
		}
		if report.Outcome != TransferSyntaxProbeImpossible || report.Selected.UID != "" {
			t.Fatalf("report = %+v, want impossible without selection", report)
		}
	})

	t.Run("encapsulated candidate", func(t *testing.T) {
		_, err := ProbeTransferSyntax(nil, TransferSyntaxProbeOptions{
			Candidates: []transfer.Syntax{transfer.JPEGBaseline},
		})
		if !errors.Is(err, ErrTransferSyntaxProbePolicy) {
			t.Fatalf("error = %v, want ErrTransferSyntaxProbePolicy", err)
		}
	})
}

func TestProbeTransferSyntaxBoundsHostileInputs(t *testing.T) {
	t.Parallel()

	// Explicit LE OB with a forged 4 GiB VL. The parser must reject it before
	// allocating, and the public diagnostic must not contain source values.
	phiCanary := "SECRET-PATIENT-NAME"
	data := append([]byte{0x10, 0x00, 0x10, 0x00, 'O', 'B', 0x00, 0x00, 0xff, 0xff, 0xff, 0x7f}, phiCanary...)
	started := time.Now()
	report, err := ProbeTransferSyntax(data, TransferSyntaxProbeOptions{
		MaxProbeBytes:        64,
		MaxCandidateBytes:    32,
		MaxElements:          8,
		MaxSequenceDepth:     2,
		MaxFragments:         2,
		MaxDuration:          time.Second,
		MaxCandidateDuration: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("ProbeTransferSyntax() error = nil, want bounded failure")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("ProbeTransferSyntax() took %v, want bounded work", elapsed)
	}
	if strings.Contains(report.String(), phiCanary) || strings.Contains(err.Error(), phiCanary) {
		t.Fatalf("diagnostic exposed source value: report=%s err=%v", report.String(), err)
	}
	for _, candidate := range report.Candidates {
		if candidate.BytesConsumed > 32 {
			t.Fatalf("candidate %q consumed %d bytes, want <= 32", candidate.Syntax.UID, candidate.BytesConsumed)
		}
	}
}

func TestProbeTransferSyntaxTreatsPrivateTagsAsNeutralEvidence(t *testing.T) {
	t.Parallel()

	elements := append(probeElements(), core.NewRawElement(core.NewTag(0x0011, 0x1010), core.VRLO, []byte("private")))
	encoded := encodeProbeDataSet(t, transfer.ExplicitVRBigEndian, elements)
	report, err := ProbeTransferSyntax(encoded, TransferSyntaxProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeTransferSyntax() error = %v; report = %s", err, report.String())
	}
	if report.Selected.UID != transfer.ExplicitVRBigEndian.UID {
		t.Fatalf("Selected UID = %q, want %q", report.Selected.UID, transfer.ExplicitVRBigEndian.UID)
	}
	selected, ok := report.SelectedCandidate()
	if !ok || selected.PrivateElements == 0 {
		t.Fatalf("selected candidate = %+v, want private element accounted for", selected)
	}
}

func TestProbeTransferSyntaxUsesSequenceGrammarAndSkipsEarlyPixelPayload(t *testing.T) {
	t.Parallel()

	sequence := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1115), VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(core.NewTag(0x0008, 0x1150), core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.2\x00")),
		}}}},
	}
	elements := append([]core.Element(nil), probeElements()[:2]...)
	elements = append(elements,
		sequence,
		core.NewRawElement(core.TagPixelData, core.VROW, bytes.Repeat([]byte{0x01}, 2<<20)),
	)
	encoded := encodeProbeDataSet(t, transfer.ExplicitVRLittleEndian, elements)
	report, err := ProbeTransferSyntax(encoded, TransferSyntaxProbeOptions{MaxProbeBytes: 256})
	if err != nil {
		t.Fatalf("ProbeTransferSyntax() error = %v; report = %s", err, report.String())
	}
	if report.Selected.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("Selected UID = %q, want %q", report.Selected.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	selected, _ := report.SelectedCandidate()
	if selected.Reason != TransferSyntaxProbeReasonPixelDataBoundary || selected.BytesConsumed > 256 || selected.Tokens <= selected.Elements {
		t.Fatalf("selected candidate = %+v, want bounded Pixel Data boundary", selected)
	}
}

func TestProbeTransferSyntaxTreatsEvidenceLimitAsCoherentAndTimeoutAsAmbiguous(t *testing.T) {
	t.Parallel()

	elements := make([]core.Element, 0, 300)
	for i := 0; i < 300; i++ {
		elements = append(elements, core.NewRawElement(core.NewTag(0x0008, 0x0018), core.VRUI, []byte("1\x00")))
	}
	encoded := encodeProbeDataSet(t, transfer.ExplicitVRLittleEndian, elements)
	report, err := ProbeTransferSyntax(encoded, TransferSyntaxProbeOptions{})
	if err != nil {
		t.Fatalf("evidence-limited probe error = %v; report=%s", err, report.String())
	}
	selected, ok := report.SelectedCandidate()
	if !ok || selected.FailureClass != TransferSyntaxProbeFailureEvidenceBudget || selected.Elements != 256 {
		t.Fatalf("selected candidate = %+v, want 256-element evidence budget", selected)
	}

	report, err = ProbeTransferSyntax(encoded, TransferSyntaxProbeOptions{MaxDuration: time.Nanosecond})
	if !errors.Is(err, ErrTransferSyntaxProbeAmbiguous) {
		t.Fatalf("timeout error = %v, want ErrTransferSyntaxProbeAmbiguous; report=%s", err, report.String())
	}
	if report.Selected.UID != "" {
		t.Fatalf("timeout selected UID = %q, want empty", report.Selected.UID)
	}
}

func TestProbeTransferSyntaxDoesNotHideMalformedHeaderAtEvidenceBoundary(t *testing.T) {
	t.Parallel()

	valid := encodeProbeDataSet(t, transfer.ExplicitVRLittleEndian, probeElements()[:2])
	// The invalid VR ends exactly at the shared prefix boundary. A byte outside
	// the prefix makes this a bounded probe rather than an EOF-only input.
	data := append(valid, 0x10, 0x00, 0x10, 0x00, 'O', 'B', 0x01, 0x00, 0xFF)
	report, err := ProbeTransferSyntax(data, TransferSyntaxProbeOptions{
		Candidates:    []transfer.Syntax{transfer.ExplicitVRLittleEndian},
		MaxProbeBytes: int64(len(data) - 1),
	})
	if err == nil {
		t.Fatalf("ProbeTransferSyntax() selected malformed candidate: %s", report.String())
	}
	if len(report.Candidates) != 1 || report.Candidates[0].FailureClass != TransferSyntaxProbeFailureMalformed {
		t.Fatalf("candidate = %+v, want malformed rather than evidence budget", report.Candidates)
	}
}

func TestProbeTransferSyntaxPixelBoundaryHonorsElementBudget(t *testing.T) {
	t.Parallel()

	elements := append([]core.Element(nil), probeElements()[:1]...)
	elements = append(elements, core.NewRawElement(core.TagPixelData, core.VROW, bytes.Repeat([]byte{0x01}, 1024)))
	data := encodeProbeDataSet(t, transfer.ExplicitVRLittleEndian, elements)
	report, err := ProbeTransferSyntax(data, TransferSyntaxProbeOptions{
		Candidates:      []transfer.Syntax{transfer.ExplicitVRLittleEndian},
		MaxElements:     1,
		MinimumElements: 1,
	})
	if err != nil {
		t.Fatalf("ProbeTransferSyntax() error = %v; report=%s", err, report.String())
	}
	selected, ok := report.SelectedCandidate()
	if !ok || selected.Elements != 1 || selected.Reason == TransferSyntaxProbeReasonPixelDataBoundary {
		t.Fatalf("selected candidate = %+v, want one-element budget before Pixel Data", selected)
	}
}

func TestProbeTransferSyntaxDoesNotPromoteTruncatedOrRandomInput(t *testing.T) {
	t.Parallel()

	encoded := encodeProbeDataSet(t, transfer.ExplicitVRLittleEndian, probeElements())
	for cut := 1; cut <= 7; cut++ {
		report, err := ProbeTransferSyntax(encoded[:len(encoded)-cut], TransferSyntaxProbeOptions{})
		if err == nil {
			if report.Selected.UID != transfer.ExplicitVRLittleEndian.UID {
				t.Fatalf("truncation by %d selected %q", cut, report.Selected.UID)
			}
		}
	}

	random := rand.New(rand.NewSource(630))
	for i := 0; i < 128; i++ {
		data := make([]byte, 64)
		if _, err := random.Read(data); err != nil {
			t.Fatal(err)
		}
		if report, err := ProbeTransferSyntax(data, TransferSyntaxProbeOptions{}); err == nil {
			t.Fatalf("random input %d selected %q with confidence %.3f", i, report.Selected.UID, report.Confidence)
		}
	}
}

func probeElements() []core.Element {
	return []core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0016), core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.2\x00")),
		core.NewRawElement(core.NewTag(0x0008, 0x0018), core.VRUI, []byte("1.2.826.0.1.3680043.10.543.630\x00")),
		core.NewRawElement(core.NewTag(0x0008, 0x0020), core.VRDA, []byte("20260807")),
		core.NewRawElement(core.NewTag(0x0008, 0x0060), core.VRCS, []byte("CT")),
		{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS}, Value: core.Uint16Value{512}},
	}
}

func encodeProbeDataSet(t *testing.T, syntax transfer.Syntax, elements []core.Element) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := NewWriter(&buf, syntax)
	for _, element := range elements {
		if err := writer.WriteElement(element); err != nil {
			t.Fatalf("WriteElement(%s) error = %v", element.Tag(), err)
		}
	}
	return buf.Bytes()
}
