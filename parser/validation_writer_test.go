package parser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestValidationWriterTransformsAndReportsCommittedBytes(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	postCalls := 0
	var postBytes int64
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "transform", Points: []validation.HookPoint{validation.HookPreSerialization},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				replacement := core.NewRawElement(tag, core.VRLO, []byte("REPLACED"))
				return validation.HookDecision{Element: &replacement}, nil
			}),
		},
		validation.HookRegistration{
			Name: "measure", Points: []validation.HookPoint{validation.HookPostWrite},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				postCalls++
				postBytes = event.BytesWritten
				if !event.WriteComplete {
					t.Error("successful write was not marked complete")
				}
				return validation.HookDecision{}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer, err := NewWriterWithValidation(context.Background(), &output, transfer.ExplicitVRLittleEndian, WriterOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteElement(core.NewRawElement(tag, core.VRLO, []byte("ORIGINAL"))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte("REPLACED")) || bytes.Contains(output.Bytes(), []byte("ORIGINAL")) {
		t.Fatalf("serialized bytes = %q", output.Bytes())
	}
	if postCalls != 1 || postBytes != int64(output.Len()) || writer.BytesWritten() != int64(output.Len()) {
		t.Fatalf("measurement calls=%d event=%d total=%d output=%d", postCalls, postBytes, writer.BytesWritten(), output.Len())
	}
	if report := writer.ValidationReport(); report.Count(validation.CodeHookTransformed) != 1 {
		t.Fatalf("writer report = %#v", report)
	}
}

func TestValidationWriterReportsPartialFailureAndPostCommitHookFailure(t *testing.T) {
	element := core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER"))
	t.Run("partial destination failure", func(t *testing.T) {
		destinationErr := errors.New("destination unavailable")
		destination := &failAfterWriter{limit: 6, err: destinationErr}
		var outcome validation.HookEvent
		chain, _ := validation.NewHookChain(validation.HookRegistration{
			Name: "observe", Points: []validation.HookPoint{validation.HookPostWrite},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				outcome = event
				return validation.HookDecision{}, nil
			}),
		})
		writer, err := NewWriterWithValidation(context.Background(), destination, transfer.ExplicitVRLittleEndian, WriterOptions{}, validation.Options{Hooks: chain})
		if err != nil {
			t.Fatal(err)
		}
		err = writer.WriteElement(element)
		if !errors.Is(err, destinationErr) {
			t.Fatalf("write error = %v, want destination error", err)
		}
		var operationErr *ValidationWriteError
		if !errors.As(err, &operationErr) || operationErr.Complete || operationErr.BytesWritten != 6 {
			t.Fatalf("operation error = %#v", operationErr)
		}
		if outcome.WriteComplete || outcome.BytesWritten != 6 {
			t.Fatalf("post-write outcome = %#v", outcome)
		}
	})

	t.Run("post hook after complete write", func(t *testing.T) {
		chain, _ := validation.NewHookChain(validation.HookRegistration{
			Name: "post-failure", Points: []validation.HookPoint{validation.HookPostWrite},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{}, errors.New("hook private payload")
			}),
		})
		var output bytes.Buffer
		writer, _ := NewWriterWithValidation(context.Background(), &output, transfer.ExplicitVRLittleEndian, WriterOptions{}, validation.Options{Hooks: chain})
		err := writer.WriteElement(element)
		var operationErr *ValidationWriteError
		if !errors.As(err, &operationErr) || !operationErr.Complete || operationErr.BytesWritten != int64(output.Len()) {
			t.Fatalf("post-hook operation error = %#v, raw=%v", operationErr, err)
		}
		if bytes.Contains([]byte(err.Error()), []byte("private payload")) {
			t.Fatalf("post-hook error leaked payload: %q", err)
		}
	})
}

