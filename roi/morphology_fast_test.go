package roi

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestRunMorphologyMatchesPixelReference(t *testing.T) {
	random := rand.New(rand.NewSource(317))
	for _, dimensions := range [][2]int{{0, 0}, {1, 1}, {2, 3}, {7, 5}, {32, 17}} {
		columns, rows := dimensions[0], dimensions[1]
		for iteration := 0; iteration < 100; iteration++ {
			mask := randomMorphologyMask(random, columns, rows)
			assertRasterMaskEqual(t, Dilate(mask), dilatePixelReference(mask))
			assertRasterMaskEqual(t, Erode(mask), erodePixelReference(mask))
		}
	}
}

func TestRunMorphologyHandlesFullMaskAndEdges(t *testing.T) {
	mask := NewRasterMask(9, 7)
	for y := 0; y < mask.Rows; y++ {
		mask.SetRun(y, 0, mask.Columns)
	}

	dilated := Dilate(mask)
	eroded := Erode(mask)

	assertRasterMaskEqual(t, dilated, mask)
	if eroded.Count() != 7*5 {
		t.Fatalf("eroded full-mask count = %d, want 35", eroded.Count())
	}
	for y := 1; y < 6; y++ {
		if got := eroded.Runs(y); !reflect.DeepEqual(got, []MaskRun{{Start: 1, End: 8}}) {
			t.Fatalf("eroded row %d = %v, want [1,8)", y, got)
		}
	}
}

func TestUnionMatchesSetRunReferenceAcrossDifferentDimensions(t *testing.T) {
	random := rand.New(rand.NewSource(317_001))
	for iteration := 0; iteration < 200; iteration++ {
		destination := randomMorphologyMask(random, 17, 11)
		other := randomMorphologyMask(random, 23, 15)
		got := destination.Clone()
		want := destination.Clone()

		got.Union(other)
		other.ForEachRun(func(y int, run MaskRun) {
			want.SetRun(y, run.Start, run.End)
		})

		assertRasterMaskEqual(t, got, want)
	}
}

func TestUnionWithSelfIsNoOp(t *testing.T) {
	mask := NewRasterMask(12, 4)
	mask.SetRun(0, 1, 4)
	mask.SetRun(0, 7, 11)
	mask.SetRun(3, 0, 12)
	want := mask.Clone()

	mask.Union(mask)

	assertRasterMaskEqual(t, mask, want)
}

func BenchmarkDilateDense256(b *testing.B) {
	mask := benchmarkMorphologyMask(256, 256, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkMorphologyResult = Dilate(mask)
	}
}

func BenchmarkDilateSparse256(b *testing.B) {
	mask := benchmarkMorphologyMask(256, 256, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkMorphologyResult = Dilate(mask)
	}
}

func BenchmarkErodeDense256(b *testing.B) {
	mask := benchmarkMorphologyMask(256, 256, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkMorphologyResult = Erode(mask)
	}
}

func BenchmarkErodeSparse256(b *testing.B) {
	mask := benchmarkMorphologyMask(256, 256, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkMorphologyResult = Erode(mask)
	}
}

func BenchmarkDilatePixelReferenceDense256(b *testing.B) {
	mask := benchmarkMorphologyMask(256, 256, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkMorphologyResult = dilatePixelReference(mask)
	}
}

func BenchmarkErodePixelReferenceDense256(b *testing.B) {
	mask := benchmarkMorphologyMask(256, 256, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkMorphologyResult = erodePixelReference(mask)
	}
}

func BenchmarkUnionFragmented256(b *testing.B) {
	benchmarkUnionFragmented256(b, func(destination, other *RasterMask) {
		destination.Union(other)
	})
}

func BenchmarkUnionSetRunReferenceFragmented256(b *testing.B) {
	benchmarkUnionFragmented256(b, func(destination, other *RasterMask) {
		other.ForEachRun(func(y int, run MaskRun) {
			destination.SetRun(y, run.Start, run.End)
		})
	})
}

func benchmarkUnionFragmented256(b *testing.B, union func(destination, other *RasterMask)) {
	b.Helper()
	first := NewRasterMask(256, 256)
	second := NewRasterMask(256, 256)
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x += 32 {
			first.SetRun(y, x+2, x+10)
			second.SetRun(y, x+7, x+18)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := first.Clone()
		union(result, second)
		benchmarkMorphologyResult = result
	}
}

var benchmarkMorphologyResult *RasterMask

func randomMorphologyMask(random *rand.Rand, columns, rows int) *RasterMask {
	mask := NewRasterMask(columns, rows)
	for y := 0; y < rows; y++ {
		for x := 0; x < columns; {
			if random.Intn(4) != 0 {
				x++
				continue
			}
			start := x
			for x < columns && random.Intn(3) != 0 {
				x++
			}
			if x == start {
				x++
			}
			mask.SetRun(y, start, x)
		}
	}
	return mask
}

func benchmarkMorphologyMask(columns, rows int, dense bool) *RasterMask {
	mask := NewRasterMask(columns, rows)
	for y := 0; y < rows; y++ {
		if dense {
			mask.SetRun(y, 0, columns)
			continue
		}
		if y%4 == 0 {
			for x := 0; x < columns; x += 32 {
				mask.SetRun(y, x+4, x+12)
			}
		}
	}
	return mask
}

func dilatePixelReference(mask *RasterMask) *RasterMask {
	if mask == nil {
		return nil
	}
	out := mask.Clone()
	mask.ForEachPixel(func(x, y int) {
		out.Set(x-1, y, true)
		out.Set(x+1, y, true)
		out.Set(x, y-1, true)
		out.Set(x, y+1, true)
	})
	return out
}

func erodePixelReference(mask *RasterMask) *RasterMask {
	if mask == nil {
		return nil
	}
	out := NewRasterMask(mask.Columns, mask.Rows)
	mask.ForEachPixel(func(x, y int) {
		if mask.Get(x-1, y) && mask.Get(x+1, y) && mask.Get(x, y-1) && mask.Get(x, y+1) {
			out.Set(x, y, true)
		}
	})
	return out
}

func assertRasterMaskEqual(t *testing.T, got, want *RasterMask) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("mask nil mismatch: got %v, want %v", got, want)
		}
		return
	}
	if got.Columns != want.Columns || got.Rows != want.Rows {
		t.Fatalf("dimensions = %dx%d, want %dx%d", got.Columns, got.Rows, want.Columns, want.Rows)
	}
	for y := 0; y < got.Rows; y++ {
		gotRuns, wantRuns := got.Runs(y), want.Runs(y)
		if len(gotRuns) != len(wantRuns) {
			t.Fatalf("row %d runs = %v, want %v", y, got.Runs(y), want.Runs(y))
		}
		for runIndex := range gotRuns {
			if gotRuns[runIndex] != wantRuns[runIndex] {
				t.Fatalf("row %d runs = %v, want %v", y, got.Runs(y), want.Runs(y))
			}
		}
	}
}
