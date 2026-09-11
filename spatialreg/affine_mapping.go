package spatialreg

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/render"
)

// MappedVolumeGeometry is the affine image of a source volume's patient-space
// extent. Corners use x-fastest order: x + 2*y + 4*z, with each coordinate at
// either zero or the last voxel center.
type MappedVolumeGeometry struct {
	Dimensions [3]int
	Corners    [8]render.Vec3
}

// MapSourcePlaneToRegistered maps a patient-space plane from the source RCS to
// the registered RCS. U and V remain edge vectors, including affine scale and
// shear.
func (r *AffineRegistration) MapSourcePlaneToRegistered(plane render.Plane) (render.Plane, error) {
	return mapPlane(plane, r.MapSourceToRegistered)
}

// MapRegisteredPlaneToSource maps a patient-space plane from the registered
// RCS back to the source RCS.
func (r *AffineRegistration) MapRegisteredPlaneToSource(plane render.Plane) (render.Plane, error) {
	return mapPlane(plane, r.MapRegisteredToSource)
}

func mapPlane(plane render.Plane, mapper func(render.Vec3) (render.Vec3, error)) (render.Plane, error) {
	if mapper == nil || !finiteVec(plane.Origin) || !finiteVec(plane.U) || !finiteVec(plane.V) {
		return render.Plane{}, fmt.Errorf("%w: plane contains non-finite geometry", ErrInvalidObject)
	}
	origin, err := mapper(plane.Origin)
	if err != nil {
		return render.Plane{}, err
	}
	uEnd, err := mapper(plane.Origin.Add(plane.U))
	if err != nil {
		return render.Plane{}, err
	}
	vEnd, err := mapper(plane.Origin.Add(plane.V))
	if err != nil {
		return render.Plane{}, err
	}
	return render.Plane{Origin: origin, U: uEnd.Sub(origin), V: vEnd.Sub(origin)}, nil
}

// MapSourceVolumeToRegistered maps the eight patient-space corner voxel
// centers of a source volume into the registered RCS. It allocates no voxel
// buffer and preserves the source dimensions.
func (r *AffineRegistration) MapSourceVolumeToRegistered(volume *render.Volume) (MappedVolumeGeometry, error) {
	return mapVolumeGeometry(volume, r.MapSourceToRegistered)
}

// MapRegisteredVolumeToSource maps the eight patient-space corner voxel
// centers of a volume described in the registered RCS back to the source RCS.
func (r *AffineRegistration) MapRegisteredVolumeToSource(volume *render.Volume) (MappedVolumeGeometry, error) {
	return mapVolumeGeometry(volume, r.MapRegisteredToSource)
}

func mapVolumeGeometry(volume *render.Volume, mapper func(render.Vec3) (render.Vec3, error)) (MappedVolumeGeometry, error) {
	if volume == nil || mapper == nil || volume.Cols <= 0 || volume.Rows <= 0 || volume.Depth <= 0 {
		return MappedVolumeGeometry{}, fmt.Errorf("%w: volume geometry is unavailable", ErrInvalidObject)
	}
	out := MappedVolumeGeometry{Dimensions: [3]int{volume.Cols, volume.Rows, volume.Depth}}
	maximum := [3]float64{float64(volume.Cols - 1), float64(volume.Rows - 1), float64(volume.Depth - 1)}
	for z := 0; z < 2; z++ {
		for y := 0; y < 2; y++ {
			for x := 0; x < 2; x++ {
				index := x + 2*y + 4*z
				point := volume.VoxelToPatient(render.Vec3{X: maximum[0] * float64(x), Y: maximum[1] * float64(y), Z: maximum[2] * float64(z)})
				mapped, err := mapper(point)
				if err != nil {
					return MappedVolumeGeometry{}, err
				}
				out.Corners[index] = mapped
			}
		}
	}
	return out, nil
}

// RegisteredVolumeSampler adapts a render.VolumeReader so callers can sample
// source voxels using points in the registered RCS. It retains one bounded
// VolumeStore lease and never materializes a transformed voxel copy.
type RegisteredVolumeSampler struct {
	registration *AffineRegistration
	reader       *render.VolumeReader
}

// AcquireRegisteredVolumeSampler acquires a patient-space sampler for source
// data addressed in the registered RCS. The caller must close the sampler.
func (r *AffineRegistration) AcquireRegisteredVolumeSampler(source *render.Volume) (*RegisteredVolumeSampler, error) {
	if r == nil || !r.SourceToRegistered.Finite() || !r.RegisteredToSource.Finite() {
		return nil, fmt.Errorf("%w: affine directions are unavailable", ErrInvalidMatrix)
	}
	if source == nil {
		return nil, fmt.Errorf("%w: source volume is nil", ErrInvalidObject)
	}
	reader, err := source.AcquireReader()
	if err != nil {
		return nil, err
	}
	return &RegisteredVolumeSampler{registration: r, reader: reader}, nil
}

// SamplePatient samples the source volume at a point expressed in the
// registered RCS. It matches render.VolumeReader's patient-space sampler
// contract: ok is false for non-finite or out-of-volume points.
func (s *RegisteredVolumeSampler) SamplePatient(registered render.Vec3) (float64, bool) {
	if s == nil || s.registration == nil || s.reader == nil {
		return 0, false
	}
	source, err := s.registration.MapRegisteredToSource(registered)
	if err != nil {
		return 0, false
	}
	return s.reader.SamplePatient(source)
}

// Close releases the source volume generation lease. Close is idempotent.
func (s *RegisteredVolumeSampler) Close() error {
	if s == nil || s.reader == nil {
		return nil
	}
	err := s.reader.Close()
	s.reader = nil
	s.registration = nil
	return err
}
