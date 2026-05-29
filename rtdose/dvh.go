package rtdose

import (
	"context"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/rtstruct"
)

// DVHOptions bounds computation and controls histogram precision.
type DVHOptions struct {
	Bins      int
	MaxBins   int
	MaxVoxels int
}

// DVHBin is a cumulative dose-volume histogram point. Volume is expressed in
// cubic centimetres and VolumePercent is relative to the rasterized ROI.
type DVHBin struct {
	Dose          float64
	VolumeCC      float64
	VolumePercent float64
}

// DVHResult is a deterministic, PHI-free RT Dose analysis result.
type DVHResult struct {
	ROINumber      int
	ROIName        string
	DoseUnit       string
	SummationType  string
	VoxelCount     int
	VolumeCC       float64
	Minimum        float64
	Maximum        float64
	Mean           float64
	D95            float64
	D50            float64
	D2             float64
	PartialOverlap bool
	Bins           []DVHBin
	Provenance     string
}

// ComputeDVH rasterizes one RTSTRUCT ROI directly on the dose grid and computes
// a cumulative DVH. It checks cancellation in bounded chunks and never stores a
// per-voxel value list.
func ComputeDVH(ctx context.Context, dose *Dose, set *rtstruct.StructureSet, roiNumber int, opts DVHOptions) (DVHResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return DVHResult{}, ctx.Err()
	default:
	}
	if dose == nil || set == nil {
		return DVHResult{}, fmt.Errorf("%w: dose and RTSTRUCT are required", ErrInvalidObject)
	}
	if dose.FrameOfReferenceUID == "" || set.FrameOfReferenceUID == "" ||
		dose.FrameOfReferenceUID != set.FrameOfReferenceUID {
		return DVHResult{}, fmt.Errorf("%w: RT Dose and RTSTRUCT frame of reference differ", ErrGeometryMismatch)
	}
	roiName := ""
	for _, item := range set.ROIs {
		if item.Number == roiNumber {
			roiName = item.Name
			break
		}
	}
	if roiName == "" {
		return DVHResult{}, fmt.Errorf("%w: ROI %d", rtstruct.ErrMissingReference, roiNumber)
	}
	if opts.MaxVoxels <= 0 {
		opts.MaxVoxels = 64 * 1024 * 1024
	}
	totalGrid, ok := checkedProduct(dose.Columns, dose.Rows, dose.Frames)
	if !ok || totalGrid > opts.MaxVoxels {
		return DVHResult{}, fmt.Errorf("%w: %d dose voxels exceed limit %d", ErrMemoryLimit, totalGrid, opts.MaxVoxels)
	}
	if opts.MaxBins <= 0 {
		opts.MaxBins = 4096
	}
	if opts.Bins <= 0 {
		opts.Bins = 512
	}
	if opts.Bins > opts.MaxBins {
		return DVHResult{}, fmt.Errorf("%w: %d bins exceed limit %d", ErrMemoryLimit, opts.Bins, opts.MaxBins)
	}

	segmentation, err := rtstruct.Rasterize(set, rtstruct.RasterizeOptions{
		ROINumber: roiNumber,
		Geometry:  dose.Geometry,
		Columns:   dose.Columns,
		Rows:      dose.Rows,
	})
	if err != nil {
		return DVHResult{}, err
	}
	if segmentation.VoxelCount() == 0 {
		return DVHResult{}, fmt.Errorf("%w: ROI %d does not overlap the dose grid", ErrGeometryMismatch, roiNumber)
	}

	result := DVHResult{
		ROINumber:      roiNumber,
		ROIName:        roiName,
		DoseUnit:       dose.DisplayUnit(),
		SummationType:  dose.SummationType,
		Minimum:        math.Inf(1),
		PartialOverlap: roiExtendsOutsideDose(set, roiNumber, dose),
		Provenance:     dose.ProvenanceLabel(),
	}
	histogram := make([]float64, opts.Bins)
	processed := 0
	for _, frame := range segmentation.Slices() {
		mask, _ := segmentation.MaskAt(frame)
		if mask == nil {
			continue
		}
		thickness := doseSliceThickness(dose, frame)
		if thickness <= 0 {
			return DVHResult{}, fmt.Errorf("%w: slice thickness is unavailable", ErrGeometryMismatch)
		}
		voxelVolumeCC := thickness *
			dose.Geometry.Slices[frame].RowSpacing *
			dose.Geometry.Slices[frame].ColSpacing / 1000
		mask.ForEachPixel(func(column, row int) {
			if err != nil {
				return
			}
			processed++
			if processed&4095 == 0 {
				select {
				case <-ctx.Done():
					err = ctx.Err()
					return
				default:
				}
			}
			value, valid := dose.ValueAt(column, row, frame)
			if !valid {
				err = ErrGeometryMismatch
				return
			}
			result.VoxelCount++
			result.VolumeCC += voxelVolumeCC
			result.Mean += value * voxelVolumeCC
			if value < result.Minimum {
				result.Minimum = value
			}
			if value > result.Maximum {
				result.Maximum = value
			}
			index := 0
			if dose.Maximum > 0 {
				index = int(math.Floor(value / dose.Maximum * float64(opts.Bins-1)))
			}
			index = max(0, min(index, opts.Bins-1))
			histogram[index] += voxelVolumeCC
		})
		if err != nil {
			return DVHResult{}, err
		}
	}
	if result.VolumeCC <= 0 {
		return DVHResult{}, fmt.Errorf("%w: zero-volume ROI", ErrGeometryMismatch)
	}
	result.Mean /= result.VolumeCC

	result.Bins = make([]DVHBin, opts.Bins)
	cumulative := 0.0
	for index := opts.Bins - 1; index >= 0; index-- {
		cumulative += histogram[index]
		doseValue := 0.0
		if opts.Bins > 1 {
			doseValue = dose.Maximum * float64(index) / float64(opts.Bins-1)
		}
		result.Bins[index] = DVHBin{
			Dose:          doseValue,
			VolumeCC:      cumulative,
			VolumePercent: cumulative / result.VolumeCC * 100,
		}
	}
	result.D95 = doseAtVolume(result.Bins, 95)
	result.D50 = doseAtVolume(result.Bins, 50)
	result.D2 = doseAtVolume(result.Bins, 2)
	return result, nil
}

