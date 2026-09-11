package waveform

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAmplitudeScaleFromEnvelopeUsesFiniteFullExtent(t *testing.T) {
	envelope := ChannelEnvelope{Buckets: []EnvelopeBucket{
		{Valid: true, Min: Sample{Valid: true, Value: -10}, Max: Sample{Valid: true, Value: 20}},
		{Valid: true, Min: Sample{Valid: true, Value: math.NaN()}, Max: Sample{Valid: true, Value: math.Inf(1)}},
	}}
	got := AmplitudeScaleFromEnvelope(envelope)
	if !got.Valid || got.Min != -10 || got.Max != 20 || got.Center != 5 || got.HalfSpan != 15 {
		t.Fatalf("scale = %+v, want finite [-10,20], center 5, half-span 15", got)
	}

	constant := AmplitudeScaleFromEnvelope(ChannelEnvelope{Buckets: []EnvelopeBucket{{
		Valid: true, Min: Sample{Valid: true, Value: -7}, Max: Sample{Valid: true, Value: -7},
	}}})
	if !constant.Valid || constant.Center != -7 || constant.HalfSpan != 7 {
		t.Fatalf("constant scale = %+v, want center -7 and fallback half-span 7", constant)
	}

	empty := AmplitudeScaleFromEnvelope(ChannelEnvelope{})
	if empty.Valid || empty.Min != -1 || empty.Max != 1 || empty.Center != 0 || empty.HalfSpan != 1 {
		t.Fatalf("empty scale = %+v, want invalid [-1,1] fallback", empty)
	}

	maximumConstant := AmplitudeScaleFromEnvelope(ChannelEnvelope{Buckets: []EnvelopeBucket{{
		Valid: true,
		Min:   Sample{Valid: true, Value: math.MaxFloat64},
		Max:   Sample{Valid: true, Value: math.MaxFloat64},
	}}})
	if !maximumConstant.Valid || maximumConstant.Center != math.MaxFloat64 || maximumConstant.HalfSpan != math.MaxFloat64 ||
		math.IsInf(maximumConstant.Center, 0) || math.IsInf(maximumConstant.HalfSpan, 0) {
		t.Fatalf("maximum constant scale = %+v, want finite MaxFloat64 center and half-span", maximumConstant)
	}

	maximumRange := AmplitudeScaleFromEnvelope(ChannelEnvelope{Buckets: []EnvelopeBucket{{
		Valid: true,
		Min:   Sample{Valid: true, Value: -math.MaxFloat64},
		Max:   Sample{Valid: true, Value: math.MaxFloat64},
	}}})
	if !maximumRange.Valid || maximumRange.Center != 0 || maximumRange.HalfSpan != math.MaxFloat64 ||
		math.IsInf(maximumRange.Center, 0) || math.IsInf(maximumRange.HalfSpan, 0) {
		t.Fatalf("maximum symmetric scale = %+v, want finite center 0 and MaxFloat64 half-span", maximumRange)
	}
}

