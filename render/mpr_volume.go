package render

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNonCorrectableGeometry marks a stack whose available patient geometry is
// internally contradictory. Callers must not silently present such a stack as a
// corrected patient-space volume.
var ErrNonCorrectableGeometry = errors.New("render: non-correctable volume geometry")

// VolumePreparationStats describes the one-time CPU work that publishes a
// canonical modality-value generation. VoxelBytes is the retained canonical
// payload; TransientBytes excludes source DICOM pixels and reports additional
// full-volume working storage.
type VolumePreparationStats struct {
	NormalizationDuration    time.Duration
	CanonicalizationDuration time.Duration
	TotalDuration            time.Duration
	VoxelBytes               uint64
	TransientBytes           uint64
	Frames                   int
	Reused                   bool
}

// GeometryGuardrailError reports a stable reason for rejecting one 3D volume.
// The original source frames remain available to the caller's 2D path.
type GeometryGuardrailError struct {
	Reason     GeometryIssue
	FrameIndex int // one-based; zero for a stack-wide issue
}

func (e *GeometryGuardrailError) Error() string {
	if e == nil {
		return ErrNonCorrectableGeometry.Error()
	}
	if e.FrameIndex > 0 {
		return fmt.Sprintf("%s: %s at frame %d", ErrNonCorrectableGeometry, e.Reason, e.FrameIndex)
	}
	return fmt.Sprintf("%s: %s", ErrNonCorrectableGeometry, e.Reason)
}

func (e *GeometryGuardrailError) Unwrap() error { return ErrNonCorrectableGeometry }

// GeometryFallbackReason extracts the stable 2D fallback reason from an MPR/VR
// geometry error.
func GeometryFallbackReason(err error) (GeometryIssue, bool) {
	var guardrail *GeometryGuardrailError
	if errors.As(err, &guardrail) {
		return guardrail.Reason, true
	}
	return GeometryIssueNone, false
}

func isNonCorrectableGeometry(err error) bool {
	return errors.Is(err, ErrNonCorrectableGeometry)
}

// GantryTiltCorrection describes the patient-space shear correction applied by
// Volume's coordinate transforms. Pixel frames remain unchanged; reslicers use
// the per-slice origins lazily.
type GantryTiltCorrection struct {
	Applied      bool
	AngleDegrees float64
	TotalOffset  Vec3
	ShearPerMM   Vec3
}

// Volume is a patient-space volume model built from a series' slices
// (mpr-toolbar-plan.md §5.1). It carries the geometry needed for oblique
// reslicing, the crosshair, and reslice-aware measurement: an orthonormal basis
// (AxisX = per-column step, AxisY = per-row step, Normal = through-plane), the
// per-axis spacing in mm, and the voxel→patient / patient→voxel transforms.
//
// Voxel coordinates are (X=column, Y=row, Z=slice). The volume references the
// slices sorted along +Normal and lazily prepares a decoded modality-value cache
// when interpolation-heavy paths such as MPR/VR first sample it.
type Volume struct {
	Cols, Rows, Depth int

	Origin Vec3 // patient coords of voxel (0,0,0)
	AxisX  Vec3 // unit, per-column (image x) direction
	AxisY  Vec3 // unit, per-row (image y) direction
	Normal Vec3 // unit, through-plane (AxisX × AxisY)

	ColSpacing   float64 // mm per column step
	RowSpacing   float64 // mm per row step
	SliceSpacing float64 // mean mm per slice step (through-plane)

	// positions holds each slice's real along-normal position (mm) relative to
	// Origin (positions[0] == 0), ascending. regular reports whether the
	// inter-slice spacing is uniform; when it is, the through-plane voxel<->patient
	// mapping uses the fast SliceSpacing path, otherwise it interpolates the real
	// positions. sliceOrigins retains only O(depth) geometry so a stable tilted
	// stack can interpolate its lateral displacement without copying pixel data.
	positions    []float64
	sliceOrigins []Vec3
	regular      bool
	tilt         GantryTiltCorrection
	geometry     VolumeGeometry

	regularizedOnce sync.Once
	regularized     *Volume
	regularizedErr  error

	slices []*Frame // renderable slices sorted ascending along +Normal

	snapshotMu       sync.Mutex
	snapshotBuilding bool
	snapshotWait     chan struct{}
	snapshotErr      error
	snapshotStats    VolumePreparationStats
	store            *VolumeStore
	generation       uint64
	closeOnce        sync.Once
	closed           atomic.Bool

	// Cached dataset HU range (3d-toolbar-plan.md §5.1), computed lazily. huMu
	// guards the cache so concurrent renders/picks (both call HURange) stay
	// race-free as the code evolves.
	huMu      sync.Mutex
	huMin     float64
	huMax     float64
	huRangeOK bool
}

