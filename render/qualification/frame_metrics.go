package qualification

import (
	"errors"
	"fmt"
	"math"
)

const RGBAFrameErrorMetricsV1Schema = "rgba8-frame-error-metrics-v1"

var ErrInvalidFrameMetrics = errors.New("qualification: invalid RGBA frame metrics")

// RGBAFrameThresholdsV1 freezes the component and silhouette cutoffs used to
// derive counts. A difference equal to a component threshold is accepted; a
// larger difference is counted.
type RGBAFrameThresholdsV1 struct {
	AbsoluteComponent     [4]uint8 `json:"absolute_component"`
	AlphaSilhouetteCutoff uint8    `json:"alpha_silhouette_cutoff"`
}

func (thresholds RGBAFrameThresholdsV1) Validate() error {
	if thresholds.AlphaSilhouetteCutoff == 0 {
		return fmt.Errorf("%w: zero alpha silhouette cutoff", ErrInvalidFrameMetrics)
	}
	return nil
}

// RGBAFrameErrorMetricsV1 records numerical error before a frame is composed
// over its presentation background. Channel order is R, G, B, A.
type RGBAFrameErrorMetricsV1 struct {
	Schema                          string                `json:"schema"`
	Width                           uint32                `json:"width"`
	Height                          uint32                `json:"height"`
	PixelCount                      uint64                `json:"pixel_count"`
	Thresholds                      RGBAFrameThresholdsV1 `json:"thresholds"`
	MeanAbsoluteError               [4]float64            `json:"mean_absolute_error"`
	RootMeanSquareError             [4]float64            `json:"root_mean_square_error"`
	P99AbsoluteError                [4]float64            `json:"p99_absolute_error"`
	MaximumAbsoluteError            [4]uint8              `json:"maximum_absolute_error"`
	ComponentsOverThreshold         [4]uint64             `json:"components_over_threshold"`
	PixelsWithAnyComponentOverLimit uint64                `json:"pixels_with_any_component_over_limit"`
	AlphaSilhouetteMismatchPixels   uint64                `json:"alpha_silhouette_mismatch_pixels"`
}

func (metrics RGBAFrameErrorMetricsV1) Validate() error {
	if metrics.Schema != RGBAFrameErrorMetricsV1Schema || metrics.Width == 0 || metrics.Height == 0 ||
		metrics.PixelCount != uint64(metrics.Width)*uint64(metrics.Height) {
		return fmt.Errorf("%w: dimensions or schema", ErrInvalidFrameMetrics)
	}
	if err := metrics.Thresholds.Validate(); err != nil {
		return err
	}
	if metrics.PixelsWithAnyComponentOverLimit > metrics.PixelCount ||
		metrics.AlphaSilhouetteMismatchPixels > metrics.PixelCount {
		return fmt.Errorf("%w: pixel count overflow", ErrInvalidFrameMetrics)
	}
	for channel := 0; channel < 4; channel++ {
		if metrics.ComponentsOverThreshold[channel] > metrics.PixelCount ||
			!boundedFrameError(metrics.MeanAbsoluteError[channel]) ||
			!boundedFrameError(metrics.RootMeanSquareError[channel]) ||
			!boundedFrameError(metrics.P99AbsoluteError[channel]) {
			return fmt.Errorf("%w: channel %d", ErrInvalidFrameMetrics, channel)
		}
		if metrics.P99AbsoluteError[channel] > float64(metrics.MaximumAbsoluteError[channel]) ||
			metrics.MeanAbsoluteError[channel] > float64(metrics.MaximumAbsoluteError[channel]) ||
			metrics.RootMeanSquareError[channel] > float64(metrics.MaximumAbsoluteError[channel]) {
			return fmt.Errorf("%w: inconsistent channel %d distribution", ErrInvalidFrameMetrics, channel)
		}
	}
	return nil
}

func (metrics RGBAFrameErrorMetricsV1) PixelFractionOverThreshold() float64 {
	if metrics.PixelCount == 0 {
		return math.NaN()
	}
	return float64(metrics.PixelsWithAnyComponentOverLimit) / float64(metrics.PixelCount)
}

func (metrics RGBAFrameErrorMetricsV1) AlphaSilhouetteMismatchFraction() float64 {
	if metrics.PixelCount == 0 {
		return math.NaN()
	}
	return float64(metrics.AlphaSilhouetteMismatchPixels) / float64(metrics.PixelCount)
}

