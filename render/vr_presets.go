package render

// The nine 3D render presets (3d-toolbar-plan.md §5.5). HU control points are our
// own, tuned from public CT conventions; they are tunable. MIP presets use
// VRModeMIP (the transfer function is unused for grayscale projection); MIP B/W
// Inverse sets Inverse so the renderer photometrically inverts the result.

// Preset names, in dropdown order.
const (
	PresetAngio        = "Angio"
	PresetAirways      = "Airways"
	PresetBonesSkin1   = "Bones and skin 1"
	PresetBonesSkin2   = "Bones and skin 2"
	PresetBonesSkin3   = "Bones and skin 3"
	PresetBonesBW      = "Bones B/W"
	PresetSkinBW       = "Skin B/W"
	PresetMIPBW        = "MIP B/W"
	PresetMIPBWInverse = "MIP B/W Inverse"
)

// VRPresets returns the nine render presets in dropdown order.
func VRPresets() []VRPreset {
	return []VRPreset{
		angioPreset(),
		airwaysPreset(),
		bonesSkinPreset(PresetBonesSkin1, 0.05),
		bonesSkinPreset(PresetBonesSkin2, 0.12),
		bonesSkinPreset(PresetBonesSkin3, 0.22),
		bonesBWPreset(),
		skinBWPreset(),
		mipBWPreset(PresetMIPBW, false),
		mipBWPreset(PresetMIPBWInverse, true),
	}
}

// VRPresetByName returns the named preset and whether it was found.
func VRPresetByName(name string) (VRPreset, bool) {
	for _, p := range VRPresets() {
		if p.Name == name {
			return p, true
		}
	}
	return VRPreset{}, false
}

// DefaultVRPreset is the preset a freshly-opened 3D window uses.
func DefaultVRPreset() VRPreset { return bonesSkinPreset(PresetBonesSkin1, 0.05) }

var (
	boneColor = []TFColorStop{
		{HU: -1000, R: 0, G: 0, B: 0},
		{HU: 300, R: 0.72, G: 0.48, B: 0.38},
		{HU: 700, R: 0.92, G: 0.82, B: 0.72},
		{HU: 1800, R: 1, G: 0.98, B: 0.92},
	}
	boneAlpha = []TFAlphaStop{
		{HU: 150, A: 0}, {HU: 300, A: 0.2}, {HU: 700, A: 0.65}, {HU: 1800, A: 0.95},
	}
)

func angioPreset() VRPreset {
	tf := NewVRTransferFunction(
		[]TFColorStop{
			{HU: 100, R: 0.5, G: 0, B: 0},
			{HU: 250, R: 0.9, G: 0.3, B: 0.1},
			{HU: 450, R: 1, G: 0.7, B: 0.4},
			{HU: 700, R: 1, G: 1, B: 1},
		},
		[]TFAlphaStop{{HU: 150, A: 0}, {HU: 250, A: 0.25}, {HU: 500, A: 0.85}},
	)
	return VRPreset{Name: PresetAngio, TF: tf, Mode: VRModeDVR, ShadingDefault: true}
}

func airwaysPreset() VRPreset {
	tf := NewVRTransferFunction(
		[]TFColorStop{
			{HU: -1000, R: 0, G: 0, B: 0},
			{HU: -600, R: 0.4, G: 0.6, B: 0.9},
			{HU: 350, R: 1, G: 0.96, B: 0.88},
		},
		[]TFAlphaStop{{HU: -900, A: 0.05}, {HU: -600, A: 0.18}, {HU: -200, A: 0.05}, {HU: 350, A: 0.3}},
	)
	return VRPreset{Name: PresetAirways, TF: tf, Mode: VRModeDVR, ShadingDefault: false}
}

func bonesSkinPreset(name string, skinAlpha float64) VRPreset {
	color := append([]TFColorStop{{HU: -200, R: 0.85, G: 0.7, B: 0.6}}, boneColor...)
	alpha := []TFAlphaStop{
		{HU: -250, A: 0}, {HU: -150, A: skinAlpha}, {HU: 100, A: skinAlpha * 0.6},
		{HU: 150, A: 0.05}, {HU: 300, A: 0.4}, {HU: 700, A: 0.75}, {HU: 1800, A: 0.95},
	}
	tf := NewVRTransferFunction(color, alpha)
	// Gradient-opacity modulation keeps the render a clean skin shell + skull
	// instead of an accumulated soft-tissue blob (reference 16).
	return VRPreset{Name: name, TF: tf, Mode: VRModeDVR, ShadingDefault: true, GradientOpacityScale: 220}
}

func bonesBWPreset() VRPreset {
	tf := NewVRTransferFunction(
		[]TFColorStop{{HU: 150, R: 0.2, G: 0.2, B: 0.2}, {HU: 700, R: 0.8, G: 0.8, B: 0.8}, {HU: 1800, R: 1, G: 1, B: 1}},
		boneAlpha,
	)
	return VRPreset{Name: PresetBonesBW, TF: tf, Mode: VRModeDVR, ShadingDefault: true}
}

func skinBWPreset() VRPreset {
	tf := NewVRTransferFunction(
		[]TFColorStop{{HU: -200, R: 0.3, G: 0.3, B: 0.3}, {HU: 100, R: 0.8, G: 0.8, B: 0.8}, {HU: 400, R: 1, G: 1, B: 1}},
		[]TFAlphaStop{{HU: -250, A: 0}, {HU: -150, A: 0.15}, {HU: 100, A: 0.35}, {HU: 400, A: 0.6}},
	)
	return VRPreset{Name: PresetSkinBW, TF: tf, Mode: VRModeDVR, ShadingDefault: false, GradientOpacityScale: 180}
}

func mipBWPreset(name string, inverse bool) VRPreset {
	// Grayscale TF (unused by the MIP path, but kept consistent).
	tf := NewVRTransferFunction(
		[]TFColorStop{{HU: 0, R: 1, G: 1, B: 1}, {HU: 1, R: 1, G: 1, B: 1}},
		[]TFAlphaStop{{HU: 0, A: 1}, {HU: 1, A: 1}},
	)
	return VRPreset{Name: name, TF: tf, Mode: VRModeMIP, ShadingDefault: false, Inverse: inverse}
}
