package spatialreg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadDatasetIdentityAndConstantDisplacementEndian(t *testing.T) {
	tests := []struct {
		name         string
		order        binary.ByteOrder
		displacement render.Vec3
	}{
		{name: "identity little endian", order: binary.LittleEndian},
		{name: "constant big endian", order: binary.BigEndian, displacement: render.Vec3{X: 1.5, Y: -2, Z: 3.25}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vectors := repeatedVectors(8, test.displacement)
			fixture := deformableFixture(test.order, [3]uint32{2, 2, 2}, render.Vec3{X: 1, Y: 1, Z: 1}, identityOrientation(), vectors, nil, nil)
			syntax := transfer.ExplicitVRLittleEndian
			if test.order == binary.BigEndian {
				syntax = transfer.ExplicitVRBigEndian
			}
			var encoded bytes.Buffer
			if err := object.WriteDataSet(&encoded, fixture, syntax); err != nil {
				t.Fatal(err)
			}
			decoded, err := object.ReadDataSet(bytes.NewReader(encoded.Bytes()), syntax)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ReadDataset(decoded)
			if err != nil {
				t.Fatal(err)
			}
			point := render.Vec3{X: 0.25, Y: 0.5, Z: 0.75}
			got, err := parsed.Registrations[0].MapRegisteredToSource(point)
			if err != nil {
				t.Fatal(err)
			}
			want := point.Add(test.displacement)
			assertVecNear(t, got, want, 1e-6)
		})
	}
}

