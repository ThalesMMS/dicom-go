package validation_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestHookChainTransformsFiltersAndRecordsProvenanceInOrder(t *testing.T) {
	var order []string
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name:   "normalize",
			Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				order = append(order, "normalize")
				replacement := *event.Element
				replacement.Value = core.StringValue{"NORMALIZED"}
				return validation.HookDecision{Element: &replacement}, nil
			}),
		},
		validation.HookRegistration{
			Name:   "filter-private",
			Points: []validation.HookPoint{validation.HookAfterElement},
			Hook: validation.HookFunc(func(_ context.Context, event validation.HookEvent) (validation.HookDecision, error) {
				order = append(order, "filter-private")
				if event.Element.Tag().IsPrivate() {
					return validation.HookDecision{Filter: true}, nil
				}
				return validation.HookDecision{}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dataset := core.DataSet{Elements: []core.Element{
		textElement(tagPatientID, core.VRLO, "ORIGINAL"),
		textElement(core.NewTag(0x0011, 0x1010), core.VRLO, "PRIVATE"),
	}}
	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		Mode:  validation.ModePreserve,
		Hooks: chain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DataSet.Elements) != 1 || result.DataSet.Elements[0].StringValue() != "NORMALIZED" {
		t.Fatalf("transformed dataset = %#v", result.DataSet)
	}
	if got, want := result.Report.Changes[0].Hook, "normalize"; got != want {
		t.Fatalf("first change hook = %q, want %q", got, want)
	}
	if result.Report.Count(validation.CodeHookTransformed) == 0 || result.Report.Count(validation.CodeHookFiltered) == 0 {
		t.Fatalf("hook findings = %#v", result.Report.Findings)
	}
	if len(order) != 4 || order[0] != "normalize" || order[1] != "filter-private" {
		t.Fatalf("hook order = %v", order)
	}
	if dataset.Elements[0].StringValue() != "ORIGINAL" || len(dataset.Elements) != 2 {
		t.Fatal("hook processing mutated the caller-owned dataset")
	}
}

func TestHookChainRejectsAndContainsPanic(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook validation.HookFunc
	}{
		{name: "reject", hook: func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			return validation.HookDecision{}, errors.New("secret patient value")
		}},
		{name: "panic", hook: func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			panic("secret patient value")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain, err := validation.NewHookChain(validation.HookRegistration{
				Name: "unsafe", Points: []validation.HookPoint{validation.HookAfterElement}, Hook: tc.hook,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = validation.ValidateDataSet(context.Background(), core.DataSet{Elements: []core.Element{
				textElement(tagPatientID, core.VRLO, "SECRET"),
			}}, validation.Options{Mode: validation.ModePreserve, Hooks: chain})
			if err == nil || !errors.Is(err, validation.ErrHookFailed) {
				t.Fatalf("hook error = %v, want ErrHookFailed", err)
			}
			if got := err.Error(); got == "" || contains(got, "secret patient value") {
				t.Fatalf("hook error leaked hook payload: %q", got)
			}
		})
	}
}

func TestHookFindingDropsCallerControlledDiagnosticFields(t *testing.T) {
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "safe-hook", Points: []validation.HookPoint{validation.HookAfterElement},
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			return validation.HookDecision{Findings: []validation.Finding{{
				Rule: "secret patient value", Message: "secret patient value", ExpectedVR: []core.VR{core.VRPN}, Offset: 42, OffsetSet: true,
			}}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: []core.Element{
		textElement(tagPatientID, core.VRLO, "SECRET"),
	}}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	finding := result.Report.Findings[0]
	if finding.Rule != "" || len(finding.ExpectedVR) != 0 || finding.OffsetSet || strings.Contains(finding.Message, "secret") {
		t.Fatalf("hook-controlled diagnostic fields leaked: %#v", finding)
	}
}

