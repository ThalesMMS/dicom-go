package parser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestReaderValidationLifecycleDefersTransformsAndSuppressesReplay(t *testing.T) {
	patientID := core.NewTag(0x0010, 0x0020)
	patientName := core.NewTag(0x0010, 0x0010)
	sopInstanceUID := core.NewTag(0x0008, 0x0018)
	encoded := encodeLifecycleElements(t,
		core.NewRawElement(sopInstanceUID, core.VRUI, []byte("1.2.3\x00")),
		core.NewRawElement(patientName, core.VRPN, []byte("DOE^JANE")),
		core.NewRawElement(patientID, core.VRLO, []byte("IDENTIFIER")),
	)
	headerCalls := 0
	decodedCalls := 0
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "reader-lifecycle", Points: []validation.HookPoint{validation.HookElementHeaderRead, validation.HookAfterElement},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			switch event.Point {
			case validation.HookElementHeaderRead:
				headerCalls++
				if event.Header.Tag == patientID {
					return validation.HookDecision{DeferValue: true}, nil
				}
			case validation.HookAfterElement:
				decodedCalls++
				if event.Element.Tag() == patientName {
					replacement := *event.Element
					replacement.Value = core.StringValue{"ANON"}
					return validation.HookDecision{Element: &replacement}, nil
				}
			}
			return validation.HookDecision{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	source := bytes.NewReader(encoded)
	reader, err := NewReaderWithValidation(context.Background(), source, transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{
		Mode: validation.ModePreserve, Dictionary: std.Dictionary, Hooks: chain,
	})
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Elements[1].StringValue() != "ANON" {
		t.Fatalf("transformed value = %q", dataset.Elements[1].StringValue())
	}
	if dataset.Elements[2].Value != nil {
		t.Fatalf("deferred value was materialized: %T", dataset.Elements[2].Value)
	}
	if _, ok := reader.ValueLocation(patientID); !ok {
		t.Fatal("deferred hook did not record a replay location")
	}
	beforeHeaders, beforeDecoded := headerCalls, decodedCalls
	var replay bytes.Buffer
	if _, err := reader.CopyElementValueTo(sopInstanceUID, &replay); err != nil {
		t.Fatal(err)
	}
	if headerCalls != beforeHeaders || decodedCalls != beforeDecoded {
		t.Fatalf("replay fired hooks: headers %d->%d decoded %d->%d", beforeHeaders, headerCalls, beforeDecoded, decodedCalls)
	}
	if got := reader.ValidationReport(); got.Count(validation.CodeHookDeferred) != 1 || got.Count(validation.CodeHookTransformed) != 1 {
		t.Fatalf("lifecycle report = %#v", got)
	}
}

func TestReaderValidationLifecycleReportsNestedPathAndAbsoluteOffset(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	referencedUID := core.NewTag(0x0008, 0x1155)
	sequence := core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(referencedUID, core.VRUI, []byte("1..2")),
		}}}},
	}
	encoded := encodeLifecycleElements(t, sequence)
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{BaseOffset: 132}, validation.Options{
		Mode: validation.ModePreserve, Dictionary: std.Dictionary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	report := reader.ValidationReport()
	for _, finding := range report.Findings {
		if finding.Tag != referencedUID || finding.Code != validation.CodeValueFormat {
			continue
		}
		wantPath := sequenceTag.String() + "[0]/" + referencedUID.String()
		if finding.Path.String() != wantPath || !finding.OffsetSet || finding.Offset < 132 {
			t.Fatalf("nested finding = %#v, want path %q with absolute offset", finding, wantPath)
		}
		return
	}
	t.Fatalf("missing nested UID finding: %#v", report.Findings)
}

