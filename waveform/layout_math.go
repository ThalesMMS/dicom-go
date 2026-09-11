package waveform

import (
	"math"
	"time"
)

func timeX(width int, at, start, duration time.Duration) int {
	if width <= 1 || duration <= 0 {
		return 0
	}
	return saturatedRound((float64(at) - float64(start)) / float64(duration) * float64(width-1))
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func saturatedRound(value float64) int {
	if math.IsNaN(value) {
		return 0
	}
	maximum := float64(maxInt())
	minimum := float64(minInt())
	if value >= maximum {
		return maxInt()
	}
	if value <= minimum {
		return minInt()
	}
	return int(math.Round(value))
}

func maxInt() int { return int(^uint(0) >> 1) }
func minInt() int { return -maxInt() - 1 }

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func saturatingDurationAdd(left, right time.Duration) time.Duration {
	if right > 0 && left > time.Duration(math.MaxInt64)-right {
		return time.Duration(math.MaxInt64)
	}
	if right < 0 && left < time.Duration(math.MinInt64)-right {
		return time.Duration(math.MinInt64)
	}
	return left + right
}

func durationFromPositiveSeconds(seconds float64) time.Duration {
	if seconds <= 0 || math.IsNaN(seconds) {
		return 0
	}
	maximumSeconds := float64(math.MaxInt64) / float64(time.Second)
	if math.IsInf(seconds, 1) || seconds >= maximumSeconds {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds * float64(time.Second))
}
