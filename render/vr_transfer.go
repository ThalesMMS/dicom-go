package render

import (
	_ "embed"
	"encoding/hex"
	"math"
	"strings"
	"sync"
)

// Transfer functions for the 3D volume renderer. Legacy functions retain
// absolute-HU control points. Clinical functions are authored over t in [0,1]
// and are addressed by the active VR window instead of the observed data range.

// RGBA is a linear, non-premultiplied color with components in [0,1].
type RGBA struct{ R, G, B, A float64 }

// TFColorStop and TFAlphaStop use HU for the legacy domain and normalized t for
// VRTransferDomainNormalized. The field name remains HU for source compatibility.
type TFColorStop struct{ HU, R, G, B float64 }
type TFAlphaStop struct{ HU, A float64 }

// VRMode selects how the renderer accumulates samples.
type VRMode int

const (
	VRModeDVR     VRMode = iota // direct volume rendering (compositing)
	VRModeMIP                   // maximum intensity projection
	VRModeMinIP                 // minimum intensity projection
	VRModeAverage               // mean intensity projection
)

// VRTransferDomain describes the authored scalar coordinate. HU is the zero
// value so existing constructors and serialized callers remain explicit legacy
// functions. New clinical resources use the normalized domain.
type VRTransferDomain uint8

const (
	VRTransferDomainHU VRTransferDomain = iota
	VRTransferDomainNormalized
)

// VRColorMapping identifies immutable canonical color resources or custom
// control points.
type VRColorMapping uint8

const (
	VRColorMappingControlPoints VRColorMapping = iota
	VRColorMappingMusclesBones256
	VRColorMappingLungAdvancedRGBA
)

// VROpacityMapping identifies how alpha is evaluated in the authored domain.
type VROpacityMapping uint8

const (
	VROpacityMappingControlPoints VROpacityMapping = iota
	VROpacityMappingLinear
	VROpacityMappingLogarithmicInverse
	VROpacityMappingEmbeddedRGBA
)

// VRColorSpace declares the source encoding. LUT samples are always stored in
// linear light; sRGB inputs are converted exactly once while baking.
type VRColorSpace uint8

const (
	VRColorSpaceSRGB VRColorSpace = iota
	VRColorSpaceLinear
)

// VRPrefilterID identifies a render-only preparation of modality values. It is
// deliberately separate from VolumeDerivation: changing presentation may
// select a different prepared snapshot, but it never mutates the source volume
// used by 2D, MPR, measurement, ROI, or DICOM export paths.
type VRPrefilterID uint8

const (
	VRPrefilterNone VRPrefilterID = iota
	VRPrefilterBasicSmooth5x5
)

func (id VRPrefilterID) String() string {
	switch id {
	case VRPrefilterNone:
		return "none"
	case VRPrefilterBasicSmooth5x5:
		return "basic-smooth-5x5"
	default:
		return "unknown"
	}
}

// VRLightingMaterial is the backend-neutral Blinn-Phong material used by DVR.
// Coefficients are intentionally not normalized: clinical materials may use a
// specular gain greater than one. Lighting modulates linear RGB only.
type VRLightingMaterial struct {
	Ambient       float64
	Diffuse       float64
	Specular      float64
	SpecularPower float64
}

// BasicClinicalVRLightingMaterial returns the canonical material used by the
// default and Soft Tissue clinical presentations.
func BasicClinicalVRLightingMaterial() VRLightingMaterial {
	return VRLightingMaterial{Ambient: 0.15, Diffuse: 0.90, Specular: 0.30, SpecularPower: 15}
}

// GlossyVascularVRLightingMaterial returns the canonical Angio material.
func GlossyVascularVRLightingMaterial() VRLightingMaterial {
	return VRLightingMaterial{Ambient: 0.15, Diffuse: 0.28, Specular: 1.42, SpecularPower: 50}
}

func legacyVRLightingMaterial() VRLightingMaterial {
	return VRLightingMaterial{Ambient: 0.3, Diffuse: 0.7, Specular: 0.3, SpecularPower: 20}
}

func (material VRLightingMaterial) valid() bool {
	return finite(material.Ambient) && material.Ambient >= 0 &&
		finite(material.Diffuse) && material.Diffuse >= 0 &&
		finite(material.Specular) && material.Specular >= 0 &&
		finite(material.SpecularPower) && material.SpecularPower >= 0
}