func TestSequenceLifecycleOrderMatchesMaterializedValidation(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	sequence := core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{}}},
	}
	run := func(parse bool) []validation.HookPoint {
		var trace []validation.HookPoint
		chain, err := validation.NewHookChain(validation.HookRegistration{
			Name: "trace", Points: []validation.HookPoint{validation.HookSequenceComplete, validation.HookAfterElement},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				if event.Element != nil && event.Element.Tag() == sequenceTag {
					trace = append(trace, event.Point)
				}
				return validation.HookDecision{}, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if parse {
			reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encodeLifecycleElements(t, sequence)), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ReadDataSet(); err != nil {
				t.Fatal(err)
			}
		} else if _, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: []core.Element{sequence}}, validation.Options{Hooks: chain}); err != nil {
			t.Fatal(err)
		}
		return trace
	}
	parsed, materialized := run(true), run(false)
	if len(parsed) != 2 || len(materialized) != 2 || parsed[0] != validation.HookSequenceComplete || parsed[1] != validation.HookAfterElement ||
		parsed[0] != materialized[0] || parsed[1] != materialized[1] {
		t.Fatalf("sequence lifecycle parser=%v materialized=%v", parsed, materialized)
	}
}

func TestReaderValidationOffsetsDistinguishDuplicateOccurrences(t *testing.T) {
	tag := core.NewTag(0x0008, 0x0018)
	encoded := encodeLifecycleElements(t,
		core.NewRawElement(tag, core.VRUI, []byte("1..2")),
		core.NewRawElement(tag, core.VRUI, []byte("2..3")),
	)
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Dictionary: std.Dictionary})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	var offsets []int64
	for _, finding := range reader.ValidationReport().Findings {
		if finding.Tag == tag && finding.Code == validation.CodeValueFormat {
			if !finding.OffsetSet {
				t.Fatalf("duplicate finding lacks offset: %#v", finding)
			}
			offsets = append(offsets, finding.Offset)
		}
	}
	if len(offsets) != 2 || offsets[0] >= offsets[1] {
		t.Fatalf("duplicate offsets = %v, want two increasing source offsets", offsets)
	}
}

func TestReaderValidationKeepsOffsetAfterTagTransformation(t *testing.T) {
	originalTag := core.NewTag(0x0011, 0x1010)
	replacementTag := core.NewTag(0x0011, 0x1011)
	encoded := encodeLifecycleElements(t, core.NewRawElement(originalTag, core.VRUI, []byte("1..2")))
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "retag", Points: []validation.HookPoint{validation.HookAfterElement},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			replacement := *event.Element
			replacement.Header.Tag = replacementTag
			return validation.HookDecision{Element: &replacement}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	for _, finding := range reader.ValidationReport().Findings {
		if finding.Tag == replacementTag && finding.Code == validation.CodeValueFormat {
			if !finding.OffsetSet || finding.Path.String() != replacementTag.String() {
				t.Fatalf("transformed finding = %#v", finding)
			}
			return
		}
	}
	t.Fatalf("missing transformed finding: %#v", reader.ValidationReport())
}

func TestReaderValidationLifecycleSkipAndUnsafeDefer(t *testing.T) {
	patientID := core.NewTag(0x0010, 0x0020)
	patientName := core.NewTag(0x0010, 0x0010)
	encoded := encodeLifecycleElements(t,
		core.NewRawElement(patientID, core.VRLO, []byte("IDENTIFIER")),
		core.NewRawElement(patientName, core.VRPN, []byte("DOE^JANE")),
	)
	for _, tc := range []struct {
		name     string
		decision validation.HookDecision
		wantErr  bool
	}{
		{name: "skip", decision: validation.HookDecision{SkipValue: true}},
		{name: "defer", decision: validation.HookDecision{DeferValue: true}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain, err := validation.NewHookChain(validation.HookRegistration{
				Name: "header-action", Points: []validation.HookPoint{validation.HookElementHeaderRead},
				Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
					if event.Header.Tag == patientID {
						return tc.decision, nil
					}
					return validation.HookDecision{}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := NewReaderWithValidation(context.Background(), bytes.NewBuffer(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
			if err != nil {
				t.Fatal(err)
			}
			dataset, err := reader.ReadDataSet()
			if tc.wantErr {
				if err == nil || !errors.Is(err, validation.ErrHookAction) {
					t.Fatalf("ReadDataSet error = %v, want ErrHookAction", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(dataset.Elements) != 2 || dataset.Elements[0].Value != nil || dataset.Elements[1].StringValue() != "DOE^JANE" {
				t.Fatalf("skipped data set = %#v", dataset)
			}
		})
	}
}

func TestReaderValidationRejectsHeaderChangingReplacementOfDeferredValue(t *testing.T) {
	patientID := core.NewTag(0x0010, 0x0020)
	encoded := encodeLifecycleElements(t, core.NewRawElement(patientID, core.VRLO, []byte("IDENTIFIER")))
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "defer", Points: []validation.HookPoint{validation.HookElementHeaderRead},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{DeferValue: true}, nil
			}),
		},
		validation.HookRegistration{
			Name: "retag", Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				replacement := *event.Element
				replacement.Header.Tag = core.NewTag(0x0010, 0x1000)
				return validation.HookDecision{Element: &replacement}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadDataSet()
	if err == nil || !errors.Is(err, validation.ErrHookAction) {
		t.Fatalf("ReadDataSet() error = %v, want ErrHookAction", err)
	}
}

func TestReaderValidationRejectsHookDeferredValueInsideSequenceItem(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	nestedTag := core.NewTag(0x0008, 0x1155)
	encoded := encodeLifecycleElements(t, core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(nestedTag, core.VRUI, []byte("1.2.3\x00")),
		}}}},
	})
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "defer-nested", Points: []validation.HookPoint{validation.HookElementHeaderRead},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			if event.Header.Tag == nestedTag {
				return validation.HookDecision{DeferValue: true}, nil
			}
			return validation.HookDecision{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadDataSet()
	if err == nil || !errors.Is(err, validation.ErrHookAction) {
		t.Fatalf("ReadDataSet() error = %v, want ErrHookAction", err)
	}
}