// CompareRGBA8Frames compares tightly packed, unpremultiplied RGBA8 frames.
// The function rejects trailing bytes so a stride or extent error cannot be
// mistaken for a successful visual comparison.
func CompareRGBA8Frames(
	reference, candidate []byte,
	width, height uint32,
	thresholds RGBAFrameThresholdsV1,
) (RGBAFrameErrorMetricsV1, error) {
	if width == 0 || height == 0 {
		return RGBAFrameErrorMetricsV1{}, fmt.Errorf("%w: zero frame extent", ErrInvalidFrameMetrics)
	}
	if err := thresholds.Validate(); err != nil {
		return RGBAFrameErrorMetricsV1{}, err
	}
	pixelCount := uint64(width) * uint64(height)
	if pixelCount > uint64(math.MaxInt)/4 {
		return RGBAFrameErrorMetricsV1{}, fmt.Errorf("%w: frame exceeds Go slice capacity", ErrInvalidFrameMetrics)
	}
	wantBytes := int(pixelCount * 4)
	if len(reference) != wantBytes || len(candidate) != wantBytes {
		return RGBAFrameErrorMetricsV1{}, fmt.Errorf(
			"%w: frame lengths %d/%d, want %d",
			ErrInvalidFrameMetrics, len(reference), len(candidate), wantBytes,
		)
	}

	metrics := RGBAFrameErrorMetricsV1{
		Schema: RGBAFrameErrorMetricsV1Schema, Width: width, Height: height,
		PixelCount: pixelCount, Thresholds: thresholds,
	}
	var differenceHistogram [4][256]uint64
	var absoluteSums [4]float64
	var squaredSums [4]float64
	for pixel := uint64(0); pixel < pixelCount; pixel++ {
		base := int(pixel * 4)
		pixelOverThreshold := false
		for channel := 0; channel < 4; channel++ {
			difference := absoluteByteDifference(reference[base+channel], candidate[base+channel])
			differenceHistogram[channel][difference]++
			absoluteSums[channel] += float64(difference)
			squaredSums[channel] += float64(difference) * float64(difference)
			if difference > metrics.MaximumAbsoluteError[channel] {
				metrics.MaximumAbsoluteError[channel] = difference
			}
			if difference > thresholds.AbsoluteComponent[channel] {
				metrics.ComponentsOverThreshold[channel]++
				pixelOverThreshold = true
			}
		}
		if pixelOverThreshold {
			metrics.PixelsWithAnyComponentOverLimit++
		}
		referenceVisible := reference[base+3] >= thresholds.AlphaSilhouetteCutoff
		candidateVisible := candidate[base+3] >= thresholds.AlphaSilhouetteCutoff
		if referenceVisible != candidateVisible {
			metrics.AlphaSilhouetteMismatchPixels++
		}
	}
	for channel := 0; channel < 4; channel++ {
		metrics.MeanAbsoluteError[channel] = absoluteSums[channel] / float64(pixelCount)
		metrics.RootMeanSquareError[channel] = math.Sqrt(squaredSums[channel] / float64(pixelCount))
		nearestRank := uint64(math.Ceil(0.99 * float64(pixelCount)))
		var cumulative uint64
		for difference, count := range differenceHistogram[channel] {
			cumulative += count
			if cumulative >= nearestRank {
				metrics.P99AbsoluteError[channel] = float64(difference)
				break
			}
		}
	}
	if err := metrics.Validate(); err != nil {
		return RGBAFrameErrorMetricsV1{}, err
	}
	return metrics, nil
}

// RGBAFrameMetricLimitsV1 is selected before rendering, normally by a
// preset-specific qualification profile.
type RGBAFrameMetricLimitsV1 struct {
	MaximumMeanAbsoluteError               [4]float64 `json:"maximum_mean_absolute_error"`
	MaximumRootMeanSquareError             [4]float64 `json:"maximum_root_mean_square_error"`
	MaximumP99AbsoluteError                [4]float64 `json:"maximum_p99_absolute_error"`
	MaximumPixelFractionOverThreshold      float64    `json:"maximum_pixel_fraction_over_threshold"`
	MaximumAlphaSilhouetteMismatchFraction float64    `json:"maximum_alpha_silhouette_mismatch_fraction"`
}

func (limits RGBAFrameMetricLimitsV1) Validate() error {
	for channel := 0; channel < 4; channel++ {
		for _, value := range []float64{
			limits.MaximumMeanAbsoluteError[channel],
			limits.MaximumRootMeanSquareError[channel],
			limits.MaximumP99AbsoluteError[channel],
		} {
			if !boundedFrameError(value) {
				return fmt.Errorf("%w: limit channel %d", ErrInvalidFrameMetrics, channel)
			}
		}
	}
	for _, fraction := range []float64{
		limits.MaximumPixelFractionOverThreshold,
		limits.MaximumAlphaSilhouetteMismatchFraction,
	} {
		if math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0 || fraction > 1 {
			return fmt.Errorf("%w: limit fraction", ErrInvalidFrameMetrics)
		}
	}
	return nil
}

// RequireWithin fails when any independently frozen metric limit is exceeded.
func (metrics RGBAFrameErrorMetricsV1) RequireWithin(limits RGBAFrameMetricLimitsV1) error {
	if err := metrics.Validate(); err != nil {
		return err
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	for channel := 0; channel < 4; channel++ {
		if metrics.MeanAbsoluteError[channel] > limits.MaximumMeanAbsoluteError[channel] ||
			metrics.RootMeanSquareError[channel] > limits.MaximumRootMeanSquareError[channel] ||
			metrics.P99AbsoluteError[channel] > limits.MaximumP99AbsoluteError[channel] {
			return fmt.Errorf("%w: channel %d exceeds frozen limit", ErrInvalidFrameMetrics, channel)
		}
	}
	if metrics.PixelFractionOverThreshold() > limits.MaximumPixelFractionOverThreshold {
		return fmt.Errorf("%w: pixel threshold fraction exceeds frozen limit", ErrInvalidFrameMetrics)
	}
	if metrics.AlphaSilhouetteMismatchFraction() > limits.MaximumAlphaSilhouetteMismatchFraction {
		return fmt.Errorf("%w: alpha silhouette fraction exceeds frozen limit", ErrInvalidFrameMetrics)
	}
	return nil
}

func absoluteByteDifference(a, b byte) uint8 {
	if a >= b {
		return a - b
	}
	return b - a
}

func boundedFrameError(value float64) bool {
	return value >= 0 && value <= 255 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