const (
	DefaultVROpacityUnitDistanceMM = 1.0
	ClinicalVRLUTSize              = 4096
	VRMusclesBonesRGBSHA256        = "6080ca9c271bb466d1994a6d5d5fd5cdb249150b0c99a0c313d871dbabcdc34e"
)

type vrLUTCacheKey struct {
	minimum float64
	maximum float64
	count   int
}

type vrLUTCache struct {
	mu      sync.Mutex
	entries map[vrLUTCacheKey]VRLUT
}

// VRTransferFunction is immutable after construction. Its private cache is
// safe to share between value copies and keeps window-only changes allocation
// free for normalized clinical functions.
type VRTransferFunction struct {
	domain                VRTransferDomain
	colorMapping          VRColorMapping
	opacityMapping        VROpacityMapping
	colorSpace            VRColorSpace
	opacityUnitDistanceMM float64
	opacityScale          float64
	opacityScaleSet       bool
	color                 []TFColorStop
	alpha                 []TFAlphaStop
	cache                 *vrLUTCache
}

// NewVRTransferFunction builds a legacy HU-authored sRGB transfer function.
func NewVRTransferFunction(color []TFColorStop, alpha []TFAlphaStop) VRTransferFunction {
	return newVRTransferFunction(
		VRTransferDomainHU,
		VRColorMappingControlPoints,
		VROpacityMappingControlPoints,
		VRColorSpaceSRGB,
		color,
		alpha,
		DefaultVROpacityUnitDistanceMM,
	)
}

// NewNormalizedVRTransferFunction builds a custom sRGB transfer function whose
// control-point coordinates are normalized t values in [0,1].
func NewNormalizedVRTransferFunction(color []TFColorStop, alpha []TFAlphaStop, opacityUnitDistanceMM float64) VRTransferFunction {
	return newVRTransferFunction(
		VRTransferDomainNormalized,
		VRColorMappingControlPoints,
		VROpacityMappingControlPoints,
		VRColorSpaceSRGB,
		color,
		alpha,
		opacityUnitDistanceMM,
	)
}

// NewMusclesBonesVRTransferFunction returns the exact 256-entry clinical color
// resource paired with either the linear or logarithmic-inverse alpha curve.
func NewMusclesBonesVRTransferFunction(opacity VROpacityMapping) VRTransferFunction {
	if opacity != VROpacityMappingLinear && opacity != VROpacityMappingLogarithmicInverse {
		opacity = VROpacityMappingLogarithmicInverse
	}
	colors := make([]TFColorStop, len(vrMusclesBonesRGB))
	for index, triplet := range vrMusclesBonesRGB {
		colors[index] = TFColorStop{
			HU: float64(index) / float64(len(vrMusclesBonesRGB)-1),
			R:  float64(triplet[0]) / 255,
			G:  float64(triplet[1]) / 255,
			B:  float64(triplet[2]) / 255,
		}
	}
	return newVRTransferFunction(
		VRTransferDomainNormalized,
		VRColorMappingMusclesBones256,
		opacity,
		VRColorSpaceSRGB,
		colors,
		nil,
		DefaultVROpacityUnitDistanceMM,
	)
}

// NewLungAdvancedVRTransferFunction returns the canonical piecewise-linear RGBA
// curve. The two interior coordinates are derived from its documented HU
// fixtures and canonical window; endpoints are stored exactly at 0 and 1.
func NewLungAdvancedVRTransferFunction() VRTransferFunction {
	const (
		green = 0.60537117719650269
		blue  = 0.70577555894851685
	)
	colors := []TFColorStop{
		{HU: 0, G: green, B: blue},
		{HU: 0.14468519748445072, G: green, B: blue},
		{HU: 0.6390236282900787, G: green, B: blue},
		{HU: 1, G: green, B: blue},
	}
	alpha := []TFAlphaStop{
		{HU: 0, A: 0},
		{HU: 0.14468519748445072, A: 0.049316491931676865},
		{HU: 0.6390236282900787, A: 0.24969108402729034},
		{HU: 1, A: 0},
	}
	return newVRTransferFunction(
		VRTransferDomainNormalized,
		VRColorMappingLungAdvancedRGBA,
		VROpacityMappingEmbeddedRGBA,
		VRColorSpaceSRGB,
		colors,
		alpha,
		DefaultVROpacityUnitDistanceMM,
	)
}