func TestReaderValidationRejectsHeaderChangeForDeferredEncapsulatedPixelData(t *testing.T) {
	encoded := encodeLifecycleElements(t, core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{OffsetTable: []byte{0, 0, 0, 0}, Fragments: [][]byte{{0xFF, 0xD8, 0xFF, 0xD9}}},
	})
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "retag-pixel", Points: []validation.HookPoint{validation.HookAfterElement},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			replacement := *event.Element
			replacement.Header.VR = core.VROW
			return validation.HookDecision{Element: &replacement}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{DeferPixelData: true}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadDataSet()
	if err == nil || !errors.Is(err, validation.ErrHookAction) {
		t.Fatalf("ReadDataSet() error = %v, want ErrHookAction", err)
	}
}

func TestReaderValidationDropsStaleOffsetAfterRetag(t *testing.T) {
	originalTag := core.NewTag(0x0011, 0x1010)
	replacementTag := core.NewTag(0x0011, 0x1011)
	encoded := encodeLifecycleElements(t,
		core.NewRawElement(originalTag, core.VRUI, []byte("1..2")),
		core.NewRawElement(originalTag, core.VRUI, []byte("2..3")),
	)
	calls := 0
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "retag-first", Points: []validation.HookPoint{validation.HookAfterElement},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			if event.Element.Tag() != originalTag || calls > 0 {
				return validation.HookDecision{}, nil
			}
			calls++
			replacement := *event.Element
			replacement.Header.Tag = replacementTag
			return validation.HookDecision{Element: &replacement}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	for _, finding := range reader.ValidationReport().Findings {
		if finding.Tag == originalTag && finding.Code == validation.CodeValueFormat {
			if !finding.OffsetSet || finding.Offset == 0 {
				t.Fatalf("remaining original-tag finding used stale offset: %#v", finding)
			}
			return
		}
	}
	t.Fatalf("missing remaining original-tag finding: %#v", reader.ValidationReport())
}

