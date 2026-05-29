package render

// VoxelToNormalized maps voxel coordinates to [0,1]³ texture coordinates
// (component i divided by the dimension along that axis).
func (v *Volume) VoxelToNormalized(p Vec3) Vec3 {
	return Vec3{
		X: safeDiv(p.X, float64(v.Cols)),
		Y: safeDiv(p.Y, float64(v.Rows)),
		Z: safeDiv(p.Z, float64(v.Depth)),
	}
}

// NormalizedToVoxel is the inverse of VoxelToNormalized.
func (v *Volume) NormalizedToVoxel(n Vec3) Vec3 {
	return Vec3{
		X: n.X * float64(v.Cols),
		Y: n.Y * float64(v.Rows),
		Z: n.Z * float64(v.Depth),
	}
}

// CenterVoxel is the voxel coordinate of the volume centre.
func (v *Volume) CenterVoxel() Vec3 {
	return Vec3{
		X: float64(v.Cols-1) / 2,
		Y: float64(v.Rows-1) / 2,
		Z: float64(v.Depth-1) / 2,
	}
}

// Center is the patient-space coordinate of the volume centre.
func (v *Volume) Center() Vec3 { return v.VoxelToPatient(v.CenterVoxel()) }

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