func newVRTransferFunction(
	domain VRTransferDomain,
	colorMapping VRColorMapping,
	opacityMapping VROpacityMapping,
	colorSpace VRColorSpace,
	color []TFColorStop,
	alpha []TFAlphaStop,
	opacityUnitDistanceMM float64,
) VRTransferFunction {
	c := append([]TFColorStop(nil), color...)
	a := append([]TFAlphaStop(nil), alpha...)
	sortColorStops(c)
	sortAlphaStops(a)
	if !finite(opacityUnitDistanceMM) || opacityUnitDistanceMM <= 0 {
		opacityUnitDistanceMM = DefaultVROpacityUnitDistanceMM
	}
	return VRTransferFunction{
		domain:                domain,
		colorMapping:          colorMapping,
		opacityMapping:        opacityMapping,
		colorSpace:            colorSpace,
		opacityUnitDistanceMM: opacityUnitDistanceMM,
		color:                 c,
		alpha:                 a,
		cache:                 &vrLUTCache{entries: make(map[vrLUTCacheKey]VRLUT)},
	}
}

// Clone returns a detached transfer function while preserving the immutable LUT
// cache. Sharing the cache lets presentation snapshots change WL/WW without
// rebaking normalized transfer arrays.
func (tf VRTransferFunction) Clone() VRTransferFunction {
	tf.color = append([]TFColorStop(nil), tf.color...)
	tf.alpha = append([]TFAlphaStop(nil), tf.alpha...)
	if tf.cache == nil {
		tf.cache = &vrLUTCache{entries: make(map[vrLUTCacheKey]VRLUT)}
	}
	return tf
}

func (tf VRTransferFunction) Domain() VRTransferDomain         { return tf.domain }
func (tf VRTransferFunction) ColorMapping() VRColorMapping     { return tf.colorMapping }
func (tf VRTransferFunction) OpacityMapping() VROpacityMapping { return tf.opacityMapping }
func (tf VRTransferFunction) ColorSpace() VRColorSpace         { return tf.colorSpace }

func (tf VRTransferFunction) OpacityUnitDistanceMM() float64 {
	if !finite(tf.opacityUnitDistanceMM) || tf.opacityUnitDistanceMM <= 0 {
		return DefaultVROpacityUnitDistanceMM
	}
	return tf.opacityUnitDistanceMM
}

func (tf VRTransferFunction) OpacityScale() float64 {
	if !tf.opacityScaleSet {
		return 1
	}
	return tf.opacityScale
}

// WithOpacityScale returns a new immutable identity, so its baked LUT cannot be
// confused with an unscaled cache entry. Zero is a valid fully-transparent scale.
func (tf VRTransferFunction) WithOpacityScale(scale float64) VRTransferFunction {
	if !finite(scale) || scale < 0 {
		scale = 1
	}
	tf.opacityScale = scale
	tf.opacityScaleSet = true
	tf.cache = &vrLUTCache{entries: make(map[vrLUTCacheKey]VRLUT)}
	return tf
}

// WithOpacityUnitDistanceMM returns a function with an explicit physical alpha
// reference distance. Invalid values use the contractual 1 mm fallback.
func (tf VRTransferFunction) WithOpacityUnitDistanceMM(distance float64) VRTransferFunction {
	if !finite(distance) || distance <= 0 {
		distance = DefaultVROpacityUnitDistanceMM
	}
	tf.opacityUnitDistanceMM = distance
	tf.cache = &vrLUTCache{entries: make(map[vrLUTCacheKey]VRLUT)}
	return tf
}

// WithControlPoints returns a custom transfer function in the same authored
// domain, color space and physical-opacity identity. Editors use this instead
// of silently converting normalized clinical functions back to legacy HU.
func (tf VRTransferFunction) WithControlPoints(color []TFColorStop, alpha []TFAlphaStop) VRTransferFunction {
	result := newVRTransferFunction(
		tf.domain,
		VRColorMappingControlPoints,
		VROpacityMappingControlPoints,
		tf.colorSpace,
		color,
		alpha,
		tf.OpacityUnitDistanceMM(),
	)
	if tf.opacityScaleSet {
		result = result.WithOpacityScale(tf.opacityScale)
	}
	return result
}

// WithColorStops returns a transfer function with custom color control points
// while preserving the original opacity mapping and physical-opacity settings.
func (tf VRTransferFunction) WithColorStops(color []TFColorStop) VRTransferFunction {
	tf.color = append([]TFColorStop(nil), color...)
	sortColorStops(tf.color)
	tf.colorMapping = VRColorMappingControlPoints
	tf.cache = &vrLUTCache{entries: make(map[vrLUTCacheKey]VRLUT)}
	return tf
}