func TestBuildRasterLayoutPreservesChannelOrderSpacingAndRounding(t *testing.T) {
	group := layoutTestGroup(2)
	options := LayoutOptions{
		Width: 101, Height: 100, Group: group, ChannelOrder: []int{1, 0},
		Envelopes: []ChannelEnvelope{
			{Channel: group.Channels[1], Buckets: []EnvelopeBucket{{
				Valid: true,
				Min:   Sample{Valid: true, Time: 500 * time.Millisecond, Value: 6},
				Max:   Sample{Valid: true, Time: 500 * time.Millisecond, Value: 6},
			}}},
			{Channel: group.Channels[0], Buckets: []EnvelopeBucket{{
				Valid: true,
				Min:   Sample{Valid: true, Time: 250 * time.Millisecond, Value: 0},
				Max:   Sample{Valid: true, Time: 750 * time.Millisecond, Value: 0},
			}}},
		},
		Scales: []AmplitudeScale{
			{Center: 0, HalfSpan: 1, Valid: true},
			{Min: -10, Max: 20, Center: 5, HalfSpan: 15, Valid: true},
		},
		Start: 0, Duration: time.Second, Amplitude: 1,
	}
	layout, err := BuildRasterLayout(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Traces) != 2 || layout.Traces[0].ChannelIndex != 1 || layout.Traces[0].Position != 0 || layout.Traces[1].ChannelIndex != 0 || layout.Traces[1].Position != 1 {
		t.Fatalf("traces do not preserve visible order: %+v", layout.Traces)
	}
	wantFirst := RasterPoint{X: 50, Y: 24}
	if got := layout.Traces[0].Lines[0].From; got != wantFirst {
		t.Fatalf("calibrated point = %+v, want %+v", got, wantFirst)
	}
	if got, want := layout.Traces[1].Lines[0], (RasterLine{From: RasterPoint{X: 25, Y: 75}, To: RasterPoint{X: 75, Y: 75}}); got != want {
		t.Fatalf("second-band trace = %+v, want %+v", got, want)
	}
}

func TestBuildRasterLayoutProjectsSubpixelChannelsWithoutDroppingPrimitives(t *testing.T) {
	group := layoutTestGroup(3)
	envelopes := make([]ChannelEnvelope, 3)
	for channel := range envelopes {
		envelopes[channel] = ChannelEnvelope{
			Channel: group.Channels[channel],
			Buckets: []EnvelopeBucket{{
				Valid: true,
				Min:   Sample{Valid: true, Time: 100 * time.Millisecond, Value: 0},
				Max:   Sample{Valid: true, Time: 900 * time.Millisecond, Value: 0},
			}},
		}
	}
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 11, Height: 1, Group: group, ChannelOrder: []int{0, 1, 2},
		Envelopes: envelopes,
		Annotations: []Annotation{{
			TemporalRangeType:     "POINT",
			ReferencedTimeOffsets: []time.Duration{500 * time.Millisecond},
		}},
		Scales: []AmplitudeScale{{HalfSpan: 1}, {HalfSpan: 1}, {HalfSpan: 1}},
		Start:  0, Duration: time.Second, Amplitude: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Traces) != 3 {
		t.Fatalf("subpixel traces = %d, want all 3 channels", len(layout.Traces))
	}
	for channel, trace := range layout.Traces {
		if len(trace.Lines) != 1 {
			t.Fatalf("channel %d has %d trace lines, want 1: %+v", channel, len(trace.Lines), trace.Lines)
		}
		if trace.Lines[0].From.Y != 0 || trace.Lines[0].To.Y != 0 {
			t.Fatalf("channel %d was not projected onto the only raster row: %+v", channel, trace.Lines[0])
		}
	}
	if len(layout.AnnotationLines) != 3 {
		t.Fatalf("subpixel annotation lines = %d, want one for every channel", len(layout.AnnotationLines))
	}
	for _, line := range layout.AnnotationLines {
		if line.From.Y != 0 || line.To.Y != 0 {
			t.Fatalf("subpixel annotation was not projected onto the only raster row: %+v", line)
		}
	}
}

