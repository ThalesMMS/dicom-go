package render

import (
	"math"
	"math/bits"
	"runtime"
	"sync"
)

// Scalpel freehand carve (3d-toolbar-plan.md §5.10). A drawn lasso (screen
// polygon) is rasterized against the volume by projecting each voxel centre to
// screen: voxels inside the lasso (Cut inside) or outside it (Cut outside) get a
// per-voxel cut flag in a VRCutMask, which the raycaster skips (VRClip.cut).

// VRCutMask is a per-voxel cut flag volume, stored as a packed bitset (one bit
// per voxel) so a 512×512×300 mask is ~9.4 MB instead of ~75 MB.
type VRCutMask struct {
	cols, rows, depth int
	bits              []uint64
}

// NewVRCutMask returns an empty cut mask sized to the volume.
func NewVRCutMask(vol *Volume) *VRCutMask {
	if vol == nil {
		return &VRCutMask{}
	}
	n := vol.Cols * vol.Rows * vol.Depth
	return &VRCutMask{cols: vol.Cols, rows: vol.Rows, depth: vol.Depth, bits: make([]uint64, (n+63)/64)}
}

// Clone returns a deep copy (for the undo/redo stack).
func (m *VRCutMask) Clone() *VRCutMask {
	if m == nil {
		return nil
	}
	c := &VRCutMask{cols: m.cols, rows: m.rows, depth: m.depth, bits: make([]uint64, len(m.bits))}
	copy(c.bits, m.bits)
	return c
}

func (m *VRCutMask) matchesVolume(vol *Volume) bool {
	if m == nil || vol == nil || m.cols != vol.Cols || m.rows != vol.Rows || m.depth != vol.Depth {
		return false
	}
	total := vol.Cols * vol.Rows * vol.Depth
	return total >= 0 && len(m.bits) == (total+63)/64
}

func (m *VRCutMask) idx(x, y, z int) int { return (z*m.rows+y)*m.cols + x }

// IsCut reports whether the integer voxel is removed.
func (m *VRCutMask) IsCut(x, y, z int) bool {
	if m == nil || x < 0 || y < 0 || z < 0 || x >= m.cols || y >= m.rows || z >= m.depth {
		return false
	}
	i := m.idx(x, y, z)
	return m.bits[i>>6]&(uint64(1)<<uint(i&63)) != 0
}

// Set marks or clears one integer voxel in the cut mask.
func (m *VRCutMask) Set(x, y, z int, cut bool) {
	if m == nil || x < 0 || y < 0 || z < 0 || x >= m.cols || y >= m.rows || z >= m.depth {
		return
	}
	i := m.idx(x, y, z)
	if cut {
		m.setIndex(i)
		return
	}
	m.bits[i>>6] &^= uint64(1) << uint(i&63)
}

// setIndex sets the bit for the given flat voxel index (callers guarantee range).
func (m *VRCutMask) setIndex(i int) { m.bits[i>>6] |= uint64(1) << uint(i&63) }

func (m *VRCutMask) set(x, y, z int) {
	if x >= 0 && y >= 0 && z >= 0 && x < m.cols && y < m.rows && z < m.depth {
		m.setIndex(m.idx(x, y, z))
	}
}

// CutCount returns how many voxels are cut (for tests/diagnostics).
func (m *VRCutMask) CutCount() int {
	n := 0
	for _, w := range m.bits {
		n += bits.OnesCount64(w)
	}
	return n
}

// cut maps a [0,1]³ texture coordinate to the nearest voxel and reports its flag.
func (m *VRCutMask) cut(tex Vec3) bool {
	if m == nil || m.cols < 2 {
		return false
	}
	x := int(tex.X*float64(m.cols-1) + 0.5)
	y := int(tex.Y*float64(m.rows-1) + 0.5)
	z := int(tex.Z*float64(m.depth-1) + 0.5)
	return m.IsCut(x, y, z)
}

// Project maps a world (box-centred mm) point to screen pixel coordinates for a
// (w×h) image; visible is false when the point is behind the camera. It inverts
// VRCamera.Ray.
func (c VRCamera) Project(p Vec3, w, h int) (px, py float64, visible bool) {
	return newVRProjector(c, w, h).project(p)
}

type vrProjector struct {
	eye        Vec3
	right      Vec3
	up         Vec3
	forward    Vec3
	projection VRCameraProjection
	aspectTan  float64
	tanHalfFov float64
	distance   float64
	width      float64
	height     float64
}