// ColorStops returns a copy of the authored sRGB/linear control points.
func (tf VRTransferFunction) ColorStops() []TFColorStop {
	return append([]TFColorStop(nil), tf.color...)
}

// AlphaStops returns an editable representation of the opacity mapping. The
// canonical analytic mappings retain their identity in OpacityMapping(), while
// exposing endpoints/samples so transfer editors do not receive an empty curve.
func (tf VRTransferFunction) AlphaStops() []TFAlphaStop {
	switch tf.opacityMapping {
	case VROpacityMappingLinear:
		return []TFAlphaStop{{HU: 0, A: 0}, {HU: 1, A: 1}}
	case VROpacityMappingLogarithmicInverse:
		// Keep the editor representation below the viewer's control-point
		// capacity, leaving room for the user to add points. Rendering continues
		// to evaluate the exact analytic curve until the user customizes it.
		const count = 8
		stops := make([]TFAlphaStop, count)
		for index := range stops {
			t := float64(index) / float64(count-1)
			stops[index] = TFAlphaStop{HU: t, A: VRLogarithmicInverseOpacity(t)}
		}
		return stops
	}
	return append([]TFAlphaStop(nil), tf.alpha...)
}

func sortColorStops(stops []TFColorStop) {
	for i := 1; i < len(stops); i++ {
		for j := i; j > 0 && stops[j-1].HU > stops[j].HU; j-- {
			stops[j-1], stops[j] = stops[j], stops[j-1]
		}
	}
}

func sortAlphaStops(stops []TFAlphaStop) {
	for i := 1; i < len(stops); i++ {
		for j := i; j > 0 && stops[j-1].HU > stops[j].HU; j-- {
			stops[j-1], stops[j] = stops[j], stops[j-1]
		}
	}
}

// ColorAt returns the interpolated authored-space RGB, clamped to endpoints.
func (tf VRTransferFunction) ColorAt(value float64) (r, g, b float64) {
	return colorAtStops(tf.color, value)
}

func colorAtStops(stops []TFColorStop, value float64) (r, g, b float64) {
	n := len(stops)
	if n == 0 {
		return 1, 1, 1
	}
	if value <= stops[0].HU {
		return stops[0].R, stops[0].G, stops[0].B
	}
	if value >= stops[n-1].HU {
		return stops[n-1].R, stops[n-1].G, stops[n-1].B
	}
	for i := 1; i < n; i++ {
		if value <= stops[i].HU {
			lower, upper := stops[i-1], stops[i]
			fraction := (value - lower.HU) / (upper.HU - lower.HU)
			return lerp(lower.R, upper.R, fraction), lerp(lower.G, upper.G, fraction), lerp(lower.B, upper.B, fraction)
		}
	}
	return stops[n-1].R, stops[n-1].G, stops[n-1].B
}

// AlphaAt returns canonical analytic alpha or piecewise-linear custom alpha.
func (tf VRTransferFunction) AlphaAt(value float64) float64 {
	switch tf.opacityMapping {
	case VROpacityMappingLinear:
		return VRLinearOpacity(value)
	case VROpacityMappingLogarithmicInverse:
		return VRLogarithmicInverseOpacity(value)
	}
	n := len(tf.alpha)
	if n == 0 {
		return 0
	}
	if value <= tf.alpha[0].HU {
		return tf.alpha[0].A
	}
	if value >= tf.alpha[n-1].HU {
		return tf.alpha[n-1].A
	}
	for i := 1; i < n; i++ {
		if value <= tf.alpha[i].HU {
			lower, upper := tf.alpha[i-1], tf.alpha[i]
			fraction := (value - lower.HU) / (upper.HU - lower.HU)
			return lerp(lower.A, upper.A, fraction)
		}
	}
	return tf.alpha[n-1].A
}

func VRLinearOpacity(t float64) float64 { return clampUnit(t) }