// BuildVolume constructs the patient-space volume from a series. It requires a
// consistent ImageOrientationPatient basis and a position
// (ImagePositionPatient, or SliceLocation as a fallback) on every contributing
// slice. Missing geometry on the whole series permits callers to use a legacy
// pixel-stack fallback; contradictory partial geometry is marked
// ErrNonCorrectableGeometry and must fail closed.
func BuildVolume(series *Stack) (*Volume, error) {
	return buildVolumeWithStore(series, NewVolumeStore())
}

// BuildVolumeWithTolerances constructs a patient-space volume using the
// caller's geometry tolerances. It is intended for workflows, such as export,
// whose acceptance threshold is intentionally stricter than the viewer's
// display-oriented defaults.
func BuildVolumeWithTolerances(series *Stack, tolerances GeometryTolerances) (*Volume, error) {
	return buildVolumeWithStoreAndTolerances(series, NewVolumeStore(), tolerances)
}

// BuildVolumeWithStore builds a volume in a caller-supplied family store. Store
// ownership transfers to the Volume: Close closes it after active leases finish.
// The caller can configure a hard ceiling before any voxel allocation is
// admitted.
func BuildVolumeWithStore(series *Stack, store *VolumeStore) (*Volume, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil volume store", ErrInvalidVolumeSnapshot)
	}
	return buildVolumeWithStore(series, store)
}

func buildVolumeWithStore(series *Stack, store *VolumeStore) (*Volume, error) {
	return buildVolumeWithStoreAndTolerances(series, store, DefaultGeometryTolerances())
}

func buildVolumeWithStoreAndTolerances(series *Stack, store *VolumeStore, tolerances GeometryTolerances) (*Volume, error) {
	if series == nil || len(series.Frames) == 0 {
		return nil, fmt.Errorf("render: no series selected")
	}
	first := firstRenderableSlice(series)
	if first == nil {
		return nil, fmt.Errorf("render: series has no displayable images")
	}
	rows := int(first.Metadata.Rows)
	cols := int(first.Metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return nil, fmt.Errorf("render: invalid slice dimensions")
	}
	if len(first.ImageOrientation) < 6 {
		return nil, fmt.Errorf("render: series lacks ImageOrientationPatient")
	}

	// Orthonormal basis: AxisX from the row-direction cosines, AxisY
	// Gram-Schmidt'd against it, Normal = AxisX × AxisY.
	axisX := Vec3{first.ImageOrientation[0], first.ImageOrientation[1], first.ImageOrientation[2]}.Normalize()
	axisYraw := Vec3{first.ImageOrientation[3], first.ImageOrientation[4], first.ImageOrientation[5]}
	axisY := axisYraw.Sub(axisX.Scale(axisYraw.Dot(axisX))).Normalize()
	normal := axisX.Cross(axisY).Normalize()
	if axisX.Length() == 0 || axisY.Length() == 0 || normal.Length() == 0 {
		return nil, fmt.Errorf("render: degenerate image orientation")
	}

	type geomSlice struct {
		sl       *Frame
		geometry SliceGeometry
		key      float64
	}
	rowSp := seriesRowSpacing(series)
	colSp := seriesColumnSpacing(series)
	if !finitePositiveSpacing(colSp) {
		colSp = 1
	}
	if !finitePositiveSpacing(rowSp) {
		rowSp = 1
	}
	collected, failure, failedIndex := collectSliceGeometries(series.Frames, normal, rowSp, colSp, rows, cols)
	if err := sliceGeometryFailureError(failure, failedIndex); err != nil {
		return nil, err
	}
	coll := make([]geomSlice, len(collected))
	for i := range collected {
		entry := collected[i]
		coll[i] = geomSlice{
			sl:       entry.frame,
			geometry: entry.geometry,
			key:      entry.geometry.PositionAlong(normal),
		}
	}
	if len(coll) == 0 {
		return nil, fmt.Errorf("render: series lacks slice positions")
	}
	sort.SliceStable(coll, func(i, j int) bool { return coll[i].key < coll[j].key })

	slices := make([]*Frame, len(coll))
	positions := make([]float64, len(coll))
	sliceOrigins := make([]Vec3, len(coll))
	for i := range coll {
		slices[i] = coll[i].sl
		positions[i] = coll[i].key - coll[0].key
		sliceOrigins[i] = coll[i].geometry.Origin
	}

	inputGeometries := make([]SliceGeometry, len(collected))
	for i := range collected {
		inputGeometries[i] = collected[i].geometry
	}
	geometry := BuildVolumeGeometry(inputGeometries, tolerances)
	applyTemporalGeometryGuardrail(&geometry, collected)
	geometry.Classification = classifyVolume(geometry)
	geometry.finalizeDisposition()
	if geometry.Disposition == GeometryUnsupported {
		return nil, &GeometryGuardrailError{
			Reason:     geometry.PrimaryIssue,
			FrameIndex: geometryFailureFrame(geometry.PrimaryIssue, collected, tolerances),
		}
	}

	sliceSp := 0.0
	if len(coll) > 1 {
		sliceSp = (coll[len(coll)-1].key - coll[0].key) / float64(len(coll)-1)
	}
	if sliceSp <= 0 || math.IsNaN(sliceSp) || math.IsInf(sliceSp, 0) {
		sliceSp = seriesSliceThickness(series)
	}
	if sliceSp <= 0 || math.IsNaN(sliceSp) || math.IsInf(sliceSp, 0) {
		sliceSp = 1
	}

	return &Volume{
		Cols:         cols,
		Rows:         rows,
		Depth:        len(coll),
		Origin:       coll[0].geometry.Origin,
		AxisX:        axisX,
		AxisY:        axisY,
		Normal:       normal,
		ColSpacing:   colSp,
		RowSpacing:   rowSp,
		SliceSpacing: sliceSp,
		positions:    positions,
		sliceOrigins: sliceOrigins,
		regular:      geometry.Regular,
		geometry:     geometry,
		tilt: GantryTiltCorrection{
			Applied:      geometry.GantryTilted,
			AngleDegrees: geometry.GantryTiltAngleDegrees,
			TotalOffset:  geometry.GantryTiltOffset,
			ShearPerMM:   geometry.GantryTiltShear,
		},
		slices: slices,
		store:  store,
	}, nil
}

