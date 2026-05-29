package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/dynamic"
)

var updateGeometryGolden = flag.Bool("update", false, "update geometry guardrail golden files")

type geometryGoldenRecord struct {
	Name                  string          `json:"name"`
	Classification        string          `json:"classification"`
	Disposition           string          `json:"disposition"`
	PrimaryIssue          GeometryIssue   `json:"primary_issue,omitempty"`
	Issues                []GeometryIssue `json:"issues"`
	Positions             []float64       `json:"positions"`
	ReversedInput         bool            `json:"reversed_input"`
	InputReordered        bool            `json:"input_reordered"`
	RequiresResampling    bool            `json:"requires_resampling"`
	SourceAffine          bool            `json:"source_affine"`
	RegularizedDepth      int             `json:"regularized_depth"`
	MaximumNormalAngle    float64         `json:"maximum_normal_angle_deg"`
	MaximumAffineResidual float64         `json:"maximum_affine_residual_mm"`
	MaximumShearDeviation float64         `json:"maximum_shear_deviation,omitempty"`
}

func TestGeometryGuardrailGoldens(t *testing.T) {
	axial := []float64{1, 0, 0, 0, 1, 0}
	angle := 20 * math.Pi / 180
	oblique := []float64{1, 0, 0, 0, math.Cos(angle), math.Sin(angle)}
	inPlane := []float64{math.Cos(angle), math.Sin(angle), 0, -math.Sin(angle), math.Cos(angle), 0}
	cases := []struct {
		name         string
		origins      []Vec3
		orientations [][]float64
		temporal     []int
	}{
		{name: "regular", origins: []Vec3{{}, {Z: 1}, {Z: 2}}, orientations: repeatOrientation(axial, 3)},
		{name: "reversed", origins: []Vec3{{Z: 2}, {Z: 1}, {}}, orientations: repeatOrientation(axial, 3)},
		{
			name:         "oblique",
			origins:      []Vec3{{}, {Y: -math.Sin(angle) * 2, Z: math.Cos(angle) * 2}, {Y: -math.Sin(angle) * 4, Z: math.Cos(angle) * 4}},
			orientations: repeatOrientation(oblique, 3),
		},
		{name: "tilted", origins: []Vec3{{}, {Y: 0.5, Z: 1}, {Y: 1, Z: 2}}, orientations: repeatOrientation(axial, 3)},
		{name: "inconsistent-shear", origins: []Vec3{{}, {Y: 0.5, Z: 1}, {Y: 0.5, Z: 2}}, orientations: repeatOrientation(axial, 3)},
		{name: "gapped", origins: []Vec3{{}, {Z: 1}, {Z: 3}}, orientations: repeatOrientation(axial, 3)},
		{name: "irregular", origins: []Vec3{{}, {Z: 1}, {Z: 2.4}, {Z: 3}}, orientations: repeatOrientation(axial, 4)},
		{name: "mixed-iop", origins: []Vec3{{}, {Z: 1}, {Z: 2}}, orientations: [][]float64{axial, inPlane, axial}},
		{name: "inconsistent-normals", origins: []Vec3{{}, {Z: 1}, {Z: 2}}, orientations: [][]float64{axial, oblique, axial}},
		{name: "duplicate", origins: []Vec3{{}, {}, {Z: 1}}, orientations: repeatOrientation(axial, 3)},
		{
			name:         "temporal-interleaved",
			origins:      []Vec3{{}, {}, {Z: 1}, {Z: 1}},
			orientations: repeatOrientation(axial, 4),
			temporal:     []int{1, 2, 1, 2},
		},
	}

	records := make([]geometryGoldenRecord, 0, len(cases))
	for _, test := range cases {
		stack := geometryTestStack(test.origins, test.orientations, test.temporal)
		assessment := stack.GeometryAssessment()
		geometry := assessment.Geometry
		issues := append([]GeometryIssue(nil), geometry.Issues...)
		if issues == nil {
			issues = []GeometryIssue{}
		}
		records = append(records, geometryGoldenRecord{
			Name:                  test.name,
			Classification:        geometry.Classification.String(),
			Disposition:           geometry.Disposition.String(),
			PrimaryIssue:          geometry.PrimaryIssue,
			Issues:                issues,
			Positions:             append([]float64(nil), geometry.Positions...),
			ReversedInput:         geometry.ReversedInput,
			InputReordered:        geometry.InputReordered,
			RequiresResampling:    geometry.RequiresResampling,
			SourceAffine:          geometry.SourceAffine,
			RegularizedDepth:      geometry.RegularizedDepth,
			MaximumNormalAngle:    roundedGeometryMetric(geometry.Metrics.MaximumNormalAngleDeg),
			MaximumAffineResidual: roundedGeometryMetric(geometry.Metrics.MaximumAffineResidualMM),
			MaximumShearDeviation: roundedGeometryMetric(geometry.Metrics.MaximumShearDeviation),
		})
	}
	actual, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	goldenPath := filepath.Join("testdata", "geometry_guardrails_v1.golden.json")
	if *updateGeometryGolden {
		if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("geometry golden mismatch\nactual:\n%s\nexpected:\n%s", actual, expected)
	}
}

