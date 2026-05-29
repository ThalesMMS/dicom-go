package render

// VRQuality controls render cost: output size and ray-march step count.
type VRQuality struct {
	Width, Height int
	MaxSteps      int
}

// DefaultVRQuality is full quality at the given output size. The step count is
// high enough that the march step is sub-voxel for typical volumes, avoiding the
// "wood-grain" ring artifacts of coarse sampling; interaction uses the cheaper
// PreviewVRQuality so this cost is paid only for the settled frame.
func DefaultVRQuality(w, h int) VRQuality {
	return VRQuality{Width: w, Height: h, MaxSteps: 512}
}

// PreviewVRQuality is the cheaper draft quality used during interaction (fewer
// march steps); callers also pass a reduced output size (3d-toolbar-plan.md §5
// performance).
func PreviewVRQuality(w, h int) VRQuality {
	return VRQuality{Width: w, Height: h, MaxSteps: 96}
}
