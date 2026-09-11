package qrmatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestMatchUniversalEmptyValue(t *testing.T) {
	query := dataset(str(tags.PatientID, core.VRLO, ""))
	candidate := dataset(str(tags.PatientID, core.VRLO, "R001"))
	result, err := Match(query, candidate, Options{})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if !result.Match {
		t.Fatal("empty PatientID must universal-match any candidate")
	}
}

func TestMatchSingleValuePaddingAndVR(t *testing.T) {
	tests := []struct {
		name      string
		query     *object.Object
		candidate *object.Object
		want      bool
	}{
		{
			name:      "LO exact",
			query:     dataset(str(tags.PatientID, core.VRLO, "R001")),
			candidate: dataset(str(tags.PatientID, core.VRLO, "R001")),
			want:      true,
		},
		{
			name:      "LO trailing space ignored",
			query:     dataset(str(tags.PatientID, core.VRLO, "R001")),
			candidate: dataset(str(tags.PatientID, core.VRLO, "R001 ")),
			want:      true,
		},
		{
			name:      "LO is case-sensitive",
			query:     dataset(str(tags.PatientID, core.VRLO, "r001")),
			candidate: dataset(str(tags.PatientID, core.VRLO, "R001")),
			want:      false,
		},
		{
			name:      "PN is case-insensitive",
			query:     dataset(str(tags.PatientName, core.VRPN, "receive^patient")),
			candidate: dataset(str(tags.PatientName, core.VRPN, "RECEIVE^PATIENT")),
			want:      true,
		},
		{
			name:      "UI trailing NUL padding ignored",
			query:     dataset(str(tags.StudyInstanceUID, core.VRUI, "1.2.3")),
			candidate: dataset(str(tags.StudyInstanceUID, core.VRUI, "1.2.3\x00")),
			want:      true,
		},
		{
			name:      "CS mismatch",
			query:     dataset(str(tags.Modality, core.VRCS, "MR")),
			candidate: dataset(str(tags.Modality, core.VRCS, "CT")),
			want:      false,
		},
		{
			name:      "missing candidate attribute does not match",
			query:     dataset(str(tags.PatientID, core.VRLO, "R001")),
			candidate: dataset(),
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Match(tt.query, tt.candidate, Options{})
			if err != nil {
				t.Fatalf("Match() error = %v", err)
			}
			if result.Match != tt.want {
				t.Fatalf("Match() = %v, want %v", result.Match, tt.want)
			}
		})
	}
}