func TestVariableFieldTrilinearInterpolation(t *testing.T) {
	vectors := make([]render.Vec3, 0, 8)
	for z := 0; z < 2; z++ {
		for y := 0; y < 2; y++ {
			for x := 0; x < 2; x++ {
				vectors = append(vectors, render.Vec3{X: float64(x), Y: 2 * float64(y), Z: 3 * float64(z)})
			}
		}
	}
	parsed, err := ReadDataset(deformableFixture(binary.LittleEndian, [3]uint32{2, 2, 2}, render.Vec3{X: 1, Y: 1, Z: 1}, identityOrientation(), vectors, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	point := render.Vec3{X: 0.5, Y: 0.25, Z: 0.75}
	got, err := parsed.Registrations[0].MapRegisteredToSource(point)
	if err != nil {
		t.Fatal(err)
	}
	assertVecNear(t, got, render.Vec3{X: 1, Y: 0.75, Z: 3}, 1e-6)
}

func TestPreDisplacementPostOrder(t *testing.T) {
	pre := translationMatrix(2, 0, 0)
	post := translationMatrix(3, 0, 0)
	parsed, err := ReadDataset(deformableFixture(
		binary.LittleEndian,
		[3]uint32{2, 1, 1},
		render.Vec3{X: 1, Y: 1, Z: 1},
		identityOrientation(),
		repeatedVectors(2, render.Vec3{X: 1}),
		&pre,
		&post,
	))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsed.Registrations[0].MapRegisteredToSource(render.Vec3{X: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	assertVecNear(t, got, render.Vec3{X: 6.5}, 1e-6)
}

func TestOrientedAnisotropicGridAndDomainEdges(t *testing.T) {
	orientation := [6]float64{0, 1, 0, -1, 0, 0}
	spacing := render.Vec3{X: 2, Y: 3, Z: 4}
	vectors := make([]render.Vec3, 8)
	for index := range vectors {
		vectors[index] = render.Vec3{X: float64(index)}
	}
	parsed, err := ReadDataset(deformableFixture(binary.LittleEndian, [3]uint32{2, 2, 2}, spacing, orientation, vectors, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	grid := parsed.Registrations[0].Grid
	point := grid.Origin.
		Add(grid.RowDirection.Scale(1)).
		Add(grid.ColumnDirection.Scale(1.5)).
		Add(grid.NormalDirection.Scale(2))
	displacement, err := grid.Sample(point)
	if err != nil {
		t.Fatal(err)
	}
	assertVecNear(t, displacement, render.Vec3{X: 3.5}, 1e-6)

	maxPoint := grid.Origin.
		Add(grid.RowDirection.Scale(2)).
		Add(grid.ColumnDirection.Scale(3)).
		Add(grid.NormalDirection.Scale(4))
	if _, err := grid.Sample(maxPoint); err != nil {
		t.Fatalf("exact maximum edge: %v", err)
	}
	outside := maxPoint.Add(grid.RowDirection.Scale(0.01))
	if _, err := grid.Sample(outside); !errors.Is(err, ErrOutsideDomain) {
		t.Fatalf("outside error = %v, want ErrOutsideDomain", err)
	}
}

func TestUndefinedVectorDoesNotSilentlyAlign(t *testing.T) {
	undefined := render.Vec3{X: math.NaN(), Y: math.NaN(), Z: math.NaN()}
	parsed, err := ReadDataset(deformableFixture(
		binary.LittleEndian,
		[3]uint32{2, 1, 1},
		render.Vec3{X: 1, Y: 1, Z: 1},
		identityOrientation(),
		[]render.Vec3{{X: 2}, undefined},
		nil,
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	grid := parsed.Registrations[0].Grid
	got, err := grid.Sample(grid.Origin)
	if err != nil {
		t.Fatalf("zero-weight undefined neighbor affected exact sample: %v", err)
	}
	assertVecNear(t, got, render.Vec3{X: 2}, 1e-6)
	if _, err := grid.Sample(grid.Origin.Add(grid.RowDirection.Scale(0.5))); !errors.Is(err, ErrUndefinedDomain) {
		t.Fatalf("interpolated undefined error = %v, want ErrUndefinedDomain", err)
	}
}

func TestReadDatasetRejectsPartialNaNVector(t *testing.T) {
	obj := deformableFixture(
		binary.LittleEndian,
		[3]uint32{1, 1, 1},
		render.Vec3{X: 1, Y: 1, Z: 1},
		identityOrientation(),
		[]render.Vec3{{X: math.NaN()}},
		nil,
		nil,
	)
	if _, err := ReadDataset(obj); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("error = %v, want ErrInvalidObject", err)
	}
}

func TestReadDatasetResourceLimitsAreTyped(t *testing.T) {
	fixture := deformableFixture(
		binary.LittleEndian,
		[3]uint32{2, 2, 2},
		render.Vec3{X: 1, Y: 1, Z: 1},
		identityOrientation(),
		repeatedVectors(8, render.Vec3{}),
		nil,
		nil,
	)
	tests := []struct {
		name  string
		opts  Options
		limit Limit
	}{
		{name: "bytes", opts: Options{MaxGridBytes: 95}, limit: LimitGridBytes},
		{name: "voxels", opts: Options{MaxGridVoxels: 7}, limit: LimitGridVoxels},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadDatasetWithOptions(fixture, test.opts)
			var limitErr *LimitError
			if !errors.As(err, &limitErr) || !errors.Is(err, ErrMemoryLimit) {
				t.Fatalf("error = %v, want typed LimitError", err)
			}
			if limitErr.Limit != test.limit {
				t.Fatalf("limit = %q, want %q", limitErr.Limit, test.limit)
			}
		})
	}
}

func TestReadDatasetValidatesDimensionsAndByteLengthBeforeAllocation(t *testing.T) {
	tests := []struct {
		name       string
		dimensions [3]uint32
		vectors    []render.Vec3
	}{
		{name: "length mismatch", dimensions: [3]uint32{2, 2, 2}, vectors: repeatedVectors(7, render.Vec3{})},
		{name: "dimension product overflow", dimensions: [3]uint32{math.MaxUint32, math.MaxUint32, math.MaxUint32}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := deformableFixture(binary.LittleEndian, test.dimensions, render.Vec3{X: 1, Y: 1, Z: 1}, identityOrientation(), test.vectors, nil, nil)
			if _, err := ReadDataset(fixture); !errors.Is(err, ErrInvalidObject) {
				t.Fatalf("error = %v, want ErrInvalidObject", err)
			}
		})
	}
}

func deformableFixture(
	order binary.ByteOrder,
	dimensions [3]uint32,
	spacing render.Vec3,
	orientation [6]float64,
	vectors []render.Vec3,
	pre *render.GeometryAffine,
	post *render.GeometryAffine,
) *object.Object {
	grid := derivedio.DataSet(
		derivedio.DS(tagImagePositionPatient, 0, 0, 0),
		derivedio.DS(tagImageOrientationPatient, orientation[:]...),
		rawUint32(tagGridDimensions, order, dimensions[:]...),
		rawFloat64(tagGridResolution, order, spacing.X, spacing.Y, spacing.Z),
		rawVectors(order, vectors),
	)
	registrationElements := []core.Element{
		derivedio.UI(tagSourceFrameOfReferenceUID, "1.2.3.source"),
		derivedio.Seq(tagDeformableRegistrationGridSequence, grid),
	}
	if pre != nil {
		registrationElements = append(registrationElements, matrixSequence(tagPreDeformationMatrixSequence, *pre))
	}
	if post != nil {
		registrationElements = append(registrationElements, matrixSequence(tagPostDeformationMatrixSequence, *post))
	}
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, DeformableSpatialRegistrationStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.3.registration"),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, "1.2.3.registered"),
		derivedio.Seq(tagDeformableRegistrationSequence, derivedio.DataSet(registrationElements...)),
	)
	dataset.SetValueByteOrder(order)
	return dataset
}

func matrixSequence(tag core.Tag, matrix render.GeometryAffine) core.Element {
	return derivedio.Seq(tag, derivedio.DataSet(
		derivedio.CS(tagFrameTransformationMatrixType, "AFFINE"),
		derivedio.DS(tagFrameTransformationMatrix, matrix[:]...),
	))
}

func rawUint32(tag core.Tag, order binary.ByteOrder, values ...uint32) core.Element {
	raw := make([]byte, len(values)*4)
	for index, value := range values {
		order.PutUint32(raw[index*4:], value)
	}
	return core.NewRawElement(tag, core.VRUL, raw)
}

func rawFloat64(tag core.Tag, order binary.ByteOrder, values ...float64) core.Element {
	raw := make([]byte, len(values)*8)
	for index, value := range values {
		order.PutUint64(raw[index*8:], math.Float64bits(value))
	}
	return core.NewRawElement(tag, core.VRFD, raw)
}

func rawVectors(order binary.ByteOrder, vectors []render.Vec3) core.Element {
	raw := make([]byte, len(vectors)*12)
	for index, vector := range vectors {
		offset := index * 12
		order.PutUint32(raw[offset:], math.Float32bits(float32(vector.X)))
		order.PutUint32(raw[offset+4:], math.Float32bits(float32(vector.Y)))
		order.PutUint32(raw[offset+8:], math.Float32bits(float32(vector.Z)))
	}
	return core.NewRawElement(tagVectorGridData, core.VROF, raw)
}

func identityOrientation() [6]float64 { return [6]float64{1, 0, 0, 0, 1, 0} }

func translationMatrix(x, y, z float64) render.GeometryAffine {
	return render.GeometryAffine{
		1, 0, 0, x,
		0, 1, 0, y,
		0, 0, 1, z,
		0, 0, 0, 1,
	}
}

func repeatedVectors(count int, value render.Vec3) []render.Vec3 {
	out := make([]render.Vec3, count)
	for index := range out {
		out[index] = value
	}
	return out
}

func assertVecNear(t *testing.T, got, want render.Vec3, tolerance float64) {
	t.Helper()
	if math.Abs(got.X-want.X) > tolerance || math.Abs(got.Y-want.Y) > tolerance || math.Abs(got.Z-want.Z) > tolerance {
		t.Fatalf("vector = %+v, want %+v within %g", got, want, tolerance)
	}
}
