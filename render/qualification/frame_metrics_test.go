package qualification

import (
	"errors"
	"math"
	"testing"
)

func TestCompareRGBA8FramesReportsMAERMSEP99ThresholdsAndSilhouette(t *testing.T) {
	reference := []byte{
		0, 10, 20, 0,
		100, 100, 100, 255,
	}
	candidate := []byte{
		10, 10, 10, 5,
		110, 80, 100, 0,
	}
	thresholds := RGBAFrameThresholdsV1{
		AbsoluteComponent: [4]uint8{5, 10, 9, 10}, AlphaSilhouetteCutoff: 1,
	}
	metrics, err := CompareRGBA8Frames(reference, candidate, 2, 1, thresholds)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.MeanAbsoluteError != [4]float64{10, 10, 5, 130} {
		t.Fatalf("MAE = %v", metrics.MeanAbsoluteError)
	}
	wantRMSE := [4]float64{10, math.Sqrt(200), math.Sqrt(50), math.Sqrt(32525)}
	for channel := range wantRMSE {
		if math.Abs(metrics.RootMeanSquareError[channel]-wantRMSE[channel]) > 1e-12 {
			t.Fatalf("RMSE[%d] = %.12f, want %.12f", channel, metrics.RootMeanSquareError[channel], wantRMSE[channel])
		}
	}
	if metrics.P99AbsoluteError != [4]float64{10, 20, 10, 255} ||
		metrics.MaximumAbsoluteError != [4]uint8{10, 20, 10, 255} ||
		metrics.ComponentsOverThreshold != [4]uint64{2, 1, 1, 1} ||
		metrics.PixelsWithAnyComponentOverLimit != 2 ||
		metrics.AlphaSilhouetteMismatchPixels != 2 ||
		metrics.PixelFractionOverThreshold() != 1 ||
		metrics.AlphaSilhouetteMismatchFraction() != 1 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestRGBAFrameMetricsRequireFrozenIndependentLimits(t *testing.T) {
	reference := []byte{10, 20, 30, 255}
	candidate := []byte{11, 18, 33, 255}
	metrics, err := CompareRGBA8Frames(
		reference, candidate, 1, 1,
		RGBAFrameThresholdsV1{AbsoluteComponent: [4]uint8{1, 2, 3, 1}, AlphaSilhouetteCutoff: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	limits := RGBAFrameMetricLimitsV1{
		MaximumMeanAbsoluteError:               [4]float64{1, 2, 3, 0},
		MaximumRootMeanSquareError:             [4]float64{1, 2, 3, 0},
		MaximumP99AbsoluteError:                [4]float64{1, 2, 3, 0},
		MaximumPixelFractionOverThreshold:      0,
		MaximumAlphaSilhouetteMismatchFraction: 0,
	}
	if err := metrics.RequireWithin(limits); err != nil {
		t.Fatalf("exact frozen limits rejected: %v", err)
	}
	limits.MaximumP99AbsoluteError[2] = 2
	if err := metrics.RequireWithin(limits); !errors.Is(err, ErrInvalidFrameMetrics) {
		t.Fatalf("over-limit metrics error = %v", err)
	}
}

func TestCompareRGBA8FramesRejectsExtentThresholdAndPayloadDrift(t *testing.T) {
	validThresholds := RGBAFrameThresholdsV1{AlphaSilhouetteCutoff: 1}
	tests := []struct {
		name       string
		reference  []byte
		candidate  []byte
		width      uint32
		height     uint32
		thresholds RGBAFrameThresholdsV1
	}{
		{"zero extent", nil, nil, 0, 1, validThresholds},
		{"short reference", []byte{0, 0, 0}, []byte{0, 0, 0, 0}, 1, 1, validThresholds},
		{"trailing candidate", []byte{0, 0, 0, 0}, []byte{0, 0, 0, 0, 0}, 1, 1, validThresholds},
		{"zero silhouette cutoff", []byte{0, 0, 0, 0}, []byte{0, 0, 0, 0}, 1, 1, RGBAFrameThresholdsV1{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CompareRGBA8Frames(
				test.reference, test.candidate, test.width, test.height, test.thresholds,
			); !errors.Is(err, ErrInvalidFrameMetrics) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCompareRGBA8FramesExactMatchProducesZeroMetrics(t *testing.T) {
	frame := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	metrics, err := CompareRGBA8Frames(
		frame, append([]byte(nil), frame...), 1, 2,
		RGBAFrameThresholdsV1{AlphaSilhouetteCutoff: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.MeanAbsoluteError != [4]float64{} ||
		metrics.RootMeanSquareError != [4]float64{} ||
		metrics.P99AbsoluteError != [4]float64{} ||
		metrics.MaximumAbsoluteError != [4]uint8{} ||
		metrics.PixelsWithAnyComponentOverLimit != 0 ||
		metrics.AlphaSilhouetteMismatchPixels != 0 {
		t.Fatalf("exact metrics are not zero: %+v", metrics)
	}
}