func TestValidationWriterStrictFailureWritesNothingAndDefaultBytesMatch(t *testing.T) {
	invalid := core.NewRawElement(core.NewTag(0x0008, 0x0018), core.VRUI, []byte("1..2"))
	var output bytes.Buffer
	strict, err := NewWriterWithValidation(context.Background(), &output, transfer.ExplicitVRLittleEndian, WriterOptions{}, validation.Options{Mode: validation.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	if err := strict.WriteElement(invalid); err == nil || !errors.Is(err, validation.ErrValidationFailed) {
		t.Fatalf("strict write error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("strict invalid write committed %d bytes", output.Len())
	}

	valid := core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER"))
	var ordinary, observed bytes.Buffer
	if err := NewWriter(&ordinary, transfer.ExplicitVRLittleEndian).WriteElement(valid); err != nil {
		t.Fatal(err)
	}
	writer, err := NewWriterWithValidation(context.Background(), &observed, transfer.ExplicitVRLittleEndian, WriterOptions{}, validation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteElement(valid); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ordinary.Bytes(), observed.Bytes()) {
		t.Fatalf("validation changed bytes:\nordinary % x\nobserved % x", ordinary.Bytes(), observed.Bytes())
	}
}

func TestValidationWriterRejectsHeaderChangeForDeferredValueBeforeWrite(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	deferred := core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VROB, Length: 4, LengthSet: true}}
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "change-vr", Points: []validation.HookPoint{validation.HookPreSerialization},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			replacement := *event.Element
			replacement.Header.VR = core.VROW
			return validation.HookDecision{Element: &replacement}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	providerCalled := false
	writer, err := NewWriterWithValidation(context.Background(), &output, transfer.ExplicitVRLittleEndian, WriterOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	err = writer.WriteDeferredElement(deferred, func(io.Writer) (int64, error) {
		providerCalled = true
		return 4, nil
	})
	if err == nil || !errors.Is(err, validation.ErrHookAction) {
		t.Fatalf("WriteDeferredElement() error = %v, want ErrHookAction", err)
	}
	if providerCalled || output.Len() != 0 {
		t.Fatalf("rejected deferred replacement called provider=%v and wrote %d bytes", providerCalled, output.Len())
	}
}

func TestValidationWriterAppliesSerializationHooksInsideSequences(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	patientID := core.NewTag(0x0010, 0x0020)
	privateTag := core.NewTag(0x0011, 0x1010)
	dataset := core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(patientID, core.VRLO, []byte("ORIGINAL")),
			core.NewRawElement(privateTag, core.VRLO, []byte("PRIVATE")),
		}}}},
	}}}
	var postPaths []string
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "nested-deid", Points: []validation.HookPoint{validation.HookPreSerialization},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				switch event.Element.Tag() {
				case patientID:
					replacement := *event.Element
					replacement.Value = core.RawValue("REPLACED")
					return validation.HookDecision{Element: &replacement}, nil
				case privateTag:
					return validation.HookDecision{Filter: true}, nil
				default:
					return validation.HookDecision{}, nil
				}
			}),
		},
		validation.HookRegistration{
			Name: "nested-measure", Points: []validation.HookPoint{validation.HookPostWrite},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				postPaths = append(postPaths, event.Path.String())
				return validation.HookDecision{}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer, err := NewWriterWithValidation(context.Background(), &output, transfer.ExplicitVRLittleEndian, WriterOptions{}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteDataSet(dataset); err != nil {
		t.Fatal(err)
	}
	parsed, err := NewReader(bytes.NewReader(output.Bytes()), transfer.ExplicitVRLittleEndian, ReaderOptions{}).ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	sequence := parsed.Elements[0].Value.(core.SequenceValue)
	if len(sequence.Items) != 1 || len(sequence.Items[0].Elements) != 1 || sequence.Items[0].Elements[0].StringValue() != "REPLACED" {
		t.Fatalf("serialized nested data set = %#v", parsed)
	}
	wantNestedPath := sequenceTag.String() + "[0]/" + patientID.String()
	foundNestedPost := false
	for _, path := range postPaths {
		if path == wantNestedPath {
			foundNestedPost = true
		}
	}
	if !foundNestedPost {
		t.Fatalf("post-write paths = %v, missing %q", postPaths, wantNestedPath)
	}
	report := writer.ValidationReport()
	if report.Count(validation.CodeHookTransformed) != 1 || report.Count(validation.CodeHookFiltered) != 1 {
		t.Fatalf("nested provenance report = %#v", report)
	}
}

func TestValidationWriterMeasuresNestedElementsFromFileStart(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	firstTag := core.NewTag(0x0010, 0x0010)
	secondTag := core.NewTag(0x0010, 0x0020)
	dataset := core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(firstTag, core.VRPN, []byte("DOE^JANE")),
			core.NewRawElement(secondTag, core.VRLO, []byte("IDENTIFIER")),
		}}}},
	}}}
	type measurement struct {
		offset int64
		bytes  int64
	}
	for _, test := range []struct {
		name   string
		policy LengthPolicy
	}{
		{name: "defined length", policy: LengthPolicyPreserve},
		{name: "undefined length", policy: LengthPolicyUndefined},
	} {
		t.Run(test.name, func(t *testing.T) {
			measured := make(map[core.Tag]measurement)
			chain, err := validation.NewHookChain(validation.HookRegistration{
				Name: "measure-nested", Points: []validation.HookPoint{validation.HookPostWrite},
				Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
					if event.Header == nil {
						return validation.HookDecision{}, nil
					}
					if tag := event.Header.Tag; tag == firstTag || tag == secondTag {
						measured[tag] = measurement{offset: event.Offset, bytes: event.BytesWritten}
					}
					return validation.HookDecision{}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			writer, err := NewWriterWithValidation(context.Background(), &output, transfer.ExplicitVRLittleEndian, WriterOptions{LengthPolicy: test.policy}, validation.Options{Hooks: chain})
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteDataSet(dataset); err != nil {
				t.Fatal(err)
			}
			want := map[core.Tag]measurement{
				firstTag:  {offset: 20, bytes: 16},
				secondTag: {offset: 36, bytes: 18},
			}
			if !reflect.DeepEqual(measured, want) {
				t.Fatalf("nested measurements = %#v, want %#v", measured, want)
			}
			if _, err := NewReader(bytes.NewReader(output.Bytes()), transfer.ExplicitVRLittleEndian, ReaderOptions{}).ReadDataSet(); err != nil {
				t.Fatalf("output did not parse: %v", err)
			}
		})
	}
}

func TestValidationWriterDefinedLengthNestedFailuresReportCommittedBytes(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	firstTag := core.NewTag(0x0010, 0x0010)
	secondTag := core.NewTag(0x0010, 0x0020)
	dataset := core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(firstTag, core.VRPN, []byte("DOE^JANE")),
			core.NewRawElement(secondTag, core.VRLO, []byte("IDENTIFIER")),
		}}}},
	}}}
	for _, test := range []struct {
		name  string
		limit int
	}{
		{name: "sequence header failure", limit: 6},
		{name: "item value failure", limit: 26},
	} {
		t.Run(test.name, func(t *testing.T) {
			destinationErr := errors.New("destination unavailable")
			destination := &failAfterWriter{limit: test.limit, err: destinationErr}
			outcomes := make(map[core.Tag]validation.HookEvent)
			committedAtHook := make(map[core.Tag]int)
			chain, err := validation.NewHookChain(validation.HookRegistration{
				Name: "measure-defined-nested-failure", Points: []validation.HookPoint{validation.HookPostWrite},
				Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
					if event.Header != nil {
						outcomes[event.Header.Tag] = event
						committedAtHook[event.Header.Tag] = destination.wrote
					}
					return validation.HookDecision{}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			writer, err := NewWriterWithValidation(context.Background(), destination, transfer.ExplicitVRLittleEndian, WriterOptions{LengthPolicy: LengthPolicyPreserve}, validation.Options{Hooks: chain})
			if err != nil {
				t.Fatal(err)
			}
			err = writer.WriteDataSet(dataset)
			if !errors.Is(err, destinationErr) {
				t.Fatalf("WriteDataSet() error = %v, want destination error", err)
			}
			if writer.BytesWritten() != int64(test.limit) {
				t.Fatalf("BytesWritten() = %d, want %d", writer.BytesWritten(), test.limit)
			}
			var operationErr *ValidationWriteError
			if !errors.As(err, &operationErr) || operationErr.BytesWritten != int64(test.limit) || operationErr.Complete {
				t.Fatalf("operation error = %#v", operationErr)
			}
			for _, tag := range []core.Tag{firstTag, secondTag} {
				if _, emitted := outcomes[tag]; emitted {
					t.Fatalf("post-write hook for uncommitted nested element %s was emitted", tag)
				}
			}
			sequence := outcomes[sequenceTag]
			if sequence.Offset != 0 || sequence.BytesWritten != int64(test.limit) || sequence.WriteComplete {
				t.Fatalf("sequence outcome = %#v", sequence)
			}
			if committedAtHook[sequenceTag] != test.limit {
				t.Fatalf("sequence post-write hook ran after %d committed bytes, want %d", committedAtHook[sequenceTag], test.limit)
			}
		})
	}
}

func TestValidationWriterDefinedLengthNestedPostWritesFollowParentCommit(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1110)
	firstTag := core.NewTag(0x0010, 0x0010)
	secondTag := core.NewTag(0x0010, 0x0020)
	dataset := core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(firstTag, core.VRPN, []byte("DOE^JANE")),
			core.NewRawElement(secondTag, core.VRLO, []byte("IDENTIFIER")),
		}}}},
	}}}
	var output bytes.Buffer
	committedAtHook := make(map[core.Tag]int)
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "measure-defined-nested-commit", Points: []validation.HookPoint{validation.HookPostWrite},
		Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
			if event.Header != nil {
				committedAtHook[event.Header.Tag] = output.Len()
			}
			return validation.HookDecision{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewWriterWithValidation(context.Background(), &output, transfer.ExplicitVRLittleEndian, WriterOptions{LengthPolicy: LengthPolicyPreserve}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteDataSet(dataset); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []core.Tag{firstTag, secondTag, sequenceTag} {
		if committedAtHook[tag] != output.Len() {
			t.Fatalf("post-write hook for %s ran after %d committed bytes, want %d", tag, committedAtHook[tag], output.Len())
		}
	}
}