func TestReaderValidationLimitsApplyDuringParsing(t *testing.T) {
	encoded := encodeLifecycleElements(t,
		core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("DOE^JANE")),
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER")),
	)
	reader, err := NewReaderWithValidation(
		context.Background(), bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian,
		ReaderOptions{}, validation.Options{MaxElements: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadDataSet()
	if err == nil || !errors.Is(err, ErrMaxElementsExceeded) {
		t.Fatalf("ReadDataSet() error = %v, want ErrMaxElementsExceeded", err)
	}
}

func TestReaderValidationMaxElementsCountsSequenceItems(t *testing.T) {
	items := make([]core.DataSet, 9)
	sequence := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1110), VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	}
	reader, err := NewReaderWithValidation(
		context.Background(), bytes.NewReader(encodeLifecycleElements(t, sequence)), transfer.ExplicitVRLittleEndian,
		ReaderOptions{}, validation.Options{MaxElements: 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err == nil || !errors.Is(err, ErrMaxElementsExceeded) {
		t.Fatalf("ReadDataSet() error = %v, want ErrMaxElementsExceeded", err)
	}
}

func TestReaderValidationMaxDepthMatchesDataSetDepth(t *testing.T) {
	leaf := core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("ID"))
	inner := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1120), VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{leaf}}}},
	}
	outer := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1110), VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{leaf}}}},
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encodeLifecycleElements(t, outer)), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatalf("one data-set nesting level failed: %v", err)
	}
	outer.Value = core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{inner}}}}
	reader, err = NewReaderWithValidation(context.Background(), bytes.NewReader(encodeLifecycleElements(t, outer)), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err == nil || !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("nested ReadDataSet() error = %v, want ErrMaxDepthExceeded", err)
	}
}

func TestReaderValidationMaxDepthAllowsNestedEncapsulatedPixelData(t *testing.T) {
	pixelData := core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: [][]byte{{0x01, 0x02}}},
	}
	sequence := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1110), VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{pixelData}}}},
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encodeLifecycleElements(t, sequence)), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatalf("nested encapsulated Pixel Data failed at valid depth: %v", err)
	}
}

func TestReaderValidationMaxElementsDoesNotCountFragments(t *testing.T) {
	fragments := make([][]byte, 9)
	for index := range fragments {
		fragments[index] = []byte{byte(index), 0}
	}
	pixelData := core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: fragments},
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encodeLifecycleElements(t, pixelData)), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{MaxElements: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatalf("fragment count consumed MaxElements budget: %v", err)
	}
}

func TestReaderValidationReplayRebuildsNestedPath(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	targetTag := core.NewTag(0x0010, 0x0010)
	followingTag := core.NewTag(0x0010, 0x0020)
	sequence := core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(targetTag, core.VRPN, []byte("DOE^JANE")),
			core.NewRawElement(followingTag, core.VRLO, []byte("IDENTIFIER")),
		}}}},
	}
	var followingPath string
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "observe-following", Points: []validation.HookPoint{validation.HookAfterElement},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			if event.Element != nil && event.Element.Tag() == followingTag {
				followingPath = event.Path.String()
			}
			return validation.HookDecision{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encodeLifecycleElements(t, sequence)), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := reader.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reader.CopyElementValueTo(targetTag, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	want := sequenceTag.String() + "[0]/" + followingTag.String()
	if followingPath != want {
		t.Fatalf("path after replay = %q, want %q", followingPath, want)
	}
}

func TestReaderSequenceReplacementUpdatesAfterElementPath(t *testing.T) {
	originalTag := core.NewTag(0x0008, 0x1110)
	replacementTag := core.NewTag(0x0008, 0x1120)
	var afterPath string
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "retag-sequence", Points: []validation.HookPoint{validation.HookSequenceComplete},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				replacement := *event.Element
				replacement.Header.Tag = replacementTag
				return validation.HookDecision{Element: &replacement}, nil
			}),
		},
		validation.HookRegistration{
			Name: "observe-retag", Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				if event.Element.Tag() == replacementTag {
					afterPath = event.Path.String()
				}
				return validation.HookDecision{}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	sequence := core.Element{Header: core.ElementHeader{Tag: originalTag, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}}
	reader, err := NewReaderWithValidation(context.Background(), bytes.NewReader(encodeLifecycleElements(t, sequence)), transfer.ExplicitVRLittleEndian, ReaderOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Elements) != 1 || result.Elements[0].Tag() != replacementTag || afterPath != replacementTag.String() {
		t.Fatalf("replacement tag/path = %v/%q, want %v/%q", result.Elements, afterPath, replacementTag, replacementTag.String())
	}
}

func encodeLifecycleElements(t *testing.T, elements ...core.Element) []byte {
	t.Helper()
	var encoded bytes.Buffer
	writer := NewWriter(&encoded, transfer.ExplicitVRLittleEndian)
	for _, element := range elements {
		if err := writer.WriteElement(element); err != nil {
			t.Fatal(err)
		}
	}
	return encoded.Bytes()
}

var _ io.Reader = (*bytes.Buffer)(nil)
