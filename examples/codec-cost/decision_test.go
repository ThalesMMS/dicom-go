package codeccost

import (
	"testing"
	"time"
)

func TestDecideHelperWhenPrototypeImprovesInteractiveP95(t *testing.T) {
	report := CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Backends: []string{"jpegxl-cli", "jpegxl-pipe", "jpegxl-helper-proto"},
		Cohorts: []CohortSummary{
			{
				Name:    "jpegxl-gray16-warm",
				Backend: "jpegxl-cli",
				Mode:    ModeWarm,
				Median: StageMedians{
					Prepare:    8_000_000,
					Launch:     12_000_000,
					Execute:    4_000_000,
					Convert:    2_000_000,
					FirstFrame: 30_000_000,
				},
				P95: StageMedians{FirstFrame: 40_000_000},
			},
			{
				Name:    "jpegxl-gray16-warm",
				Backend: "jpegxl-helper-proto",
				Mode:    ModeWarm,
				Median:  StageMedians{FirstFrame: 6_000_000, Execute: 4_000_000},
				P95:     StageMedians{FirstFrame: 9_000_000},
			},
			{
				Name:    "jpegxl-series-switch",
				Backend: "jpegxl-cli",
				Mode:    ModeSeriesSwitch,
				Median:  StageMedians{FirstFrame: 35_000_000, Launch: 12_000_000, Prepare: 8_000_000},
				P95:     StageMedians{FirstFrame: 45_000_000},
			},
			{
				Name:    "jpegxl-series-switch",
				Backend: "jpegxl-helper-proto",
				Mode:    ModeSeriesSwitch,
				Median:  StageMedians{FirstFrame: 7_000_000},
				P95:     StageMedians{FirstFrame: 11_000_000},
			},
			{
				Name:    "jpegxl-gray16-cold",
				Backend: "jpegxl-cli",
				Mode:    ModeCold,
				Median:  StageMedians{FirstFrame: 50_000_000, Launch: 12_000_000, Prepare: 8_000_000},
			},
			{
				Name:    "jpegxl-gray16-cold",
				Backend: "jpegxl-helper-proto",
				Mode:    ModeCold,
				Median:  StageMedians{FirstFrame: 20_000_000},
			},
		},
	}
	decision := Decide(report)
	if decision.Choice != DecisionHelper {
		t.Fatalf("Decide() = %s (%s), want %s", decision.Choice, decision.Reason, DecisionHelper)
	}
}

func TestDecidePointOptimizeWhenPrepareIsUniqueBottleneck(t *testing.T) {
	report := CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Cohorts: []CohortSummary{
			warmCLI("jpegxl-gray16-warm", StageMedians{Prepare: 20_000_000, Launch: 3_000_000, Execute: 4_000_000, Convert: 1_000_000, FirstFrame: 30_000_000}),
			warmCLI("jpegxl-rgb8-lossy-warm", StageMedians{Prepare: 18_000_000, Launch: 3_000_000, Execute: 5_000_000, Convert: 1_000_000, FirstFrame: 28_000_000}),
			warmCLI("jpegxl-large-gray16-warm", StageMedians{Prepare: 40_000_000, Launch: 3_000_000, Execute: 8_000_000, Convert: 2_000_000, FirstFrame: 55_000_000}),
		},
	}
	decision := Decide(report)
	if decision.Choice != DecisionPointOptimize {
		t.Fatalf("Decide() = %s (%s), want %s", decision.Choice, decision.Reason, DecisionPointOptimize)
	}
}

func TestDecidePointOptimizeWhenPrepareExceedsEveryExclusiveStage(t *testing.T) {
	report := CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Cohorts: []CohortSummary{
			warmCLI("jpegxl-gray16-warm", StageMedians{
				Preflight: 2_000_000, Admission: 1_000_000, Prepare: 20_000_000,
				Launch: 3_000_000, Execute: 4_000_000, Convert: 1_000_000, Deliver: 2_000_000,
				FirstFrame: 33_000_000,
			}),
			warmCLI("jpegxl-rgb8-lossy-warm", StageMedians{
				Preflight: 1_000_000, Admission: 1_000_000, Prepare: 18_000_000,
				Launch: 3_000_000, Execute: 5_000_000, Convert: 1_000_000, Deliver: 1_000_000,
				FirstFrame: 30_000_000,
			}),
			warmCLI("jpegxl-large-gray16-warm", StageMedians{
				Preflight: 2_000_000, Admission: 1_000_000, Prepare: 40_000_000,
				Launch: 3_000_000, Execute: 8_000_000, Convert: 2_000_000, Deliver: 3_000_000,
				FirstFrame: 59_000_000,
			}),
		},
	}
	decision := Decide(report)
	if decision.Choice != DecisionPointOptimize {
		t.Fatalf("Decide() = %s (%s), want %s when prepare exceeds every exclusive stage", decision.Choice, decision.Reason, DecisionPointOptimize)
	}
}