func TestBuildRasterLayoutUsesSameSubpixelBandsForGridTraceAndAnnotations(t *testing.T) {
	group := layoutTestGroup(3)
	envelopes := make([]ChannelEnvelope, 3)
	for channel := range envelopes {
		envelopes[channel] = ChannelEnvelope{
			Channel: group.Channels[channel],
			Buckets: []EnvelopeBucket{{
				Valid: true,
				Min:   Sample{Valid: true, Time: 100 * time.Millisecond, Value: 0},
				Max:   Sample{Valid: true, Time: 900 * time.Millisecond, Value: 0},
			}},
		}
	}
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 11, Height: 2, Group: group, ChannelOrder: []int{0, 1, 2},
		Envelopes: envelopes,
		Annotations: []Annotation{{
			TemporalRangeType:     "POINT",
			ReferencedTimeOffsets: []time.Duration{500 * time.Millisecond},
		}},
		Scales: []AmplitudeScale{{HalfSpan: 1}, {HalfSpan: 1}, {HalfSpan: 1}},
		Start:  0, Duration: time.Second, Amplitude: 1, Grid: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRows := []int{0, 0, 1}
	if len(layout.AnnotationLines) != 3 {
		t.Fatalf("annotation lines = %d, want 3", len(layout.AnnotationLines))
	}
	for channel, wantRow := range wantRows {
		trace := layout.Traces[channel]
		if len(trace.Lines) != 1 || trace.Lines[0].From.Y != wantRow || trace.Lines[0].To.Y != wantRow {
			t.Fatalf("channel %d trace = %+v, want row %d", channel, trace.Lines, wantRow)
		}
		annotation := layout.AnnotationLines[channel]
		if annotation.From.Y != wantRow || annotation.To.Y != wantRow {
			t.Fatalf("channel %d annotation = %+v, want row %d", channel, annotation, wantRow)
		}
		foundGrid := false
		for _, line := range layout.GridLines {
			if line.ChannelIndex != channel || line.Kind != GridMinor {
				continue
			}
			foundGrid = true
			if line.Line.From.Y != wantRow || line.Line.To.Y != wantRow {
				t.Fatalf("channel %d amplitude grid = %+v, want row %d", channel, line, wantRow)
			}
		}
		if !foundGrid {
			t.Fatalf("channel %d has no amplitude grid primitives", channel)
		}
		for _, label := range layout.GridLabels {
			if label.ChannelIndex == channel && label.At.Y != wantRow-2 {
				t.Fatalf("channel %d amplitude label = %+v, want baseline row %d", channel, label, wantRow-2)
			}
		}
	}
}

func TestRasterCursorLineMatchesLayoutAndRejectsInvalidViews(t *testing.T) {
	want := RasterLine{From: RasterPoint{X: 50, Y: 0}, To: RasterPoint{X: 50, Y: 9}}
	got, ok := RasterCursorLine(101, 10, 0, time.Second, 500*time.Millisecond)
	if !ok || got != want {
		t.Fatalf("RasterCursorLine = (%+v,%v), want (%+v,true)", got, ok, want)
	}
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 101, Height: 10, Group: layoutTestGroup(1), ChannelOrder: []int{0},
		Start: 0, Duration: time.Second, Amplitude: 1, Cursor: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Cursor == nil || *layout.Cursor != got {
		t.Fatalf("BuildRasterLayout cursor = %+v, want helper result %+v", layout.Cursor, got)
	}
	tests := []struct {
		name                    string
		width, height           int
		start, duration, cursor time.Duration
	}{
		{name: "width", width: 0, height: 1, duration: 1},
		{name: "height", width: 1, height: 0, duration: 1},
		{name: "duration", width: 1, height: 1, duration: 0},
		{name: "before", width: 1, height: 1, start: 1, duration: 1, cursor: 0},
		{name: "after", width: 1, height: 1, duration: 1, cursor: 2},
		{name: "overflow", width: 1, height: 1, start: time.Duration(math.MaxInt64), duration: 1, cursor: time.Duration(math.MaxInt64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if line, ok := RasterCursorLine(test.width, test.height, test.start, test.duration, test.cursor); ok {
				t.Fatalf("invalid cursor view returned %+v", line)
			}
		})
	}
}