func TestMatchListOfUIDs(t *testing.T) {
	query := dataset(str(tags.SOPInstanceUID, core.VRUI, "1.2.3", "1.2.4"))
	matched, err := Match(query, dataset(str(tags.SOPInstanceUID, core.VRUI, "1.2.4")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !matched.Match {
		t.Fatal("UID list must match when the candidate UID is in the list")
	}
	miss, err := Match(query, dataset(str(tags.SOPInstanceUID, core.VRUI, "1.2.5")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if miss.Match {
		t.Fatal("UID list must not match a UID outside the list")
	}
}

func TestMatchWildcardOnlyOnAllowedVR(t *testing.T) {
	starPN, err := Match(
		dataset(str(tags.PatientName, core.VRPN, "REC*")),
		dataset(str(tags.PatientName, core.VRPN, "RECEIVE^PATIENT")),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !starPN.Match {
		t.Fatal("PN wildcard must match")
	}

	questionLO, err := Match(
		dataset(str(tags.PatientID, core.VRLO, "R00?")),
		dataset(str(tags.PatientID, core.VRLO, "R001")),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !questionLO.Match {
		t.Fatal("LO wildcard ? must match one character")
	}

	literalUID, err := Match(
		dataset(str(tags.StudyInstanceUID, core.VRUI, "1.2.*")),
		dataset(str(tags.StudyInstanceUID, core.VRUI, "1.2.3")),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if literalUID.Match {
		t.Fatal("wildcard in UI must not be treated as a pattern")
	}
	exactStarUID, err := Match(
		dataset(str(tags.StudyInstanceUID, core.VRUI, "1.2.*")),
		dataset(str(tags.StudyInstanceUID, core.VRUI, "1.2.*")),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !exactStarUID.Match {
		t.Fatal("UI value containing * must match the same literal UID")
	}
}

func TestMatchDateTimeRangesAndPartialPrecision(t *testing.T) {
	tests := []struct {
		name      string
		query     core.Element
		candidate core.Element
		want      bool
	}{
		{
			name:      "DA single day",
			query:     str(tags.StudyDate, core.VRDA, "20260604"),
			candidate: str(tags.StudyDate, core.VRDA, "20260604"),
			want:      true,
		},
		{
			name:      "DA closed range inclusive",
			query:     str(tags.StudyDate, core.VRDA, "20260101-20261231"),
			candidate: str(tags.StudyDate, core.VRDA, "20260604"),
			want:      true,
		},
		{
			name:      "DA open start",
			query:     str(tags.StudyDate, core.VRDA, "-20260604"),
			candidate: str(tags.StudyDate, core.VRDA, "20260101"),
			want:      true,
		},
		{
			name:      "DA open end",
			query:     str(tags.StudyDate, core.VRDA, "20260604-"),
			candidate: str(tags.StudyDate, core.VRDA, "20261231"),
			want:      true,
		},
		{
			name:      "DA outside range",
			query:     str(tags.StudyDate, core.VRDA, "20260101-20260131"),
			candidate: str(tags.StudyDate, core.VRDA, "20260604"),
			want:      false,
		},
		{
			name:      "DA partial year matches day in year",
			query:     str(tags.StudyDate, core.VRDA, "2026"),
			candidate: str(tags.StudyDate, core.VRDA, "20260604"),
			want:      true,
		},
		{
			name:      "DA partial year range",
			query:     str(tags.StudyDate, core.VRDA, "2025-2026"),
			candidate: str(tags.StudyDate, core.VRDA, "20260604"),
			want:      true,
		},
		{
			name:      "TM closed range inclusive",
			query:     str(tags.StudyTime, core.VRTM, "100000-120000"),
			candidate: str(tags.StudyTime, core.VRTM, "110000"),
			want:      true,
		},
		{
			name:      "TM partial hour expands upper bound",
			query:     str(tags.StudyTime, core.VRTM, "10-12"),
			candidate: str(tags.StudyTime, core.VRTM, "125959"),
			want:      true,
		},
		{
			name:      "DT range inclusive",
			query:     str(tags.AcquisitionDateTime, core.VRDT, "20260604100000-20260604120000"),
			candidate: str(tags.AcquisitionDateTime, core.VRDT, "20260604110000"),
			want:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Match(dataset(tt.query), dataset(tt.candidate), Options{})
			if err != nil {
				t.Fatalf("Match() error = %v", err)
			}
			if result.Match != tt.want {
				t.Fatalf("Match() = %v, want %v", result.Match, tt.want)
			}
		})
	}
}

func TestMatchSequenceSameItemOnly(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1140)
	classTag := tags.SOPClassUID
	instTag := tags.SOPInstanceUID

	query := dataset(seq(seqTag,
		[]core.Element{
			str(classTag, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
			str(instTag, core.VRUI, "1.2.3"),
		},
	))
	sameItem := dataset(seq(seqTag,
		[]core.Element{
			str(classTag, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
			str(instTag, core.VRUI, "1.2.3"),
		},
		[]core.Element{
			str(classTag, core.VRUI, "1.2.840.10008.5.1.4.1.1.4"),
			str(instTag, core.VRUI, "9.9.9"),
		},
	))
	result, err := Match(query, sameItem, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match {
		t.Fatal("sequence matching must succeed when one item satisfies every key")
	}

	splitItems := dataset(seq(seqTag,
		[]core.Element{str(classTag, core.VRUI, "1.2.840.10008.5.1.4.1.1.2")},
		[]core.Element{str(instTag, core.VRUI, "1.2.3")},
	))
	split, err := Match(query, splitItems, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if split.Match {
		t.Fatal("sequence matching must not combine attributes from distinct items")
	}
}

func TestMatchUnsupportedOptionalKeysDoNotFilter(t *testing.T) {
	query := dataset(
		str(tags.PatientID, core.VRLO, "R001"),
		str(core.NewTag(0x0032, 0x1060), core.VRLO, "SHOULD-NOT-FILTER"),
	)
	candidate := dataset(str(tags.PatientID, core.VRLO, "R001"))
	result, err := Match(query, candidate, Options{
		SupportedMatching: []core.Tag{tags.PatientID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match {
		t.Fatal("unsupported optional keys must not make a populated archive look empty")
	}
	if len(result.UnsupportedOptional) != 1 || result.UnsupportedOptional[0] != core.NewTag(0x0032, 0x1060) {
		t.Fatalf("UnsupportedOptional = %v", result.UnsupportedOptional)
	}
}

func TestMatchControlTagsAreNotMatchingKeys(t *testing.T) {
	query := dataset(
		str(tags.QueryRetrieveLevel, core.VRCS, "IMAGE"),
		str(tags.PatientID, core.VRLO, "R001"),
	)
	candidate := dataset(str(tags.PatientID, core.VRLO, "R001"))
	result, err := Match(query, candidate, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match {
		t.Fatal("QueryRetrieveLevel must not be compared against the candidate")
	}
}

func TestMatchAdversarialWildcardReturnsPromptly(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := Match(
			dataset(str(tags.PatientName, core.VRPN, strings.Repeat("*A", 18)+"*B")),
			dataset(str(tags.PatientName, core.VRPN, strings.Repeat("A", 60))),
			Options{Limits: Limits{MaxWildcardSteps: 50_000}},
		)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("Match() error = %v, want nil or ErrResourceLimit", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("adversarial wildcard did not return promptly")
	}
}

func TestMatchHonorsCancelAndLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Match(
		dataset(str(tags.PatientID, core.VRLO, "R001")),
		dataset(str(tags.PatientID, core.VRLO, "R001")),
		Options{Context: ctx},
	)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("Match() error = %v, want ErrCanceled", err)
	}

	tooMany := make([]string, 8)
	for i := range tooMany {
		tooMany[i] = "1.2." + string(rune('0'+i))
	}
	_, err = Match(
		dataset(str(tags.SOPInstanceUID, core.VRUI, tooMany...)),
		dataset(str(tags.SOPInstanceUID, core.VRUI, "1.2.0")),
		Options{Limits: Limits{MaxValues: 4}},
	)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("Match() error = %v, want ErrResourceLimit", err)
	}
}

func TestWildcardAllowed(t *testing.T) {
	if !WildcardAllowed(core.VRPN) || !WildcardAllowed(core.VRLO) || !WildcardAllowed(core.VRCS) {
		t.Fatal("wildcard matching is required for PN/LO/CS")
	}
	if WildcardAllowed(core.VRUI) || WildcardAllowed(core.VRDA) || WildcardAllowed(core.VRTM) || WildcardAllowed(core.VRDT) {
		t.Fatal("wildcard matching must not be allowed for UI/DA/TM/DT")
	}
}

func dataset(elems ...core.Element) *object.Object {
	return object.FromElements(elems, std.Dictionary)
}

func str(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue(values),
	}
}

func seq(tag core.Tag, items ...[]core.Element) core.Element {
	datasets := make([]core.DataSet, len(items))
	for i, elems := range items {
		datasets[i] = core.DataSet{Elements: elems}
	}
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: datasets},
	}
}