func TestDecideKeepCLIWhenPrepareIsNotUniqueAmongExclusiveStages(t *testing.T) {
	base := StageMedians{
		Preflight: 1_000_000, Admission: 1_000_000, Prepare: 20_000_000,
		Launch: 3_000_000, Execute: 4_000_000, Convert: 1_000_000, Deliver: 1_000_000,
		FirstFrame: 31_000_000,
	}
	tests := []struct {
		name   string
		mutate func(*StageMedians)
	}{
		{"preflight", func(m *StageMedians) { m.Preflight = 25_000_000 }},
		{"admission", func(m *StageMedians) { m.Admission = 25_000_000 }},
		{"deliver", func(m *StageMedians) { m.Deliver = 25_000_000 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gray := base
			tc.mutate(&gray)
			report := CampaignReport{
				Criteria: DefaultPromotionCriteria(),
				Cohorts: []CohortSummary{
					warmCLI("jpegxl-gray16-warm", gray),
					warmCLI("jpegxl-rgb8-lossy-warm", StageMedians{Prepare: 18_000_000, Launch: 3_000_000, Execute: 5_000_000, Convert: 1_000_000, FirstFrame: 28_000_000}),
					warmCLI("jpegxl-large-gray16-warm", StageMedians{Prepare: 40_000_000, Launch: 3_000_000, Execute: 8_000_000, Convert: 2_000_000, FirstFrame: 55_000_000}),
				},
			}
			decision := Decide(report)
			if decision.Choice == DecisionPointOptimize {
				t.Fatalf("Decide() = %s (%s), prepare must not be unique when %s is larger", decision.Choice, decision.Reason, tc.name)
			}
		})
	}
}

func TestExclusiveStagesExceptPrepareOmitsOnlyPrepare(t *testing.T) {
	m := StageMedians{
		Preflight: 1, Admission: 2, Prepare: 99, Launch: 3, Execute: 4, Convert: 5, Deliver: 6,
	}
	others := exclusiveStagesExceptPrepare(m)
	if got, want := len(others), len(ExclusiveStages())-1; got != want {
		t.Fatalf("exclusiveStagesExceptPrepare len = %d, want %d", got, want)
	}
	seen := map[time.Duration]int{}
	for _, d := range others {
		if d == 0 {
			t.Fatal("exclusive stage missing from StageMedians mapping")
		}
		if d == 99 {
			t.Fatal("prepare was compared against itself")
		}
		seen[d]++
	}
	for _, want := range []time.Duration{1, 2, 3, 4, 5, 6} {
		if seen[want] != 1 {
			t.Fatalf("exclusive duration %d counted %d times", want, seen[want])
		}
	}
}

func TestDecideKeepCLIWhenRequiredWarmSizeCohortMissing(t *testing.T) {
	report := CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Skips:    []Skip{{Cohort: "jpegxl-large-gray16", Reason: "cjxl not found; large synthetic cohort skipped"}},
		Cohorts: []CohortSummary{
			warmCLI("jpegxl-gray16-warm", StageMedians{Prepare: 20_000_000, Launch: 3_000_000, Execute: 4_000_000, Convert: 1_000_000, FirstFrame: 30_000_000}),
			warmCLI("jpegxl-rgb8-lossy-warm", StageMedians{Prepare: 18_000_000, Launch: 3_000_000, Execute: 5_000_000, Convert: 1_000_000, FirstFrame: 28_000_000}),
		},
	}
	decision := Decide(report)
	if decision.Choice != DecisionKeepCLI {
		t.Fatalf("Decide() = %s (%s), want %s when jpegxl-large-gray16-warm is missing", decision.Choice, decision.Reason, DecisionKeepCLI)
	}
}

