package render

import (
	"math"
	"sort"

	"github.com/ThalesMMS/dicom-go/dynamic"
)

// SliceGeometry is the patient-space geometry of a single image, derived from
// ImageOrientationPatient, ImagePositionPatient, and Pixel Spacing. It is the
// per-slice building block of VolumeGeometry and keeps MPR, fusion, crosshair,
// measurement, and CPR work from re-deriving spatial context from raw tags.
//
// Directions follow DICOM conventions: RowDir is the direction of increasing
// column index (ImageOrientationPatient values 1-3) and ColDir the direction of
// increasing row index (values 4-6); Normal = RowDir × ColDir.
type SliceGeometry struct {
	Origin     Vec3 // ImagePositionPatient: patient coords of the first voxel center
	RowDir     Vec3 // unit direction of increasing column index
	ColDir     Vec3 // unit direction of increasing row index
	Normal     Vec3 // unit through-plane direction (RowDir × ColDir)
	RowSpacing float64
	ColSpacing float64
	Rows       int
	Columns    int
}

// WorldDistance returns the Euclidean distance in millimeters between two
// patient-coordinate points.
func WorldDistance(a, b WorldPoint) float64 {
	return Vec3{X: b.X - a.X, Y: b.Y - a.Y, Z: b.Z - a.Z}.Length()
}

// WorldAngle returns the angle at vertex between a and b in degrees. When
// acute is true, the supplementary angle is folded into the acute range.
func WorldAngle(a, vertex, b WorldPoint, acute bool) (float64, bool) {
	return WorldSegmentAngle(vertex, a, vertex, b, acute)
}

// WorldSegmentAngle returns the angle between patient-coordinate segments ab
// and cd in degrees. Degenerate segments return ok=false.
func WorldSegmentAngle(a, b, c, d WorldPoint, acute bool) (float64, bool) {
	u := Vec3{X: b.X - a.X, Y: b.Y - a.Y, Z: b.Z - a.Z}
	v := Vec3{X: d.X - c.X, Y: d.Y - c.Y, Z: d.Z - c.Z}
	denominator := u.Length() * v.Length()
	if denominator == 0 || math.IsNaN(denominator) || math.IsInf(denominator, 0) {
		return 0, false
	}
	cosine := u.Dot(v) / denominator
	if acute {
		cosine = math.Abs(cosine)
	}
	cosine = math.Max(-1, math.Min(1, cosine))
	return math.Acos(cosine) * 180 / math.Pi, true
}

// PositionAlong projects the slice origin onto a reference normal, giving its
// real position (mm) along the stacking axis.
func (g SliceGeometry) PositionAlong(normal Vec3) float64 {
	return g.Origin.Dot(normal)
}

// VolumeClassification summarizes the dominant geometric character of a volume.
type VolumeClassification int

const (
	// VolumeRegular is an evenly-spaced, orientation-stable, untilted stack.
	VolumeRegular VolumeClassification = iota
	// VolumeSingleSlice has fewer than two slices.
	VolumeSingleSlice
	// VolumeIrregularSpacing has inconsistent inter-slice spacing that is not a
	// clean integer-multiple "missing slice" pattern.
	VolumeIrregularSpacing
	// VolumeMissingSlices has gaps that are integer multiples of a base spacing.
	VolumeMissingSlices
	// VolumeGantryTilted has a stacking direction not parallel to the slice
	// normal (sheared stack), e.g. a tilted CT gantry.
	VolumeGantryTilted
	// VolumeUnstableOrientation has slices whose orientations diverge beyond
	// tolerance.
	VolumeUnstableOrientation
	// VolumeDuplicatePositions contains two acquired frames at the same
	// patient-space position without enough temporal identity to separate them.
	VolumeDuplicatePositions
	// VolumeMixedOrientation changes its in-plane ImageOrientationPatient basis
	// while keeping a nominally common normal.
	VolumeMixedOrientation
	// VolumeInconsistentNormals changes through-plane normal beyond tolerance.
	VolumeInconsistentNormals
	// VolumeTemporalInterleaved contains more than one temporal volume and must
	// be split before a 3D renderer consumes it.
	VolumeTemporalInterleaved
)

func (c VolumeClassification) String() string {
	switch c {
	case VolumeRegular:
		return "regular"
	case VolumeSingleSlice:
		return "single-slice"
	case VolumeIrregularSpacing:
		return "irregular-spacing"
	case VolumeMissingSlices:
		return "missing-slices"
	case VolumeGantryTilted:
		return "gantry-tilted"
	case VolumeUnstableOrientation:
		return "unstable-orientation"
	case VolumeDuplicatePositions:
		return "duplicate-positions"
	case VolumeMixedOrientation:
		return "mixed-orientation"
	case VolumeInconsistentNormals:
		return "inconsistent-normals"
	case VolumeTemporalInterleaved:
		return "temporal-interleaving"
	default:
		return "unknown"
	}
}

// GeometryDisposition is the guardrail decision for a stack.
type GeometryDisposition uint8

const (
	// GeometryUnsupported cannot safely become one 3D volume. Its source frames
	// remain valid for independent 2D display. It is also the safe zero value.
	GeometryUnsupported GeometryDisposition = iota
	// GeometryRegularFastPath needs neither patient-space resampling nor a
	// fallback and preserves the existing regular-grid path.
	GeometryRegularFastPath
	// GeometryRegularizable is spatially coherent but needs deterministic
	// ordering and/or patient-space resampling before a uniform-grid consumer.
	GeometryRegularizable
)

