package render

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// STL iso-surface export for the 3D window (3d-toolbar-plan.md §5.14). This
// extracts a watertight voxel iso-surface (the outward faces of voxels at or
// above the threshold, emitted in patient-space mm) and writes a binary STL.
// The export intentionally keeps the blocky voxel boundary instead of smoothing
// with marching cubes: every emitted triangle lies on a thresholded voxel face,
// so the mesh is deterministic, watertight, and preserves the exact selected
// voxel support for measurement/export workflows. Use VR raycasting for smooth
// clinical previews; use STL export when topological fidelity matters more than
// cosmetic interpolation.

// STLTriangle is a single iso-surface triangle in patient-space mm. It is the
// element type of ISOSurfaceTriangles, so callers outside the package can inspect
// the vertices.
type STLTriangle struct {
	A, B, C Vec3
}

// ISOSurfaceTriangles returns the triangles of the iso-surface at the given HU
// threshold, in patient-space mm.
func (v *Volume) ISOSurfaceTriangles(iso float64) ([]STLTriangle, error) {
	return v.isoSurfaceTriangles(iso)
}

func (v *Volume) isoSurfaceTriangles(iso float64) ([]STLTriangle, error) {
	if v == nil {
		return nil, nil
	}
	sampler, err := newVolumeSamplerWithError(v)
	if err != nil {
		return nil, err
	}
	defer sampler.Close()
	voxelCount, ok := checkedVoxelCount(v.Cols, v.Rows, v.Depth)
	if !ok {
		return nil, fmt.Errorf("render: iso-surface dimensions overflow")
	}
	insideVoxels := make([]bool, voxelCount)
	for z := 0; z < v.Depth; z++ {
		for y := 0; y < v.Rows; y++ {
			for x := 0; x < v.Cols; x++ {
				point := Vec3{X: float64(x), Y: float64(y), Z: float64(z)}
				if sampler.vol != v {
					point = sampler.vol.PatientToVoxel(v.VoxelToPatient(point))
				}
				value, sampleOK := sampler.trilinearAt(point)
				insideVoxels[(z*v.Rows+y)*v.Cols+x] = sampleOK && value >= iso
			}
		}
	}
	inside := func(x, y, z int) bool {
		if x < 0 || y < 0 || z < 0 || x >= v.Cols || y >= v.Rows || z >= v.Depth {
			return false
		}
		return insideVoxels[(z*v.Rows+y)*v.Cols+x]
	}
	var tris []STLTriangle
	// Each face is a quad (two triangles) on the boundary between an inside voxel
	// and an outside neighbour, placed at the voxel face (±0.5 in voxel space).
	addQuad := func(p0, p1, p2, p3 Vec3) {
		a := v.VoxelToPatient(p0)
		b := v.VoxelToPatient(p1)
		c := v.VoxelToPatient(p2)
		d := v.VoxelToPatient(p3)
		tris = append(tris, STLTriangle{a, b, c}, STLTriangle{a, c, d})
	}
	for z := 0; z < v.Depth; z++ {
		for y := 0; y < v.Rows; y++ {
			for x := 0; x < v.Cols; x++ {
				if !inside(x, y, z) {
					continue
				}
				fx, fy, fz := float64(x), float64(y), float64(z)
				if !inside(x+1, y, z) {
					addQuad(Vec3{fx + 0.5, fy - 0.5, fz - 0.5}, Vec3{fx + 0.5, fy + 0.5, fz - 0.5}, Vec3{fx + 0.5, fy + 0.5, fz + 0.5}, Vec3{fx + 0.5, fy - 0.5, fz + 0.5})
				}
				if !inside(x-1, y, z) {
					addQuad(Vec3{fx - 0.5, fy - 0.5, fz - 0.5}, Vec3{fx - 0.5, fy - 0.5, fz + 0.5}, Vec3{fx - 0.5, fy + 0.5, fz + 0.5}, Vec3{fx - 0.5, fy + 0.5, fz - 0.5})
				}
				if !inside(x, y+1, z) {
					addQuad(Vec3{fx - 0.5, fy + 0.5, fz - 0.5}, Vec3{fx - 0.5, fy + 0.5, fz + 0.5}, Vec3{fx + 0.5, fy + 0.5, fz + 0.5}, Vec3{fx + 0.5, fy + 0.5, fz - 0.5})
				}
				if !inside(x, y-1, z) {
					addQuad(Vec3{fx - 0.5, fy - 0.5, fz - 0.5}, Vec3{fx + 0.5, fy - 0.5, fz - 0.5}, Vec3{fx + 0.5, fy - 0.5, fz + 0.5}, Vec3{fx - 0.5, fy - 0.5, fz + 0.5})
				}
				if !inside(x, y, z+1) {
					addQuad(Vec3{fx - 0.5, fy - 0.5, fz + 0.5}, Vec3{fx + 0.5, fy - 0.5, fz + 0.5}, Vec3{fx + 0.5, fy + 0.5, fz + 0.5}, Vec3{fx - 0.5, fy + 0.5, fz + 0.5})
				}
				if !inside(x, y, z-1) {
					addQuad(Vec3{fx - 0.5, fy - 0.5, fz - 0.5}, Vec3{fx - 0.5, fy + 0.5, fz - 0.5}, Vec3{fx + 0.5, fy + 0.5, fz - 0.5}, Vec3{fx + 0.5, fy - 0.5, fz - 0.5})
				}
			}
		}
	}
	return tris, nil
}

// WriteBinarySTL writes the volume's iso-surface as a binary STL.
func WriteBinarySTL(w io.Writer, vol *Volume, iso float64) error {
	tris, err := vol.isoSurfaceTriangles(iso)
	if err != nil {
		return fmt.Errorf("render: prepare iso-surface: %w", err)
	}
	if len(tris) == 0 {
		return fmt.Errorf("render: iso-surface at HU %.0f is empty", iso)
	}
	bw := bufio.NewWriter(w)
	var header [80]byte
	copy(header[:], "Twin Viewer iso-surface")
	if _, err := bw.Write(header[:]); err != nil {
		return err
	}
	if err := binary.Write(bw, binary.LittleEndian, uint32(len(tris))); err != nil {
		return err
	}
	buf := make([]float32, 12)
	for _, t := range tris {
		n := triNormal(t.A, t.B, t.C)
		buf[0], buf[1], buf[2] = float32(n.X), float32(n.Y), float32(n.Z)
		buf[3], buf[4], buf[5] = float32(t.A.X), float32(t.A.Y), float32(t.A.Z)
		buf[6], buf[7], buf[8] = float32(t.B.X), float32(t.B.Y), float32(t.B.Z)
		buf[9], buf[10], buf[11] = float32(t.C.X), float32(t.C.Y), float32(t.C.Z)
		if err := binary.Write(bw, binary.LittleEndian, buf); err != nil {
			return err
		}
		if err := binary.Write(bw, binary.LittleEndian, uint16(0)); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func triNormal(a, b, c Vec3) Vec3 {
	n := b.Sub(a).Cross(c.Sub(a))
	if n.Length() < 1e-12 {
		return Vec3{}
	}
	l := n.Length()
	return Vec3{X: n.X / l, Y: n.Y / l, Z: n.Z / l}
}
