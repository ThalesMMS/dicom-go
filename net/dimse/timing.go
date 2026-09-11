package dimse

import "time"

// elapsedTimer measures completed DIMSE work. Completed measurements are at
// least one nanosecond even when the host clock does not advance; callers that
// fail before completion leave their result duration at zero and return the
// phase-specific error instead.
type elapsedTimer struct {
	now     func() time.Time
	started time.Time
}

func startElapsedTimer(now func() time.Time) elapsedTimer {
	return elapsedTimer{now: now, started: now()}
}

func (timer elapsedTimer) elapsed() time.Duration {
	if elapsed := timer.now().Sub(timer.started); elapsed > 0 {
		return elapsed
	}
	return time.Nanosecond
}