func VRLogarithmicInverseOpacity(t float64) float64 {
	t = clampUnit(t)
	return clampUnit(1 - math.Log10(10-9*t))
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// VRLUT is an immutable baked transfer-function lookup table. Entries are
// linear-light RGBA and Lookup interpolates linearly between adjacent samples.
type VRLUT struct {
	entries  []RGBA
	min, max float64
}

// BakeLUT samples the authored function over [domainMin, domainMax]. Repeated
// calls on the same immutable function identity reuse the backing table.
func (tf VRTransferFunction) BakeLUT(domainMin, domainMax float64, count int) VRLUT {
	if count < 2 {
		count = 2
	}
	if !finite(domainMin) {
		domainMin = 0
	}
	if !finite(domainMax) || domainMax <= domainMin {
		domainMax = domainMin + 1
	}
	key := vrLUTCacheKey{minimum: domainMin, maximum: domainMax, count: count}
	if tf.cache != nil {
		tf.cache.mu.Lock()
		if cached, ok := tf.cache.entries[key]; ok {
			tf.cache.mu.Unlock()
			return cached
		}
		tf.cache.mu.Unlock()
	}

	// Convert authored color samples before interpolation. In particular, the
	// exact 8-bit Muscles-Bones entries become linear-light samples once, and
	// every intermediate lookup is then a linear interpolation between them.
	linearColor := append([]TFColorStop(nil), tf.color...)
	for index := range linearColor {
		if tf.colorSpace == VRColorSpaceSRGB {
			linearColor[index].R = srgbToLinear(linearColor[index].R)
			linearColor[index].G = srgbToLinear(linearColor[index].G)
			linearColor[index].B = srgbToLinear(linearColor[index].B)
		} else {
			linearColor[index].R = clampUnit(linearColor[index].R)
			linearColor[index].G = clampUnit(linearColor[index].G)
			linearColor[index].B = clampUnit(linearColor[index].B)
		}
	}
	entries := make([]RGBA, count)
	for index := range entries {
		value := domainMin + float64(index)/float64(count-1)*(domainMax-domainMin)
		r, g, b := colorAtStops(linearColor, value)
		entries[index] = RGBA{
			R: r,
			G: g,
			B: b,
			A: clampUnit(tf.AlphaAt(value) * tf.OpacityScale()),
		}
	}
	lut := VRLUT{entries: entries, min: domainMin, max: domainMax}
	if tf.cache != nil {
		tf.cache.mu.Lock()
		if cached, ok := tf.cache.entries[key]; ok {
			tf.cache.mu.Unlock()
			return cached
		}
		tf.cache.entries[key] = lut
		tf.cache.mu.Unlock()
	}
	return lut
}

// BakeWindowLUT produces the LUT consumed by DVR. Normalized functions always
// bake over 0..1, so changing WL/WW reuses the same immutable table. Legacy HU
// functions bake over the active window to retain their absolute-HU meaning.
func (tf VRTransferFunction) BakeWindowLUT(window WindowLevel, count int) VRLUT {
	if tf.domain == VRTransferDomainNormalized {
		return tf.BakeLUT(0, 1, count)
	}
	lower, upper := VRWindowBounds(window)
	return tf.BakeLUT(lower, upper, count)
}

// PreferredLUTSize preserves the high-resolution Lung breakpoints and analytic
// clinical opacity curves without forcing legacy presets above their old cost.
func (tf VRTransferFunction) PreferredLUTSize() int {
	if tf.domain == VRTransferDomainNormalized {
		return ClinicalVRLUTSize
	}
	return 1024
}

func srgbToLinear(value float64) float64 {
	value = clampUnit(value)
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func linearToSRGB(value float64) float64 {
	value = clampUnit(value)
	if value <= 0.0031308 {
		return 12.92 * value
	}
	return 1.055*math.Pow(value, 1/2.4) - 0.055
}

// Lookup linearly interpolates the LUT at normalized coordinate t.
func (lut VRLUT) Lookup(t float64) RGBA {
	n := len(lut.entries)
	if n == 0 {
		return RGBA{}
	}
	if !finite(t) {
		return lut.entries[0]
	}
	if t <= 0 {
		return lut.entries[0]
	}
	if t >= 1 {
		return lut.entries[n-1]
	}
	position := t * float64(n-1)
	lower := int(math.Floor(position))
	upper := lower + 1
	if upper >= n {
		upper = n - 1
	}
	fraction := position - float64(lower)
	a, b := lut.entries[lower], lut.entries[upper]
	return RGBA{
		R: lerp(a.R, b.R, fraction),
		G: lerp(a.G, b.G, fraction),
		B: lerp(a.B, b.B, fraction),
		A: lerp(a.A, b.A, fraction),
	}
}

func (lut VRLUT) At(index int) RGBA {
	if index < 0 || index >= len(lut.entries) {
		return RGBA{}
	}
	return lut.entries[index]
}

func (lut VRLUT) Len() int { return len(lut.entries) }

func (lut VRLUT) Domain() (minimum, maximum float64) { return lut.min, lut.max }

// VRWindowBounds implements the continuous clinical window used as the DVR LUT
// address. It is intentionally distinct from DICOM's discrete display-window
// formula used by 2D and non-DVR projections.
func VRWindowBounds(window WindowLevel) (lower, upper float64) {
	if !finite(window.Center) || !finite(window.Width) || window.Width <= 0 {
		return 0, 1
	}
	lower, upper = window.Center-window.Width/2, window.Center+window.Width/2
	if !finite(lower) || !finite(upper) || upper <= lower {
		return 0, 1
	}
	return lower, upper
}

func VRWindowCoordinate(value float64, window WindowLevel) float64 {
	lower, upper := VRWindowBounds(window)
	return clampUnit((value - lower) / (upper - lower))
}

func (tf VRTransferFunction) windowCoordinate(value float64, window WindowLevel) float64 {
	if tf.domain == VRTransferDomainNormalized {
		return VRWindowCoordinate(value, window)
	}
	return windowedUnit(value, window)
}

// CorrectVRAlpha applies the Beer-Lambert-style physical step correction used
// by both CPU and GPU DVR. Invalid unit distance falls back explicitly to 1 mm.
func CorrectVRAlpha(sampleAlpha, opacityScale, sampleDistanceMM, opacityUnitDistanceMM float64) float64 {
	if !finite(opacityScale) || opacityScale < 0 {
		opacityScale = 1
	}
	if !finite(sampleDistanceMM) || sampleDistanceMM <= 0 {
		return 0
	}
	if !finite(opacityUnitDistanceMM) || opacityUnitDistanceMM <= 0 {
		opacityUnitDistanceMM = DefaultVROpacityUnitDistanceMM
	}
	base := clampUnit(sampleAlpha * opacityScale)
	if base <= 0 || base >= 1 {
		return base
	}
	return clampUnit(1 - math.Pow(1-base, sampleDistanceMM/opacityUnitDistanceMM))
}

// VRPreset is a named render preset: a transfer function, an accumulation mode,
// a default-shading flag, and an invert flag (for MIP B/W Inverse).
type VRPreset struct {
	Name           string
	TF             VRTransferFunction
	Mode           VRMode
	ShadingDefault bool
	Inverse        bool

	// LightingMaterialSet distinguishes an explicit all-zero material from old
	// callers that predate the material contract. Legacy callers retain the
	// historical material; clinical presentation adapters always set it.
	LightingMaterial    VRLightingMaterial
	LightingMaterialSet bool
	Prefilter           VRPrefilterID
	// Background is linear, non-premultiplied viewport color. The viewport
	// contract is opaque; an invalid/legacy zero value resolves to black alpha 1.
	Background RGBA

	// GradientOpacityScale modulates base sample opacity by the HU gradient
	// magnitude before physical-distance correction. Zero disables modulation.
	GradientOpacityScale float64
}

// EffectiveLightingMaterial returns a finite, non-negative material. This is a
// compatibility fallback for programmatic presets created before materials
// became explicit; frozen clinical state always takes the first branch.
func (preset VRPreset) EffectiveLightingMaterial() VRLightingMaterial {
	if preset.LightingMaterialSet && preset.LightingMaterial.valid() {
		return preset.LightingMaterial
	}
	return legacyVRLightingMaterial()
}

// EffectiveBackground returns the opaque viewport background. Transparent
// export, if added, must use a separate explicit API rather than changing this
// presentation contract.
func (preset VRPreset) EffectiveBackground() RGBA {
	background := preset.Background
	if finite(background.R) && background.R >= 0 && background.R <= 1 &&
		finite(background.G) && background.G >= 0 && background.G <= 1 &&
		finite(background.B) && background.B >= 0 && background.B <= 1 &&
		background.A == 1 {
		return background
	}
	return RGBA{A: 1}
}

//go:embed resources/vr_muscles_bones_rgb.hex
var vrMusclesBonesRGBHex string

var vrMusclesBonesRGB = decodeVRMusclesBonesRGB(vrMusclesBonesRGBHex)

func decodeVRMusclesBonesRGB(encoded string) [256][3]uint8 {
	data, err := hex.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(data) != 256*3 {
		panic("render: invalid VR Muscles-Bones RGB resource")
	}
	var result [256][3]uint8
	for index := range result {
		copy(result[index][:], data[index*3:index*3+3])
	}
	return result
}

// VRMusclesBonesRGB8 returns a detached copy of the exact 8-bit source table.
func VRMusclesBonesRGB8() [256][3]uint8 { return vrMusclesBonesRGB }