func TestGeometryAffineRoundTripProperty(t *testing.T) {
	random := rand.New(rand.NewSource(10010))
	const volumeSnapshotV1Tolerance = 1e-9
	for iteration := 0; iteration < 500; iteration++ {
		yaw := random.Float64()*2*math.Pi - math.Pi
		pitch := random.Float64()*math.Pi - math.Pi/2
		row := Vec3{X: math.Cos(yaw), Y: math.Sin(yaw)}.Normalize()
		normalSeed := Vec3{
			X: math.Cos(pitch) * math.Cos(yaw+math.Pi/2),
			Y: math.Cos(pitch) * math.Sin(yaw+math.Pi/2),
			Z: math.Sin(pitch),
		}.Normalize()
		col := normalSeed.Cross(row).Normalize()
		normal := row.Cross(col).Normalize()
		colSpacing := 0.1 + random.Float64()*2.9
		rowSpacing := 0.1 + random.Float64()*2.9
		sliceSpacing := 0.1 + random.Float64()*4.9
		origin := Vec3{
			X: random.Float64()*2000 - 1000,
			Y: random.Float64()*2000 - 1000,
			Z: random.Float64()*2000 - 1000,
		}
		slices := make([]SliceGeometry, 8)
		for index := range slices {
			slices[index] = SliceGeometry{
				Origin: origin.Add(normal.Scale(float64(index) * sliceSpacing)),
				RowDir: row, ColDir: col, Normal: normal,
				RowSpacing: rowSpacing, ColSpacing: colSpacing, Rows: 512, Columns: 512,
			}
		}
		geometry := BuildVolumeGeometry(slices, DefaultGeometryTolerances())
		if geometry.Disposition != GeometryRegularFastPath {
			t.Fatalf("iteration %d disposition = %s, issues=%v", iteration, geometry.Disposition, geometry.Issues)
		}
		for sample := 0; sample < 20; sample++ {
			voxel := Vec3{
				X: random.Float64() * 511,
				Y: random.Float64() * 511,
				Z: random.Float64() * 7,
			}
			patient := geometry.VoxelToPatientAffine.TransformPoint(voxel)
			roundTrip := geometry.PatientToVoxelAffine.TransformPoint(patient)
			if deviation := roundTrip.Sub(voxel).Length(); deviation > volumeSnapshotV1Tolerance {
				t.Fatalf("iteration %d affine round-trip error %.12g exceeds VolumeSnapshot v1 %.12g", iteration, deviation, volumeSnapshotV1Tolerance)
			}
		}
		if determinant := affineDirectionDeterminant(geometry.VoxelToPatientAffine); determinant <= 0 {
			t.Fatalf("iteration %d affine handedness determinant = %g, want positive", iteration, determinant)
		}
	}
}

func TestGeometryOrderingProperty(t *testing.T) {
	random := rand.New(rand.NewSource(10011))
	for iteration := 0; iteration < 200; iteration++ {
		const depth = 32
		slices := make([]SliceGeometry, depth)
		for index := range slices {
			slices[index] = geometrySlice(Vec3{Z: float64(index) * 1.25}, []float64{1, 0, 0, 0, 1, 0})
		}
		random.Shuffle(len(slices), func(i, j int) { slices[i], slices[j] = slices[j], slices[i] })
		geometry := BuildVolumeGeometry(slices, DefaultGeometryTolerances())
		for index := 1; index < len(geometry.Positions); index++ {
			if geometry.Positions[index] <= geometry.Positions[index-1] {
				t.Fatalf("iteration %d ordering = %v", iteration, geometry.Positions)
			}
		}
		if geometry.Classification != VolumeRegular {
			t.Fatalf("iteration %d classification = %s, want regular", iteration, geometry.Classification)
		}
	}
}