func TestBuildRasterLayoutMatchesLegacyCalibratedPoint(t *testing.T) {
	group := layoutTestGroup(1)
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 101, Height: 100, Group: group, ChannelOrder: []int{0},
		Envelopes: []ChannelEnvelope{{Channel: group.Channels[0], Buckets: []EnvelopeBucket{{
			Valid: true,
			Min:   Sample{Valid: true, Time: 500 * time.Millisecond, Value: 6},
			Max:   Sample{Valid: true, Time: 500 * time.Millisecond, Value: 6},
		}}}},
		Scales: []AmplitudeScale{{Min: -10, Max: 20, Center: 5, HalfSpan: 15, Valid: true}},
		Start:  0, Duration: time.Second, Amplitude: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := layout.Traces[0].Lines[0].From, (RasterPoint{X: 50, Y: 47}); got != want {
		t.Fatalf("calibrated point = %+v, want legacy raster position %+v", got, want)
	}
}

func TestBuildRasterLayoutClipsTraceToItsOwnBand(t *testing.T) {
	group := layoutTestGroup(2)
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 101, Height: 100, Group: group, ChannelOrder: []int{0, 1},
		Envelopes: []ChannelEnvelope{
			{Channel: group.Channels[0], Buckets: []EnvelopeBucket{{
				Valid: true,
				Min:   Sample{Valid: true, Time: 0, Value: 100},
				Max:   Sample{Valid: true, Time: time.Second, Value: -100},
			}}},
			{Channel: group.Channels[1]},
		},
		Scales: []AmplitudeScale{{Center: 0, HalfSpan: 1, Valid: true}, {Center: 0, HalfSpan: 1, Valid: true}},
		Start:  0, Duration: time.Second, Amplitude: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Traces[0].Lines) != 1 {
		t.Fatalf("clipped trace lines = %+v", layout.Traces[0].Lines)
	}
	line := layout.Traces[0].Lines[0]
	for _, point := range []RasterPoint{line.From, line.To} {
		if point.X < 0 || point.X > 100 || point.Y < 0 || point.Y > 49 {
			t.Fatalf("trace endpoint leaked outside first channel band: %+v", line)
		}
	}
}

func TestBuildRasterLayoutInvalidBucketBreaksTraceContinuity(t *testing.T) {
	group := layoutTestGroup(1)
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 101, Height: 100, Group: group, ChannelOrder: []int{0},
		Envelopes: []ChannelEnvelope{{Channel: group.Channels[0], Buckets: []EnvelopeBucket{
			{Valid: true, Min: Sample{Valid: true, Time: 100 * time.Millisecond}, Max: Sample{Valid: true, Time: 200 * time.Millisecond}},
			{Valid: false},
			{Valid: true, Min: Sample{Valid: true, Time: 800 * time.Millisecond}, Max: Sample{Valid: true, Time: 900 * time.Millisecond}},
		}}},
		Scales: []AmplitudeScale{{HalfSpan: 1, Valid: true}},
		Start:  0, Duration: time.Second, Amplitude: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := layout.Traces[0].Lines
	if len(lines) != 2 {
		t.Fatalf("trace has %d lines, want two disconnected bucket extrema: %+v", len(lines), lines)
	}
	if lines[0].To.X != 20 || lines[1].From.X != 80 {
		t.Fatalf("gap endpoints = %+v, want 20 then 80 without bridge", lines)
	}
}

func TestBuildRasterLayoutGridAndRawCountUnits(t *testing.T) {
	group := layoutTestGroup(1)
	group.Channels[0].Calibration = Calibration{Status: CalibrationMissingSensitivity}
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 201, Height: 100, Group: group, ChannelOrder: []int{0},
		Scales: []AmplitudeScale{{Min: -10, Max: 20, Center: 5, HalfSpan: 15, Valid: true}},
		Start:  0, Duration: 2 * time.Second, Amplitude: 1, Grid: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	majorX := make([]int, 0)
	for _, line := range layout.GridLines {
		if line.Kind == GridMajor {
			majorX = append(majorX, line.Line.From.X)
		}
	}
	if got, want := intsString(majorX), "0,100,200"; got != want {
		t.Fatalf("major grid x = %s, want %s", got, want)
	}
	var foundRaw bool
	for _, label := range layout.GridLabels {
		if label.Kind == GridValueLabel && strings.Contains(label.Text, "raw counts") {
			foundRaw = true
		}
	}
	if !foundRaw {
		t.Fatalf("grid labels do not expose incomplete calibration as raw counts: %+v", layout.GridLabels)
	}
	if got := ChannelUnit(ChannelInfo{Calibration: Calibration{Status: CalibrationComplete, Units: CodedConcept{Value: "mV", Meaning: " millivolt "}}}); got != "millivolt" {
		t.Fatalf("complete calibration unit = %q", got)
	}
}

