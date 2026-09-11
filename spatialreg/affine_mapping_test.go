package spatialreg

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/render"
)

func TestAffineRegistrationMapsPlaneDirections(t *testing.T) {
	registration := testAffineRegistration(render.GeometryAffine{
		0, -2, 0, 10,
		3, 0, 0, 20,
		0, 0, 4, 30,
		0, 0, 0, 1,
	})
	plane := render.Plane{
		Origin: render.Vec3{X: 1, Y: 2, Z: 3},
		U:      render.Vec3{X: 5},
		V:      render.Vec3{Y: 7},
	}

	mapped, err := registration.MapSourcePlaneToRegistered(plane)
	if err != nil {
		t.Fatal(err)
	}
	assertVecNear(t, mapped.Origin, render.Vec3{X: 6, Y: 23, Z: 42}, 1e-12)
	assertVecNear(t, mapped.U, render.Vec3{Y: 15}, 1e-12)
	assertVecNear(t, mapped.V, render.Vec3{X: -14}, 1e-12)

	roundTrip, err := registration.MapRegisteredPlaneToSource(mapped)
	if err != nil {
		t.Fatal(err)
	}
	assertVecNear(t, roundTrip.Origin, plane.Origin, 1e-12)
	assertVecNear(t, roundTrip.U, plane.U, 1e-12)
	assertVecNear(t, roundTrip.V, plane.V, 1e-12)
}

func TestAffineRegistrationMapsVolumeGeometryWithoutVoxelCopy(t *testing.T) {
	volume := testAffineVolume(t, nil)
	defer volume.Close()
	registration := testAffineRegistration(render.GeometryAffine{
		2, 0, 0, 10,
		0, 3, 0, 20,
		0, 0, 4, 30,
		0, 0, 0, 1,
	})

	before := volume.VolumeStoreStats().LiveBytes
	geometry, err := registration.MapSourceVolumeToRegistered(volume)
	if err != nil {
		t.Fatal(err)
	}
	if geometry.Dimensions != [3]int{3, 2, 2} {
		t.Fatalf("dimensions = %v, want [3 2 2]", geometry.Dimensions)
	}
	assertVecNear(t, geometry.Corners[0], render.Vec3{X: 10, Y: 20, Z: 30}, 1e-12)
	assertVecNear(t, geometry.Corners[7], render.Vec3{X: 14, Y: 23, Z: 34}, 1e-12)
	if after := volume.VolumeStoreStats().LiveBytes; after != before {
		t.Fatalf("geometry mapping changed live voxel bytes from %d to %d", before, after)
	}
}

func TestRegisteredVolumeSamplerUsesExplicitInverseAndExistingBudget(t *testing.T) {
	store := render.NewVolumeStore(render.VolumeStoreOptions{MaxLiveBytes: 3 * 2 * 2 * 4})
	volume := testAffineVolume(t, store)
	defer volume.Close()

	// Prime the canonical generation so the assertion distinguishes a shared
	// lease from an accidental transformed copy.
	reader, err := volume.AcquireReader()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	before := volume.VolumeStoreStats().LiveBytes

	registration := testAffineRegistration(render.GeometryAffine{
		1, 0, 0, 10,
		0, 1, 0, 20,
		0, 0, 1, 30,
		0, 0, 0, 1,
	})
	sampler, err := registration.AcquireRegisteredVolumeSampler(volume)
	if err != nil {
		t.Fatal(err)
	}
	defer sampler.Close()

	value, ok := sampler.SamplePatient(render.Vec3{X: 12, Y: 21, Z: 31})
	if !ok || math.Abs(value-112) > 1e-9 {
		t.Fatalf("registered sample = %v/%v, want 112/true", value, ok)
	}
	if after := volume.VolumeStoreStats().LiveBytes; after != before {
		t.Fatalf("registered sampler changed live voxel bytes from %d to %d", before, after)
	}
}

func TestRegisteredVolumeSamplerPreservesVolumeStoreLimitError(t *testing.T) {
	store := render.NewVolumeStore(render.VolumeStoreOptions{MaxLiveBytes: 3*2*2*4 - 1})
	volume := testAffineVolume(t, store)
	defer volume.Close()
	registration := testAffineRegistration(identityAffine())

	_, err := registration.AcquireRegisteredVolumeSampler(volume)
	if !errors.Is(err, render.ErrVolumeBudgetExceeded) {
		t.Fatalf("AcquireRegisteredVolumeSampler error = %v, want ErrVolumeBudgetExceeded", err)
	}
}

func TestAffineMappingRejectsInvalidGeometry(t *testing.T) {
	registration := testAffineRegistration(identityAffine())
	if _, err := registration.MapSourcePlaneToRegistered(render.Plane{Origin: render.Vec3{X: math.NaN()}}); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("non-finite plane error = %v, want ErrInvalidObject", err)
	}
	if _, err := registration.MapSourceVolumeToRegistered(nil); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("nil volume error = %v, want ErrInvalidObject", err)
	}
}

func testAffineRegistration(forward render.GeometryAffine) *AffineRegistration {
	inverse, _, err := inverseAffine(forward)
	if err != nil {
		panic(err)
	}
	return &AffineRegistration{SourceToRegistered: forward, RegisteredToSource: inverse}
}

func testAffineVolume(t *testing.T, store *render.VolumeStore) *render.Volume {
	t.Helper()
	stack := &render.Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z := 0; z < 2; z++ {
		data := make([]byte, 3*2)
		for y := 0; y < 2; y++ {
			for x := 0; x < 3; x++ {
				data[y*3+x] = byte(x + 10*y + 100*z)
			}
		}
		stack.Frames = append(stack.Frames, &render.Frame{
			Metadata: pixeldata.Metadata{
				Rows:                      2,
				Columns:                   3,
				SamplesPerPixel:           1,
				BitsAllocated:             8,
				BitsStored:                8,
				HighBit:                   7,
				PhotometricInterpretation: "MONOCHROME2",
			},
			ByteOrder:        binary.LittleEndian,
			PixelBytes:       data,
			Rescale:          render.Rescale{Slope: 1},
			ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
			ImagePosition:    []float64{0, 0, float64(z)},
			PixelSpacing:     []float64{1, 1},
		})
	}
	if store != nil {
		if err := stack.SetVolumeStore(store); err != nil {
			t.Fatal(err)
		}
	}
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	return volume
}