func TestBuildVolumeGuardrailRetainsFailureFrame(t *testing.T) {
	axial := []float64{1, 0, 0, 0, 1, 0}
	stack := geometryTestStack(
		[]Vec3{{}, {}, {Z: 1}},
		repeatOrientation(axial, 3),
		nil,
	)
	assessment := stack.GeometryAssessment()
	if assessment.FailureFrame != 2 {
		t.Fatalf("GeometryAssessment failure frame = %d, want 2", assessment.FailureFrame)
	}
	_, err := BuildVolume(stack)
	var guardrail *GeometryGuardrailError
	if !errors.As(err, &guardrail) || guardrail.FrameIndex != assessment.FailureFrame {
		t.Fatalf("BuildVolume error = %v, want guardrail frame %d", err, assessment.FailureFrame)
	}
}

func TestUnsupportedGeometryAssessmentClassificationMatchesIssue(t *testing.T) {
	tests := []struct {
		reason GeometryIssue
		want   VolumeClassification
	}{
		{GeometryIssueSingleSlice, VolumeSingleSlice},
		{GeometryIssueDuplicatePositions, VolumeDuplicatePositions},
		{GeometryIssueMixedOrientation, VolumeMixedOrientation},
		{GeometryIssueInconsistentNormals, VolumeInconsistentNormals},
		{GeometryIssueTemporalInterleaving, VolumeTemporalInterleaved},
		{GeometryIssueDifferentPixelGrid, VolumeRegular},
	}
	for _, test := range tests {
		assessment := unsupportedGeometryAssessment(test.reason, 2)
		if assessment.Geometry.Classification != test.want {
			t.Errorf("%s classification = %s, want %s", test.reason, assessment.Geometry.Classification, test.want)
		}
		if assessment.Geometry.PrimaryIssue != test.reason || assessment.FailureFrame != 2 {
			t.Errorf("%s assessment = %+v", test.reason, assessment)
		}
	}
}

func TestSingleSliceGeometryExposesStableIssue(t *testing.T) {
	geometry := BuildVolumeGeometry(
		[]SliceGeometry{geometrySlice(Vec3{}, []float64{1, 0, 0, 0, 1, 0})},
		DefaultGeometryTolerances(),
	)
	if geometry.Disposition != GeometryRegularFastPath ||
		geometry.Classification != VolumeSingleSlice ||
		geometry.PrimaryIssue != GeometryIssueSingleSlice ||
		!geometry.hasIssue(GeometryIssueSingleSlice) {
		t.Fatalf("single-slice geometry = %+v", geometry)
	}
}

func TestGeometryToleranceBoundariesMatchFrozenContract(t *testing.T) {
	tolerances := DefaultGeometryTolerances()
	if tolerances.AffineRoundTrip != 1e-9 || tolerances.PositionAbs != 1e-6 ||
		math.Abs(tolerances.ShearAbs-math.Tan(math.Pi/180)) > 1e-15 ||
		tolerances.SpacingAbs != 0.01 || tolerances.SpacingRel != 0.05 {
		t.Fatalf("default tolerances drifted: %+v", tolerances)
	}
	withinSpacing := BuildVolumeGeometry([]SliceGeometry{
		geometrySlice(Vec3{}, []float64{1, 0, 0, 0, 1, 0}),
		geometrySlice(Vec3{Z: 1}, []float64{1, 0, 0, 0, 1, 0}),
		geometrySlice(Vec3{Z: 2.049}, []float64{1, 0, 0, 0, 1, 0}),
	}, tolerances)
	if !withinSpacing.Regular {
		t.Fatalf("4.9%% spacing variation exceeded tolerance: %+v", withinSpacing)
	}
	beyondSpacing := BuildVolumeGeometry([]SliceGeometry{
		geometrySlice(Vec3{}, []float64{1, 0, 0, 0, 1, 0}),
		geometrySlice(Vec3{Z: 1}, []float64{1, 0, 0, 0, 1, 0}),
		geometrySlice(Vec3{Z: 2.051}, []float64{1, 0, 0, 0, 1, 0}),
	}, tolerances)
	if beyondSpacing.Regular || beyondSpacing.PrimaryIssue != GeometryIssueIrregularSpacing {
		t.Fatalf("5.1%% spacing variation was silently accepted: %+v", beyondSpacing)
	}

	orientation := func(degrees float64) []float64 {
		radians := degrees * math.Pi / 180
		return []float64{1, 0, 0, 0, math.Cos(radians), math.Sin(radians)}
	}
	withinOrientation := BuildVolumeGeometry([]SliceGeometry{
		geometrySlice(Vec3{}, orientation(0)),
		geometrySlice(Vec3{Z: 1}, orientation(0.999)),
	}, tolerances)
	if !withinOrientation.OrientationStable {
		t.Fatalf("sub-degree normal drift exceeded tolerance: %+v", withinOrientation)
	}
	beyondOrientation := BuildVolumeGeometry([]SliceGeometry{
		geometrySlice(Vec3{}, orientation(0)),
		geometrySlice(Vec3{Z: 1}, orientation(1.001)),
	}, tolerances)
	if beyondOrientation.Disposition != GeometryUnsupported ||
		beyondOrientation.PrimaryIssue != GeometryIssueInconsistentNormals {
		t.Fatalf("super-degree normal drift was silently accepted: %+v", beyondOrientation)
	}

	duplicate := BuildVolumeGeometry([]SliceGeometry{
		geometrySlice(Vec3{}, orientation(0)),
		geometrySlice(Vec3{Z: tolerances.PositionAbs / 2}, orientation(0)),
	}, tolerances)
	if !duplicate.DuplicatePositions || duplicate.Disposition != GeometryUnsupported {
		t.Fatalf("near-duplicate patient positions were silently accepted: %+v", duplicate)
	}
}

