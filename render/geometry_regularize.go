package render

import (
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// RegularGridGeometry describes the exact patient-space target grid used to
// regularize a geometry that cannot be represented by one acquired affine.
// It contains no pixel data and is safe to use during a metadata-only planning
// pass.
type RegularGridGeometry struct {
	Dimensions           [3]int
	Origin               Vec3
	AxisX                Vec3
	AxisY                Vec3
	Normal               Vec3
	ColSpacing           float64
	RowSpacing           float64
	SliceSpacing         float64
	VoxelToPatientAffine GeometryAffine
	PatientToVoxelAffine GeometryAffine
}

// PlanRegularGridGeometry returns the same target grid that RegularGrid uses
// for resampling. Callers can therefore validate dimensions, memory budgets,
// and affines before decoding any pixels.
func PlanRegularGridGeometry(geometry VolumeGeometry) (RegularGridGeometry, error) {
	if len(geometry.Slices) == 0 {
		return RegularGridGeometry{}, fmt.Errorf("render: cannot plan empty regular grid")
	}
	first := geometry.Slices[0]
	if first.Columns <= 0 || first.Rows <= 0 ||
		!finitePositiveSpacing(first.ColSpacing) || !finitePositiveSpacing(first.RowSpacing) {
		return RegularGridGeometry{}, fmt.Errorf("render: invalid source geometry for regular grid")
	}

	minX, minY, minZ := math.Inf(1), math.Inf(1), math.Inf(1)
	maxX, maxY, maxZ := math.Inf(-1), math.Inf(-1), math.Inf(-1)
	base := first.Origin
	for _, slice := range geometry.Slices {
		for _, x := range []float64{0, float64(first.Columns-1) * first.ColSpacing} {
			for _, y := range []float64{0, float64(first.Rows-1) * first.RowSpacing} {
				point := slice.Origin.Add(geometry.RowDir.Scale(x)).Add(geometry.ColDir.Scale(y))
				delta := point.Sub(base)
				localX := delta.Dot(geometry.RowDir)
				localY := delta.Dot(geometry.ColDir)
				localZ := delta.Dot(geometry.Normal)
				minX, maxX = math.Min(minX, localX), math.Max(maxX, localX)
				minY, maxY = math.Min(minY, localY), math.Max(maxY, localY)
				minZ, maxZ = math.Min(minZ, localZ), math.Max(maxZ, localZ)
			}
		}
	}
	if !finiteBounds(minX, maxX, minY, maxY, minZ, maxZ) {
		return RegularGridGeometry{}, fmt.Errorf("render: non-finite patient bounds during regularization")
	}

	sliceSpacing := regularizedSliceSpacing(geometry.Spacings, geometry.MissingSlices)
	if geometry.RegularizedDepth > 1 && maxZ > minZ {
		sliceSpacing = (maxZ - minZ) / float64(geometry.RegularizedDepth-1)
	}
	if !finitePositiveSpacing(sliceSpacing) {
		return RegularGridGeometry{}, fmt.Errorf("render: invalid slice spacing for regular grid")
	}
	cols := maxInt(int(math.Ceil((maxX-minX)/first.ColSpacing-1e-12))+1, 1)
	rows := maxInt(int(math.Ceil((maxY-minY)/first.RowSpacing-1e-12))+1, 1)
	depth := maxInt(int(math.Ceil((maxZ-minZ)/sliceSpacing-1e-12))+1, 1)
	if cols > math.MaxUint16 || rows > math.MaxUint16 {
		return RegularGridGeometry{}, fmt.Errorf("render: regularized grid %dx%d exceeds DICOM frame limits", cols, rows)
	}
	if _, ok := checkedVoxelCount(cols, rows, depth); !ok {
		return RegularGridGeometry{}, fmt.Errorf("render: regularized grid dimensions overflow")
	}

	origin := base.
		Add(geometry.RowDir.Scale(minX)).
		Add(geometry.ColDir.Scale(minY)).
		Add(geometry.Normal.Scale(minZ))
	indexToPatient, patientToIndex, ok := geometryAffinePair(
		origin,
		geometry.RowDir.Scale(first.ColSpacing),
		geometry.ColDir.Scale(first.RowSpacing),
		geometry.Normal.Scale(sliceSpacing),
	)
	if !ok {
		return RegularGridGeometry{}, fmt.Errorf("render: regularized grid affine is not invertible")
	}
	return RegularGridGeometry{
		Dimensions: [3]int{cols, rows, depth},
		Origin:     origin, AxisX: geometry.RowDir, AxisY: geometry.ColDir, Normal: geometry.Normal,
		ColSpacing: first.ColSpacing, RowSpacing: first.RowSpacing, SliceSpacing: sliceSpacing,
		VoxelToPatientAffine: indexToPatient, PatientToVoxelAffine: patientToIndex,
	}, nil
}

// RegularGrid returns a uniform, untilted patient-space grid for consumers such
// as VR that cannot represent per-slice positions. Regular input returns the
// receiver unchanged. Irregular spacing, gaps, and gantry tilt are resampled
// once and cached; acquired frames are never mutated.
func (v *Volume) RegularGrid() (*Volume, error) {
	if v == nil {
		return nil, fmt.Errorf("render: nil volume")
	}
	if v.closed.Load() {
		return nil, ErrVolumeStoreClosed
	}
	if !v.geometry.RequiresResampling {
		return v, nil
	}
	v.regularizedOnce.Do(func() {
		if v.closed.Load() {
			v.regularizedErr = ErrVolumeStoreClosed
			return
		}
		v.regularized, v.regularizedErr = regularizePatientGrid(v)
	})
	return v.regularized, v.regularizedErr
}

func regularizePatientGrid(source *Volume) (*Volume, error) {
	if source == nil || source.Cols <= 0 || source.Rows <= 0 || source.Depth <= 0 {
		return nil, fmt.Errorf("render: cannot regularize empty volume")
	}
	sampler, ok := newDecodedVolumeSampler(source)
	if !ok {
		return nil, fmt.Errorf("render: cannot prepare source sampler for regularization")
	}
	defer sampler.Close()

	grid, err := PlanRegularGridGeometry(source.geometry)
	if err != nil {
		return nil, err
	}
	cols, rows, depth := grid.Dimensions[0], grid.Dimensions[1], grid.Dimensions[2]
	voxelCount, ok := checkedVoxelCount(cols, rows, depth)
	if !ok {
		return nil, fmt.Errorf("render: regularized grid dimensions overflow")
	}
	origin := grid.Origin
	sliceSpacing := grid.SliceSpacing
	photometric := normalizedPhotometric(source.Photometric())
	if photometric == "" {
		photometric = "MONOCHROME2"
	}
	defaultWindow := WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth}
	if len(source.slices) > 0 && source.slices[0] != nil && source.slices[0].DefaultWindow.Width > 0 {
		defaultWindow = source.slices[0].DefaultWindow
	}
	frames := make([]*Frame, depth)
	values := make([]float32, voxelCount)
	positions := make([]float64, depth)
	origins := make([]Vec3, depth)
	sliceGeometries := make([]SliceGeometry, depth)
	sourceOriginVoxel := source.PatientToVoxel(origin)
	columnDelta := source.PatientToVoxel(origin.Add(source.AxisX.Scale(source.ColSpacing))).Sub(sourceOriginVoxel)
	rowDelta := source.PatientToVoxel(origin.Add(source.AxisY.Scale(source.RowSpacing))).Sub(sourceOriginVoxel)
	sourceSliceOrigins := make([]Vec3, depth)
	for z := range sourceSliceOrigins {
		targetSliceOrigin := origin.Add(source.Normal.Scale(float64(z) * sliceSpacing))
		sourceSliceOrigins[z] = source.PatientToVoxel(targetSliceOrigin)
	}
	for z := 0; z < depth; z++ {
		sliceOrigin := origin.Add(source.Normal.Scale(float64(z) * sliceSpacing))
		planeOffset := z * rows * cols
		for y := 0; y < rows; y++ {
			sourceVoxel := sourceSliceOrigins[z].Add(rowDelta.Scale(float64(y)))
			for x := 0; x < cols; x++ {
				value, valid := sampler.trilinearAt(sourceVoxel)
				if valid {
					values[planeOffset+y*cols+x] = float32(value)
				}
				sourceVoxel = sourceVoxel.Add(columnDelta)
			}
		}
		metadata := pixeldata.Metadata{
			Rows:                      uint16(rows),
			Columns:                   uint16(cols),
			SamplesPerPixel:           1,
			BitsAllocated:             32,
			BitsStored:                32,
			HighBit:                   31,
			PhotometricInterpretation: photometric,
		}
		frames[z] = &Frame{Metadata: metadata, DefaultWindow: defaultWindow}
		positions[z] = float64(z) * sliceSpacing
		origins[z] = sliceOrigin
		sliceGeometries[z] = SliceGeometry{
			Origin: sliceOrigin, RowDir: source.AxisX, ColDir: source.AxisY, Normal: source.Normal,
			RowSpacing: source.RowSpacing, ColSpacing: source.ColSpacing, Rows: rows, Columns: cols,
		}
	}
	geometry := BuildVolumeGeometry(sliceGeometries, DefaultGeometryTolerances())
	target := &Volume{
		Cols: cols, Rows: rows, Depth: depth,
		Origin: origin, AxisX: source.AxisX, AxisY: source.AxisY, Normal: source.Normal,
		ColSpacing: source.ColSpacing, RowSpacing: source.RowSpacing, SliceSpacing: sliceSpacing,
		positions: positions, sliceOrigins: origins, regular: true, geometry: geometry,
		slices: frames, store: source.store,
	}
	if target.store == nil {
		target.store = NewVolumeStore()
	}
	// The source acquisition cannot be represented by one frozen affine when
	// spacing or gantry geometry is irregular. Cache a normalized copy of the
	// regular grid as the explicit parent so its descriptor and payload describe
	// the same voxel coordinates.
	sourceDescriptor, err := target.snapshotDescriptor(VolumeDerivationNormalized, 0)
	if err != nil {
		return nil, fmt.Errorf("render: describe source generation: %w", err)
	}
	sourceValues := append([]float32(nil), values...)
	sourceGeneration, err := target.store.replaceFloat32Owned(sourceDescriptor, sourceValues)
	if err != nil {
		return nil, fmt.Errorf("render: install source generation: %w", err)
	}
	source.snapshotMu.Lock()
	source.generation = sourceGeneration
	source.snapshotStats = VolumePreparationStats{
		VoxelBytes: uint64(len(sourceValues)) * 4,
		Frames:     target.Depth,
	}
	source.snapshotMu.Unlock()
	parentLease, err := target.store.Acquire(sourceGeneration)
	if err != nil {
		return nil, fmt.Errorf("render: lease source generation: %w", err)
	}
	parentGeneration := sourceGeneration
	descriptor, err := target.snapshotDescriptor(VolumeDerivationRegularized, parentGeneration)
	if err != nil {
		_ = parentLease.Release()
		return nil, err
	}
	targetGeneration, installErr := target.store.replaceRegularizedFloat32Owned(parentGeneration, descriptor, values)
	_ = parentLease.Release()
	if installErr != nil {
		return nil, installErr
	}
	// Mark the explicit generation as already installed. Future acquisitions
	// reuse it instead of decoding the metadata-only regularized frames.
	target.snapshotMu.Lock()
	target.generation = targetGeneration
	target.snapshotStats = VolumePreparationStats{
		VoxelBytes: uint64(len(values)) * 4,
		Frames:     target.Depth,
	}
	target.snapshotMu.Unlock()
	return target, nil
}

func finiteBounds(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func checkedVoxelCount(cols, rows, depth int) (int, bool) {
	if cols <= 0 || rows <= 0 || depth <= 0 || cols > math.MaxInt/rows {
		return 0, false
	}
	plane := cols * rows
	if plane > math.MaxInt/depth {
		return 0, false
	}
	return plane * depth, true
}
