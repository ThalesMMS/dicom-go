package nifti

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/render"
)

func TestBuildHeaderWritesNIfTI1FieldsAtOfficialOffsets(t *testing.T) {
	spec := headerSpec{
		Dimensions:  [4]int{64, 32, 12, 1},
		Datatype:    4,
		BitPix:      16,
		Spacing:     [4]float64{1, 2, 3, 1},
		VoxelOffset: 352,
		Slope:       2,
		Intercept:   -1024,
		AffineRAS: render.GeometryAffine{
			-1, 0, 0, -10,
			0, -2, 0, -20,
			0, 0, 3, 30,
			0, 0, 0, 1,
		},
	}

	header, err := buildHeader(spec)
	if err != nil {
		t.Fatalf("buildHeader() error = %v", err)
	}
	if len(header) != 348 {
		t.Fatalf("header length = %d, want 348", len(header))
	}
	if got := headerInt32(header, 0); got != 348 {
		t.Errorf("sizeof_hdr = %d, want 348", got)
	}
	wantDims := [8]int16{3, 64, 32, 12, 1, 1, 1, 1}
	for index, want := range wantDims {
		if got := headerInt16(header, 40+2*index); got != want {
			t.Errorf("dim[%d] = %d, want %d", index, got, want)
		}
	}
	if got := headerInt16(header, 70); got != 4 {
		t.Errorf("datatype = %d, want 4", got)
	}
	if got := headerInt16(header, 72); got != 16 {
		t.Errorf("bitpix = %d, want 16", got)
	}
	for index, want := range [5]float32{1, 1, 2, 3, 1} {
		if got := headerFloat32(header, 76+4*index); got != want {
			t.Errorf("pixdim[%d] = %g, want %g", index, got, want)
		}
	}
	if got := headerFloat32(header, 108); got != 352 {
		t.Errorf("vox_offset = %g, want 352", got)
	}
	if got := headerFloat32(header, 112); got != 2 {
		t.Errorf("scl_slope = %g, want 2", got)
	}
	if got := headerFloat32(header, 116); got != -1024 {
		t.Errorf("scl_inter = %g, want -1024", got)
	}
	if got := header[123]; got != 2 {
		t.Errorf("xyzt_units = %d, want millimetres (2)", got)
	}
	if got := headerInt16(header, 252); got != 1 {
		t.Errorf("qform_code = %d, want scanner anatomical (1)", got)
	}
	if got := headerInt16(header, 254); got != 1 {
		t.Errorf("sform_code = %d, want scanner anatomical (1)", got)
	}
	if got := string(header[148 : 148+len(headerDescription)]); got != headerDescription {
		t.Errorf("descrip = %q, want %q", got, headerDescription)
	}
	if got := string(header[344:348]); got != "n+1\x00" {
		t.Errorf("magic = %q, want n+1\\x00", got)
	}
}

func TestBuildHeaderQFormReconstructsSFormForPatientOrientations(t *testing.T) {
	const oblique = math.Pi / 6
	tests := []struct {
		name string
		x    [3]float64
		y    [3]float64
		z    [3]float64
		qfac float64
	}{
		{name: "axial", x: [3]float64{1, 0, 0}, y: [3]float64{0, 1, 0}, z: [3]float64{0, 0, 1}, qfac: 1},
		{name: "coronal", x: [3]float64{1, 0, 0}, y: [3]float64{0, 0, -1}, z: [3]float64{0, 1, 0}, qfac: 1},
		{name: "sagittal", x: [3]float64{0, 1, 0}, y: [3]float64{0, 0, -1}, z: [3]float64{-1, 0, 0}, qfac: 1},
		{
			name: "oblique",
			x:    [3]float64{math.Cos(oblique), math.Sin(oblique), 0},
			y:    [3]float64{-math.Sin(oblique), math.Cos(oblique), 0},
			z:    [3]float64{0, 0, 1},
			qfac: 1,
		},
		{name: "left handed", x: [3]float64{1, 0, 0}, y: [3]float64{0, 1, 0}, z: [3]float64{0, 0, -1}, qfac: -1},
	}
	spacing := [4]float64{0.7, 1.1, 2.3, 1}
	origin := [3]float64{12.5, -8.25, 41}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lps := affineFromAxes(test.x, test.y, test.z, spacing, origin)
			wantRAS := affineLPS2RAS(lps)
			header, err := buildHeader(headerSpec{
				Dimensions:  [4]int{7, 5, 3, 1},
				Datatype:    16,
				BitPix:      32,
				Spacing:     spacing,
				VoxelOffset: 352,
				Slope:       1,
				AffineRAS:   wantRAS,
			})
			if err != nil {
				t.Fatalf("buildHeader() error = %v", err)
			}
			if got := float64(headerFloat32(header, 76)); got != test.qfac {
				t.Fatalf("qfac = %g, want %g", got, test.qfac)
			}

			qform := qFormAffine(header)
			sform := sFormAffine(header)
			assertAffineNear(t, "qform vs sform", qform, sform, 2e-5)
			assertAffineNear(t, "sform vs LPS-to-RAS", sform, wantRAS, 2e-5)
		})
	}
}