func TestHookTimeoutAsFindingDoesNotApplyLateMutation(t *testing.T) {
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name:      "slow",
		Points:    []validation.HookPoint{validation.HookAfterElement},
		Timeout:   10 * time.Millisecond,
		OnFailure: validation.HookFailureFinding,
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			time.Sleep(50 * time.Millisecond)
			replacement := textElement(tagPatientID, core.VRLO, "LATE")
			return validation.HookDecision{Element: &replacement}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: []core.Element{
		textElement(tagPatientID, core.VRLO, "ORIGINAL"),
	}}, validation.Options{Mode: validation.ModePreserve, Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 45*time.Millisecond {
		t.Fatalf("non-cooperative hook unexpectedly returned before completing: %s", elapsed)
	}
	if result.DataSet.Elements[0].StringValue() != "ORIGINAL" {
		t.Fatalf("timed-out hook changed value to %q", result.DataSet.Elements[0].StringValue())
	}
	assertFindingCode(t, result.Report, validation.CodeHookTimeout)
}

func TestUnsafeHookIsSerializedWhenChainIsShared(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "serialized", Points: []validation.HookPoint{validation.HookAfterElement}, ConcurrentSafe: false,
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
			return validation.HookDecision{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = validation.ValidateDataSet(context.Background(), core.DataSet{Elements: []core.Element{
				textElement(tagPatientID, core.VRLO, "ID"),
			}}, validation.Options{Mode: validation.ModePreserve, Hooks: chain})
		}()
	}
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("maximum concurrent unsafe hook calls = %d, want 1", maxActive)
	}
}

func TestHookChainRejectsActionsThatAreUnsafeForTheLifecyclePoint(t *testing.T) {
	replacement := textElement(tagPatientID, core.VRLO, "replacement")
	tests := []struct {
		name     string
		point    validation.HookPoint
		decision validation.HookDecision
	}{
		{name: "header replacement", point: validation.HookElementHeaderRead, decision: validation.HookDecision{Element: &replacement}},
		{name: "header filter", point: validation.HookElementHeaderRead, decision: validation.HookDecision{Filter: true}},
		{name: "decoded skip", point: validation.HookAfterElement, decision: validation.HookDecision{SkipValue: true}},
		{name: "post-write replacement", point: validation.HookPostWrite, decision: validation.HookDecision{Element: &replacement}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chain, err := validation.NewHookChain(validation.HookRegistration{
				Name: "invalid-action", Points: []validation.HookPoint{tc.point},
				Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
					return tc.decision, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = chain.Run(context.Background(), validation.HookEvent{
				Point: tc.point, Element: &replacement, Header: &replacement.Header,
			})
			if err == nil || !errors.Is(err, validation.ErrHookAction) {
				t.Fatalf("Run() error = %v, want ErrHookAction", err)
			}
			if strings.Contains(err.Error(), "replacement") {
				t.Fatalf("action error leaked element value: %q", err)
			}
		})
	}
}

func TestHookChainRejectsConflictingHeaderActionsAcrossHooks(t *testing.T) {
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "skip", Points: []validation.HookPoint{validation.HookElementHeaderRead},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{SkipValue: true}, nil
			}),
		},
		validation.HookRegistration{
			Name: "defer", Points: []validation.HookPoint{validation.HookElementHeaderRead},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{DeferValue: true}, nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	header := core.ElementHeader{Tag: tagPatientID, VR: core.VRLO, Length: 2, LengthSet: true}
	_, err = chain.Run(context.Background(), validation.HookEvent{Point: validation.HookElementHeaderRead, Header: &header})
	if err == nil || !errors.Is(err, validation.ErrHookAction) {
		t.Fatalf("Run() error = %v, want ErrHookAction", err)
	}
}

func TestReportFromHookResultAppliesModeBoundsAndProvenance(t *testing.T) {
	report, err := validation.ReportFromHookResult(validation.Options{
		Mode: validation.ModeWarn, MaxFindings: 2,
	}, validation.HookResult{
		Findings: []validation.Finding{
			{Code: validation.CodeHookDiagnostic, Message: "safe"},
			{Code: validation.CodeHookError, Message: "safe"},
		},
		Changes: []validation.Change{{Tag: tagPatientID, Hook: "normalize", Kind: validation.ChangeTransformed}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 2 || !report.Truncated || report.Dropped != 2 {
		t.Fatalf("bounded hook report = %#v", report)
	}
	for _, finding := range report.Findings {
		if finding.Severity == validation.SeverityError {
			t.Fatalf("warn report retained error severity: %#v", report.Findings)
		}
	}
	if len(report.Changes) != 0 {
		t.Fatalf("change beyond shared report bound was retained: %#v", report.Changes)
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
