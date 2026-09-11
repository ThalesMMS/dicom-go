package codeccost

import (
	"fmt"
	"time"
)

const (
	DecisionKeepCLI       = "keep-cli"
	DecisionPointOptimize = "point-optimize"
	DecisionHelper        = "recommend-helper"

	backendCLI    = "jpegxl-cli"
	backendPipe   = "jpegxl-pipe"
	backendHelper = "jpegxl-helper-proto"

	cohortWarm         = "jpegxl-gray16-warm"
	cohortRGBWarm      = "jpegxl-rgb8-lossy-warm"
	cohortLargeWarm    = "jpegxl-large-gray16-warm"
	cohortSeriesSwitch = "jpegxl-series-switch"
	cohortCold         = "jpegxl-gray16-cold"
)

// PromotionCriteria are fixed before a campaign is compared. They compare
// measured quantities and do not inject a gain percentage.
type PromotionCriteria struct {
	WarmCohort         string   `json:"warmCohort"`
	SeriesSwitchCohort string   `json:"seriesSwitchCohort"`
	ColdCohort         string   `json:"coldCohort"`
	CLIBackend         string   `json:"cliBackend"`
	HelperBackend      string   `json:"helperBackend"`
	WarmSizeCohorts    []string `json:"warmSizeCohorts,omitempty"`
}

// DefaultPromotionCriteria is the issue #923 promotion contract.
func DefaultPromotionCriteria() PromotionCriteria {
	return PromotionCriteria{
		WarmCohort:         cohortWarm,
		SeriesSwitchCohort: cohortSeriesSwitch,
		ColdCohort:         cohortCold,
		CLIBackend:         backendCLI,
		HelperBackend:      backendHelper,
		WarmSizeCohorts:    []string{cohortWarm, cohortRGBWarm, cohortLargeWarm},
	}
}

// Decision is the architecture recommendation for JPEG XL.
type Decision struct {
	Choice string `json:"choice"`
	Reason string `json:"reason"`
}

// CampaignReport is the campaign-level input to Decide.
type CampaignReport struct {
	Criteria PromotionCriteria `json:"criteria"`
	Backends []string          `json:"backends,omitempty"`
	Cohorts  []CohortSummary   `json:"cohorts"`
	Skips    []Skip            `json:"skips,omitempty"`
}

// CohortSummary is percentile evidence for one cohort/backend/mode.
type CohortSummary struct {
	Name    string       `json:"name"`
	Backend string       `json:"backend"`
	Mode    string       `json:"mode"`
	Median  StageMedians `json:"median"`
	P95     StageMedians `json:"p95"`
}

// StageMedians stores nanoseconds.
type StageMedians struct {
	Preflight  time.Duration `json:"preflightNanoseconds,omitempty"`
	Admission  time.Duration `json:"admissionNanoseconds,omitempty"`
	Prepare    time.Duration `json:"prepareNanoseconds,omitempty"`
	Launch     time.Duration `json:"launchNanoseconds,omitempty"`
	Execute    time.Duration `json:"executeNanoseconds,omitempty"`
	Convert    time.Duration `json:"convertNanoseconds,omitempty"`
	Deliver    time.Duration `json:"deliverNanoseconds,omitempty"`
	FirstFrame time.Duration `json:"firstFrameNanoseconds,omitempty"`
}

// Skip records an explicit missing dependency rather than a zero-cost row.
type Skip struct {
	Backend string `json:"backend"`
	Cohort  string `json:"cohort,omitempty"`
	Reason  string `json:"reason"`
}