func TestBuildHeaderRejectsInvalidSpecifications(t *testing.T) {
	valid := headerSpec{
		Dimensions:  [4]int{8, 7, 6, 1},
		Datatype:    4,
		BitPix:      16,
		Spacing:     [4]float64{1, 2, 3, 1},
		VoxelOffset: 352,
		Slope:       1,
		AffineRAS: render.GeometryAffine{
			1, 0, 0, 0,
			0, 2, 0, 0,
			0, 0, 3, 0,
			0, 0, 0, 1,
		},
	}
	tests := []struct {
		name   string
		mutate func(*headerSpec)
	}{
		{name: "zero dimension", mutate: func(spec *headerSpec) { spec.Dimensions[1] = 0 }},
		{name: "dimension overflow", mutate: func(spec *headerSpec) { spec.Dimensions[2] = 32768 }},
		{name: "offset before extension flag", mutate: func(spec *headerSpec) { spec.VoxelOffset = 336 }},
		{name: "offset not aligned", mutate: func(spec *headerSpec) { spec.VoxelOffset = 353 }},
		{name: "offset not exact as float32", mutate: func(spec *headerSpec) { spec.VoxelOffset = 1<<40 + 16 }},
		{name: "unsupported datatype", mutate: func(spec *headerSpec) { spec.Datatype = 128; spec.BitPix = 24 }},
		{name: "datatype bitpix mismatch", mutate: func(spec *headerSpec) { spec.BitPix = 8 }},
		{name: "zero spacing", mutate: func(spec *headerSpec) { spec.Spacing[0] = 0 }},
		{name: "spacing underflows float32", mutate: func(spec *headerSpec) { spec.Spacing[0] = 1e-50 }},
		{name: "nonfinite spacing", mutate: func(spec *headerSpec) { spec.Spacing[1] = math.NaN() }},
		{name: "spacing overflows float32", mutate: func(spec *headerSpec) { spec.Spacing[2] = math.MaxFloat64 }},
		{name: "zero slope", mutate: func(spec *headerSpec) { spec.Slope = 0 }},
		{name: "slope underflows float32", mutate: func(spec *headerSpec) { spec.Slope = 1e-50 }},
		{name: "nonfinite slope", mutate: func(spec *headerSpec) { spec.Slope = math.Inf(1) }},
		{name: "slope overflows float32", mutate: func(spec *headerSpec) { spec.Slope = math.MaxFloat64 }},
		{name: "nonfinite intercept", mutate: func(spec *headerSpec) { spec.Intercept = math.NaN() }},
		{name: "intercept overflows float32", mutate: func(spec *headerSpec) { spec.Intercept = -math.MaxFloat64 }},
		{name: "invalid homogeneous row", mutate: func(spec *headerSpec) { spec.AffineRAS[15] = 0 }},
		{name: "nonfinite affine", mutate: func(spec *headerSpec) { spec.AffineRAS[3] = math.Inf(1) }},
		{name: "affine overflows float32", mutate: func(spec *headerSpec) { spec.AffineRAS[3] = math.MaxFloat64 }},
		{name: "zero affine column", mutate: func(spec *headerSpec) { spec.AffineRAS[0] = 0 }},
		{name: "nonorthogonal affine", mutate: func(spec *headerSpec) { spec.AffineRAS[1] = 0.25 }},
		{name: "affine spacing mismatch", mutate: func(spec *headerSpec) { spec.AffineRAS[0] = 1.5 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := valid
			test.mutate(&spec)
			if _, err := buildHeader(spec); err == nil {
				t.Fatal("buildHeader() error = nil, want invalid specification error")
			}
		})
	}
}

