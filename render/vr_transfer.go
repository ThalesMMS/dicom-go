package render

import "math"

// Transfer functions for the 3D volume renderer (3d-toolbar-plan.md §5.5). A
// VRTransferFunction is piecewise-linear color and alpha control points over the
// HU domain; it bakes to a fixed-size RGBA LUT indexed by the normalized data
// density. Color stops are authored as display sRGB values and baked into
// linear-light RGB so DVR compositing is colorimetrically correct.

// RGBA is a linear, non-premultiplied color with components in [0,1].
type RGBA struct{ R, G, B, A float64 }

// TFColorStop / TFAlphaStop are control points in HU.
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

// VRTransferFunction maps HU to color/alpha by piecewise-linear interpolation.
type VRTransferFunction struct {
	color []TFColorStop
	alpha []TFAlphaStop
}

// NewVRTransferFunction builds a TF from (already roughly sorted) control points;
// it sorts defensively by HU.
func NewVRTransferFunction(color []TFColorStop, alpha []TFAlphaStop) VRTransferFunction {
	c := append([]TFColorStop(nil), color...)
	a := append([]TFAlphaStop(nil), alpha...)
	sortColorStops(c)
	sortAlphaStops(a)
	return VRTransferFunction{color: c, alpha: a}
}

// ColorStops returns a copy of the color control points.
func (tf VRTransferFunction) ColorStops() []TFColorStop {
	return append([]TFColorStop(nil), tf.color...)
}

// AlphaStops returns a copy of the alpha control points.
func (tf VRTransferFunction) AlphaStops() []TFAlphaStop {
	return append([]TFAlphaStop(nil), tf.alpha...)
}

func sortColorStops(s []TFColorStop) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].HU > s[j].HU; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func sortAlphaStops(s []TFAlphaStop) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].HU > s[j].HU; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// ColorAt returns the interpolated sRGB at hu, clamped to the endpoints.
func (tf VRTransferFunction) ColorAt(hu float64) (r, g, b float64) {
	n := len(tf.color)
	if n == 0 {
		return 1, 1, 1
	}
	if hu <= tf.color[0].HU {
		return tf.color[0].R, tf.color[0].G, tf.color[0].B
	}
	if hu >= tf.color[n-1].HU {
		return tf.color[n-1].R, tf.color[n-1].G, tf.color[n-1].B
	}
	for i := 1; i < n; i++ {
		if hu <= tf.color[i].HU {
			a, b2 := tf.color[i-1], tf.color[i]
			t := (hu - a.HU) / (b2.HU - a.HU)
			return lerp(a.R, b2.R, t), lerp(a.G, b2.G, t), lerp(a.B, b2.B, t)
		}
	}
	return tf.color[n-1].R, tf.color[n-1].G, tf.color[n-1].B
}

// AlphaAt returns the interpolated alpha at hu, clamped to the endpoints.
func (tf VRTransferFunction) AlphaAt(hu float64) float64 {
	n := len(tf.alpha)
	if n == 0 {
		return 0
	}
	if hu <= tf.alpha[0].HU {
		return tf.alpha[0].A
	}
	if hu >= tf.alpha[n-1].HU {
		return tf.alpha[n-1].A
	}
	for i := 1; i < n; i++ {
		if hu <= tf.alpha[i].HU {
			a, b := tf.alpha[i-1], tf.alpha[i]
			t := (hu - a.HU) / (b.HU - a.HU)
			return lerp(a.A, b.A, t)
		}
	}
	return tf.alpha[n-1].A
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// VRLUT is a baked transfer-function lookup table over a HU domain.
type VRLUT struct {
	entries  []RGBA
	min, max float64
}

// BakeLUT samples the TF into an n-entry LUT spanning [domainMin, domainMax].
func (tf VRTransferFunction) BakeLUT(domainMin, domainMax float64, n int) VRLUT {
	if n < 2 {
		n = 2
	}
	if domainMax <= domainMin {
		domainMax = domainMin + 1
	}
	entries := make([]RGBA, n)
	for i := 0; i < n; i++ {
		hu := domainMin + (float64(i)/float64(n-1))*(domainMax-domainMin)
		r, g, b := tf.ColorAt(hu)
		entries[i] = RGBA{R: srgbToLinear(r), G: srgbToLinear(g), B: srgbToLinear(b), A: tf.AlphaAt(hu)}
	}
	return VRLUT{entries: entries, min: domainMin, max: domainMax}
}

func srgbToLinear(v float64) float64 {
	v = clampUnit(v)
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func linearToSRGB(v float64) float64 {
	v = clampUnit(v)
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// Lookup indexes the LUT by a normalized data density in [0,1].
func (l VRLUT) Lookup(density float64) RGBA {
	n := len(l.entries)
	if n == 0 {
		return RGBA{}
	}
	if density <= 0 {
		return l.entries[0]
	}
	if density >= 1 {
		return l.entries[n-1]
	}
	i := int(density*float64(n-1) + 0.5)
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	return l.entries[i]
}

// Len reports the LUT size.
func (l VRLUT) Len() int { return len(l.entries) }

// VRPreset is a named render preset: a transfer function, an accumulation mode,
// a default-shading flag, and an invert flag (for MIP B/W Inverse).
type VRPreset struct {
	Name           string
	TF             VRTransferFunction
	Mode           VRMode
	ShadingDefault bool
	Inverse        bool

	// GradientOpacityScale modulates each sample's opacity by its HU gradient
	// magnitude (alpha *= clamp(|grad|/scale)), so homogeneous interiors fade out
	// and only surfaces survive — the standard DVR technique that keeps a
	// bones+skin render a clean shell instead of a soft-tissue blob (reference 16).
	// It is the gradient magnitude (HU, central difference) at which opacity is
	// full; 0 disables the modulation.
	GradientOpacityScale float64
}