func TestBuildRasterLayoutAnnotationsClampAndPreserveSemantics(t *testing.T) {
	group := layoutTestGroup(2)
	annotations := []Annotation{
		{
			TemporalRangeType:     "SEGMENT",
			Channels:              []ChannelReference{{GroupNumber: 1, ChannelNumber: 1}},
			ReferencedTimeOffsets: []time.Duration{-time.Second, 2 * time.Second},
		},
		{
			TemporalRangeType:     "POINT",
			Channels:              []ChannelReference{{GroupNumber: 1, ChannelNumber: 2}},
			ReferencedTimeOffsets: []time.Duration{2 * time.Second},
		},
		{
			TemporalRangeType:         "MULTIPOINT",
			ReferencedSamplePositions: []uint64{1, 51},
		},
	}
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 101, Height: 100, Group: group, ChannelOrder: []int{1, 0},
		Annotations: annotations, Start: 0, Duration: time.Second, Amplitude: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.AnnotationLines) != 7 {
		t.Fatalf("annotation lines = %d, want 7: %+v", len(layout.AnnotationLines), layout.AnnotationLines)
	}
	wantSpan := RasterLine{From: RasterPoint{X: 0, Y: 74}, To: RasterPoint{X: 100, Y: 74}}
	if got := layout.AnnotationLines[0]; got != wantSpan {
		t.Fatalf("clamped channel-scoped segment = %+v, want %+v", got, wantSpan)
	}
	for _, line := range layout.AnnotationLines {
		for _, point := range []RasterPoint{line.From, line.To} {
			if point.X < 0 || point.X > 100 || point.Y < 0 || point.Y > 99 {
				t.Fatalf("annotation endpoint outside viewport: %+v", line)
			}
		}
	}
}

func TestAnnotationAppliesToChannelsUsesOneBasedReferencesAndVisibleOrder(t *testing.T) {
	annotation := Annotation{Channels: []ChannelReference{{GroupNumber: 1, ChannelNumber: 1}}}
	if !AnnotationAppliesToChannels(annotation, 1, []int{1, 0}) {
		t.Fatal("channel 1 annotation did not apply to reordered visible channels")
	}
	if AnnotationAppliesToChannels(annotation, 2, []int{1, 0}) {
		t.Fatal("annotation applied to a different multiplex group")
	}
	if AnnotationAppliesToChannels(annotation, 1, []int{1}) {
		t.Fatal("annotation applied when its channel was hidden")
	}
	all := Annotation{Channels: []ChannelReference{{GroupNumber: 1, ChannelNumber: 0}}}
	if !AnnotationAppliesToChannels(all, 1, []int{1}) {
		t.Fatal("zero channel reference did not select all visible channels")
	}
	if AnnotationAppliesToChannels(Annotation{}, 1, nil) {
		t.Fatal("unscoped annotation applied with no visible channels")
	}
}