func TestBuildHeaderWritesSupportedDatatypePairsAndTemporalAxis(t *testing.T) {
	pairs := []struct {
		datatype int16
		bitpix   int16
	}{
		{2, 8},
		{4, 16},
		{8, 32},
		{16, 32},
		{64, 64},
		{256, 8},
		{512, 16},
		{768, 32},
	}
	for _, pair := range pairs {
		header, err := buildHeader(headerSpec{
			Dimensions:  [4]int{8, 7, 6, 5},
			Datatype:    pair.datatype,
			BitPix:      pair.bitpix,
			Spacing:     [4]float64{1, 1, 1, 0.25},
			VoxelOffset: 368,
			Slope:       1,
			AffineRAS: render.GeometryAffine{
				1, 0, 0, 0,
				0, 1, 0, 0,
				0, 0, 1, 0,
				0, 0, 0, 1,
			},
		})
		if err != nil {
			t.Fatalf("buildHeader(datatype=%d) error = %v", pair.datatype, err)
		}
		if got := headerInt16(header, 40); got != 4 {
			t.Errorf("datatype %d dim[0] = %d, want 4", pair.datatype, got)
		}
		if got := headerInt16(header, 70); got != pair.datatype {
			t.Errorf("datatype field = %d, want %d", got, pair.datatype)
		}
		if got := headerInt16(header, 72); got != pair.bitpix {
			t.Errorf("bitpix field = %d, want %d", got, pair.bitpix)
		}
		if got := header[123]; got != 2|8 {
			t.Errorf("xyzt_units = %d, want millimetres|seconds (%d)", got, 2|8)
		}
		if got := headerFloat32(header, 108); got != 368 {
			t.Errorf("vox_offset = %g, want 368", got)
		}
	}
}

func TestAffineLPS2RASPremultipliesPatientAxes(t *testing.T) {
	lps := render.GeometryAffine{
		1, 2, 3, 4,
		5, 6, 7, 8,
		9, 10, 11, 12,
		0, 0, 0, 1,
	}
	want := render.GeometryAffine{
		-1, -2, -3, -4,
		-5, -6, -7, -8,
		9, 10, 11, 12,
		0, 0, 0, 1,
	}
	if got := affineLPS2RAS(lps); got != want {
		t.Fatalf("affineLPS2RAS() =\n%v\nwant\n%v", got, want)
	}
}

func affineFromAxes(x, y, z [3]float64, spacing [4]float64, origin [3]float64) render.GeometryAffine {
	return render.GeometryAffine{
		x[0] * spacing[0], y[0] * spacing[1], z[0] * spacing[2], origin[0],
		x[1] * spacing[0], y[1] * spacing[1], z[1] * spacing[2], origin[1],
		x[2] * spacing[0], y[2] * spacing[1], z[2] * spacing[2], origin[2],
		0, 0, 0, 1,
	}
}

func qFormAffine(header []byte) render.GeometryAffine {
	b := float64(headerFloat32(header, 256))
	c := float64(headerFloat32(header, 260))
	d := float64(headerFloat32(header, 264))
	aSquared := 1 - (b*b + c*c + d*d)
	var a float64
	if aSquared < 1e-7 {
		length := math.Sqrt(b*b + c*c + d*d)
		b, c, d = b/length, c/length, d/length
	} else {
		a = math.Sqrt(aSquared)
	}
	r := [9]float64{
		a*a + b*b - c*c - d*d,
		2*b*c - 2*a*d,
		2*b*d + 2*a*c,
		2*b*c + 2*a*d,
		a*a + c*c - b*b - d*d,
		2*c*d - 2*a*b,
		2*b*d - 2*a*c,
		2*c*d + 2*a*b,
		a*a + d*d - c*c - b*b,
	}
	dx := float64(headerFloat32(header, 80))
	dy := float64(headerFloat32(header, 84))
	dz := float64(headerFloat32(header, 88)) * float64(headerFloat32(header, 76))
	return render.GeometryAffine{
		r[0] * dx, r[1] * dy, r[2] * dz, float64(headerFloat32(header, 268)),
		r[3] * dx, r[4] * dy, r[5] * dz, float64(headerFloat32(header, 272)),
		r[6] * dx, r[7] * dy, r[8] * dz, float64(headerFloat32(header, 276)),
		0, 0, 0, 1,
	}
}

func sFormAffine(header []byte) render.GeometryAffine {
	var affine render.GeometryAffine
	for row, offset := range []int{280, 296, 312} {
		for column := 0; column < 4; column++ {
			affine[4*row+column] = float64(headerFloat32(header, offset+4*column))
		}
	}
	affine[15] = 1
	return affine
}

func assertAffineNear(t *testing.T, label string, got, want render.GeometryAffine, tolerance float64) {
	t.Helper()
	for index := range want {
		if math.Abs(got[index]-want[index]) > tolerance {
			t.Errorf("%s affine[%d] = %.9g, want %.9g", label, index, got[index], want[index])
		}
	}
}

func headerInt16(header []byte, offset int) int16 {
	return int16(binary.LittleEndian.Uint16(header[offset : offset+2]))
}

func headerInt32(header []byte, offset int) int32 {
	return int32(binary.LittleEndian.Uint32(header[offset : offset+4]))
}

func headerFloat32(header []byte, offset int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(header[offset : offset+4]))
}