func TestDecideHelperDoesNotCountPrepareTwiceInColdAllowance(t *testing.T) {
	report := CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Cohorts: []CohortSummary{
			{
				Name:    "jpegxl-gray16-warm",
				Backend: "jpegxl-cli",
				Mode:    ModeWarm,
				Median:  StageMedians{Prepare: 8_000_000, Launch: 12_000_000, Execute: 4_000_000, Convert: 2_000_000, FirstFrame: 30_000_000},
				P95:     StageMedians{FirstFrame: 40_000_000},
			},
			{
				Name:    "jpegxl-gray16-warm",
				Backend: "jpegxl-helper-proto",
				Mode:    ModeWarm,
				Median:  StageMedians{FirstFrame: 6_000_000, Execute: 4_000_000},
				P95:     StageMedians{FirstFrame: 9_000_000},
			},
			{
				Name:    "jpegxl-series-switch",
				Backend: "jpegxl-cli",
				Mode:    ModeSeriesSwitch,
				Median:  StageMedians{FirstFrame: 35_000_000, Launch: 12_000_000, Prepare: 8_000_000},
				P95:     StageMedians{FirstFrame: 45_000_000},
			},
			{
				Name:    "jpegxl-series-switch",
				Backend: "jpegxl-helper-proto",
				Mode:    ModeSeriesSwitch,
				Median:  StageMedians{FirstFrame: 7_000_000},
				P95:     StageMedians{FirstFrame: 11_000_000},
			},
			{
				Name:    "jpegxl-gray16-cold",
				Backend: "jpegxl-cli",
				Mode:    ModeCold,
				Median:  StageMedians{FirstFrame: 50_000_000, Launch: 10_000_000, Prepare: 20_000_000},
			},
			{
				Name:    "jpegxl-gray16-cold",
				Backend: "jpegxl-helper-proto",
				Mode:    ModeCold,
				Median:  StageMedians{FirstFrame: 61_000_000},
			},
		},
	}
	decision := Decide(report)
	if decision.Choice == DecisionHelper {
		t.Fatalf("Decide() = %s (%s), helper cold first-frame exceeds CLI first-frame plus one launch", decision.Choice, decision.Reason)
	}
}

func TestDecideKeepCLIWhenHelperComparisonDurationsAreZero(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CampaignReport)
	}{
		{"helper warm p95", func(r *CampaignReport) { setFirstFrame(r, cohortWarm, backendHelper, ModeWarm, true, 0) }},
		{"cli warm p95", func(r *CampaignReport) { setFirstFrame(r, cohortWarm, backendCLI, ModeWarm, true, 0) }},
		{"helper switch p95", func(r *CampaignReport) {
			setFirstFrame(r, cohortSeriesSwitch, backendHelper, ModeSeriesSwitch, true, 0)
		}},
		{"cli switch p95", func(r *CampaignReport) { setFirstFrame(r, cohortSeriesSwitch, backendCLI, ModeSeriesSwitch, true, 0) }},
		{"helper cold first-frame", func(r *CampaignReport) { setFirstFrame(r, cohortCold, backendHelper, ModeCold, false, 0) }},
		{"cli cold first-frame", func(r *CampaignReport) { setFirstFrame(r, cohortCold, backendCLI, ModeCold, false, 0) }},
		{"cli cold launch", func(r *CampaignReport) {
			for i := range r.Cohorts {
				if r.Cohorts[i].Name == cohortCold && r.Cohorts[i].Backend == backendCLI && r.Cohorts[i].Mode == ModeCold {
					r.Cohorts[i].Median.Launch = 0
				}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := jpegxlHelperPromotionReport()
			tc.mutate(&report)
			decision := Decide(report)
			if decision.Choice == DecisionHelper {
				t.Fatalf("Decide() = %s (%s), missing duration must not recommend helper", decision.Choice, decision.Reason)
			}
		})
	}
}

func TestDecideHelperWhenOptionalHelperCohortIsSkipped(t *testing.T) {
	report := jpegxlHelperPromotionReport()
	report.Skips = []Skip{{Backend: backendHelper, Cohort: "jpegxl-large-gray16", Reason: "optional large helper cohort skipped"}}
	decision := Decide(report)
	if decision.Choice != DecisionHelper {
		t.Fatalf("Decide() = %s (%s), want %s when only an optional helper cohort is skipped", decision.Choice, decision.Reason, DecisionHelper)
	}
}

func TestDecideKeepCLIWhenHelperUnavailable(t *testing.T) {
	report := CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Skips:    []Skip{{Backend: "jpegxl-helper-proto", Reason: "libjxl helper compile failed"}},
		Cohorts: []CohortSummary{
			warmCLI("jpegxl-gray16-warm", StageMedians{Prepare: 1_000_000, Launch: 2_000_000, Execute: 20_000_000, Convert: 1_000_000, FirstFrame: 25_000_000}),
		},
	}
	decision := Decide(report)
	if decision.Choice != DecisionKeepCLI {
		t.Fatalf("Decide() = %s (%s), want %s", decision.Choice, decision.Reason, DecisionKeepCLI)
	}
}

