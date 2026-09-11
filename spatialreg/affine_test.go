package spatialreg

import (
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
)

func TestReadAffineDatasetComposesMatricesInDICOMOrder(t *testing.T) {
	translation := translationMatrix(10, -2, 4)
	rotation := render.GeometryAffine{
		0, -1, 0, 0,
		1, 0, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
	registration, err := ReadAffineDataset(affineFixture(
		affineMatrix(TransformRigid, translation),
		affineMatrix(TransformRigid, rotation),
	))
	if err != nil {
		t.Fatalf("ReadAffineDataset() error = %v", err)
	}
	if len(registration.Registrations) != 1 {
		t.Fatalf("Registrations = %d, want 1", len(registration.Registrations))
	}
	mapping := &registration.Registrations[0]
	got, err := mapping.MapSourceToRegistered(render.Vec3{X: 1, Y: 2, Z: 3})
	if err != nil {
		t.Fatalf("MapSourceToRegistered() error = %v", err)
	}
	// M1 translates to (11,0,7), then M2 rotates to (0,11,7).
	assertVecNear(t, got, render.Vec3{X: 0, Y: 11, Z: 7}, 1e-12)
	roundTrip, err := mapping.MapRegisteredToSource(got)
	if err != nil {
		t.Fatalf("MapRegisteredToSource() error = %v", err)
	}
	assertVecNear(t, roundTrip, render.Vec3{X: 1, Y: 2, Z: 3}, 1e-12)
}

func TestReadAffineDatasetIdentity(t *testing.T) {
	parsed, err := ReadAffineDataset(affineFixture(affineMatrix(TransformRigid, identityAffine())))
	if err != nil {
		t.Fatal(err)
	}
	point := render.Vec3{X: -4, Y: 5, Z: 6}
	mapped, err := parsed.Registrations[0].MapSourceToRegistered(point)
	if err != nil {
		t.Fatal(err)
	}
	assertVecNear(t, mapped, point, 0)
}

func TestReadAffineDatasetAcceptsRigidScaleAndAffine(t *testing.T) {
	tests := []struct {
		name   string
		kind   TransformType
		matrix render.GeometryAffine
	}{
		{name: "rigid scale", kind: TransformRigidScale, matrix: render.GeometryAffine{
			2, 0, 0, 1,
			0, 3, 0, 2,
			0, 0, 4, 3,
			0, 0, 0, 1,
		}},
		{name: "affine shear", kind: TransformAffine, matrix: render.GeometryAffine{
			1, .25, 0, 1,
			0, 1, .1, 2,
			0, 0, 1, 3,
			0, 0, 0, 1,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ReadAffineDataset(affineFixture(affineMatrix(test.kind, test.matrix)))
			if err != nil {
				t.Fatalf("ReadAffineDataset() error = %v", err)
			}
			if got := TransformType(parsed.Registrations[0].Matrices[0].Type); got != test.kind {
				t.Fatalf("matrix type = %q, want %q", got, test.kind)
			}
		})
	}
}

func TestReadAffineDatasetRejectsTypedMatrixFailures(t *testing.T) {
	identity := identityAffine()
	nanMatrix := identity
	nanMatrix[0] = math.NaN()
	infMatrix := identity
	infMatrix[0] = math.Inf(1)
	tests := []struct {
		name   string
		kind   TransformType
		values []float64
		want   error
	}{
		{name: "invalid VM", kind: TransformAffine, values: identity[:15], want: ErrInvalidMatrix},
		{name: "NaN", kind: TransformAffine, values: nanMatrix[:], want: ErrInvalidMatrix},
		{name: "infinity", kind: TransformAffine, values: infMatrix[:], want: ErrInvalidMatrix},
		{name: "singular", kind: TransformAffine, values: []float64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}, want: ErrSingularMatrix},
		{name: "ill conditioned", kind: TransformAffine, values: []float64{1, 0, 0, 0, 0, 1e-11, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}, want: ErrIllConditioned},
		{name: "scale declared rigid", kind: TransformRigid, values: []float64{2, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}, want: ErrInvalidMatrix},
		{name: "shear declared rigid scale", kind: TransformRigidScale, values: []float64{1, .2, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}, want: ErrInvalidMatrix},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadAffineDataset(affineFixture(derivedio.DataSet(
				derivedio.CS(tagFrameTransformationMatrixType, string(test.kind)),
				derivedio.DS(tagFrameTransformationMatrix, test.values...),
			)))
			if !errors.Is(err, test.want) {
				t.Fatalf("ReadAffineDataset() error = %v, want errors.Is(%v)", err, test.want)
			}
			var matrixErr *MatrixError
			if !errors.As(err, &matrixErr) {
				t.Fatalf("ReadAffineDataset() error = %T, want *MatrixError", err)
			}
		})
	}
}

func TestReadAffineDatasetRejectsUnsupportedType(t *testing.T) {
	identity := identityAffine()
	_, err := ReadAffineDataset(affineFixture(derivedio.DataSet(
		derivedio.CS(tagFrameTransformationMatrixType, "PERSPECTIVE"),
		derivedio.DS(tagFrameTransformationMatrix, identity[:]...),
	)))
	if !errors.Is(err, ErrUnsupportedMatrix) {
		t.Fatalf("ReadAffineDataset() error = %v, want ErrUnsupportedMatrix", err)
	}
}

func TestAffineRoundTripProperty(t *testing.T) {
	random := rand.New(rand.NewSource(27))
	for sample := 0; sample < 500; sample++ {
		angle := random.Float64()*2*math.Pi - math.Pi
		cosine, sine := math.Cos(angle), math.Sin(angle)
		matrix := render.GeometryAffine{
			cosine, -sine, 0, random.Float64()*100 - 50,
			sine, cosine, 0, random.Float64()*100 - 50,
			0, 0, 1, random.Float64()*100 - 50,
			0, 0, 0, 1,
		}
		parsed, err := ReadAffineDataset(affineFixture(affineMatrix(TransformRigid, matrix)))
		if err != nil {
			t.Fatalf("sample %d ReadAffineDataset() error = %v", sample, err)
		}
		point := render.Vec3{X: random.Float64()*200 - 100, Y: random.Float64()*200 - 100, Z: random.Float64()*200 - 100}
		registered, err := parsed.Registrations[0].MapSourceToRegistered(point)
		if err != nil {
			t.Fatalf("sample %d forward error = %v", sample, err)
		}
		roundTrip, err := parsed.Registrations[0].MapRegisteredToSource(registered)
		if err != nil {
			t.Fatalf("sample %d inverse error = %v", sample, err)
		}
		assertVecNear(t, roundTrip, point, 1e-9)
	}
}

func affineFixture(matrixItems ...core.DataSet) *object.Object {
	return derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, SpatialRegistrationStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.3.affine"),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, "1.2.3.registered"),
		derivedio.Seq(tagRegistrationSequence, derivedio.DataSet(
			derivedio.UI(derivedio.TagFrameOfReferenceUID, "1.2.3.source"),
			derivedio.Seq(tagMatrixRegistrationSequence, derivedio.DataSet(
				derivedio.Seq(tagMatrixSequence, matrixItems...),
			)),
		)),
	)
}

func affineMatrix(kind TransformType, matrix render.GeometryAffine) core.DataSet {
	return derivedio.DataSet(
		derivedio.CS(tagFrameTransformationMatrixType, string(kind)),
		derivedio.DS(tagFrameTransformationMatrix, matrix[:]...),
	)
}