func (d GeometryDisposition) String() string {
	switch d {
	case GeometryRegularFastPath:
		return "regular-fast-path"
	case GeometryRegularizable:
		return "regularizable"
	case GeometryUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// GeometryIssue is a stable machine-readable reason recorded by geometry
// inspection and fallback errors.
type GeometryIssue string

const (
	GeometryIssueNone                     GeometryIssue = ""
	GeometryIssueSingleSlice              GeometryIssue = "single-slice"
	GeometryIssueReversedStack            GeometryIssue = "reversed-stack"
	GeometryIssueDuplicatePositions       GeometryIssue = "duplicate-positions"
	GeometryIssueSliceGaps                GeometryIssue = "slice-gaps"
	GeometryIssueIrregularSpacing         GeometryIssue = "irregular-spacing"
	GeometryIssueGantryTilt               GeometryIssue = "gantry-tilt"
	GeometryIssueInconsistentGantryShear  GeometryIssue = "inconsistent-gantry-shear"
	GeometryIssueMixedOrientation         GeometryIssue = "mixed-orientation"
	GeometryIssueInconsistentNormals      GeometryIssue = "inconsistent-normals"
	GeometryIssueInconsistentPixelSpacing GeometryIssue = "inconsistent-pixel-spacing"
	GeometryIssueTemporalInterleaving     GeometryIssue = "temporal-interleaving"
	GeometryIssueDifferentPixelGrid       GeometryIssue = "different-pixel-grid"
	GeometryIssueInvalidGeometry          GeometryIssue = "invalid-geometry"
	GeometryIssueMixedPositionSource      GeometryIssue = "mixed-position-source"
)

// GeometryTolerances bounds the classification thresholds. The zero value is not
// useful; use DefaultGeometryTolerances.
type GeometryTolerances struct {
	// SpacingRel is the fractional tolerance on inter-slice spacing relative to
	// the base spacing (e.g. 0.05 = 5%).
	SpacingRel float64
	// SpacingAbs is the absolute spacing tolerance floor in mm.
	SpacingAbs float64
	// OrientationCos is the minimum signed dot product between each slice basis
	// vector and its reference for the orientation to count as stable.
	OrientationCos float64
	// TiltCos is the minimum |dot| between the stacking direction and the slice
	// normal for the stack to count as untilted.
	TiltCos float64
	// PositionAbs is the absolute patient-space tolerance in millimetres used to
	// distinguish duplicate slice positions and affine residuals.
	PositionAbs float64
	// ShearAbs is the maximum difference between interval and whole-stack
	// in-plane shear per millimetre. tan(1 degree) matches the tilt boundary.
	ShearAbs float64
	// AffineRoundTrip is the maximum absolute voxel-coordinate error accepted
	// by the voxel->patient->voxel conformance check.
	AffineRoundTrip float64
}

// DefaultGeometryTolerances returns clinically reasonable defaults: 5% (or
// 0.01 mm) spacing tolerance, ~1° orientation stability, and ~1° gantry-tilt
// threshold.
func DefaultGeometryTolerances() GeometryTolerances {
	return GeometryTolerances{
		SpacingRel:      0.05,
		SpacingAbs:      0.01,
		OrientationCos:  math.Cos(1 * math.Pi / 180),
		TiltCos:         math.Cos(1 * math.Pi / 180),
		PositionAbs:     1e-6,
		ShearAbs:        math.Tan(1 * math.Pi / 180),
		AffineRoundTrip: 1e-9,
	}
}

// GeometryMetrics records the measured values used by the classification. It
// makes tolerance decisions inspectable rather than silently averaging them.
type GeometryMetrics struct {
	SliceCount                 int
	MinimumSpacingMM           float64
	MaximumSpacingMM           float64
	MeanSpacingMM              float64
	MaximumSpacingDeviationMM  float64
	MaximumOrientationAngleDeg float64
	MaximumNormalAngleDeg      float64
	MaximumAffineResidualMM    float64
	MaximumShearDeviation      float64
}

// VolumeGeometry is the patient-space geometry of a stack of slices, classifying
// regularity, orientation stability, and gantry tilt and preserving the real
// position of every slice along the stacking normal.
type VolumeGeometry struct {
	Slices    []SliceGeometry // sorted ascending along Normal
	RowDir    Vec3            // reference in-plane directions (from the first slice)
	ColDir    Vec3
	Normal    Vec3
	Positions []float64 // real position (mm) along Normal, one per slice, ascending
	// Spacings holds the inter-slice spacing (mm) between consecutive slices,
	// len(Slices)-1 entries.
	Spacings    []float64
	MeanSpacing float64

	Regular           bool
	OrientationStable bool
	GantryTilted      bool
	// GantryTiltOffset is the total first-to-last in-plane displacement in
	// patient millimeters. GantryTiltShear is that displacement per millimeter
	// travelled along Normal. The vector preserves tilt direction; the angle is
	// the unsigned divergence between the stack direction and Normal.
	GantryTiltOffset       Vec3
	GantryTiltShear        Vec3
	GantryTiltAngleDegrees float64
	MissingSlices          bool
	Classification         VolumeClassification

	Disposition          GeometryDisposition
	PrimaryIssue         GeometryIssue
	Issues               []GeometryIssue
	ReversedInput        bool
	InputReordered       bool
	DuplicatePositions   bool
	TemporalInterleaved  bool
	RequiresResampling   bool
	SourceAffine         bool
	RegularizedDepth     int
	VoxelToPatientAffine GeometryAffine
	PatientToVoxelAffine GeometryAffine
	Metrics              GeometryMetrics
}

// GeometryAssessment is the complete ingest decision exposed to viewers. A
// GeometryUnsupported result retains a stable fallback reason while source
// frames remain independently renderable in 2D.
type GeometryAssessment struct {
	Geometry       VolumeGeometry
	Disposition    GeometryDisposition
	FallbackReason GeometryIssue
	FailureFrame   int // one-based; zero when the issue is stack-wide
}

// GeometryFrameGroup is one deterministic temporal/spatial stack suitable for
// independent volume inspection. Frame pointers refer to the immutable source
// inputs; callers that mutate stacks must copy them.
type GeometryFrameGroup struct {
	TemporalOrdinal  int
	TemporalPosition int
	HasTemporal      bool
	StackID          string
	Frames           []*Frame
}

// BuildVolumeGeometry sorts the slices along the reference normal and classifies
// the stack. The reference orientation is taken from the first input slice.
func BuildVolumeGeometry(slices []SliceGeometry, tol GeometryTolerances) VolumeGeometry {
	tol = normalizedGeometryTolerances(tol)
	out := VolumeGeometry{
		OrientationStable: true,
		Regular:           true,
		Disposition:       GeometryRegularFastPath,
	}
	if len(slices) == 0 {
		out.Classification = VolumeSingleSlice
		out.Disposition = GeometryUnsupported
		out.PrimaryIssue = GeometryIssueInvalidGeometry
		out.Issues = []GeometryIssue{GeometryIssueInvalidGeometry}
		return out
	}

	ref := slices[0]
	out.RowDir = ref.RowDir
	out.ColDir = ref.ColDir
	out.Normal = ref.Normal
	out.Metrics.SliceCount = len(slices)

	type indexedGeometry struct {
		index    int
		geometry SliceGeometry
		position float64
	}
	indexed := make([]indexedGeometry, len(slices))
	inputPositions := make([]float64, len(slices))
	for i, geometry := range slices {
		position := geometry.PositionAlong(ref.Normal)
		indexed[i] = indexedGeometry{index: i, geometry: geometry, position: position}
		inputPositions[i] = position
	}
	sort.SliceStable(indexed, func(i, j int) bool {
		return indexed[i].position < indexed[j].position
	})
	sorted := make([]SliceGeometry, len(indexed))
	for i := range indexed {
		sorted[i] = indexed[i].geometry
		out.InputReordered = out.InputReordered || indexed[i].index != i
	}
	out.Slices = sorted
	out.ReversedInput = reversedGeometryInput(inputPositions, tol.PositionAbs)
	if out.ReversedInput {
		out.addIssue(GeometryIssueReversedStack)
	}

	out.Positions = make([]float64, len(sorted))
	for i, g := range sorted {
		out.Positions[i] = g.PositionAlong(ref.Normal)
	}

	// Orientation stability: every complete in-plane basis must stay within
	// tolerance of the reference basis. Comparing only normals would miss
	// rotations around Normal and accepting absolute dot products would treat a
	// 180-degree pixel-axis flip as stable even though one shared voxel basis
	// cannot represent it.
	mixedOrientation := false
	inconsistentNormals := false
	inconsistentPixelSpacing := false
	for _, g := range sorted {
		rowAngle := vectorAngleDegrees(g.RowDir, ref.RowDir)
		colAngle := vectorAngleDegrees(g.ColDir, ref.ColDir)
		normalAngle := vectorAngleDegrees(g.Normal, ref.Normal)
		out.Metrics.MaximumOrientationAngleDeg = math.Max(out.Metrics.MaximumOrientationAngleDeg, math.Max(rowAngle, colAngle))
		out.Metrics.MaximumNormalAngleDeg = math.Max(out.Metrics.MaximumNormalAngleDeg, normalAngle)
		if g.Normal.Dot(ref.Normal) < tol.OrientationCos {
			inconsistentNormals = true
		} else if g.RowDir.Dot(ref.RowDir) < tol.OrientationCos ||
			g.ColDir.Dot(ref.ColDir) < tol.OrientationCos {
			mixedOrientation = true
		}
		if !spacingWithinTolerance(g.RowSpacing, ref.RowSpacing, tol) ||
			!spacingWithinTolerance(g.ColSpacing, ref.ColSpacing, tol) {
			inconsistentPixelSpacing = true
		}
	}
	out.OrientationStable = !mixedOrientation && !inconsistentNormals
	if inconsistentNormals {
		out.addIssue(GeometryIssueInconsistentNormals)
	}
	if mixedOrientation {
		out.addIssue(GeometryIssueMixedOrientation)
	}
	if inconsistentPixelSpacing {
		out.addIssue(GeometryIssueInconsistentPixelSpacing)
	}

	if len(sorted) < 2 {
		out.Classification = VolumeSingleSlice
		out.RegularizedDepth = 1
		out.SourceAffine = true
		out.addIssue(GeometryIssueSingleSlice)
		out.buildAffines(ref.Origin, ref.Normal, ref.ColSpacing, ref.RowSpacing, 1, tol)
		if !out.affinesConform(tol) {
			out.addIssue(GeometryIssueInvalidGeometry)
		}
		out.finalizeDisposition()
		return out
	}

	out.Spacings = make([]float64, len(sorted)-1)
	var spacingSum float64
	for i := 1; i < len(sorted); i++ {
		s := out.Positions[i] - out.Positions[i-1]
		out.Spacings[i-1] = s
		spacingSum += s
		if s <= tol.PositionAbs {
			out.DuplicatePositions = true
		}
	}
	out.MeanSpacing = spacingSum / float64(len(out.Spacings))
	out.Metrics.MeanSpacingMM = out.MeanSpacing
	out.Metrics.MinimumSpacingMM, out.Metrics.MaximumSpacingMM = spacingRange(out.Spacings)
	for _, spacing := range out.Spacings {
		out.Metrics.MaximumSpacingDeviationMM = math.Max(
			out.Metrics.MaximumSpacingDeviationMM,
			math.Abs(spacing-out.MeanSpacing),
		)
	}
	if out.DuplicatePositions {
		out.addIssue(GeometryIssueDuplicatePositions)
	}

	out.Regular, out.MissingSlices = classifySpacing(out.Spacings, tol)
	out.GantryTilted = detectGantryTilt(sorted, ref.Normal, tol)
	out.GantryTiltOffset, out.GantryTiltShear, out.GantryTiltAngleDegrees = gantryTiltMetrics(sorted, ref.Normal)
	if out.GantryTilted {
		out.Metrics.MaximumShearDeviation = maximumShearDeviation(sorted, ref.Normal, out.GantryTiltShear)
	}
	if out.MissingSlices {
		out.addIssue(GeometryIssueSliceGaps)
	} else if !out.Regular && !out.DuplicatePositions {
		out.addIssue(GeometryIssueIrregularSpacing)
	}
	if out.GantryTilted {
		out.addIssue(GeometryIssueGantryTilt)
		if out.Metrics.MaximumShearDeviation > tol.ShearAbs {
			out.addIssue(GeometryIssueInconsistentGantryShear)
		}
	}

	zStep := sorted[len(sorted)-1].Origin.Sub(sorted[0].Origin).Scale(1 / float64(len(sorted)-1))
	out.Metrics.MaximumAffineResidualMM = maximumAffineResidual(sorted, sorted[0].Origin, zStep)
	affineResidualLimit := math.Max(tol.PositionAbs, math.Max(tol.SpacingAbs, tol.SpacingRel*math.Abs(out.MeanSpacing)))
	out.SourceAffine = out.OrientationStable && !inconsistentPixelSpacing && !out.DuplicatePositions &&
		out.Metrics.MaximumAffineResidualMM <= affineResidualLimit

	targetSpacing := regularizedSliceSpacing(out.Spacings, out.MissingSlices)
	span := out.Positions[len(out.Positions)-1] - out.Positions[0]
	out.RegularizedDepth = regularizedDepth(span, targetSpacing, len(sorted))
	if out.RegularizedDepth > 1 && span > 0 {
		targetSpacing = span / float64(out.RegularizedDepth-1)
	}
	out.buildAffines(sorted[0].Origin, ref.Normal, ref.ColSpacing, ref.RowSpacing, targetSpacing, tol)
	if !out.affinesConform(tol) {
		out.addIssue(GeometryIssueInvalidGeometry)
	}

	out.Classification = classifyVolume(out)
	out.RequiresResampling = out.MissingSlices || (!out.Regular && !out.DuplicatePositions) || out.GantryTilted || !out.SourceAffine
	out.finalizeDisposition()
	return out
}

func normalizedGeometryTolerances(tol GeometryTolerances) GeometryTolerances {
	defaults := DefaultGeometryTolerances()
	if !finitePositiveSpacing(tol.SpacingRel) {
		tol.SpacingRel = defaults.SpacingRel
	}
	if !finitePositiveSpacing(tol.SpacingAbs) {
		tol.SpacingAbs = defaults.SpacingAbs
	}
	if tol.OrientationCos <= 0 || tol.OrientationCos > 1 || math.IsNaN(tol.OrientationCos) {
		tol.OrientationCos = defaults.OrientationCos
	}
	if tol.TiltCos <= 0 || tol.TiltCos > 1 || math.IsNaN(tol.TiltCos) {
		tol.TiltCos = defaults.TiltCos
	}
	if !finitePositiveSpacing(tol.PositionAbs) {
		tol.PositionAbs = defaults.PositionAbs
	}
	if !finitePositiveSpacing(tol.ShearAbs) {
		tol.ShearAbs = defaults.ShearAbs
	}
	if !finitePositiveSpacing(tol.AffineRoundTrip) {
		tol.AffineRoundTrip = defaults.AffineRoundTrip
	}
	return tol
}

func (g *VolumeGeometry) addIssue(issue GeometryIssue) {
	if g == nil || issue == GeometryIssueNone {
		return
	}
	for _, existing := range g.Issues {
		if existing == issue {
			return
		}
	}
	g.Issues = append(g.Issues, issue)
	if g.PrimaryIssue == GeometryIssueNone {
		g.PrimaryIssue = issue
	}
}

func (g *VolumeGeometry) finalizeDisposition() {
	if g == nil {
		return
	}
	unsupported := false
	for _, issue := range g.Issues {
		switch issue {
		case GeometryIssueDuplicatePositions,
			GeometryIssueInconsistentGantryShear,
			GeometryIssueMixedOrientation,
			GeometryIssueInconsistentNormals,
			GeometryIssueInconsistentPixelSpacing,
			GeometryIssueTemporalInterleaving,
			GeometryIssueDifferentPixelGrid,
			GeometryIssueInvalidGeometry,
			GeometryIssueMixedPositionSource:
			unsupported = true
		}
	}
	switch {
	case unsupported:
		g.Disposition = GeometryUnsupported
	case g.RequiresResampling || g.ReversedInput:
		g.Disposition = GeometryRegularizable
	default:
		g.Disposition = GeometryRegularFastPath
	}
	priority := []GeometryIssue{
		GeometryIssueTemporalInterleaving,
		GeometryIssueDuplicatePositions,
		GeometryIssueInconsistentGantryShear,
		GeometryIssueInconsistentNormals,
		GeometryIssueMixedOrientation,
		GeometryIssueInconsistentPixelSpacing,
		GeometryIssueDifferentPixelGrid,
		GeometryIssueMixedPositionSource,
		GeometryIssueInvalidGeometry,
		GeometryIssueSingleSlice,
		GeometryIssueSliceGaps,
		GeometryIssueIrregularSpacing,
		GeometryIssueGantryTilt,
		GeometryIssueReversedStack,
	}
	g.PrimaryIssue = GeometryIssueNone
	for _, issue := range priority {
		if g.hasIssue(issue) {
			g.PrimaryIssue = issue
			break
		}
	}
}

func (g *VolumeGeometry) buildAffines(origin, normal Vec3, colSpacing, rowSpacing, sliceSpacing float64, _ GeometryTolerances) {
	if g == nil {
		return
	}
	g.VoxelToPatientAffine, g.PatientToVoxelAffine, _ = geometryAffinePair(
		origin,
		g.RowDir.Scale(colSpacing),
		g.ColDir.Scale(rowSpacing),
		normal.Scale(sliceSpacing),
	)
}

func (g VolumeGeometry) affinesConform(tol GeometryTolerances) bool {
	if !g.VoxelToPatientAffine.Finite() || !g.PatientToVoxelAffine.Finite() || len(g.Slices) == 0 {
		return false
	}
	columns := g.Slices[0].Columns
	rows := g.Slices[0].Rows
	depth := g.RegularizedDepth
	if columns <= 0 || rows <= 0 || depth <= 0 {
		return false
	}
	points := []Vec3{
		{},
		{X: float64(columns - 1)},
		{Y: float64(rows - 1)},
		{Z: float64(depth - 1)},
		{X: float64(columns-1) / 2, Y: float64(rows-1) / 2, Z: float64(depth-1) / 2},
	}
	for _, point := range points {
		roundTrip := g.PatientToVoxelAffine.TransformPoint(g.VoxelToPatientAffine.TransformPoint(point))
		if math.Abs(roundTrip.X-point.X) > tol.AffineRoundTrip ||
			math.Abs(roundTrip.Y-point.Y) > tol.AffineRoundTrip ||
			math.Abs(roundTrip.Z-point.Z) > tol.AffineRoundTrip {
			return false
		}
	}
	return true
}

func reversedGeometryInput(positions []float64, tolerance float64) bool {
	if len(positions) < 2 {
		return false
	}
	hasDecrease := false
	for i := 1; i < len(positions); i++ {
		delta := positions[i] - positions[i-1]
		if delta > tolerance {
			return false
		}
		if delta < -tolerance {
			hasDecrease = true
		}
	}
	return hasDecrease
}

func vectorAngleDegrees(a, b Vec3) float64 {
	denominator := a.Length() * b.Length()
	if !finitePositiveSpacing(denominator) {
		return 180
	}
	cosine := math.Max(-1, math.Min(1, a.Dot(b)/denominator))
	return math.Acos(cosine) * 180 / math.Pi
}

func spacingWithinTolerance(value, reference float64, tol GeometryTolerances) bool {
	if !finitePositiveSpacing(value) || !finitePositiveSpacing(reference) {
		return false
	}
	limit := math.Max(tol.SpacingAbs, tol.SpacingRel*reference)
	return math.Abs(value-reference) <= limit
}

func spacingRange(spacings []float64) (minimum, maximum float64) {
	minimum = math.Inf(1)
	for _, spacing := range spacings {
		if spacing < minimum {
			minimum = spacing
		}
		if spacing > maximum {
			maximum = spacing
		}
	}
	if math.IsInf(minimum, 1) {
		minimum = 0
	}
	return minimum, maximum
}

func maximumAffineResidual(slices []SliceGeometry, origin, zStep Vec3) float64 {
	maximum := 0.0
	for index, slice := range slices {
		expected := origin.Add(zStep.Scale(float64(index)))
		maximum = math.Max(maximum, slice.Origin.Sub(expected).Length())
	}
	return maximum
}

func regularizedSliceSpacing(spacings []float64, missing bool) float64 {
	positive := make([]float64, 0, len(spacings))
	for _, spacing := range spacings {
		if finitePositiveSpacing(spacing) {
			positive = append(positive, spacing)
		}
	}
	if len(positive) == 0 {
		return 1
	}
	sort.Float64s(positive)
	if missing {
		return positive[0]
	}
	middle := len(positive) / 2
	if len(positive)%2 == 1 {
		return positive[middle]
	}
	return (positive[middle-1] + positive[middle]) / 2
}

func regularizedDepth(span, spacing float64, fallback int) int {
	if !finitePositiveSpacing(span) || !finitePositiveSpacing(spacing) {
		return maxInt(fallback, 1)
	}
	depth := int(math.Ceil(span/spacing-1e-12)) + 1
	return maxInt(depth, 2)
}

// classifySpacing reports whether inter-slice spacings are uniform (regular) and
// whether the irregularity is a clean integer-multiple "missing slice" pattern.
func classifySpacing(spacings []float64, tol GeometryTolerances) (regular, missing bool) {
	if len(spacings) == 0 {
		return true, false
	}
	base := math.Inf(1)
	for _, s := range spacings {
		if s > 0 && s < base {
			base = s
		}
	}
	if math.IsInf(base, 1) || base <= 0 {
		return false, false
	}
	limit := math.Max(tol.SpacingAbs, tol.SpacingRel*base)

	regular = true
	allIntegerMultiples := true
	maxMultiple := 1
	for _, s := range spacings {
		if math.Abs(s-base) > limit {
			regular = false
		}
		k := int(math.Round(s / base))
		if k < 1 {
			k = 1
		}
		if math.Abs(s-float64(k)*base) > limit {
			allIntegerMultiples = false
		}
		if k > maxMultiple {
			maxMultiple = k
		}
	}
	missing = !regular && allIntegerMultiples && maxMultiple >= 2
	return regular, missing
}

// detectGantryTilt reports whether the stacking direction (first to last slice
// origin) diverges from the slice normal beyond tolerance.
func detectGantryTilt(sorted []SliceGeometry, normal Vec3, tol GeometryTolerances) bool {
	if len(sorted) < 2 {
		return false
	}
	stack := sorted[len(sorted)-1].Origin.Sub(sorted[0].Origin).Normalize()
	if stack == (Vec3{}) {
		return false
	}
	return math.Abs(stack.Dot(normal)) < tol.TiltCos
}

func gantryTiltMetrics(sorted []SliceGeometry, normal Vec3) (offset, shear Vec3, angleDegrees float64) {
	if len(sorted) < 2 {
		return Vec3{}, Vec3{}, 0
	}
	delta := sorted[len(sorted)-1].Origin.Sub(sorted[0].Origin)
	normalDistance := delta.Dot(normal)
	offset = delta.Sub(normal.Scale(normalDistance))
	span := math.Abs(normalDistance)
	if span <= 0 || math.IsNaN(span) || math.IsInf(span, 0) {
		return Vec3{}, Vec3{}, 0
	}
	shear = offset.Scale(1 / span)
	angleDegrees = math.Atan(shear.Length()) * 180 / math.Pi
	return offset, shear, angleDegrees
}

func maximumShearDeviation(sorted []SliceGeometry, normal, referenceShear Vec3) float64 {
	maximum := 0.0
	for index := 1; index < len(sorted); index++ {
		delta := sorted[index].Origin.Sub(sorted[index-1].Origin)
		normalSpan := delta.Dot(normal)
		if !finitePositiveSpacing(normalSpan) {
			return math.Inf(1)
		}
		offset := delta.Sub(normal.Scale(normalSpan))
		intervalShear := offset.Scale(1 / normalSpan)
		maximum = math.Max(maximum, intervalShear.Sub(referenceShear).Length())
	}
	return maximum
}

type slicePositionSource uint8

const (
	slicePositionUnknown slicePositionSource = iota
	slicePositionImagePosition
	slicePositionLocationFallback
)

// observe records the patient-position source used by a renderable slice. A
// stack is only correctable when every contributing slice uses the same source.
func (source *slicePositionSource) observe(sl *Frame) bool {
	next := slicePositionLocationFallback
	if len(sl.ImagePosition) >= 3 {
		next = slicePositionImagePosition
	}
	if *source != slicePositionUnknown && *source != next {
		return false
	}
	*source = next
	return true
}

type sliceGeometryFailure uint8

const (
	sliceGeometryValid sliceGeometryFailure = iota
	sliceGeometryGridMismatch
	sliceGeometryInvalid
	sliceGeometryMixedPositionSource
)

type collectedSliceGeometry struct {
	frame      *Frame
	geometry   SliceGeometry
	frameIndex int
}

// collectSliceGeometries applies the shared renderable-frame filter and
// fail-closed patient-geometry checks used by both public geometry inspection
// and volume construction. expectedRows/expectedCols may be zero when the
// caller does not need to enforce a common pixel grid.
func collectSliceGeometries(frames []*Frame, refNormal Vec3, rowSp, colSp float64, expectedRows, expectedCols int) ([]collectedSliceGeometry, sliceGeometryFailure, int) {
	collected := make([]collectedSliceGeometry, 0, len(frames))
	positionSource := slicePositionUnknown
	for i, sl := range frames {
		if sl == nil || sl.DecodeErr != nil || sl.Metadata.Rows <= 0 || sl.Metadata.Columns <= 0 {
			continue
		}
		if expectedRows > 0 && expectedCols > 0 &&
			(int(sl.Metadata.Rows) != expectedRows || int(sl.Metadata.Columns) != expectedCols) {
			return nil, sliceGeometryGridMismatch, i
		}
		geometry, ok := sliceGeometry(sl, refNormal, rowSp, colSp)
		if !ok {
			return nil, sliceGeometryInvalid, i
		}
		if !positionSource.observe(sl) {
			return nil, sliceGeometryMixedPositionSource, i
		}
		collected = append(collected, collectedSliceGeometry{
			frame:      sl,
			geometry:   geometry,
			frameIndex: i + 1,
		})
	}
	return collected, sliceGeometryValid, -1
}

func classifyVolume(g VolumeGeometry) VolumeClassification {
	switch {
	case len(g.Slices) < 2:
		return VolumeSingleSlice
	case g.TemporalInterleaved:
		return VolumeTemporalInterleaved
	case g.DuplicatePositions:
		return VolumeDuplicatePositions
	case g.hasIssue(GeometryIssueInconsistentNormals):
		return VolumeInconsistentNormals
	case g.hasIssue(GeometryIssueMixedOrientation):
		return VolumeMixedOrientation
	case !g.OrientationStable:
		return VolumeUnstableOrientation
	case g.GantryTilted:
		return VolumeGantryTilted
	case g.MissingSlices:
		return VolumeMissingSlices
	case !g.Regular:
		return VolumeIrregularSpacing
	default:
		return VolumeRegular
	}
}

func (g VolumeGeometry) hasIssue(issue GeometryIssue) bool {
	for _, candidate := range g.Issues {
		if candidate == issue {
			return true
		}
	}
	return false
}

// sliceGeometry derives a SliceGeometry from a renderable slice using the given
// fallback in-plane spacings and a reference normal for position-only slices. It
// returns false when the slice lacks the orientation/position needed to place it
// in patient space.
func sliceGeometry(sl *Frame, refNormal Vec3, rowSp, colSp float64) (SliceGeometry, bool) {
	if sl == nil || len(sl.ImageOrientation) < 6 {
		return SliceGeometry{}, false
	}
	rowDir := Vec3{sl.ImageOrientation[0], sl.ImageOrientation[1], sl.ImageOrientation[2]}.Normalize()
	colRaw := Vec3{sl.ImageOrientation[3], sl.ImageOrientation[4], sl.ImageOrientation[5]}
	colDir := colRaw.Sub(rowDir.Scale(colRaw.Dot(rowDir))).Normalize()
	normal := rowDir.Cross(colDir).Normalize()
	if rowDir == (Vec3{}) || colDir == (Vec3{}) || normal == (Vec3{}) ||
		!finiteVec3(rowDir) || !finiteVec3(colDir) || !finiteVec3(normal) {
		return SliceGeometry{}, false
	}

	var origin Vec3
	switch {
	case len(sl.ImagePosition) >= 3:
		origin = Vec3{sl.ImagePosition[0], sl.ImagePosition[1], sl.ImagePosition[2]}
	case sl.SliceLocationOK:
		base := refNormal
		if base == (Vec3{}) {
			base = normal
		}
		origin = base.Scale(sl.SliceLocation)
	default:
		return SliceGeometry{}, false
	}
	if !finiteVec3(origin) {
		return SliceGeometry{}, false
	}
	if len(sl.PixelSpacing) >= 2 {
		if value := sl.PixelSpacing[0]; finitePositiveSpacing(value) {
			rowSp = value
		}
		if value := sl.PixelSpacing[1]; finitePositiveSpacing(value) {
			colSp = value
		}
	}

	return SliceGeometry{
		Origin:     origin,
		RowDir:     rowDir,
		ColDir:     colDir,
		Normal:     normal,
		RowSpacing: rowSp,
		ColSpacing: colSp,
		Rows:       int(sl.Metadata.Rows),
		Columns:    int(sl.Metadata.Columns),
	}, true
}

// VolumeGeometry builds the patient-space geometry model for the series,
// classifying regularity, orientation stability, and gantry tilt. It returns
// false when the series lacks the orientation/position tags needed to place its
// slices in patient space.
func (s *Stack) VolumeGeometry() (VolumeGeometry, bool) {
	assessment := s.GeometryAssessment()
	if len(assessment.Geometry.Slices) == 0 {
		return VolumeGeometry{}, false
	}
	return assessment.Geometry, true
}

// GeometryAssessment inspects every renderable frame with the same geometry
// rules used by BuildVolume. It never decodes pixel data and reports why MPR/VR
// must fall back while leaving ordinary 2D frame rendering available.
func (s *Stack) GeometryAssessment() GeometryAssessment {
	if s == nil {
		return unsupportedGeometryAssessment(GeometryIssueInvalidGeometry, 0)
	}
	first := firstRenderableSlice(s)
	if first == nil || len(first.ImageOrientation) < 6 {
		return unsupportedGeometryAssessment(GeometryIssueInvalidGeometry, 0)
	}
	rowDir := Vec3{first.ImageOrientation[0], first.ImageOrientation[1], first.ImageOrientation[2]}.Normalize()
	colRaw := Vec3{first.ImageOrientation[3], first.ImageOrientation[4], first.ImageOrientation[5]}
	colDir := colRaw.Sub(rowDir.Scale(colRaw.Dot(rowDir))).Normalize()
	refNormal := rowDir.Cross(colDir).Normalize()
	if rowDir == (Vec3{}) || colDir == (Vec3{}) || refNormal == (Vec3{}) {
		return unsupportedGeometryAssessment(GeometryIssueInvalidGeometry, 0)
	}

	rowSp := seriesRowSpacing(s)
	colSp := seriesColumnSpacing(s)
	if rowSp <= 0 || math.IsNaN(rowSp) {
		rowSp = 1
	}
	if colSp <= 0 || math.IsNaN(colSp) {
		colSp = 1
	}

	rows := int(first.Metadata.Rows)
	cols := int(first.Metadata.Columns)
	collected, failure, failedIndex := collectSliceGeometries(s.Frames, refNormal, rowSp, colSp, rows, cols)
	if failure != sliceGeometryValid || len(collected) == 0 {
		reason := geometryIssueForFailure(failure)
		frame := 0
		if failedIndex >= 0 {
			frame = failedIndex + 1
		}
		return unsupportedGeometryAssessment(reason, frame)
	}
	geometries := make([]SliceGeometry, len(collected))
	for i := range collected {
		geometries[i] = collected[i].geometry
	}
	geometry := BuildVolumeGeometry(geometries, DefaultGeometryTolerances())
	applyTemporalGeometryGuardrail(&geometry, collected)
	geometry.Classification = classifyVolume(geometry)
	geometry.finalizeDisposition()
	return GeometryAssessment{
		Geometry:       geometry,
		Disposition:    geometry.Disposition,
		FallbackReason: geometry.PrimaryIssue,
		FailureFrame:   geometryFailureFrame(geometry.PrimaryIssue, collected, DefaultGeometryTolerances()),
	}
}

func unsupportedGeometryAssessment(reason GeometryIssue, failureFrame int) GeometryAssessment {
	geometry := VolumeGeometry{
		Disposition:  GeometryUnsupported,
		PrimaryIssue: reason,
		Issues:       []GeometryIssue{reason},
	}
	geometry.Classification, _ = classificationForGeometryIssue(reason)
	return GeometryAssessment{
		Geometry:       geometry,
		Disposition:    GeometryUnsupported,
		FallbackReason: reason,
		FailureFrame:   failureFrame,
	}
}

func classificationForGeometryIssue(reason GeometryIssue) (VolumeClassification, bool) {
	switch reason {
	case GeometryIssueSingleSlice:
		return VolumeSingleSlice, true
	case GeometryIssueDuplicatePositions:
		return VolumeDuplicatePositions, true
	case GeometryIssueSliceGaps:
		return VolumeMissingSlices, true
	case GeometryIssueIrregularSpacing:
		return VolumeIrregularSpacing, true
	case GeometryIssueGantryTilt, GeometryIssueInconsistentGantryShear:
		return VolumeGantryTilted, true
	case GeometryIssueMixedOrientation:
		return VolumeMixedOrientation, true
	case GeometryIssueInconsistentNormals:
		return VolumeInconsistentNormals, true
	case GeometryIssueTemporalInterleaving:
		return VolumeTemporalInterleaved, true
	default:
		return VolumeRegular, false
	}
}

func geometryFailureFrame(reason GeometryIssue, collected []collectedSliceGeometry, tol GeometryTolerances) int {
	if len(collected) == 0 {
		return 0
	}
	tol = normalizedGeometryTolerances(tol)
	ref := collected[0].geometry
	switch reason {
	case GeometryIssueDuplicatePositions:
		positions := make([]float64, 0, len(collected))
		for _, candidate := range collected {
			position := candidate.geometry.PositionAlong(ref.Normal)
			for _, seen := range positions {
				if math.Abs(position-seen) <= tol.PositionAbs {
					return candidate.frameIndex
				}
			}
			positions = append(positions, position)
		}
	case GeometryIssueInconsistentNormals:
		for _, candidate := range collected[1:] {
			if candidate.geometry.Normal.Dot(ref.Normal) < tol.OrientationCos {
				return candidate.frameIndex
			}
		}
	case GeometryIssueMixedOrientation:
		for _, candidate := range collected[1:] {
			geometry := candidate.geometry
			if geometry.Normal.Dot(ref.Normal) >= tol.OrientationCos &&
				(geometry.RowDir.Dot(ref.RowDir) < tol.OrientationCos ||
					geometry.ColDir.Dot(ref.ColDir) < tol.OrientationCos) {
				return candidate.frameIndex
			}
		}
	case GeometryIssueInconsistentPixelSpacing:
		for _, candidate := range collected[1:] {
			geometry := candidate.geometry
			if !spacingWithinTolerance(geometry.RowSpacing, ref.RowSpacing, tol) ||
				!spacingWithinTolerance(geometry.ColSpacing, ref.ColSpacing, tol) {
				return candidate.frameIndex
			}
		}
	}
	return 0
}

func geometryIssueForFailure(failure sliceGeometryFailure) GeometryIssue {
	switch failure {
	case sliceGeometryGridMismatch:
		return GeometryIssueDifferentPixelGrid
	case sliceGeometryMixedPositionSource:
		return GeometryIssueMixedPositionSource
	default:
		return GeometryIssueInvalidGeometry
	}
}

func applyTemporalGeometryGuardrail(geometry *VolumeGeometry, collected []collectedSliceGeometry) {
	if geometry == nil || !temporalFramesInterleaved(collected) {
		return
	}
	geometry.TemporalInterleaved = true
	geometry.addIssue(GeometryIssueTemporalInterleaving)
	geometry.Classification = VolumeTemporalInterleaved
}

func temporalFramesInterleaved(collected []collectedSliceGeometry) bool {
	return geometryTimeline(collected).Dynamic
}

func geometryTimeline(collected []collectedSliceGeometry) dynamic.Timeline {
	frames := make([]dynamic.FrameMetadata, len(collected))
	normal := Vec3{}
	if len(collected) > 0 {
		normal = collected[0].geometry.Normal
	}
	for index, item := range collected {
		frame := item.frame.Temporal
		frame.FrameIndex = index
		if !frame.HasSpatialPosition {
			frame.SpatialPosition = item.geometry.PositionAlong(normal)
			frame.HasSpatialPosition = true
		}
		frames[index] = frame
	}
	return dynamic.Build(frames)
}

// GeometryFrameGroups separates temporal interleaving and StackID dimensions
// deterministically before any 3D volume is built.
func (s *Stack) GeometryFrameGroups() []GeometryFrameGroup {
	if s == nil {
		return nil
	}
	first := firstRenderableSlice(s)
	if first == nil || len(first.ImageOrientation) < 6 {
		return nil
	}
	row := Vec3{first.ImageOrientation[0], first.ImageOrientation[1], first.ImageOrientation[2]}.Normalize()
	colRaw := Vec3{first.ImageOrientation[3], first.ImageOrientation[4], first.ImageOrientation[5]}
	col := colRaw.Sub(row.Scale(colRaw.Dot(row))).Normalize()
	normal := row.Cross(col).Normalize()
	rowSpacing := seriesRowSpacing(s)
	if !finitePositiveSpacing(rowSpacing) {
		rowSpacing = 1
	}
	colSpacing := seriesColumnSpacing(s)
	if !finitePositiveSpacing(colSpacing) {
		colSpacing = 1
	}
	collected, failure, _ := collectSliceGeometries(
		s.Frames,
		normal,
		rowSpacing,
		colSpacing,
		int(first.Metadata.Rows),
		int(first.Metadata.Columns),
	)
	if failure != sliceGeometryValid || len(collected) == 0 {
		return nil
	}
	timeline := geometryTimeline(collected)
	groups := make([]GeometryFrameGroup, 0)
	for _, point := range timeline.Points {
		for _, stack := range point.Stacks {
			group := GeometryFrameGroup{
				TemporalOrdinal:  point.Ordinal,
				TemporalPosition: point.TemporalPosition,
				HasTemporal:      point.HasPosition || timeline.Dynamic,
				StackID:          stack.ID,
				Frames:           make([]*Frame, 0, len(stack.FrameIndices)),
			}
			for _, index := range stack.FrameIndices {
				if index >= 0 && index < len(collected) {
					group.Frames = append(group.Frames, collected[index].frame)
				}
			}
			groups = append(groups, group)
		}
	}
	return groups
}