func TestGeometryMatchesIsisGantryTiltFixtures(t *testing.T) {
	axial := []float64{1, 0, 0, 0, 1, 0}
	for _, direction := range []float64{-1, 1} {
		geometry := BuildVolumeGeometry([]SliceGeometry{
			geometrySlice(Vec3{}, axial),
			geometrySlice(Vec3{X: direction * 0.5, Z: 1}, axial),
			geometrySlice(Vec3{X: direction * 1.5, Z: 3}, axial),
		}, DefaultGeometryTolerances())
		if geometry.Disposition != GeometryRegularizable || !geometry.GantryTilted {
			t.Fatalf("direction %v geometry = %+v, want stable correctable tilt", direction, geometry)
		}
		if math.Abs(geometry.GantryTiltAngleDegrees-math.Atan(0.5)*180/math.Pi) > 1e-9 ||
			math.Abs(geometry.GantryTiltOffset.X-direction*1.5) > 1e-9 ||
			math.Abs(geometry.GantryTiltShear.X-direction*0.5) > 1e-9 {
			t.Fatalf("direction %v tilt metrics = angle:%v offset:%+v shear:%+v", direction, geometry.GantryTiltAngleDegrees, geometry.GantryTiltOffset, geometry.GantryTiltShear)
		}
		if len(geometry.Positions) != 3 || geometry.Positions[0] != 0 ||
			geometry.Positions[1] != 1 || geometry.Positions[2] != 3 {
			t.Fatalf("direction %v positions = %v, want [0 1 3]", direction, geometry.Positions)
		}
	}

	inconsistent := BuildVolumeGeometry([]SliceGeometry{
		geometrySlice(Vec3{}, axial),
		geometrySlice(Vec3{X: 0.5, Z: 1}, axial),
		geometrySlice(Vec3{X: 0.5, Z: 2}, axial),
	}, DefaultGeometryTolerances())
	if inconsistent.Disposition != GeometryUnsupported ||
		inconsistent.PrimaryIssue != GeometryIssueInconsistentGantryShear {
		t.Fatalf("inconsistent shear geometry = %+v, want fail-closed parity", inconsistent)
	}
}

func TestTemporalInterleavingSeparatesDeterministicallyAndFallsBackTo2D(t *testing.T) {
	axial := []float64{1, 0, 0, 0, 1, 0}
	stack := geometryTestStack(
		[]Vec3{{}, {}, {Z: 1}, {Z: 1}},
		repeatOrientation(axial, 4),
		[]int{1, 2, 1, 2},
	)
	assessment := stack.GeometryAssessment()
	if assessment.Disposition != GeometryUnsupported || assessment.FallbackReason != GeometryIssueTemporalInterleaving {
		t.Fatalf("assessment = %+v, want temporal fallback", assessment)
	}
	_, err := BuildVolume(stack)
	if !errors.Is(err, ErrNonCorrectableGeometry) {
		t.Fatalf("BuildVolume() error = %v, want geometry guardrail", err)
	}
	if reason, ok := GeometryFallbackReason(err); !ok || reason != GeometryIssueTemporalInterleaving {
		t.Fatalf("GeometryFallbackReason() = %q/%v", reason, ok)
	}
	if _, err := RenderFrame(stack.Frames[0], WindowLevel{Center: 128, Width: 256}); err != nil {
		t.Fatalf("safe 2D fallback failed: %v", err)
	}

	groups := stack.GeometryFrameGroups()
	if len(groups) != 2 {
		t.Fatalf("GeometryFrameGroups() = %+v, want two temporal stacks", groups)
	}
	for index, group := range groups {
		if group.TemporalPosition != index+1 || len(group.Frames) != 2 {
			t.Fatalf("group %d = %+v", index, group)
		}
		separated := &Stack{
			PixelSpacing:   []float64{1, 1},
			SliceThickness: 1,
			Frames:         append([]*Frame(nil), group.Frames...),
		}
		geometry, ok := separated.VolumeGeometry()
		if !ok || geometry.Disposition != GeometryRegularFastPath {
			t.Fatalf("separated group %d geometry = %+v/%v", index, geometry, ok)
		}
	}
}

