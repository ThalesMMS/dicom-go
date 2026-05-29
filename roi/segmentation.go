package roi

import (
	"sort"

	"github.com/ThalesMMS/dicom-go/render"
)

// Segmentation3D is a volumetric label built from per-slice RasterMasks. It is
// the 3D layer of the ROI model: 2D RasterMasks (themselves convertible from
// VectorROIs) are placed at slice indices, and the segmentation keeps a
// reference to the VolumeGeometry so later volumetric statistics and DICOM SEG
// export can use real patient-space spacing without re-deriving spatial context.
type Segmentation3D struct {
	// Geometry is the patient-space context of the volume this segmentation
	// labels (origin, spacings, real slice positions, classification).
	Geometry render.VolumeGeometry
	Columns  int
	Rows     int

	masks map[int]*RasterMask
}

// NewSegmentation3D creates an empty segmentation over a volume of the given
// in-plane size, carrying the supplied geometry.
func NewSegmentation3D(geometry render.VolumeGeometry, columns, rows int) *Segmentation3D {
	return &Segmentation3D{
		Geometry: geometry,
		Columns:  columns,
		Rows:     rows,
		masks:    map[int]*RasterMask{},
	}
}

// SetMask attaches (or replaces) the 2D mask for a slice index. A nil or empty
// mask removes the slice's entry to keep the segmentation sparse.
func (s *Segmentation3D) SetMask(slice int, mask *RasterMask) {
	if s == nil {
		return
	}
	if mask == nil || mask.Empty() {
		delete(s.masks, slice)
		return
	}
	s.masks[slice] = mask
}

// Mask returns the 2D mask for a slice index, creating an empty one on demand so
// callers can paint into it. The created mask is tracked but stays sparse-empty
// until pixels are set; call Prune to drop emptied slices.
func (s *Segmentation3D) Mask(slice int) *RasterMask {
	if s == nil {
		return nil
	}
	if m, ok := s.masks[slice]; ok {
		return m
	}
	m := NewRasterMask(s.Columns, s.Rows)
	s.masks[slice] = m
	return m
}

// MaskAt returns the mask for a slice index without creating one.
func (s *Segmentation3D) MaskAt(slice int) (*RasterMask, bool) {
	if s == nil {
		return nil, false
	}
	m, ok := s.masks[slice]
	return m, ok
}

// SetVoxel sets or clears a single voxel, creating the slice mask as needed.
func (s *Segmentation3D) SetVoxel(x, y, slice int, on bool) {
	if s == nil {
		return
	}
	s.Mask(slice).Set(x, y, on)
}

// Voxel reports whether a voxel is set.
func (s *Segmentation3D) Voxel(x, y, slice int) bool {
	if s == nil {
		return false
	}
	m, ok := s.masks[slice]
	if !ok {
		return false
	}
	return m.Get(x, y)
}

// VoxelCount returns the total number of set voxels across all slices.
func (s *Segmentation3D) VoxelCount() int {
	if s == nil {
		return 0
	}
	n := 0
	for _, m := range s.masks {
		n += m.Count()
	}
	return n
}

// Slices returns the sorted slice indices that currently hold set voxels.
func (s *Segmentation3D) Slices() []int {
	if s == nil {
		return nil
	}
	out := make([]int, 0, len(s.masks))
	for idx, m := range s.masks {
		if !m.Empty() {
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out
}

// Prune drops empty per-slice masks so the segmentation stays sparse.
func (s *Segmentation3D) Prune() {
	if s == nil {
		return
	}
	for idx, m := range s.masks {
		if m.Empty() {
			delete(s.masks, idx)
		}
	}
}