func sliceGeometryFailureError(failure sliceGeometryFailure, failedIndex int) error {
	switch failure {
	case sliceGeometryValid:
		return nil
	case sliceGeometryGridMismatch:
		return &GeometryGuardrailError{Reason: GeometryIssueDifferentPixelGrid, FrameIndex: failedIndex + 1}
	case sliceGeometryInvalid:
		return &GeometryGuardrailError{Reason: GeometryIssueInvalidGeometry, FrameIndex: failedIndex + 1}
	case sliceGeometryMixedPositionSource:
		return &GeometryGuardrailError{Reason: GeometryIssueMixedPositionSource}
	default:
		return &GeometryGuardrailError{Reason: GeometryIssueInvalidGeometry}
	}
}

// Volume returns the cached patient-space volume for the series, building it
// on first use. It returns an error when the series lacks the geometry tags.
func (s *Stack) Volume() (*Volume, error) {
	if s == nil {
		return nil, fmt.Errorf("render: no series selected")
	}
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	if s.volumeClosed {
		return nil, ErrVolumeStoreClosed
	}
	if s.mprVolume != nil {
		return s.mprVolume, nil
	}
	if s.volumeStore == nil {
		s.volumeStore = NewVolumeStore()
	}
	vol, err := buildVolumeWithStore(s, s.volumeStore)
	if err != nil {
		return nil, err
	}
	s.mprVolume = vol
	return vol, nil
}

// GantryTiltCorrection returns the immutable correction metadata for this
// volume.
func (v *Volume) GantryTiltCorrection() GantryTiltCorrection {
	if v == nil {
		return GantryTiltCorrection{}
	}
	return v.tilt
}

// Geometry returns an immutable copy of the source stack's guardrail result.
func (v *Volume) Geometry() VolumeGeometry {
	if v == nil {
		return VolumeGeometry{}
	}
	out := v.geometry
	out.Slices = append([]SliceGeometry(nil), v.geometry.Slices...)
	out.Positions = append([]float64(nil), v.geometry.Positions...)
	out.Spacings = append([]float64(nil), v.geometry.Spacings...)
	out.Issues = append([]GeometryIssue(nil), v.geometry.Issues...)
	return out
}