func TestRegularGridPreservesFastPathAndResamplesOnlyWhenRequired(t *testing.T) {
	regular, err := BuildVolume(gradientXZStack(4, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	fast, err := regular.RegularGrid()
	if err != nil {
		t.Fatal(err)
	}
	if fast != regular {
		t.Fatal("regular fast path allocated a replacement volume")
	}

	irregularStack := gradientXZStack(4, 4, 3)
	for index, position := range []float64{0, 1, 3} {
		irregularStack.Frames[index].ImagePosition = []float64{0, 0, position}
	}
	irregular, err := BuildVolume(irregularStack)
	if err != nil {
		t.Fatal(err)
	}
	if !irregular.Geometry().RequiresResampling {
		t.Fatal("gapped source did not request regularization")
	}
	camera := NewVRCamera(irregular.BoundingRadiusMM())
	camera.FitVolume(irregular)
	_ = RenderVR(
		irregular,
		camera,
		opaqueAbovePreset(),
		WindowLevel{Center: 64, Width: 128},
		false,
		nil,
		DefaultVRQuality(8, 8),
	)
	if irregular.regularized == nil {
		t.Fatal("VR did not route gapped input through the regularized grid")
	}
	resampled, err := irregular.RegularGrid()
	if err != nil {
		t.Fatal(err)
	}
	if resampled == irregular || !resampled.Geometry().Regular || resampled.Depth != 4 {
		t.Fatalf("regularized volume = source:%v geometry:%+v depth:%d", resampled == irregular, resampled.Geometry(), resampled.Depth)
	}
	again, err := irregular.RegularGrid()
	if err != nil || again != resampled {
		t.Fatalf("regularization cache = %p/%v, want %p", again, err, resampled)
	}
	for _, z := range []int{0, 1, 3} {
		value, _, ok := resampled.ValueAt(2, 2, z)
		if !ok {
			t.Fatalf("regularized sample z=%d unavailable", z)
		}
		want := []float64{2, 52, 102}[map[int]int{0: 0, 1: 1, 3: 2}[z]]
		if math.Abs(value-want) > 1e-5 {
			t.Fatalf("regularized sample z=%d = %v, want %v", z, value, want)
		}
	}
}

func geometryTestStack(origins []Vec3, orientations [][]float64, temporal []int) *Stack {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for index, origin := range origins {
		frame := volumeTestFrame(4, 4, make([]byte, 16), index)
		frame.ImagePosition = []float64{origin.X, origin.Y, origin.Z}
		frame.ImageOrientation = append([]float64(nil), orientations[index]...)
		if index < len(temporal) && temporal[index] != 0 {
			frame.Temporal = dynamic.FrameMetadata{
				FrameIndex: index, TemporalPosition: temporal[index], HasTemporalPosition: true,
			}
		}
		stack.Frames = append(stack.Frames, frame)
	}
	return stack
}

func geometrySlice(origin Vec3, orientation []float64) SliceGeometry {
	row := Vec3{X: orientation[0], Y: orientation[1], Z: orientation[2]}.Normalize()
	colRaw := Vec3{X: orientation[3], Y: orientation[4], Z: orientation[5]}
	col := colRaw.Sub(row.Scale(colRaw.Dot(row))).Normalize()
	return SliceGeometry{
		Origin: origin, RowDir: row, ColDir: col, Normal: row.Cross(col).Normalize(),
		RowSpacing: 1, ColSpacing: 1, Rows: 4, Columns: 4,
	}
}

func repeatOrientation(orientation []float64, count int) [][]float64 {
	out := make([][]float64, count)
	for index := range out {
		out[index] = append([]float64(nil), orientation...)
	}
	return out
}

func roundedGeometryMetric(value float64) float64 {
	return math.Round(value*1e9) / 1e9
}

func affineDirectionDeterminant(affine GeometryAffine) float64 {
	return affine[0]*(affine[5]*affine[10]-affine[6]*affine[9]) -
		affine[1]*(affine[4]*affine[10]-affine[6]*affine[8]) +
		affine[2]*(affine[4]*affine[9]-affine[5]*affine[8])
}
