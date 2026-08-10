package validation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestOperationFreezesHookRegistryAndAnnotatesRuleOffsets(t *testing.T) {
	lateCalls := 0
	var chain *validation.HookChain
	chain, _ = validation.NewHookChain(validation.HookRegistration{
		Name: "register-late", Points: []validation.HookPoint{validation.HookElementHeaderRead},
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			_ = chain.Add(validation.HookRegistration{
				Name: "late", Points: []validation.HookPoint{validation.HookElementHeaderRead},
				Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
					lateCalls++
					return validation.HookDecision{}, nil
				}),
			})
			return validation.HookDecision{}, nil
		}),
	})
	op, err := validation.NewOperation(context.Background(), validation.Options{Mode: validation.ModePreserve, Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	header := core.ElementHeader{Tag: tagSOPInstanceUID, VR: core.VRUI}
	path := validation.Path{{Tag: tagSOPInstanceUID, ItemIndex: validation.NoItem}}
	if _, err := op.Handle(validation.HookEvent{Point: validation.HookElementHeaderRead, Header: &header, Path: path, Offset: 132, OffsetSet: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Handle(validation.HookEvent{Point: validation.HookElementHeaderRead, Header: &header, Path: path, Offset: 132, OffsetSet: true}); err != nil {
		t.Fatal(err)
	}
	if lateCalls != 0 {
		t.Fatalf("hook added after operation start ran %d times", lateCalls)
	}

	result, err := op.ValidateParsedDataSet(core.DataSet{Elements: []core.Element{
		textElement(tagSOPInstanceUID, core.VRUI, "1..2"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range result.Report.Findings {
		if finding.Code == validation.CodeValueFormat {
			found = true
			if !finding.OffsetSet || finding.Offset != 132 {
				t.Fatalf("rule finding offset = (%d,%v), want (132,true)", finding.Offset, finding.OffsetSet)
			}
		}
	}
	if !found {
		t.Fatalf("missing value-format finding: %#v", result.Report.Findings)
	}
}

func TestOperationValidateParsedDataSetDoesNotRepeatDecodedHooks(t *testing.T) {
	calls := 0
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "decoded", Points: []validation.HookPoint{validation.HookAfterElement},
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			calls++
			return validation.HookDecision{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	op, err := validation.NewOperation(context.Background(), validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	element := textElement(tagPatientID, core.VRLO, "ID")
	if _, err := op.Handle(validation.HookEvent{Point: validation.HookAfterElement, Element: &element}); err != nil {
		t.Fatal(err)
	}
	if _, err := op.ValidateParsedDataSet(core.DataSet{Elements: []core.Element{element}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("decoded hook calls = %d, want 1", calls)
	}
}

func TestOperationStrictReturnsCombinedValidationReport(t *testing.T) {
	op, err := validation.NewOperation(context.Background(), validation.Options{Mode: validation.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	result, err := op.ValidateParsedDataSet(core.DataSet{Elements: []core.Element{
		textElement(tagSOPInstanceUID, core.VRUI, "1..2"),
	}})
	if err == nil || !errors.Is(err, validation.ErrValidationFailed) {
		t.Fatalf("error = %v, want ErrValidationFailed", err)
	}
	if result.Report.Count(validation.CodeValueFormat) != 1 {
		t.Fatalf("combined strict report = %#v", result.Report)
	}
}

func TestOperationStopFirstSkipsRulesAfterPreValidationFinding(t *testing.T) {
	ruleCalls := 0
	postCalls := 0
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "pre-finding", Points: []validation.HookPoint{validation.HookPreValidation},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{Findings: []validation.Finding{{Code: validation.CodeHookDiagnostic}}}, nil
			}),
		},
		validation.HookRegistration{
			Name: "post", Points: []validation.HookPoint{validation.HookDataSetComplete, validation.HookPostValidation},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				postCalls++
				return validation.HookDecision{}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	op, err := validation.NewOperation(context.Background(), validation.Options{
		StopFirst: true,
		Hooks:     chain,
		DataSetRules: []validation.DataSetRuleRegistration{{
			Name: "must-not-run", Rule: validation.DataSetRuleFunc(func(context.Context, validation.DataSetContext) []validation.Finding {
				ruleCalls++
				return nil
			}),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := op.ValidateParsedDataSet(core.DataSet{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Findings) != 1 || ruleCalls != 0 || postCalls != 0 {
		t.Fatalf("result=%#v ruleCalls=%d postCalls=%d", result, ruleCalls, postCalls)
	}
}

func TestOperationStopFirstStopsHookChainAndPreservesAtomicChange(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	original := core.NewRawElement(tag, core.VRLO, []byte("ORIGINAL"))
	replacement := core.NewRawElement(tag, core.VRLO, []byte("REPLACED"))
	laterCalls := 0
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "finding-and-transform", Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{
					Element:  &replacement,
					Findings: []validation.Finding{{Code: validation.CodeHookDiagnostic}},
				}, nil
			}),
		},
		validation.HookRegistration{
			Name: "later", Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				laterCalls++
				return validation.HookDecision{Filter: true}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	op, err := validation.NewOperation(context.Background(), validation.Options{StopFirst: true, Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	result, err := op.Handle(validation.HookEvent{
		Point: validation.HookAfterElement, Element: &original,
		Path: validation.Path{{Tag: tag, ItemIndex: validation.NoItem}},
	})
	if err != nil {
		t.Fatal(err)
	}
	report := op.Report()
	if laterCalls != 0 || result.Filter || result.Element == nil || result.Element.StringValue() != "REPLACED" {
		t.Fatalf("result=%#v laterCalls=%d", result, laterCalls)
	}
	if len(report.Findings) != 1 || len(report.Changes) != 1 || report.Changes[0].Kind != validation.ChangeTransformed {
		t.Fatalf("atomic finding/change report = %#v", report)
	}
}