func newVRProjector(cam VRCamera, w, h int) vrProjector {
	right, up, forward := cam.basis()
	aspect := 1.0
	if h > 0 {
		aspect = float64(w) / float64(h)
	}
	tan := math.Tan(cam.FovY / 2)
	if tan == 0 {
		tan = 1e-6
	}
	projection := cam.Projection.normalized()
	eye := cam.Position()
	if projection == VRProjectionEndoscopy {
		eye = cam.Target
	}
	return vrProjector{
		eye:        eye,
		right:      right,
		up:         up,
		forward:    forward,
		projection: projection,
		aspectTan:  aspect * tan,
		tanHalfFov: tan,
		distance:   cam.Distance,
		width:      float64(w),
		height:     float64(h),
	}
}

func (p vrProjector) project(point Vec3) (px, py float64, visible bool) {
	rel := point.Sub(p.eye)
	depth := rel.Dot(p.forward)
	if depth <= 1e-6 {
		return 0, 0, false
	}
	var ndcx, ndcy float64
	if p.projection == VRProjectionParallel {
		xScale := p.aspectTan * p.distance
		yScale := p.tanHalfFov * p.distance
		if math.Abs(xScale) < 1e-6 || math.Abs(yScale) < 1e-6 {
			return 0, 0, false
		}
		ndcx = rel.Dot(p.right) / xScale
		ndcy = rel.Dot(p.up) / yScale
	} else {
		ndcx = (rel.Dot(p.right) / depth) / p.aspectTan
		ndcy = (rel.Dot(p.up) / depth) / p.tanHalfFov
	}
	px = (ndcx + 1) / 2 * p.width
	py = (1 - ndcy) / 2 * p.height
	return px, py, true
}

// CarveVR applies a scalpel carve: it returns a new cut mask (the prior cuts plus
// the new ones). cutInside removes voxels whose centre projects inside the lasso;
// otherwise it removes those projecting outside it. The lasso is a screen polygon
// in the (w×h) render's pixel coordinates.
func CarveVR(vol *Volume, cam VRCamera, prev *VRCutMask, lasso [][2]float64, w, h int, cutInside bool) *VRCutMask {
	var mask *VRCutMask
	if prev.matchesVolume(vol) {
		mask = prev.Clone()
	} else {
		mask = NewVRCutMask(vol)
	}
	if vol == nil || len(lasso) < 3 {
		return mask
	}

	// Precompute the camera projector ONCE. cam.Project recomputed it on every
	// call; doing that per voxel froze the UI on large volumes.
	projector := newVRProjector(cam, w, h)
	size := vol.PhysicalSize()
	boxMin := size.Scale(-0.5)
	cxf := math.Max(1, float64(vol.Cols-1))
	cyf := math.Max(1, float64(vol.Rows-1))
	czf := math.Max(1, float64(vol.Depth-1))
	rowCols := vol.Rows * vol.Cols
	total := vol.Depth * rowCols

	// carve processes the flat voxel index range [lo,hi), projecting each voxel
	// centre with the precomputed basis (an inlined VRCamera.Project).
	carve := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			z := i / rowCols
			rem := i - z*rowCols
			y := rem / vol.Cols
			x := rem - y*vol.Cols
			point := Vec3{
				X: boxMin.X + float64(x)/cxf*size.X,
				Y: boxMin.Y + float64(y)/cyf*size.Y,
				Z: boxMin.Z + float64(z)/czf*size.Z,
			}
			inside := false
			if px, py, visible := projector.project(point); visible {
				inside = pointInPolygon(px, py, lasso)
			}
			if cutInside == inside {
				mask.setIndex(i)
			}
		}
	}

	// Parallelize across cores (matching RenderVR). Chunk boundaries are rounded
	// to a multiple of 64 so each goroutine owns a disjoint set of bitset words —
	// no two goroutines touch the same uint64, so there is no data race.
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers == 1 || total < 1<<16 {
		carve(0, total)
		return mask
	}
	chunk := (total + workers - 1) / workers
	chunk = (chunk + 63) &^ 63
	if chunk < 64 {
		chunk = 64
	}
	var wg sync.WaitGroup
	for lo := 0; lo < total; lo += chunk {
		hi := lo + chunk
		if hi > total {
			hi = total
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			carve(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
	return mask
}

// pointInPolygon is the standard even-odd ray-casting test.
func pointInPolygon(px, py float64, poly [][2]float64) bool {
	in := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > py) != (yj > py) && px < (xj-xi)*(py-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}
