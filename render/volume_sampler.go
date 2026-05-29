package render

import (
	"fmt"
	"math"
)

// VolumeReader holds one read lease on the canonical immutable volume
// generation. It is safe to share between rendering workers as long as Close
// runs after those workers finish.
//
// Acquiring one reader per render avoids store synchronization in inner pixel
// loops while preserving generation lifetime across replacement and eviction.
type VolumeReader struct {
	source  *Volume
	sampler *volumeSampler
}

// AcquireReader acquires the volume generation once for repeated sampling.
// Geometry that requires correction is resolved to its explicit regularized
// generation before the reader is returned.
func (v *Volume) AcquireReader() (*VolumeReader, error) {
	if v == nil {
		return nil, fmt.Errorf("render: nil volume")
	}
	if v.closed.Load() {
		return nil, ErrVolumeStoreClosed
	}
	sampler, ok := newVolumeSampler(v)
	if !ok {
		if v.closed.Load() {
			return nil, ErrVolumeStoreClosed
		}
		return nil, fmt.Errorf("render: volume sampler unavailable")
	}
	return &VolumeReader{source: v, sampler: sampler}, nil
}

// TrilinearAt samples fractional voxel coordinates in the source Volume's
// coordinate system. If the source required regularization, the coordinate is
// mapped through patient space to the canonical regular grid.
func (r *VolumeReader) TrilinearAt(p Vec3) (float64, bool) {
	if r == nil || r.source == nil || r.sampler == nil {
		return 0, false
	}
	if r.sampler.vol != r.source {
		p = r.sampler.vol.PatientToVoxel(r.source.VoxelToPatient(p))
	}
	return r.sampler.trilinearAt(p)
}

// SamplePatient samples a point expressed in DICOM patient LPS coordinates.
func (r *VolumeReader) SamplePatient(patient Vec3) (float64, bool) {
	if r == nil || r.sampler == nil || r.sampler.vol == nil {
		return 0, false
	}
	return r.sampler.trilinearAt(r.sampler.vol.PatientToVoxel(patient))
}

// Close releases the generation lease. Close is idempotent and must run after
// all concurrent sampling calls have completed.
func (r *VolumeReader) Close() error {
	if r == nil || r.sampler == nil {
		return nil
	}
	r.sampler.Close()
	r.sampler = nil
	r.source = nil
	return nil
}

type volumeSampler struct {
	vol         *Volume
	lease       *VolumeLease
	descriptor  VolumeDescriptor
	payload     volumePayload
	values      []float32
	cols        int
	rows        int
	depth       int
	photometric string
	invert      bool
}

// newVolumeSampler creates a volumeSampler from the given Volume, validating its dimensions and determining its photometric interpretation. It returns the sampler and true if vol is valid and non-nil, or nil and false otherwise.
func newVolumeSampler(vol *Volume) (*volumeSampler, bool) {
	sampler, err := newVolumeSamplerWithError(vol)
	return sampler, err == nil
}

func newVolumeSamplerWithError(vol *Volume) (*volumeSampler, error) {
	if vol == nil || vol.Cols <= 0 || vol.Rows <= 0 || vol.Depth <= 0 {
		return nil, fmt.Errorf("render: invalid volume dimensions")
	}
	if vol.geometry.RequiresResampling {
		grid, err := vol.RegularGrid()
		if err != nil {
			return nil, err
		}
		vol = grid
	}
	lease, err := vol.acquireDirectSnapshot()
	if err != nil {
		return nil, err
	}
	// Resolve the immutable backing record once while the lease is active.
	// Sampling through VolumeSnapshot.ModalityAt would intentionally revalidate
	// the public lease on every call and therefore take the store mutex in the
	// hottest MPR/VR loop.
	record, err := lease.record()
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	photometric := normalizedPhotometric(vol.Photometric())
	sampler := &volumeSampler{
		vol:         vol,
		lease:       lease,
		descriptor:  record.descriptor,
		payload:     record.payload,
		cols:        vol.Cols,
		rows:        vol.Rows,
		depth:       vol.Depth,
		photometric: photometric,
		invert:      photometric == "MONOCHROME1",
	}
	packedRow := uint64(vol.Cols) * 4
	if payload, ok := record.payload.(*float32VolumePayload); ok &&
		record.descriptor.RowStrideBytes == packedRow &&
		record.descriptor.SliceStrideBytes == uint64(vol.Rows)*packedRow {
		sampler.values = payload.data
	}
	return sampler, nil
}