func doseAtVolume(bins []DVHBin, percent float64) float64 {
	value := 0.0
	for _, bin := range bins {
		if bin.VolumePercent+1e-9 >= percent {
			value = bin.Dose
		}
	}
	return value
}

func doseSliceThickness(dose *Dose, index int) float64 {
	if dose == nil {
		return 0
	}
	positions := dose.Geometry.Positions
	if index < 0 || index >= len(positions) {
		return 0
	}
	if len(positions) == 1 {
		return dose.SliceThickness
	}
	if index == 0 {
		return math.Abs(positions[1] - positions[0])
	}
	if index == len(positions)-1 {
		return math.Abs(positions[index] - positions[index-1])
	}
	return math.Abs(positions[index+1]-positions[index-1]) / 2
}

func roiExtendsOutsideDose(set *rtstruct.StructureSet, roiNumber int, dose *Dose) bool {
	if set == nil || dose == nil || len(dose.Geometry.Slices) == 0 {
		return true
	}
	first := dose.Geometry.Slices[0]
	firstThickness := doseSliceThickness(dose, 0)
	lastThickness := doseSliceThickness(dose, len(dose.Geometry.Positions)-1)
	if firstThickness <= 0 || lastThickness <= 0 {
		return true
	}
	minPosition := dose.Geometry.Positions[0] - firstThickness/2
	maxPosition := dose.Geometry.Positions[len(dose.Geometry.Positions)-1] + lastThickness/2
	for _, item := range set.ROIs {
		if item.Number != roiNumber {
			continue
		}
		for _, contour := range item.Contours {
			for _, point := range contour.Points {
				patient := render.Vec3{X: point.X, Y: point.Y, Z: point.Z}
				delta := patient.Sub(first.Origin)
				column := delta.Dot(first.RowDir) / first.ColSpacing
				row := delta.Dot(first.ColDir) / first.RowSpacing
				position := patient.Dot(dose.Geometry.Normal)
				if column < -0.5 || column > float64(dose.Columns)-0.5 ||
					row < -0.5 || row > float64(dose.Rows)-0.5 ||
					position < minPosition || position > maxPosition {
					return true
				}
			}
		}
	}
	return false
}