func TestDecideKeepCLIWhenHelperDoesNotImproveInteractiveP95(t *testing.T) {
	report := CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Cohorts: []CohortSummary{
			{
				Name: "jpegxl-gray16-warm", Backend: "jpegxl-cli", Mode: ModeWarm,
				Median: StageMedians{Prepare: 8_000_000, Launch: 12_000_000, Execute: 4_000_000, FirstFrame: 30_000_000},
				P95:    StageMedians{FirstFrame: 12_000_000},
			},
			{
				Name: "jpegxl-gray16-warm", Backend: "jpegxl-helper-proto", Mode: ModeWarm,
				Median: StageMedians{FirstFrame: 6_000_000},
				P95:    StageMedians{FirstFrame: 40_000_000},
			},
			{
				Name: "jpegxl-series-switch", Backend: "jpegxl-cli", Mode: ModeSeriesSwitch,
				Median: StageMedians{FirstFrame: 35_000_000, Launch: 12_000_000, Prepare: 8_000_000},
				P95:    StageMedians{FirstFrame: 20_000_000},
			},
			{
				Name: "jpegxl-series-switch", Backend: "jpegxl-helper-proto", Mode: ModeSeriesSwitch,
				Median: StageMedians{FirstFrame: 7_000_000},
				P95:    StageMedians{FirstFrame: 50_000_000},
			},
			{
				Name: "jpegxl-gray16-cold", Backend: "jpegxl-cli", Mode: ModeCold,
				Median: StageMedians{FirstFrame: 50_000_000, Launch: 12_000_000, Prepare: 8_000_000},
			},
			{
				Name: "jpegxl-gray16-cold", Backend: "jpegxl-helper-proto", Mode: ModeCold,
				Median: StageMedians{FirstFrame: 20_000_000},
			},
		},
	}
	decision := Decide(report)
	if decision.Choice != DecisionKeepCLI {
		t.Fatalf("Decide() = %s (%s), want %s", decision.Choice, decision.Reason, DecisionKeepCLI)
	}
}

func warmCLI(name string, medians StageMedians) CohortSummary {
	return CohortSummary{Name: name, Backend: "jpegxl-cli", Mode: ModeWarm, Median: medians, P95: StageMedians{FirstFrame: medians.FirstFrame}}
}

func jpegxlHelperPromotionReport() CampaignReport {
	return CampaignReport{
		Criteria: DefaultPromotionCriteria(),
		Cohorts: []CohortSummary{
			{
				Name:    cohortWarm,
				Backend: backendCLI,
				Mode:    ModeWarm,
				Median:  StageMedians{Prepare: 8_000_000, Launch: 12_000_000, Execute: 4_000_000, Convert: 2_000_000, FirstFrame: 30_000_000},
				P95:     StageMedians{FirstFrame: 40_000_000},
			},
			{
				Name:    cohortWarm,
				Backend: backendHelper,
				Mode:    ModeWarm,
				Median:  StageMedians{FirstFrame: 6_000_000, Execute: 4_000_000},
				P95:     StageMedians{FirstFrame: 9_000_000},
			},
			{
				Name:    cohortSeriesSwitch,
				Backend: backendCLI,
				Mode:    ModeSeriesSwitch,
				Median:  StageMedians{FirstFrame: 35_000_000, Launch: 12_000_000, Prepare: 8_000_000},
				P95:     StageMedians{FirstFrame: 45_000_000},
			},
			{
				Name:    cohortSeriesSwitch,
				Backend: backendHelper,
				Mode:    ModeSeriesSwitch,
				Median:  StageMedians{FirstFrame: 7_000_000},
				P95:     StageMedians{FirstFrame: 11_000_000},
			},
			{
				Name:    cohortCold,
				Backend: backendCLI,
				Mode:    ModeCold,
				Median:  StageMedians{FirstFrame: 50_000_000, Launch: 12_000_000, Prepare: 8_000_000},
			},
			{
				Name:    cohortCold,
				Backend: backendHelper,
				Mode:    ModeCold,
				Median:  StageMedians{FirstFrame: 20_000_000},
			},
		},
	}
}

func setFirstFrame(report *CampaignReport, name, backend, mode string, p95 bool, value int64) {
	for i := range report.Cohorts {
		if report.Cohorts[i].Name != name || report.Cohorts[i].Backend != backend || report.Cohorts[i].Mode != mode {
			continue
		}
		if p95 {
			report.Cohorts[i].P95.FirstFrame = time.Duration(value)
			return
		}
		report.Cohorts[i].Median.FirstFrame = time.Duration(value)
		return
	}
}