// VoxelToPatient maps voxel coordinates (X=col, Y=row, Z=slice; may be
// fractional) to patient-space (mm). Stable tilted stacks interpolate the full
// per-slice origin, preserving lateral shear as well as irregular through-plane
// positions.
func (v *Volume) VoxelToPatient(p Vec3) Vec3 {
	if v.tilt.Applied {
		return v.sliceOriginAt(p.Z).
			Add(v.AxisX.Scale(p.X * v.ColSpacing)).
			Add(v.AxisY.Scale(p.Y * v.RowSpacing))
	}
	normalDist := p.Z * v.effectiveSliceSpacing()
	if !v.regular {
		normalDist = v.indexToPosition(p.Z)
	}
	return v.Origin.
		Add(v.AxisX.Scale(p.X * v.ColSpacing)).
		Add(v.AxisY.Scale(p.Y * v.RowSpacing)).
		Add(v.Normal.Scale(normalDist))
}

// PatientToVoxel is the inverse of VoxelToPatient. For tilted stacks it first
// locates Z along Normal, then subtracts the interpolated full slice origin
// before resolving X/Y. This makes patient-space crosshairs and measurements
// land on the correct voxel without modifying source frames.
func (v *Volume) PatientToVoxel(p Vec3) Vec3 {
	d := p.Sub(v.Origin)
	z := d.Dot(v.Normal) / v.effectiveSliceSpacing()
	if len(v.positions) > 0 {
		z = v.positionToIndex(d.Dot(v.Normal))
	}
	if v.tilt.Applied {
		d = p.Sub(v.sliceOriginAt(z))
	}
	return Vec3{
		X: d.Dot(v.AxisX) / v.ColSpacing,
		Y: d.Dot(v.AxisY) / v.RowSpacing,
		Z: z,
	}
}

func (v *Volume) effectiveSliceSpacing() float64 {
	if v != nil && finitePositiveSpacing(v.SliceSpacing) {
		return v.SliceSpacing
	}
	return 1
}

// sliceOriginAt interpolates the complete patient-space origin at a fractional
// slice index, extrapolating linearly at the two ends.
func (v *Volume) sliceOriginAt(z float64) Vec3 {
	n := len(v.sliceOrigins)
	if math.IsNaN(z) || math.IsInf(z, 0) {
		if n > 0 {
			return v.sliceOrigins[0]
		}
		return v.Origin
	}
	if n == 0 {
		return v.Origin.Add(v.Normal.Scale(v.indexToPosition(z)))
	}
	if n == 1 {
		return v.sliceOrigins[0]
	}
	if z <= 0 {
		return v.sliceOrigins[0].Add(v.sliceOrigins[1].Sub(v.sliceOrigins[0]).Scale(z))
	}
	if z >= float64(n-1) {
		return v.sliceOrigins[n-1].Add(v.sliceOrigins[n-1].Sub(v.sliceOrigins[n-2]).Scale(z - float64(n-1)))
	}
	i := int(math.Floor(z))
	frac := z - float64(i)
	return v.sliceOrigins[i].Add(v.sliceOrigins[i+1].Sub(v.sliceOrigins[i]).Scale(frac))
}

// indexToPosition maps a (possibly fractional) slice index to its real
// along-normal position (mm, relative to Origin) by interpolating the recorded
// slice positions, extrapolating linearly past either end.
func (v *Volume) indexToPosition(z float64) float64 {
	n := len(v.positions)
	if n == 0 {
		return z * v.effectiveSliceSpacing()
	}
	if n == 1 {
		return v.positions[0]
	}
	if z <= 0 {
		return v.positions[0] + z*(v.positions[1]-v.positions[0])
	}
	if z >= float64(n-1) {
		return v.positions[n-1] + (z-float64(n-1))*(v.positions[n-1]-v.positions[n-2])
	}
	i := int(math.Floor(z))
	frac := z - float64(i)
	return v.positions[i] + frac*(v.positions[i+1]-v.positions[i])
}

