package validation

import (
	"context"
	"errors"

	"github.com/ThalesMMS/dicom-go/core"
)

// Operation freezes validation and hook configuration for one parse or write
// operation. It accumulates a bounded report and correlates rule findings with
// offsets previously observed by lifecycle events.
//
// Operation is intentionally not safe for concurrent use. A HookChain can be
// shared across concurrent operations; hooks declared non-concurrent-safe are
// serialized by the chain.
type Operation struct {
	ctx       context.Context
	opts      Options
	hooks     *HookChain
	report    Report
	locations map[string][]int64
	stopped   bool
}

func NewOperation(ctx context.Context, opts Options) (*Operation, error) {
	if err := ValidateOptions(opts); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &Operation{
		ctx: ctx, opts: opts, hooks: opts.Hooks.snapshot(),
		locations: make(map[string][]int64),
	}, nil
}

func (o *Operation) Handle(event HookEvent) (HookResult, error) {
	if o == nil {
		return HookResult{Element: cloneElementPtr(event.Element)}, nil
	}
	if err := o.ctx.Err(); err != nil {
		return HookResult{}, err
	}
	if o.stopped {
		if o.opts.Mode == ModeStrict && o.report.HasErrors() {
			return HookResult{}, &ValidationError{Report: o.Report()}
		}
		return HookResult{Element: cloneElementPtr(event.Element)}, nil
	}
	if event.OffsetSet && len(event.Path) > 0 {
		o.recordLocation(event.Path, event.Offset)
	}
	result, err := o.hooks.run(o.ctx, event, o.opts.StopFirst)
	transformedTag := event.OffsetSet && result.Element != nil && len(event.Path) > 0 && result.Element.Tag() != eventTag(event)
	if event.OffsetSet && len(event.Path) > 0 && (result.Filter || transformedTag) {
		o.removeLocation(event.Path, event.Offset)
	}
	if transformedTag {
		path := event.Path.Clone()
		path[len(path)-1].Tag = result.Element.Tag()
		o.recordLocation(path, event.Offset)
	}
	report, reportErr := ReportFromHookResult(o.opts, result)
	if reportErr != nil {
		return result, reportErr
	}
	o.merge(report)
	o.stopIfRequested()
	if o.stopped && o.opts.Mode == ModeStrict && o.report.HasErrors() {
		return result, &ValidationError{Report: o.Report()}
	}
	return result, err
}

// ValidateParsedDataSet runs validation rules plus pre/post-validation and
// dataset-complete hooks. Element/item/sequence lifecycle hooks are not
// replayed; parsers call Handle for those phases while consuming the stream.
func (o *Operation) ValidateParsedDataSet(dataset core.DataSet) (Result, error) {
	if o == nil {
		return Result{DataSet: dataset}, nil
	}
	if o.stopped {
		result := Result{DataSet: dataset, Report: o.Report()}
		if o.opts.Mode == ModeStrict && result.Report.HasErrors() {
			return result, &ValidationError{Report: result.Report.Clone()}
		}
		return result, nil
	}
	if _, err := o.Handle(HookEvent{Point: HookPreValidation, DataSet: &dataset}); err != nil {
		return Result{DataSet: dataset, Report: o.Report()}, err
	}
	if o.stopped {
		return o.stoppedResult(dataset)
	}
	ruleOpts := o.opts
	ruleOpts.Hooks = nil
	result, ruleErr := ValidateDataSet(o.ctx, dataset, ruleOpts)
	o.annotateOffsets(&result.Report)
	o.merge(result.Report)
	o.stopIfRequested()
	if ruleErr != nil && !errors.Is(ruleErr, ErrValidationFailed) {
		return Result{DataSet: result.DataSet, Report: o.Report()}, ruleErr
	}
	if o.stopped {
		return o.stoppedResult(result.DataSet)
	}
	if _, err := o.Handle(HookEvent{Point: HookDataSetComplete, DataSet: &result.DataSet}); err != nil {
		return Result{DataSet: result.DataSet, Report: o.Report()}, err
	}
	if _, err := o.Handle(HookEvent{Point: HookPostValidation, DataSet: &result.DataSet}); err != nil {
		return Result{DataSet: result.DataSet, Report: o.Report()}, err
	}
	combined := o.Report()
	result.Report = combined
	if o.opts.Mode == ModeStrict && combined.HasErrors() {
		return result, &ValidationError{Report: combined.Clone()}
	}
	return result, nil
}

func (o *Operation) stoppedResult(dataset core.DataSet) (Result, error) {
	result := Result{DataSet: dataset, Report: o.Report()}
	if o.opts.Mode == ModeStrict && result.Report.HasErrors() {
		return result, &ValidationError{Report: result.Report.Clone()}
	}
	return result, nil
}

func (o *Operation) Report() Report {
	if o == nil {
		return Report{}
	}
	return o.report.Clone()
}

// AddFinding records a library-generated diagnostic under the operation's
// mode and shared collection bound.
func (o *Operation) AddFinding(finding Finding) {
	if o == nil {
		return
	}
	report, err := MergeReports(o.opts, Report{Findings: []Finding{finding}})
	if err == nil {
		o.merge(report)
		o.stopIfRequested()
	}
}

// AddReport merges a rule report, annotating findings with any matching
// lifecycle offsets observed by this operation.
func (o *Operation) AddReport(report Report) {
	if o == nil {
		return
	}
	o.annotateOffsets(&report)
	o.merge(report)
	o.stopIfRequested()
}

func (o *Operation) stopIfRequested() {
	if o != nil && o.opts.StopFirst && len(o.report.Findings) > 0 {
		o.stopped = true
	}
}

func (o *Operation) merge(report Report) {
	merged, err := MergeReports(o.opts, o.report, report)
	if err == nil {
		o.report = merged
	}
}

func (o *Operation) annotateOffsets(report *Report) {
	if o == nil || report == nil {
		return
	}
	for i := range report.Findings {
		if report.Findings[i].OffsetSet {
			continue
		}
		if offsets := o.locations[report.Findings[i].Path.String()]; report.Findings[i].occurrence < len(offsets) {
			report.Findings[i].Offset = offsets[report.Findings[i].occurrence]
			report.Findings[i].OffsetSet = true
		}
	}
}

func (o *Operation) recordLocation(path Path, offset int64) {
	key := path.String()
	offsets := o.locations[key]
	if len(offsets) == 0 || offsets[len(offsets)-1] != offset {
		o.locations[key] = append(offsets, offset)
	}
}

func (o *Operation) removeLocation(path Path, offset int64) {
	key := path.String()
	offsets := o.locations[key]
	for index := len(offsets) - 1; index >= 0; index-- {
		if offsets[index] != offset {
			continue
		}
		offsets = append(offsets[:index], offsets[index+1:]...)
		if len(offsets) == 0 {
			delete(o.locations, key)
		} else {
			o.locations[key] = offsets
		}
		return
	}
}