func newDecodedVolumeSampler(vol *Volume) (*volumeSampler, bool) {
	if vol == nil || vol.Cols <= 0 || vol.Rows <= 0 || vol.Depth <= 0 {
		return nil, false
	}
	values, err := decodeVolumeFloat32(vol)
	if err != nil {
		return nil, false
	}
	photometric := normalizedPhotometric(vol.Photometric())
	return &volumeSampler{
		vol:         vol,
		values:      values,
		cols:        vol.Cols,
		rows:        vol.Rows,
		depth:       vol.Depth,
		photometric: photometric,
		invert:      photometric == "MONOCHROME1",
	}, true
}

func (s *volumeSampler) Close() {
	if s == nil || s.lease == nil {
		return
	}
	_ = s.lease.Release()
	s.lease = nil
	s.descriptor = VolumeDescriptor{}
	s.payload = nil
	s.values = nil
}

func (s *volumeSampler) valueAt(col, row, slice int) (float64, bool) {
	if s == nil || s.vol == nil || slice < 0 || slice >= s.depth || col < 0 || row < 0 || col >= s.cols || row >= s.rows {
		return 0, false
	}
	if len(s.values) > 0 {
		offset := (slice*s.rows+row)*s.cols + col
		if offset >= 0 && offset < len(s.values) {
			return float64(s.values[offset]), true
		}
	}
	if s.payload == nil {
		return 0, false
	}
	scalarSize, ok := s.descriptor.ScalarFormat.scalarSize()
	if !ok {
		return 0, false
	}
	offset := uint64(slice)*s.descriptor.SliceStrideBytes +
		uint64(row)*s.descriptor.RowStrideBytes +
		uint64(col)*scalarSize
	return s.payload.modalityAt(offset, s.descriptor)
}

func (s *volumeSampler) trilinearAt(p Vec3) (float64, bool) {
	sample, ok := s.voxelSample(p)
	return sample.value(), ok
}

func (s *volumeSampler) textureAt(tex Vec3) (float64, bool) {
	return s.trilinearAt(Vec3{
		X: tex.X * float64(s.cols-1),
		Y: tex.Y * float64(s.rows-1),
		Z: tex.Z * float64(s.depth-1),
	})
}

// volumeTextureSample retains the eight voxel values and fractional position
// of one trilinear sample. DVR can use the same values for both the HU lookup
// and the trilinear field derivative instead of issuing six more trilinear
// samples for an opaque point.
type volumeTextureSample struct {
	x0, y0, z0             int
	fx, fy, fz             float64
	c000, c100, c010, c110 float64
	c001, c101, c011, c111 float64
}

func (s *volumeSampler) textureSample(tex Vec3) (volumeTextureSample, bool) {
	if s == nil {
		return volumeTextureSample{}, false
	}
	p := Vec3{
		X: tex.X * float64(s.cols-1),
		Y: tex.Y * float64(s.rows-1),
		Z: tex.Z * float64(s.depth-1),
	}
	return s.voxelSample(p)
}

func (s *volumeSampler) voxelSample(p Vec3) (volumeTextureSample, bool) {
	if s == nil {
		return volumeTextureSample{}, false
	}
	x0 := int(math.Floor(p.X))
	y0 := int(math.Floor(p.Y))
	z0 := int(math.Floor(p.Z))
	if x0 < -1 || y0 < -1 || z0 < -1 || x0 >= s.cols || y0 >= s.rows || z0 >= s.depth {
		return volumeTextureSample{}, false
	}
	return volumeTextureSample{
		x0: x0, y0: y0, z0: z0,
		fx: p.X - float64(x0), fy: p.Y - float64(y0), fz: p.Z - float64(z0),
		c000: s.valueOrZero(x0, y0, z0), c100: s.valueOrZero(x0+1, y0, z0),
		c010: s.valueOrZero(x0, y0+1, z0), c110: s.valueOrZero(x0+1, y0+1, z0),
		c001: s.valueOrZero(x0, y0, z0+1), c101: s.valueOrZero(x0+1, y0, z0+1),
		c011: s.valueOrZero(x0, y0+1, z0+1), c111: s.valueOrZero(x0+1, y0+1, z0+1),
	}, true
}

func (sample volumeTextureSample) value() float64 {
	c00 := sample.c000*(1-sample.fx) + sample.c100*sample.fx
	c10 := sample.c010*(1-sample.fx) + sample.c110*sample.fx
	c01 := sample.c001*(1-sample.fx) + sample.c101*sample.fx
	c11 := sample.c011*(1-sample.fx) + sample.c111*sample.fx
	c0 := c00*(1-sample.fy) + c10*sample.fy
	c1 := c01*(1-sample.fy) + c11*sample.fy
	return c0*(1-sample.fz) + c1*sample.fz
}