// positionToIndex maps a real along-normal position (mm, relative to Origin) to
// a fractional slice index, the inverse of indexToPosition.
func (v *Volume) positionToIndex(pos float64) float64 {
	n := len(v.positions)
	if n == 0 {
		return pos / v.effectiveSliceSpacing()
	}
	if n == 1 {
		return 0
	}
	if pos <= v.positions[0] {
		span := v.positions[1] - v.positions[0]
		if span == 0 {
			return 0
		}
		return (pos - v.positions[0]) / span
	}
	if pos >= v.positions[n-1] {
		span := v.positions[n-1] - v.positions[n-2]
		if span == 0 {
			return float64(n - 1)
		}
		return float64(n-1) + (pos-v.positions[n-1])/span
	}
	lo, hi := 0, n-1
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if v.positions[mid] <= pos {
			lo = mid
		} else {
			hi = mid
		}
	}
	span := v.positions[hi] - v.positions[lo]
	if span == 0 {
		return float64(lo)
	}
	return float64(lo) + (pos-v.positions[lo])/span
}

// ValueAt returns the rescaled value and photometric interpretation at integer
// voxel coordinates, sampling the sorted slice for that depth.
func (v *Volume) ValueAt(col, row, slice int) (float64, string, bool) {
	if v == nil || slice < 0 || slice >= len(v.slices) || col < 0 || row < 0 || col >= v.Cols || row >= v.Rows {
		return 0, "", false
	}
	if !v.geometry.RequiresResampling {
		lease, err := v.acquireDirectSnapshot()
		if err == nil {
			snapshot, snapshotErr := lease.Snapshot()
			if snapshotErr == nil {
				value, ok := snapshot.ModalityAt(uint32(col), uint32(row), uint32(slice))
				_ = lease.Release()
				if ok {
					return value, v.Photometric(), true
				}
			} else {
				_ = lease.Release()
			}
		}
	}
	sl := v.slices[slice]
	pv, ok := PixelValueAt(sl, col, row)
	if !ok {
		return 0, "", false
	}
	return pv.Rescaled, sl.Metadata.PhotometricInterpretation, true
}

// AcquireSnapshot returns the canonical immutable regular-affine generation.
// Geometry requiring correction is regularized first, so it never crosses the
// VolumeSnapshot boundary with an inexpressible per-slice transform.
func (v *Volume) AcquireSnapshot() (*VolumeLease, error) {
	return v.AcquireSnapshotContext(context.Background())
}

