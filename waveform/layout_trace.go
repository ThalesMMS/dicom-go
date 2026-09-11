package waveform

func layoutTraces(options LayoutOptions) []TraceLayout {
	traces := make([]TraceLayout, 0, len(options.Envelopes))
	bandHeight := float64(options.Height) / float64(len(options.ChannelOrder))
	for position, envelope := range options.Envelopes {
		channelIndex := options.ChannelOrder[position]
		scale := scaleAt(options.Scales, channelIndex)
		trace := TraceLayout{ChannelIndex: channelIndex, Position: position}
		band := channelRasterBand(position, len(options.ChannelOrder), options.Height)
		var previous RasterPoint
		havePrevious := false
		for _, bucket := range envelope.Buckets {
			if !bucket.Valid || !bucket.Min.Valid || !bucket.Max.Valid ||
				!finite(bucket.Min.Value) || !finite(bucket.Max.Value) {
				havePrevious = false
				continue
			}
			first, second := bucket.Min, bucket.Max
			if second.Time < first.Time {
				first, second = second, first
			}
			firstPoint := samplePoint(options, band, bandHeight, first, scale)
			secondPoint := samplePoint(options, band, bandHeight, second, scale)
			if havePrevious {
				appendClippedLine(&trace.Lines, RasterLine{From: previous, To: firstPoint}, 0, band.top, options.Width-1, band.bottom)
			}
			appendClippedLine(&trace.Lines, RasterLine{From: firstPoint, To: secondPoint}, 0, band.top, options.Width-1, band.bottom)
			previous = secondPoint
			havePrevious = true
		}
		traces = append(traces, trace)
	}
	return traces
}

func appendClippedLine(lines *[]RasterLine, line RasterLine, minX, minY, maxX, maxY int) {
	if clipped, ok := clipRasterLine(line, minX, minY, maxX, maxY); ok {
		*lines = append(*lines, clipped)
	}
}

// clipRasterLine applies Liang-Barsky clipping. Lines already inside are
// returned byte-for-byte so normal rendering retains its existing rounding.
// Endpoint changes are limited to traces that would otherwise cross their
// channel band or viewport.
func clipRasterLine(line RasterLine, minX, minY, maxX, maxY int) (RasterLine, bool) {
	if minX > maxX || minY > maxY {
		return RasterLine{}, false
	}
	inside := func(point RasterPoint) bool {
		return point.X >= minX && point.X <= maxX && point.Y >= minY && point.Y <= maxY
	}
	if inside(line.From) && inside(line.To) {
		return line, true
	}
	x0, y0 := float64(line.From.X), float64(line.From.Y)
	dx := float64(line.To.X) - x0
	dy := float64(line.To.Y) - y0
	t0, t1 := 0.0, 1.0
	clip := func(p, q float64) bool {
		if p == 0 {
			return q >= 0
		}
		ratio := q / p
		if p < 0 {
			if ratio > t1 {
				return false
			}
			if ratio > t0 {
				t0 = ratio
			}
			return true
		}
		if ratio < t0 {
			return false
		}
		if ratio < t1 {
			t1 = ratio
		}
		return true
	}
	if !clip(-dx, x0-float64(minX)) ||
		!clip(dx, float64(maxX)-x0) ||
		!clip(-dy, y0-float64(minY)) ||
		!clip(dy, float64(maxY)-y0) {
		return RasterLine{}, false
	}
	pointAt := func(t float64) RasterPoint {
		return RasterPoint{
			X: clampInt(saturatedRound(x0+t*dx), minX, maxX),
			Y: clampInt(saturatedRound(y0+t*dy), minY, maxY),
		}
	}
	return RasterLine{From: pointAt(t0), To: pointAt(t1)}, true
}

func samplePoint(options LayoutOptions, band rasterBand, bandHeight float64, sample Sample, scale AmplitudeScale) RasterPoint {
	x := saturatedRound((float64(sample.Time) - float64(options.Start)) / float64(options.Duration) * float64(options.Width-1))
	if band.collapsed {
		return RasterPoint{X: x, Y: band.center}
	}
	center := band.traceCenter
	yScale := 0.43 * bandHeight / scale.HalfSpan * options.Amplitude
	y := saturatedRound(center - (sample.Value-scale.Center)*yScale)
	return RasterPoint{X: x, Y: y}
}

type rasterBand struct {
	top         int
	bottom      int
	center      int
	traceCenter float64
	collapsed   bool
}

// channelRasterBand retains the existing integer partition whenever it owns at
// least one pixel. When there are more channels than rows, an empty subpixel
// band is projected onto the nearest row; multiple channels may intentionally
// overlap because no raster can represent them separately at that resolution.
func channelRasterBand(position, channelCount, height int) rasterBand {
	top := position * height / channelCount
	bottom := (position+1)*height/channelCount - 1
	continuousCenter := (float64(position) + 0.5) * float64(height) / float64(channelCount)
	if bottom < top {
		pixel := clampInt(saturatedRound(continuousCenter-0.5), 0, height-1)
		return rasterBand{top: pixel, bottom: pixel, center: pixel, traceCenter: float64(pixel), collapsed: true}
	}
	roundedCenter := saturatedRound(continuousCenter)
	if roundedCenter < top || roundedCenter > bottom {
		continuousCenter = float64(clampInt(roundedCenter, top, bottom))
	}
	return rasterBand{
		top: top, bottom: bottom, center: (top + bottom) / 2,
		traceCenter: continuousCenter,
	}
}