// volumeGradientCell retains the central-difference gradient at the eight
// corners of one voxel cell. Full-quality DVR takes several sub-voxel samples
// before crossing into the next cell, so the stencil is normally prepared once
// and then only trilinearly interpolated for adjacent samples.
type volumeGradientCell struct {
	sampler    *volumeSampler
	x0, y0, z0 int
	gradients  [2][2][2]Vec3
}

func (cell *volumeGradientCell) gradient(s *volumeSampler, sample volumeTextureSample) Vec3 {
	if cell == nil || s == nil {
		return Vec3{}
	}
	if cell.sampler != s || cell.x0 != sample.x0 || cell.y0 != sample.y0 || cell.z0 != sample.z0 {
		cell.prepare(s, sample)
	}
	g := cell.gradients
	g00 := lerpVec3(g[0][0][0], g[0][0][1], sample.fx)
	g10 := lerpVec3(g[0][1][0], g[0][1][1], sample.fx)
	g01 := lerpVec3(g[1][0][0], g[1][0][1], sample.fx)
	g11 := lerpVec3(g[1][1][0], g[1][1][1], sample.fx)
	return lerpVec3(lerpVec3(g00, g10, sample.fy), lerpVec3(g01, g11, sample.fy), sample.fz)
}

func (cell *volumeGradientCell) prepare(s *volumeSampler, sample volumeTextureSample) {
	cell.sampler = s
	cell.x0, cell.y0, cell.z0 = sample.x0, sample.y0, sample.z0
	xScale := float64(s.cols-1) / float64(maxInt(s.cols, 2))
	yScale := float64(s.rows-1) / float64(maxInt(s.rows, 2))
	zScale := float64(s.depth-1) / float64(maxInt(s.depth, 2))
	c := [2][2][2]float64{
		{{sample.c000, sample.c100}, {sample.c010, sample.c110}},
		{{sample.c001, sample.c101}, {sample.c011, sample.c111}},
	}
	for z := 0; z < 2; z++ {
		for y := 0; y < 2; y++ {
			left := s.valueOrZero(sample.x0-1, sample.y0+y, sample.z0+z)
			right := s.valueOrZero(sample.x0+2, sample.y0+y, sample.z0+z)
			cell.gradients[z][y][0].X = (c[z][y][1] - left) * xScale
			cell.gradients[z][y][1].X = (right - c[z][y][0]) * xScale
		}
	}
	for z := 0; z < 2; z++ {
		for x := 0; x < 2; x++ {
			below := s.valueOrZero(sample.x0+x, sample.y0-1, sample.z0+z)
			above := s.valueOrZero(sample.x0+x, sample.y0+2, sample.z0+z)
			cell.gradients[z][0][x].Y = (c[z][1][x] - below) * yScale
			cell.gradients[z][1][x].Y = (above - c[z][0][x]) * yScale
		}
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			behind := s.valueOrZero(sample.x0+x, sample.y0+y, sample.z0-1)
			inFront := s.valueOrZero(sample.x0+x, sample.y0+y, sample.z0+2)
			cell.gradients[0][y][x].Z = (c[1][y][x] - behind) * zScale
			cell.gradients[1][y][x].Z = (inFront - c[0][y][x]) * zScale
		}
	}
}

func lerpVec3(a, b Vec3, fraction float64) Vec3 {
	return Vec3{
		X: lerp(a.X, b.X, fraction),
		Y: lerp(a.Y, b.Y, fraction),
		Z: lerp(a.Z, b.Z, fraction),
	}
}

func (s *volumeSampler) valueOrZero(col, row, slice int) float64 {
	value, _ := s.valueAt(col, row, slice)
	return value
}

func (s *volumeSampler) gradientAt(tex Vec3) Vec3 {
	dx := 1.0 / float64(maxInt(s.cols, 2))
	dy := 1.0 / float64(maxInt(s.rows, 2))
	dz := 1.0 / float64(maxInt(s.depth, 2))
	hx1, _ := s.textureAt(Vec3{tex.X + dx, tex.Y, tex.Z})
	hx0, _ := s.textureAt(Vec3{tex.X - dx, tex.Y, tex.Z})
	hy1, _ := s.textureAt(Vec3{tex.X, tex.Y + dy, tex.Z})
	hy0, _ := s.textureAt(Vec3{tex.X, tex.Y - dy, tex.Z})
	hz1, _ := s.textureAt(Vec3{tex.X, tex.Y, tex.Z + dz})
	hz0, _ := s.textureAt(Vec3{tex.X, tex.Y, tex.Z - dz})
	return Vec3{X: hx1 - hx0, Y: hy1 - hy0, Z: hz1 - hz0}
}

func (s *volumeSampler) displayGrayMapped(value float64, mapper preparedVOI) uint8 {
	gray := windowedGrayMapped(value, mapper)
	if s != nil && s.invert {
		return 255 - gray
	}
	return gray
}
