package waveform

import (
	"math"
	"strings"
	"time"
)

// AmplitudeScaleFromEnvelope derives the full finite extent of an envelope.
func AmplitudeScaleFromEnvelope(envelope ChannelEnvelope) AmplitudeScale {
	scale := AmplitudeScale{}
	for _, bucket := range envelope.Buckets {
		if !bucket.Valid || !bucket.Min.Valid || !bucket.Max.Valid {
			continue
		}
		for _, value := range []float64{bucket.Min.Value, bucket.Max.Value} {
			if !finite(value) {
				continue
			}
			if !scale.Valid {
				scale.Min, scale.Max, scale.Valid = value, value, true
				continue
			}
			scale.Min = math.Min(scale.Min, value)
			scale.Max = math.Max(scale.Max, value)
		}
	}
	if !scale.Valid {
		return AmplitudeScale{Min: -1, Max: 1, Center: 0, HalfSpan: 1, Valid: false}
	}
	// Halve before adding/subtracting so finite extrema near MaxFloat64 do
	// not overflow while deriving a finite center and half-span.
	scale.Center = scale.Min/2 + scale.Max/2
	scale.HalfSpan = scale.Max/2 - scale.Min/2
	if !finite(scale.Center) {
		return AmplitudeScale{Min: -1, Max: 1, Center: 0, HalfSpan: 1, Valid: false}
	}
	if scale.HalfSpan <= 0 || !finite(scale.HalfSpan) {
		scale.HalfSpan = math.Max(math.Abs(scale.Center), 1)
	}
	return scale
}

// GroupSampleBounds returns the first and last recorded sample times across all
// channels, including signed channel skew and offset.
func GroupSampleBounds(group GroupInfo) (time.Duration, time.Duration) {
	if group.SampleCount <= 0 || group.SamplingFrequencyHz <= 0 {
		return 0, 0
	}
	lastBase := durationFromPositiveSeconds(float64(group.SampleCount-1) / group.SamplingFrequencyHz)
	if len(group.Channels) == 0 {
		return 0, lastBase
	}
	firstShift := saturatingDurationAdd(group.Channels[0].TimeSkew, group.Channels[0].ChannelOffset)
	start, end := firstShift, saturatingDurationAdd(lastBase, firstShift)
	for _, channel := range group.Channels[1:] {
		shift := saturatingDurationAdd(channel.TimeSkew, channel.ChannelOffset)
		if shift < start {
			start = shift
		}
		if shiftedEnd := saturatingDurationAdd(lastBase, shift); shiftedEnd > end {
			end = shiftedEnd
		}
	}
	return start, end
}

// GroupDisplayBounds returns the full multiplex-group display interval,
// including signed channel skew and offset.
func GroupDisplayBounds(group GroupInfo) (time.Duration, time.Duration) {
	start, _ := GroupSampleBounds(group)
	end := group.Duration
	for _, channel := range group.Channels {
		shift := saturatingDurationAdd(channel.TimeSkew, channel.ChannelOffset)
		if shiftedEnd := saturatingDurationAdd(group.Duration, shift); shiftedEnd > end {
			end = shiftedEnd
		}
	}
	if end <= start {
		if start == time.Duration(math.MaxInt64) {
			start--
		}
		end = start + time.Nanosecond
	}
	return start, end
}

// CursorBounds intersects the recorded sample interval with a visible view.
func CursorBounds(group GroupInfo, start, duration time.Duration) (time.Duration, time.Duration) {
	sampleStart, sampleEnd := GroupSampleBounds(group)
	viewEnd := saturatingDurationAdd(start, duration)
	if sampleStart < start {
		sampleStart = start
	}
	if sampleEnd > viewEnd {
		sampleEnd = viewEnd
	}
	if sampleEnd < sampleStart {
		sampleEnd = sampleStart
	}
	return sampleStart, sampleEnd
}

// CursorStep returns the normalized slider step for one recorded sample.
func CursorStep(group GroupInfo, start, duration time.Duration) float64 {
	cursorStart, cursorEnd := CursorBounds(group, start, duration)
	if group.SampleCount <= 1 || group.SamplingFrequencyHz <= 0 || cursorEnd <= cursorStart {
		return 1
	}
	intervalCount := (float64(cursorEnd) - float64(cursorStart)) / float64(time.Second) * group.SamplingFrequencyHz
	if !finite(intervalCount) || intervalCount >= math.MaxInt64 {
		return math.SmallestNonzeroFloat64
	}
	intervals := int64(math.Round(intervalCount))
	if intervals < 1 {
		return 1
	}
	return 1 / float64(intervals)
}

// ChannelUnit returns the display unit represented by a channel's calibration.
func ChannelUnit(channel ChannelInfo) string {
	if channel.Calibration.Status != CalibrationComplete {
		return "raw counts"
	}
	if meaning := strings.TrimSpace(channel.Calibration.Units.Meaning); meaning != "" {
		return meaning
	}
	if value := strings.TrimSpace(channel.Calibration.Units.Value); value != "" {
		return value
	}
	return "physical units"
}

func scaleAt(scales []AmplitudeScale, channelIndex int) AmplitudeScale {
	if channelIndex >= 0 && channelIndex < len(scales) {
		scale := scales[channelIndex]
		if scale.HalfSpan > 0 && finite(scale.HalfSpan) && finite(scale.Center) {
			return scale
		}
	}
	return AmplitudeScale{Min: -1, Max: 1, HalfSpan: 1}
}
