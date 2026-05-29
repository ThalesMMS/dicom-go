package render

import "math"

// MeasureSpacing is the effective in-plane millimetres per output pixel.
type MeasureSpacing struct{ X, Y float64 }

func clampFloat(value, minValue, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}

func spatialSortValue(frame *Frame) float64 {
	if frame == nil {
		return 0
	}
	if frame.SliceLocationOK {
		return frame.SliceLocation
	}
	if len(frame.ImagePosition) >= 3 {
		return frame.ImagePosition[2]
	}
	if frame.Sort != 0 {
		return frame.Sort
	}
	return float64(frame.InstanceNumber)
}
