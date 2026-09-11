package waveform

import (
	"fmt"
	"math"
	"time"
)

func layoutGrid(layout *RasterLayout, options LayoutOptions) {
	majorDivision := niceDuration(options.Duration, options.Width)
	minorDivision := majorDivision / 5
	if minorDivision <= 0 {
		minorDivision = majorDivision
	}
	firstMinor := options.Start - options.Start%minorDivision
	if firstMinor < options.Start {
		firstMinor += minorDivision
	}
	viewEnd := options.Start + options.Duration
	for at := firstMinor; at <= viewEnd; {
		x := timeX(options.Width, at, options.Start, options.Duration)
		kind := GridMinor
		if at%majorDivision == 0 {
			kind = GridMajor
			layout.GridLabels = append(layout.GridLabels, GridText{
				Kind: GridTimeLabel, At: RasterPoint{X: x + 3, Y: 13},
				Text: formatGridTime(at), ChannelIndex: -1,
			})
		}
		layout.GridLines = append(layout.GridLines, GridLine{
			Kind: kind, ChannelIndex: -1,
			Line: RasterLine{From: RasterPoint{X: x, Y: 0}, To: RasterPoint{X: x, Y: options.Height - 1}},
		})
		if at > viewEnd-minorDivision {
			break
		}
		at += minorDivision
	}

	if len(options.ChannelOrder) == 0 {
		return
	}
	bandHeight := float64(options.Height) / float64(len(options.ChannelOrder))
	for position, channelIndex := range options.ChannelOrder {
		band := channelRasterBand(position, len(options.ChannelOrder), options.Height)
		scale := scaleAt(options.Scales, channelIndex)
		visibleHalfSpan := scale.HalfSpan / math.Max(options.Amplitude, math.SmallestNonzeroFloat64)
		division := niceNumber(visibleHalfSpan / 2)
		if division > 0 {
			minimum := scale.Center - visibleHalfSpan
			maximum := scale.Center + visibleHalfSpan
			firstValue := math.Ceil(minimum/division) * division
			for value := firstValue; finite(value) && value <= maximum+division*1e-9; {
				y := valueY(band, bandHeight, value, scale, options.Amplitude)
				layout.GridLines = append(layout.GridLines, GridLine{
					Kind: GridMinor, ChannelIndex: channelIndex,
					Line: RasterLine{From: RasterPoint{X: 0, Y: y}, To: RasterPoint{X: options.Width - 1, Y: y}},
				})
				layout.GridLabels = append(layout.GridLabels, GridText{
					Kind: GridValueLabel, At: RasterPoint{X: 3, Y: y - 2}, ChannelIndex: channelIndex,
					Text: fmt.Sprintf("%.4g %s", value, ChannelUnit(options.Group.Channels[channelIndex])),
				})
				next := value + division
				if next <= value || !finite(next) {
					break
				}
				value = next
			}
		}
		if position > 0 {
			y := clampInt(int(math.Round(float64(position)*bandHeight)), 0, options.Height-1)
			layout.GridLines = append(layout.GridLines, GridLine{
				Kind: GridChannelSeparator, ChannelIndex: channelIndex,
				Line: RasterLine{From: RasterPoint{X: 0, Y: y}, To: RasterPoint{X: options.Width - 1, Y: y}},
			})
		}
	}
}

func valueY(band rasterBand, bandHeight, value float64, scale AmplitudeScale, amplitude float64) int {
	if band.collapsed {
		return band.center
	}
	center := band.traceCenter
	if scale.HalfSpan <= 0 {
		return clampInt(saturatedRound(center), band.top, band.bottom)
	}
	return clampInt(
		saturatedRound(center-(value-scale.Center)*0.43*bandHeight/scale.HalfSpan*amplitude),
		band.top,
		band.bottom,
	)
}

func niceDuration(duration time.Duration, width int) time.Duration {
	if duration <= 0 {
		return time.Nanosecond
	}
	targetDivisions := math.Max(1, float64(width)/100)
	seconds := niceNumber(duration.Seconds() / targetDivisions)
	division := durationFromPositiveSeconds(seconds)
	if division <= 0 {
		return time.Nanosecond
	}
	return division
}

func niceNumber(value float64) float64 {
	if value <= 0 || !finite(value) {
		return 0
	}
	power := math.Pow(10, math.Floor(math.Log10(value)))
	fraction := value / power
	switch {
	case fraction <= 1:
		return power
	case fraction <= 2:
		return 2 * power
	case fraction <= 5:
		return 5 * power
	default:
		return 10 * power
	}
}

func formatGridTime(at time.Duration) string {
	if at < time.Second {
		return fmt.Sprintf("%.0f ms", float64(at)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.3g s", at.Seconds())
}