func TestBuildRasterLayoutFiltersNonFiniteSamplesAndExtremeCoordinates(t *testing.T) {
	group := layoutTestGroup(1)
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 17, Height: 9, Group: group, ChannelOrder: []int{0},
		Envelopes: []ChannelEnvelope{{Channel: group.Channels[0], Buckets: []EnvelopeBucket{
			{Valid: true, Min: Sample{Valid: true, Value: math.NaN()}, Max: Sample{Valid: true, Value: 1}},
			{Valid: true, Min: Sample{Valid: true, Time: 0, Value: -math.MaxFloat64}, Max: Sample{Valid: true, Time: time.Second, Value: math.MaxFloat64}},
		}}},
		Scales: []AmplitudeScale{{Center: 0, HalfSpan: 1, Valid: true}},
		Start:  0, Duration: time.Second, Amplitude: math.MaxFloat64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Traces[0].Lines) > 1 {
		t.Fatalf("unexpected non-finite trace primitives: %+v", layout.Traces[0].Lines)
	}
	for _, line := range layout.Traces[0].Lines {
		for _, point := range []RasterPoint{line.From, line.To} {
			if point.X < 0 || point.X >= layout.Width || point.Y < 0 || point.Y >= layout.Height {
				t.Fatalf("adversarial endpoint escaped viewport: %+v", line)
			}
		}
	}
}

func TestWaveformLayoutBoundsIncludeSignedChannelTiming(t *testing.T) {
	group := GroupInfo{
		SamplingFrequencyHz: 100,
		SampleCount:         3,
		Duration:            30 * time.Millisecond,
		Channels: []ChannelInfo{
			{TimeSkew: -10 * time.Millisecond},
			{ChannelOffset: 20 * time.Millisecond},
		},
	}
	start, end := GroupDisplayBounds(group)
	if start != -10*time.Millisecond || end != 50*time.Millisecond {
		t.Fatalf("display bounds = [%v,%v), want [-10ms,50ms)", start, end)
	}
	cursorStart, cursorEnd := CursorBounds(group, start, end-start)
	if cursorStart != -10*time.Millisecond || cursorEnd != 40*time.Millisecond {
		t.Fatalf("cursor bounds = [%v,%v], want [-10ms,40ms]", cursorStart, cursorEnd)
	}
	if got, want := CursorStep(group, start, end-start), 1.0/5.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("cursor step = %g, want %g", got, want)
	}
}