// Decide applies PromotionCriteria. Helper is recommended only when the
// prototype ran and improved interactive first-frame p95; cmd.Wait is not
// treated as algorithm-only.
func Decide(report CampaignReport) Decision {
	criteria := report.Criteria
	if criteria.WarmCohort == "" {
		criteria = DefaultPromotionCriteria()
	}
	if len(criteria.WarmSizeCohorts) == 0 {
		criteria.WarmSizeCohorts = DefaultPromotionCriteria().WarmSizeCohorts
	}
	if helperSkipped(report, criteria.HelperBackend) {
		if pointOptimize(report, criteria) {
			return Decision{Choice: DecisionPointOptimize, Reason: "helper unavailable; prepare is the unique exclusive bottleneck on every warm JPEG XL size cohort"}
		}
		return Decision{Choice: DecisionKeepCLI, Reason: "helper prototype skipped; CLI overhead does not uniquely dominate as prepare on every size cohort"}
	}
	cliWarm, okCLIWarm := lookup(report, criteria.WarmCohort, criteria.CLIBackend, ModeWarm)
	helperWarm, okHelperWarm := lookup(report, criteria.WarmCohort, criteria.HelperBackend, ModeWarm)
	cliSwitch, okCLISwitch := lookup(report, criteria.SeriesSwitchCohort, criteria.CLIBackend, ModeSeriesSwitch)
	helperSwitch, okHelperSwitch := lookup(report, criteria.SeriesSwitchCohort, criteria.HelperBackend, ModeSeriesSwitch)
	cliCold, okCLICold := lookup(report, criteria.ColdCohort, criteria.CLIBackend, ModeCold)
	helperCold, okHelperCold := lookup(report, criteria.ColdCohort, criteria.HelperBackend, ModeCold)
	if okCLIWarm && okHelperWarm && okCLISwitch && okHelperSwitch && okCLICold && okHelperCold {
		if positiveDurations(
			helperWarm.P95.FirstFrame, cliWarm.P95.FirstFrame,
			helperSwitch.P95.FirstFrame, cliSwitch.P95.FirstFrame,
			helperCold.Median.FirstFrame, cliCold.Median.FirstFrame, cliCold.Median.Launch,
		) && helperWarm.P95.FirstFrame < cliWarm.P95.FirstFrame &&
			helperSwitch.P95.FirstFrame < cliSwitch.P95.FirstFrame &&
			helperCold.Median.FirstFrame <= cliCold.Median.FirstFrame+cliCold.Median.Launch {
			return Decision{
				Choice: DecisionHelper,
				Reason: fmt.Sprintf("helper first-frame p95 is lower on %s and %s, and helper cold start is within one CLI spawn of the CLI cold first-frame; CLI execute/Wait is not treated as algorithm-only", criteria.WarmCohort, criteria.SeriesSwitchCohort),
			}
		}
	}
	if pointOptimize(report, criteria) {
		return Decision{Choice: DecisionPointOptimize, Reason: "prepare is the unique exclusive bottleneck on every warm JPEG XL size cohort; prefer local I/O changes before a helper"}
	}
	return Decision{Choice: DecisionKeepCLI, Reason: "helper did not improve interactive first-frame p95"}
}

func helperSkipped(report CampaignReport, backend string) bool {
	for _, skip := range report.Skips {
		if skip.Backend == backend && skip.Cohort == "" {
			return true
		}
	}
	return false
}

func lookup(report CampaignReport, name, backend, mode string) (CohortSummary, bool) {
	for _, cohort := range report.Cohorts {
		if cohort.Name == name && cohort.Backend == backend && cohort.Mode == mode {
			return cohort, true
		}
	}
	return CohortSummary{}, false
}

func pointOptimize(report CampaignReport, criteria PromotionCriteria) bool {
	if len(criteria.WarmSizeCohorts) == 0 {
		return false
	}
	for _, name := range criteria.WarmSizeCohorts {
		cohort, ok := lookup(report, name, criteria.CLIBackend, ModeWarm)
		if !ok {
			return false
		}
		if !uniqueMax(cohort.Median.Prepare, exclusiveStagesExceptPrepare(cohort.Median)...) {
			return false
		}
	}
	return true
}

func positiveDurations(values ...time.Duration) bool {
	for _, value := range values {
		if value <= 0 {
			return false
		}
	}
	return true
}

func exclusiveDuration(m StageMedians, stage Stage) time.Duration {
	switch stage {
	case StagePreflight:
		return m.Preflight
	case StageAdmission:
		return m.Admission
	case StagePrepare:
		return m.Prepare
	case StageLaunch:
		return m.Launch
	case StageExecute:
		return m.Execute
	case StageConvert:
		return m.Convert
	case StageDeliver:
		return m.Deliver
	default:
		return 0
	}
}

func exclusiveStagesExceptPrepare(m StageMedians) []time.Duration {
	stages := ExclusiveStages()
	others := make([]time.Duration, 0, len(stages))
	for _, stage := range stages {
		if stage == StagePrepare {
			continue
		}
		others = append(others, exclusiveDuration(m, stage))
	}
	return others
}

func uniqueMax(candidate time.Duration, others ...time.Duration) bool {
	if candidate <= 0 {
		return false
	}
	for _, other := range others {
		if other >= candidate {
			return false
		}
	}
	return true
}
