package waveform

import (
	"fmt"
	"math"
	"time"
)

// RasterPoint is an integer point in a zero-origin raster viewport.
type RasterPoint struct {
	X int
	Y int
}

// RasterLine is a line between two integer raster points.
type RasterLine struct {
	From RasterPoint
	To   RasterPoint
}

// AmplitudeScale describes the stable full-group range used to position one
// channel. Valid is false when the source envelope contained no finite values.
type AmplitudeScale struct {
	Min      float64
	Max      float64
	Center   float64
	HalfSpan float64
	Valid    bool
}

// TraceLayout contains the ordered line segments for one visible channel.
type TraceLayout struct {
	ChannelIndex int
	Position     int
	Lines        []RasterLine
}

// GridLineKind identifies the semantic role of a grid line without prescribing
// its color or stroke.
type GridLineKind uint8

const (
	GridMinor GridLineKind = iota
	GridMajor
	GridChannelSeparator
)

// GridLine is a presentation-neutral grid line. ChannelIndex is -1 for time
// divisions and otherwise identifies the encoded channel.
type GridLine struct {
	Kind         GridLineKind
	Line         RasterLine
	ChannelIndex int
}

// GridTextKind identifies the semantic role of a grid label.
type GridTextKind uint8

const (
	GridTimeLabel GridTextKind = iota
	GridValueLabel
)

// GridText is a presentation-neutral grid label and baseline anchor.
type GridText struct {
	Kind         GridTextKind
	At           RasterPoint
	Text         string
	ChannelIndex int
}

// RasterLayout is a deterministic, presentation-neutral waveform layout.
// Consumers paint grid lines and labels, traces, annotations, then the cursor.
type RasterLayout struct {
	Width           int
	Height          int
	GridLines       []GridLine
	GridLabels      []GridText
	Traces          []TraceLayout
	AnnotationLines []RasterLine
	Cursor          *RasterLine
}

// LayoutOptions selects the waveform data and raster-space view to lay out.
// Scales are indexed by encoded channel and should be derived from full-group
// envelopes so zooming and panning do not change vertical calibration. A nil or
// empty Envelopes slice is valid while a caller loads samples asynchronously.
type LayoutOptions struct {
	Width        int
	Height       int
	Group        GroupInfo
	ChannelOrder []int
	Envelopes    []ChannelEnvelope
	Annotations  []Annotation
	Scales       []AmplitudeScale
	Start        time.Duration
	Duration     time.Duration
	Amplitude    float64
	Grid         bool
	Cursor       time.Duration
}

// BuildRasterLayout converts decoded waveform envelopes and annotations into
// deterministic integer raster primitives without selecting colors or fonts.
func BuildRasterLayout(options LayoutOptions) (RasterLayout, error) {
	layout := RasterLayout{Width: options.Width, Height: options.Height}
	if err := validateLayoutOptions(options); err != nil {
		return layout, err
	}
	if options.Grid {
		layoutGrid(&layout, options)
	}
	if !options.Group.Supported || len(options.ChannelOrder) == 0 {
		return layout, nil
	}
	if len(options.Envelopes) > 0 {
		layout.Traces = layoutTraces(options)
	}
	layout.AnnotationLines = layoutAnnotations(options)
	if line, ok := RasterCursorLine(options.Width, options.Height, options.Start, options.Duration, options.Cursor); ok {
		layout.Cursor = &line
	}
	return layout, nil
}

// RasterCursorLine returns the vertical cursor primitive for a visible time.
// It is independent of BuildRasterLayout so interactive callers can update a
// cursor without rebuilding static traces, grids, or annotations.
func RasterCursorLine(width, height int, start, duration, at time.Duration) (RasterLine, bool) {
	if width <= 0 || height <= 0 || duration <= 0 || start > time.Duration(math.MaxInt64)-duration {
		return RasterLine{}, false
	}
	if at < start || at > start+duration {
		return RasterLine{}, false
	}
	x := timeX(width, at, start, duration)
	return RasterLine{
		From: RasterPoint{X: x, Y: 0},
		To:   RasterPoint{X: x, Y: height - 1},
	}, true
}

func validateLayoutOptions(options LayoutOptions) error {
	if options.Width <= 0 || options.Height <= 0 {
		return fmt.Errorf("waveform: raster dimensions must be positive, got %dx%d", options.Width, options.Height)
	}
	if options.Duration <= 0 {
		return fmt.Errorf("waveform: layout duration must be positive")
	}
	if options.Start > time.Duration(math.MaxInt64)-options.Duration {
		return fmt.Errorf("waveform: layout interval overflows duration")
	}
	if options.Amplitude <= 0 || !finite(options.Amplitude) {
		return fmt.Errorf("waveform: layout amplitude must be positive and finite")
	}
	seen := make(map[int]struct{}, len(options.ChannelOrder))
	for _, channelIndex := range options.ChannelOrder {
		if channelIndex < 0 || channelIndex >= len(options.Group.Channels) {
			return fmt.Errorf("waveform: channel index %d out of range", channelIndex)
		}
		if _, exists := seen[channelIndex]; exists {
			return fmt.Errorf("waveform: duplicate channel index %d", channelIndex)
		}
		seen[channelIndex] = struct{}{}
	}
	if len(options.Envelopes) > 0 {
		if len(options.Envelopes) != len(options.ChannelOrder) {
			return fmt.Errorf("waveform: %d envelopes do not match %d visible channels", len(options.Envelopes), len(options.ChannelOrder))
		}
		for position, envelope := range options.Envelopes {
			if envelope.Channel.Index != options.ChannelOrder[position] {
				return fmt.Errorf("waveform: envelope %d is channel %d, want channel %d", position, envelope.Channel.Index, options.ChannelOrder[position])
			}
		}
	}
	return nil
}