func TestWaveformLayoutBoundsAndGridSaturateExtremeFiniteValues(t *testing.T) {
	group := layoutTestGroup(1)
	group.SampleCount = math.MaxInt64
	group.SamplingFrequencyHz = math.SmallestNonzeroFloat64
	group.Duration = time.Duration(math.MaxInt64)
	group.Channels[0].TimeSkew = time.Hour
	start, end := GroupSampleBounds(group)
	if start != time.Hour || end != time.Duration(math.MaxInt64) {
		t.Fatalf("extreme sample bounds = [%v,%v], want [1h,MaxInt64]", start, end)
	}
	displayStart, displayEnd := GroupDisplayBounds(group)
	if displayEnd <= displayStart {
		t.Fatalf("extreme display bounds are not ordered: [%v,%v]", displayStart, displayEnd)
	}

	group = layoutTestGroup(1)
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 11, Height: 11, Group: group, ChannelOrder: []int{0},
		Scales: []AmplitudeScale{{Center: math.MaxFloat64, HalfSpan: 1, Valid: true}},
		Start:  0, Duration: time.Second, Amplitude: 1, Grid: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range layout.GridLines {
		for _, point := range []RasterPoint{line.Line.From, line.Line.To} {
			if point.X < 0 || point.X >= layout.Width || point.Y < 0 || point.Y >= layout.Height {
				t.Fatalf("extreme grid produced unsafe primitive: %+v", line)
			}
		}
	}

	maxDurationLayout, err := BuildRasterLayout(LayoutOptions{
		Width: 100, Height: 10, Group: layoutTestGroup(1),
		Start: 0, Duration: time.Duration(math.MaxInt64), Amplitude: 1, Grid: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count := len(maxDurationLayout.GridLines); count == 0 || count > 20 {
		t.Fatalf("MaxInt64 duration produced %d grid lines, want a bounded positive count", count)
	}

	extremeAnnotationGroup := layoutTestGroup(1)
	extremeAnnotationGroup.SamplingFrequencyHz = math.SmallestNonzeroFloat64
	extremeAnnotationLayout, err := BuildRasterLayout(LayoutOptions{
		Width: 100, Height: 10, Group: extremeAnnotationGroup, ChannelOrder: []int{0},
		Annotations: []Annotation{{
			TemporalRangeType:         "POINT",
			ReferencedSamplePositions: []uint64{math.MaxUint64},
		}},
		Start: 0, Duration: time.Second, Amplitude: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(extremeAnnotationLayout.AnnotationLines) != 0 {
		t.Fatalf("overflowing annotation sample position wrapped into viewport: %+v", extremeAnnotationLayout.AnnotationLines)
	}
}

func TestBuildRasterLayoutUnsupportedGroupKeepsOnlyGrid(t *testing.T) {
	group := layoutTestGroup(1)
	group.Supported = false
	layout, err := BuildRasterLayout(LayoutOptions{
		Width: 101, Height: 100, Group: group, ChannelOrder: []int{0},
		Annotations: []Annotation{{ReferencedTimeOffsets: []time.Duration{time.Second / 2}}},
		Start:       0, Duration: time.Second, Amplitude: 1, Grid: true, Cursor: time.Second / 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.GridLines) == 0 || len(layout.Traces) != 0 || len(layout.AnnotationLines) != 0 || layout.Cursor != nil {
		t.Fatalf("unsupported layout = %+v, want grid only", layout)
	}
}

func TestBuildRasterLayoutRejectsInvalidInputs(t *testing.T) {
	group := layoutTestGroup(2)
	valid := LayoutOptions{
		Width: 100, Height: 100, Group: group, ChannelOrder: []int{0},
		Start: 0, Duration: time.Second, Amplitude: 1,
	}
	tests := []struct {
		name   string
		mutate func(*LayoutOptions)
	}{
		{name: "width", mutate: func(options *LayoutOptions) { options.Width = 0 }},
		{name: "height", mutate: func(options *LayoutOptions) { options.Height = -1 }},
		{name: "duration", mutate: func(options *LayoutOptions) { options.Duration = 0 }},
		{name: "interval overflow", mutate: func(options *LayoutOptions) { options.Start = time.Duration(math.MaxInt64); options.Duration = 1 }},
		{name: "zero amplitude", mutate: func(options *LayoutOptions) { options.Amplitude = 0 }},
		{name: "infinite amplitude", mutate: func(options *LayoutOptions) { options.Amplitude = math.Inf(1) }},
		{name: "channel range", mutate: func(options *LayoutOptions) { options.ChannelOrder = []int{2} }},
		{name: "duplicate channel", mutate: func(options *LayoutOptions) { options.ChannelOrder = []int{0, 0} }},
		{name: "envelope count", mutate: func(options *LayoutOptions) {
			options.Envelopes = []ChannelEnvelope{{Channel: group.Channels[0]}, {Channel: group.Channels[1]}}
		}},
		{name: "envelope order", mutate: func(options *LayoutOptions) { options.Envelopes = []ChannelEnvelope{{Channel: group.Channels[1]}} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			if _, err := BuildRasterLayout(options); err == nil {
				t.Fatal("BuildRasterLayout accepted invalid options")
			}
		})
	}
}

func layoutTestGroup(channelCount int) GroupInfo {
	channels := make([]ChannelInfo, channelCount)
	for index := range channels {
		channels[index] = ChannelInfo{
			Index: index,
			Calibration: Calibration{
				Status: CalibrationComplete,
				Units:  CodedConcept{Value: "mV", Scheme: "UCUM", Meaning: "millivolt"},
			},
		}
	}
	return GroupInfo{
		Index: 0, SamplingFrequencyHz: 100, SampleCount: 101,
		Duration: time.Second, Channels: channels, Supported: true,
	}
}

func intsString(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}
