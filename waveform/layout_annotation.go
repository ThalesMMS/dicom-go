package waveform

import (
	"sort"
	"strings"
	"time"
)

// AnnotationAppliesToChannels reports whether an annotation targets at least
// one channel in the supplied presentation order. groupNumber and encoded
// annotation channel references are one-based; order contains zero-based
// channel indexes.
func AnnotationAppliesToChannels(annotation Annotation, groupNumber int, order []int) bool {
	return len(annotationChannelPositions(annotation, groupNumber, order)) > 0
}

func layoutAnnotations(options LayoutOptions) []RasterLine {
	lines := make([]RasterLine, 0)
	for _, annotation := range options.Annotations {
		lines = append(lines, annotationLines(
			annotation,
			options.Group.Index+1,
			options.Group.SamplingFrequencyHz,
			options.ChannelOrder,
			options.Start,
			options.Duration,
			options.Width,
			options.Height,
		)...)
	}
	return lines
}

func annotationLines(annotation Annotation, groupNumber int, frequency float64, order []int, start, duration time.Duration, width, height int) []RasterLine {
	channelPositions := annotationChannelPositions(annotation, groupNumber, order)
	if len(channelPositions) == 0 {
		return nil
	}
	times := annotationTimes(annotation, frequency)
	if len(times) == 0 {
		return nil
	}
	viewEnd := start + duration
	xAt := func(at time.Duration) int {
		if at < start {
			at = start
		} else if at > viewEnd {
			at = viewEnd
		}
		return timeX(width, at, start, duration)
	}
	inView := func(at time.Duration) bool { return at >= start && at <= viewEnd }
	lines := make([]RasterLine, 0)
	addMarker := func(position int, at time.Duration) {
		if !inView(at) {
			return
		}
		top, bottom, _ := annotationBand(position, len(order), height)
		if bottom < top {
			return
		}
		x := xAt(at)
		lines = append(lines, RasterLine{From: RasterPoint{X: x, Y: top}, To: RasterPoint{X: x, Y: bottom}})
	}
	addSegment := func(position int, from, to time.Duration) {
		if to < from {
			from, to = to, from
		}
		if to < start || from > viewEnd {
			return
		}
		top, bottom, center := annotationBand(position, len(order), height)
		if bottom < top {
			return
		}
		x0, x1 := xAt(from), xAt(to)
		lines = append(lines,
			RasterLine{From: RasterPoint{X: x0, Y: center}, To: RasterPoint{X: x1, Y: center}},
			RasterLine{From: RasterPoint{X: x0, Y: top}, To: RasterPoint{X: x0, Y: bottom}},
			RasterLine{From: RasterPoint{X: x1, Y: top}, To: RasterPoint{X: x1, Y: bottom}},
		)
	}

	rangeType := strings.ToUpper(strings.TrimSpace(annotation.TemporalRangeType))
	for _, position := range channelPositions {
		switch rangeType {
		case "POINT":
			addMarker(position, times[0])
		case "MULTIPOINT":
			for _, at := range times {
				addMarker(position, at)
			}
		case "SEGMENT":
			if len(times) >= 2 {
				addSegment(position, times[0], times[1])
			} else {
				addMarker(position, times[0])
			}
		case "MULTISEGMENT":
			for index := 0; index+1 < len(times); index += 2 {
				addSegment(position, times[index], times[index+1])
			}
		case "BEGIN":
			addSegment(position, times[0], viewEnd)
		case "END":
			addSegment(position, start, times[0])
		default:
			for _, at := range times {
				addMarker(position, at)
			}
		}
	}
	return lines
}

func annotationBand(position, channelCount, height int) (top, bottom, center int) {
	band := channelRasterBand(position, channelCount, height)
	return band.top, band.bottom, band.center
}

func annotationTimes(annotation Annotation, frequency float64) []time.Duration {
	times := append([]time.Duration(nil), annotation.ReferencedTimeOffsets...)
	for _, position := range annotation.ReferencedSamplePositions {
		if position > 0 && frequency > 0 {
			times = append(times, durationFromPositiveSeconds(float64(position-1)/frequency))
		}
	}
	return times
}

func annotationChannelPositions(annotation Annotation, groupNumber int, order []int) []int {
	if len(annotation.Channels) == 0 {
		positions := make([]int, len(order))
		for position := range order {
			positions[position] = position
		}
		return positions
	}
	selected := make(map[int]bool)
	for _, reference := range annotation.Channels {
		if reference.GroupNumber != groupNumber {
			continue
		}
		if reference.ChannelNumber == 0 {
			positions := make([]int, len(order))
			for position := range order {
				positions[position] = position
			}
			return positions
		}
		channelIndex := reference.ChannelNumber - 1
		for position, visibleChannel := range order {
			if visibleChannel == channelIndex {
				selected[position] = true
			}
		}
	}
	positions := make([]int, 0, len(selected))
	for position := range selected {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	return positions
}
