package codeccost

import (
	"testing"
	"time"
)

func TestClockRecordsExclusiveNonOverlappingStages(t *testing.T) {
	now := time.Unix(0, 0)
	clock := NewClock(func() time.Time {
		return now
	})
	clock.Start()
	now = now.Add(3 * time.Millisecond)
	clock.Step(StagePreflight)
	now = now.Add(5 * time.Millisecond)
	clock.Step(StageAdmission)
	now = now.Add(11 * time.Millisecond)
	clock.Step(StagePrepare)
	spans := clock.Exclusive()
	if len(spans) != 3 {
		t.Fatalf("Exclusive() len = %d, want 3", len(spans))
	}
	want := []struct {
		stage    Stage
		duration time.Duration
	}{
		{StagePreflight, 3 * time.Millisecond},
		{StageAdmission, 5 * time.Millisecond},
		{StagePrepare, 11 * time.Millisecond},
	}
	for i, span := range spans {
		if span.Stage != want[i].stage || span.Duration != want[i].duration {
			t.Fatalf("span[%d] = %s %s, want %s %s", i, span.Stage, span.Duration, want[i].stage, want[i].duration)
		}
	}
	if err := ValidateExclusive(spans); err != nil {
		t.Fatal(err)
	}
}

func TestFirstFrameIsInclusiveAndNotSummedWithExclusiveStages(t *testing.T) {
	now := time.Unix(0, 0)
	clock := NewClock(func() time.Time { return now })
	clock.Start()
	now = now.Add(2 * time.Millisecond)
	clock.Step(StagePreflight)
	now = now.Add(8 * time.Millisecond)
	clock.Step(StageDeliver)
	first := clock.Inclusive()
	exclusive := SumDurations(clock.Exclusive())
	if first != 10*time.Millisecond {
		t.Fatalf("Inclusive() = %s, want 10ms", first)
	}
	if exclusive != first {
		t.Fatalf("exclusive sum %s != inclusive %s", exclusive, first)
	}
	record := clock.Record()
	if record.FirstFrame != first {
		t.Fatalf("FirstFrame = %s, want %s", record.FirstFrame, first)
	}
	if record.ExclusiveTotal != exclusive {
		t.Fatalf("ExclusiveTotal = %s, want %s", record.ExclusiveTotal, exclusive)
	}
	if record.FirstFrameCountedInExclusive {
		t.Fatal("FirstFrame must not be labeled as an exclusive stage")
	}
}

func TestValidateExclusiveRejectsDuplicateStageCost(t *testing.T) {
	err := ValidateExclusive([]Span{
		{Stage: StageLaunch, Duration: time.Millisecond},
		{Stage: StageLaunch, Duration: 2 * time.Millisecond},
	})
	if err == nil {
		t.Fatal("ValidateExclusive() = nil, want duplicate stage error")
	}
}

func TestCmdRunMustNotBeLabeledExecuteOnly(t *testing.T) {
	note := CombinedLaunchExecuteNote()
	if note == "" {
		t.Fatal("CombinedLaunchExecuteNote() is empty")
	}
	if !containsAll(note, "launch", "execute") {
		t.Fatalf("note %q does not disclose combined launch/execute", note)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !containsFold(value, part) {
			return false
		}
	}
	return true
}

func containsFold(value, part string) bool {
	return len(value) >= len(part) && (value == part || len(findIndex(value, part)) > 0)
}

func findIndex(value, part string) string {
	for i := 0; i+len(part) <= len(value); i++ {
		if equalFoldASCII(value[i:i+len(part)], part) {
			return part
		}
	}
	return ""
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