// AcquireSnapshotContext is AcquireSnapshot with cancellation while a caller
// waits for or performs the one-time CPU normalization. A canceled build is not
// cached, so a later caller can retry the same volume.
func (v *Volume) AcquireSnapshotContext(ctx context.Context) (*VolumeLease, error) {
	if v == nil {
		return nil, fmt.Errorf("render: nil volume")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v.geometry.RequiresResampling {
		grid, err := v.RegularGrid()
		if err != nil {
			return nil, err
		}
		return grid.acquireDirectSnapshotContext(ctx)
	}
	return v.acquireDirectSnapshotContext(ctx)
}

func (v *Volume) acquireDirectSnapshot() (*VolumeLease, error) {
	return v.acquireDirectSnapshotContext(context.Background())
}

func (v *Volume) acquireDirectSnapshotContext(ctx context.Context) (*VolumeLease, error) {
	if v == nil {
		return nil, fmt.Errorf("render: nil volume")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := v.prepareDirectSnapshot(ctx); err != nil {
		return nil, err
	}
	v.snapshotMu.Lock()
	store := v.store
	generation := v.generation
	v.snapshotMu.Unlock()
	if store == nil || generation == 0 {
		return nil, ErrVolumeNotFound
	}
	return store.Acquire(generation)
}

func decodeVolumeFloat32(v *Volume) ([]float32, error) {
	return decodeVolumeFloat32Context(context.Background(), v)
}

func decodeVolumeFloat32Context(ctx context.Context, v *Volume) ([]float32, error) {
	if v == nil {
		return nil, fmt.Errorf("render: nil volume")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	count, ok := checkedVoxelCount(v.Cols, v.Rows, v.Depth)
	if !ok {
		return nil, fmt.Errorf("%w: volume dimensions overflow", ErrInvalidVolumeSnapshot)
	}
	values := make([]float32, count)
	plane := v.Cols * v.Rows
	for index, frame := range v.slices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		destination := values[index*plane : (index+1)*plane]
		if err := decodeGrayscaleSliceInto(destination, frame); err != nil {
			return nil, fmt.Errorf("render: decode volume frame %d: %w", index+1, err)
		}
	}
	return values, nil
}

// PrepareSnapshotContext performs the one-time normalization without retaining
// a renderer lease. Concurrent callers share one build; waiters may cancel
// independently. Reused preparations report zero stage durations.
func (v *Volume) PrepareSnapshotContext(ctx context.Context) (VolumePreparationStats, error) {
	if v == nil {
		return VolumePreparationStats{}, fmt.Errorf("render: nil volume")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	target := v
	if v.geometry.RequiresResampling {
		if err := ctx.Err(); err != nil {
			return VolumePreparationStats{}, err
		}
		grid, err := v.RegularGrid()
		if err != nil {
			return VolumePreparationStats{}, err
		}
		target = grid
	}
	stats, err := target.prepareDirectSnapshot(ctx)
	stats.TotalDuration = time.Since(started)
	return stats, err
}

func (v *Volume) prepareDirectSnapshot(ctx context.Context) (VolumePreparationStats, error) {
	for {
		if err := ctx.Err(); err != nil {
			return VolumePreparationStats{}, err
		}
		if v.closed.Load() {
			return VolumePreparationStats{}, ErrVolumeStoreClosed
		}

		v.snapshotMu.Lock()
		if v.generation != 0 && v.snapshotErr == nil {
			stats := VolumePreparationStats{
				VoxelBytes:     v.snapshotStats.VoxelBytes,
				TransientBytes: v.snapshotStats.TransientBytes,
				Frames:         v.snapshotStats.Frames,
				Reused:         true,
			}
			v.snapshotMu.Unlock()
			return stats, nil
		}
		if v.snapshotErr != nil {
			err := v.snapshotErr
			v.snapshotMu.Unlock()
			return VolumePreparationStats{}, err
		}
		if v.snapshotBuilding {
			wait := v.snapshotWait
			v.snapshotMu.Unlock()
			select {
			case <-ctx.Done():
				return VolumePreparationStats{}, ctx.Err()
			case <-wait:
				continue
			}
		}
		v.snapshotBuilding = true
		v.snapshotWait = make(chan struct{})
		wait := v.snapshotWait
		v.snapshotMu.Unlock()

		stats, generation, err := v.buildDirectSnapshot(ctx)
		v.snapshotMu.Lock()
		if v.closed.Load() {
			err = ErrVolumeStoreClosed
		}
		if err == nil {
			v.generation = generation
			v.snapshotStats = stats
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			v.snapshotErr = err
		}
		v.snapshotBuilding = false
		close(wait)
		v.snapshotWait = nil
		v.snapshotMu.Unlock()
		return stats, err
	}
}

func (v *Volume) buildDirectSnapshot(ctx context.Context) (VolumePreparationStats, uint64, error) {
	started := time.Now()
	normalizationStarted := time.Now()
	values, err := decodeVolumeFloat32Context(ctx, v)
	stats := VolumePreparationStats{
		NormalizationDuration: time.Since(normalizationStarted),
		Frames:                v.Depth,
	}
	if err != nil {
		stats.TotalDuration = time.Since(started)
		return stats, 0, err
	}
	stats.VoxelBytes = uint64(len(values)) * 4
	if err := ctx.Err(); err != nil {
		stats.TotalDuration = time.Since(started)
		return stats, 0, err
	}

	canonicalStarted := time.Now()
	descriptor, err := v.snapshotDescriptor(VolumeDerivationNormalized, 0)
	if err != nil {
		stats.CanonicalizationDuration = time.Since(canonicalStarted)
		stats.TotalDuration = time.Since(started)
		return stats, 0, err
	}
	v.snapshotMu.Lock()
	if v.store == nil {
		v.store = NewVolumeStore()
	}
	store := v.store
	v.snapshotMu.Unlock()
	generation, err := store.replaceFloat32Owned(descriptor, values)
	stats.CanonicalizationDuration = time.Since(canonicalStarted)
	stats.TotalDuration = time.Since(started)
	return stats, generation, err
}

func (v *Volume) snapshotDescriptor(derivation VolumeDerivation, parent uint64) (VolumeDescriptor, error) {
	if v == nil || v.Cols <= 0 || v.Rows <= 0 || v.Depth <= 0 ||
		v.Cols > math.MaxUint32 || v.Rows > math.MaxUint32 || v.Depth > math.MaxUint32 {
		return VolumeDescriptor{}, fmt.Errorf("%w: invalid volume dimensions", ErrInvalidVolumeSnapshot)
	}
	indexToPatient, patientToIndex, ok := geometryAffinePair(
		v.Origin,
		v.AxisX.Scale(v.ColSpacing),
		v.AxisY.Scale(v.RowSpacing),
		v.Normal.Scale(v.SliceSpacing),
	)
	if !ok {
		return VolumeDescriptor{}, fmt.Errorf("%w: invalid patient affine", ErrInvalidVolumeSnapshot)
	}
	rowStride := uint64(v.Cols) * 4
	sliceStride := uint64(v.Rows) * rowStride
	return VolumeDescriptor{
		ContractVersion:   VolumeSnapshotContractVersion,
		HeaderSize:        VolumeSnapshotHeaderSizeV1,
		ParentGeneration:  parent,
		Derivation:        derivation,
		Dimensions:        [3]uint32{uint32(v.Cols), uint32(v.Rows), uint32(v.Depth)},
		Components:        1,
		ScalarFormat:      VolumeScalarF32ModalityLE,
		SampleDomain:      VolumeSampleDomainModality,
		RowStrideBytes:    rowStride,
		SliceStrideBytes:  sliceStride,
		ByteLength:        uint64(v.Depth) * sliceStride,
		RescaleSlope:      1,
		SpacingMM:         [3]float64{v.ColSpacing, v.RowSpacing, v.SliceSpacing},
		IndexToPatientLPS: indexToPatient,
		PatientLPSToIndex: patientToIndex,
	}, nil
}

// VolumeGeneration returns the installed canonical generation. It is zero
// until the first snapshot acquisition and never represents presentation state.
func (v *Volume) VolumeGeneration() uint64 {
	if v == nil {
		return 0
	}
	if v.geometry.RequiresResampling {
		grid, err := v.RegularGrid()
		if err != nil {
			return 0
		}
		return grid.VolumeGeneration()
	}
	lease, err := v.acquireDirectSnapshot()
	if err != nil {
		return 0
	}
	generation := lease.Generation()
	_ = lease.Release()
	return generation
}

// VolumeStoreIdentity returns the opaque identity of the canonical store used
// by this volume family. It is comparable in-process but intentionally cannot
// be serialized or converted to an address.
func (v *Volume) VolumeStoreIdentity() VolumeStoreIdentity {
	if v == nil {
		return VolumeStoreIdentity{}
	}
	if v.geometry.RequiresResampling {
		grid, err := v.RegularGrid()
		if err != nil {
			return VolumeStoreIdentity{}
		}
		return grid.VolumeStoreIdentity()
	}
	if v.store == nil {
		return VolumeStoreIdentity{}
	}
	return v.store.storeIdentity()
}

// VolumeStoreStats returns exact residency accounting for this volume family.
func (v *Volume) VolumeStoreStats() VolumeStoreStats {
	if v == nil || v.store == nil {
		return VolumeStoreStats{}
	}
	return v.store.Stats()
}

// TrackMemory attaches an explicit raw/render/backend allocation to the
// canonical generation.
func (v *Volume) TrackMemory(kind VolumeMemoryKind, byteCount uint64, release func()) (*VolumeMemoryLease, error) {
	lease, err := v.AcquireSnapshot()
	if err != nil {
		return nil, err
	}
	generation := lease.Generation()
	_ = lease.Release()
	grid := v
	if v.geometry.RequiresResampling {
		grid, err = v.RegularGrid()
		if err != nil {
			return nil, err
		}
	}
	return grid.store.TrackMemory(generation, kind, byteCount, release)
}

// Close retires every generation. Active renderer leases remain valid until
// they release, after which all voxel and tracked buffers become reclaimable.
func (v *Volume) Close() error {
	if v == nil {
		return nil
	}
	var err error
	v.closeOnce.Do(func() {
		v.closed.Store(true)
		v.regularizedOnce.Do(func() {
			v.regularizedErr = ErrVolumeStoreClosed
		})
		v.snapshotMu.Lock()
		v.snapshotErr = ErrVolumeStoreClosed
		v.snapshotMu.Unlock()
		if v.regularized != nil && v.regularized != v {
			err = v.regularized.Close()
		}
		if v.store != nil {
			closeErr := v.store.Close()
			if err == nil {
				err = closeErr
			}
		}
	})
	return err
}
