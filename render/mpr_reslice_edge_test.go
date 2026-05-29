package render

import (
	"math"
	"testing"
)

// gradientColumnStackWithSpacing is like gradientColumnStack but with
// caller-supplied anisotropic pixel spacing and slice spacing, so tests can
// catch row/column/slice spacing mix-ups that isotropic (1,1,1) fixtures
// can't reveal.
func gradientColumnStackWithSpacing(rows, cols, depth int, rowSp, colSp, sliceSp float64) *Stack {
	stack := &Stack{PixelSpacing: []float64{rowSp, colSp}, SliceThickness: sliceSp}
	for z := 0; z < depth; z++ {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				data[y*cols+x] = byte(x)
			}
		}
		frame := volumeTestFrame(rows, cols, data, 0)
		frame.ImagePosition = []float64{0, 0, float64(z) * sliceSp}
		stack.Frames = append(stack.Frames, frame)
	}
	return stack
}

func TestResliceObliqueAnisotropicSpacing(t *testing.T) {
	rows, cols, depth := 16, 16, 8
	const rowSp, colSp, sliceSp = 1.0, 0.5, 3.0
	vol, err := BuildVolume(gradientColumnStackWithSpacing(rows, cols, depth, rowSp, colSp, sliceSp))
	if err != nil {
		t.Fatal(err)
	}
	if vol.RowSpacing != rowSp || vol.ColSpacing != colSp {
		t.Fatalf("volume spacing = row %.3f col %.3f, want row %.3f col %.3f", vol.RowSpacing, vol.ColSpacing, rowSp, colSp)
	}
	if math.Abs(vol.SliceSpacing-sliceSp) > 1e-6 {
		t.Fatalf("volume SliceSpacing = %.3f, want %.3f", vol.SliceSpacing, sliceSp)
	}

	window := WindowLevel{Center: 128, Width: 256}
	plane := obliqueInteriorPlane(vol)
	outW, outH := 7, 7
	img := ResliceOblique(vol, plane, outW, outH, window)

	for j := 0; j < outH; j++ {
		for i := 0; i < outW; i++ {
			s := float64(i) / float64(outW-1)
			tfrac := float64(j) / float64(outH-1)
			p := plane.At(s, tfrac)
			expected := vol.PatientToVoxel(p).X
			want := displayGrayMapped(expected, prepareWindow(window), "MONOCHROME2")
			got := grayAt(img, i, j)
			if diff := int(got) - int(want); diff < -1 || diff > 1 {
				t.Errorf("anisotropic oblique sample (%d,%d) = %d, want ~%d", i, j, got, want)
			}
		}
	}
}

func TestResliceObliqueRotationAngleSweep(t *testing.T) {
	rows, cols, depth := 16, 16, 8
	vol, err := BuildVolume(gradientColumnStack(rows, cols, depth))
	if err != nil {
		t.Fatal(err)
	}
	window := WindowLevel{Center: 128, Width: 256}
	center := vol.Center()
	const half = 3.0
	outW, outH := 7, 7

	for _, deg := range []float64{0, 90, 180, 270, -30} {
		ang := deg * math.Pi / 180
		u := vol.AxisX.Scale(math.Cos(ang)).Add(vol.Normal.Scale(math.Sin(ang)))
		v := vol.AxisY
		origin := center.Sub(u.Scale(half)).Sub(v.Scale(half))
		plane := Plane{Origin: origin, U: u.Scale(2 * half), V: v.Scale(2 * half)}
		img := ResliceOblique(vol, plane, outW, outH, window)

		for j := 0; j < outH; j++ {
			for i := 0; i < outW; i++ {
				s := float64(i) / float64(outW-1)
				tfrac := float64(j) / float64(outH-1)
				p := plane.At(s, tfrac)
				expected := vol.PatientToVoxel(p).X
				want := displayGrayMapped(expected, prepareWindow(window), "MONOCHROME2")
				got := grayAt(img, i, j)
				if diff := int(got) - int(want); diff < -1 || diff > 1 {
					t.Errorf("angle %.0f sample (%d,%d) = %d, want ~%d", deg, i, j, got, want)
				}
			}
		}
	}
}

func TestResliceObliqueOutOfBoundsIsBlackBackground(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(8, 8, 6))
	if err != nil {
		t.Fatal(err)
	}
	window := WindowLevel{Center: 128, Width: 256}
	// Translate the plane far along the through-plane normal so every sample
	// falls outside the volume (mpr_reslice.go: "Out-of-bounds samples leave
	// the background").
	plane := vol.OrthogonalPlane(MPRPlaneAxial, vol.Center())
	plane.Origin = plane.Origin.Add(vol.Normal.Scale(1000))
	img := ResliceOblique(vol, plane, 8, 8, window)

	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			if got := grayAt(img, i, j); got != 0 {
				t.Fatalf("out-of-bounds sample (%d,%d) = %d, want 0 (black background)", i, j, got)
			}
		}
	}
}

func TestResliceObliqueSlabThicknessEdgeCasesDoNotPanic(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(8, 8, 6))
	if err != nil {
		t.Fatal(err)
	}
	window := WindowLevel{Center: 128, Width: 256}
	plane := vol.OrthogonalPlane(MPRPlaneAxial, vol.Center())
	plain := ResliceOblique(vol, plane, 8, 8, window)

	cases := []struct {
		name      string
		thickness int
		mode      SlabMode
	}{
		{"zero thickness falls back to plain reslice", 0, SlabMIP},
		{"negative thickness falls back to plain reslice", -5, SlabMIP},
		{"thickness larger than volume depth (MIP)", 100, SlabMIP},
		{"thickness larger than volume depth (MinIP)", 100, SlabMinIP},
		{"thickness larger than volume depth (Average)", 100, SlabAverage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			img := ResliceObliqueSlab(vol, plane, 8, 8, c.thickness, c.mode, window)
			if img == nil {
				t.Fatal("ResliceObliqueSlab returned a nil image")
			}
			if c.thickness <= 1 {
				if grayAt(img, 4, 4) != grayAt(plain, 4, 4) {
					t.Errorf("thickness %d should fall back to the plain reslice value", c.thickness)
				}
			}
		})
	}
}

func TestResliceObliqueSlabRejectsInfiniteStep(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(8, 8, 6))
	if err != nil {
		t.Fatal(err)
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	plane := vol.OrthogonalPlane(MPRPlaneAxial, vol.Center())
	mapper := prepareWindow(WindowLevel{Center: 128, Width: 256})

	want := resliceObliqueSlabRangeWithSampler(vol, sampler, plane, vol.Normal, 8, 8, -1, 3, 1, SlabMIP, mapper)
	got := resliceObliqueSlabRangeWithSampler(vol, sampler, plane, vol.Normal, 8, 8, -1, 3, math.Inf(1), SlabMIP, mapper)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if gotPixel, wantPixel := grayAt(got, x, y), grayAt(want, x, y); gotPixel != wantPixel {
				t.Fatalf("infinite-step sample (%d,%d) = %d, want finite fallback %d", x, y, gotPixel, wantPixel)
			}
		}
	}
}

func TestClampIndexHandlesEmptyRange(t *testing.T) {
	if got := clampIndex(3, 0); got != 0 {
		t.Fatalf("clampIndex(3, 0) = %d, want 0", got)
	}
}
