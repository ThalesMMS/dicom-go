package render

import "math"

// Vec3 is a 3-component vector used by the MPR/3D engine for patient-space (mm)
// geometry. It is Fyne-free so both UIs share it.
type Vec3 struct{ X, Y, Z float64 }

func (a Vec3) Add(b Vec3) Vec3      { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3      { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Scale(s float64) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func (a Vec3) Dot(b Vec3) float64   { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }

func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{
		X: a.Y*b.Z - a.Z*b.Y,
		Y: a.Z*b.X - a.X*b.Z,
		Z: a.X*b.Y - a.Y*b.X,
	}
}

func (a Vec3) Length() float64 { return math.Sqrt(a.Dot(a)) }

// Normalize returns the unit vector in a's direction, or the zero vector when a
// has (near-)zero length.
func (a Vec3) Normalize() Vec3 {
	l := a.Length()
	if l == 0 || math.IsNaN(l) || math.IsInf(l, 0) {
		return Vec3{}
	}
	return a.Scale(1 / l)
}

// Rotate returns a rotated about the given axis by theta radians (Rodrigues'
// rotation formula). The axis is normalized internally.
func (a Vec3) Rotate(axis Vec3, theta float64) Vec3 {
	k := axis.Normalize()
	if k == (Vec3{}) {
		return a
	}
	cos, sin := math.Cos(theta), math.Sin(theta)
	return a.Scale(cos).
		Add(k.Cross(a).Scale(sin)).
		Add(k.Scale(k.Dot(a) * (1 - cos)))
}
