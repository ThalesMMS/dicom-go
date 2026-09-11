// Package codeccost measures per-frame codec cost by exclusive pipeline stage.
//
// First-frame time is inclusive wall-clock from Start through delivery. Exclusive
// stage durations never include that inclusive interval a second time.
package codeccost

import (
	"fmt"
	"time"
)

// Stage is one exclusive decode-pipeline phase. cmd.Run is never labeled execute.
type Stage string

const (
	StagePreflight Stage = "preflight"
	StageAdmission Stage = "admission"
	StagePrepare   Stage = "prepare"
	StageLaunch    Stage = "launch"
	StageExecute   Stage = "execute"
	StageConvert   Stage = "convert"
	StageDeliver   Stage = "deliver"
)

// ExclusiveStages is the canonical exclusive order. first_frame is not a member.
func ExclusiveStages() []Stage {
	return []Stage{
		StagePreflight,
		StageAdmission,
		StagePrepare,
		StageLaunch,
		StageExecute,
		StageConvert,
		StageDeliver,
	}
}

// Span is one exclusive interval.
type Span struct {
	Stage    Stage         `json:"stage"`
	Duration time.Duration `json:"durationNanoseconds"`
	Note     string        `json:"note,omitempty"`
}

// Clock records non-overlapping exclusive stages plus inclusive first-frame time.
type Clock struct {
	now   func() time.Time
	start time.Time
	last  time.Time
	spans []Span
}

// NewClock uses now for timestamps. A nil now uses time.Now.
func NewClock(now func() time.Time) *Clock {
	if now == nil {
		now = time.Now
	}
	return &Clock{now: now}
}

// Start begins inclusive first-frame time and the exclusive stage clock.
func (c *Clock) Start() {
	if c == nil {
		return
	}
	ts := c.now()
	c.start = ts
	c.last = ts
	c.spans = nil
}

// Step closes the previous exclusive stage. The first Step after Start names the
// stage that occupied [Start, now).
func (c *Clock) Step(stage Stage) {
	if c == nil {
		return
	}
	ts := c.now()
	c.spans = append(c.spans, Span{Stage: stage, Duration: ts.Sub(c.last)})
	c.last = ts
}

// Annotate sets a note on the most recent exclusive span.
func (c *Clock) Annotate(note string) {
	if c == nil || len(c.spans) == 0 {
		return
	}
	c.spans[len(c.spans)-1].Note = note
}

// Exclusive returns a copy of closed exclusive spans.
func (c *Clock) Exclusive() []Span {
	if c == nil {
		return nil
	}
	out := make([]Span, len(c.spans))
	copy(out, c.spans)
	return out
}

// Inclusive is wall-clock since Start through the last Step.
func (c *Clock) Inclusive() time.Duration {
	if c == nil || c.start.IsZero() {
		return 0
	}
	if len(c.spans) == 0 {
		return c.now().Sub(c.start)
	}
	return c.last.Sub(c.start)
}

// Record snapshots exclusive spans and inclusive first-frame time.
func (c *Clock) Record() StageRecord {
	exclusive := c.Exclusive()
	return StageRecord{
		Spans:                          exclusive,
		FirstFrame:                     c.Inclusive(),
		ExclusiveTotal:                 SumDurations(exclusive),
		FirstFrameCountedInExclusive:   false,
		CombinedLaunchExecuteDisclosed: false,
	}
}

// StageRecord is the timing payload stored on one frame measurement.
type StageRecord struct {
	Spans                          []Span        `json:"exclusiveSpans"`
	FirstFrame                     time.Duration `json:"firstFrameNanoseconds"`
	ExclusiveTotal                 time.Duration `json:"exclusiveTotalNanoseconds"`
	FirstFrameCountedInExclusive   bool          `json:"firstFrameCountedInExclusive"`
	CombinedLaunchExecuteDisclosed bool          `json:"combinedLaunchExecuteDisclosed"`
}

// SumDurations adds exclusive span durations.
func SumDurations(spans []Span) time.Duration {
	var total time.Duration
	for _, span := range spans {
		if span.Duration > 0 {
			total += span.Duration
		}
	}
	return total
}

// ValidateExclusive rejects duplicate stage names so the same cost cannot be
// attributed twice. Missing stages are allowed (zero implied).
func ValidateExclusive(spans []Span) error {
	seen := make(map[Stage]int, len(spans))
	for _, span := range spans {
		seen[span.Stage]++
		if seen[span.Stage] > 1 {
			return fmt.Errorf("codeccost: exclusive stage %s counted more than once", span.Stage)
		}
	}
	return nil
}

// CombinedLaunchExecuteNote must be attached when Start/Wait cannot be split.
func CombinedLaunchExecuteNote() string {
	return "cmd.Run includes launch and execute; this span is not execute-only"
}

// WaitIncludesChildInitNote is attached to exclusive execute after cmd.Wait.
func WaitIncludesChildInitNote() string {
	return "cmd.Wait includes child process initialization plus decode; it is not algorithm-only"
}
