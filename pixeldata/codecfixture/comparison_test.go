package codecfixture

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestComparisonCanonicalLayoutsAndCorruption(t *testing.T) {
	le := SampleLayout{Rows: 1, Columns: 3, Components: 3, BitsAllocated: 16, BitsStored: 12, HighBit: 11, Signed: true}
	be := le
	be.BigEndian, be.Planar, be.HighBit = true, true, 15
	samples := []int16{-2048, 0, 2047, -1, 1, 256, -700, 999, -12}
	want, got := make([]byte, 18), make([]byte, 18)
	for i, value := range samples {
		binary.LittleEndian.PutUint16(want[i*2:], uint16(value)&0xfff)
		binary.BigEndian.PutUint16(got[((i%3)*3+i/3)*2:], uint16(value)<<4)
	}
	r, err := CompareSamples([][]byte{got}, be, [][]byte{want}, le, SamplePolicy{})
	if err != nil || !r.Qualified || r.Samples != 9 {
		t.Fatalf("equivalent signed layouts: %+v %v", r, err)
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"endian", "signedness", "channel", "missing frame", "last sample"} {
		t.Run(name, func(t *testing.T) {
			layout, frames := be, [][]byte{append([]byte(nil), got...)}
			switch name {
			case "endian":
				layout.BigEndian = false
			case "signedness":
				layout.Signed = false
			case "channel":
				copy(frames[0][:6], got[6:12])
			case "missing frame":
				frames = nil
			case "last sample":
				frames[0][17] ^= 0x10
			}
			if _, err := CompareSamples(frames, layout, [][]byte{want}, le, SamplePolicy{}); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}

func TestComparisonPackedPaddingAnd32BitExtremes(t *testing.T) {
	l := SampleLayout{Rows: 1, Columns: 9, Components: 1, BitsAllocated: 1, BitsStored: 1}
	if _, err := CompareSamples([][]byte{{0x55, 0xff}}, l, [][]byte{{0x55, 1}}, l, SamplePolicy{}); err != nil {
		t.Fatal(err)
	}
	l.Columns, l.BitsAllocated, l.BitsStored, l.HighBit = 3, 8, 8, 7
	if _, err := CompareSamples([][]byte{{1, 2, 3, 0}}, l, [][]byte{{1, 2, 3}}, l, SamplePolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := CompareSamples([][]byte{{1, 2, 3, 99}}, l, [][]byte{{1, 2, 3}}, l, SamplePolicy{}); err == nil {
		t.Fatal("nonzero padding accepted")
	}
	l.Columns, l.BitsAllocated, l.BitsStored, l.HighBit = 1, 32, 32, 31
	for _, signed := range []bool{false, true} {
		l.Signed = signed
		a, b := []byte{0, 0, 0, 0}, []byte{255, 255, 255, 255}
		if signed {
			a, b = []byte{0, 0, 0, 128}, []byte{255, 255, 255, 127}
		}
		r, err := CompareSamples([][]byte{a}, l, [][]byte{b}, l, SamplePolicy{})
		if err == nil || r.Components[0].MaxAbsoluteError != math.MaxUint32 {
			t.Fatalf("32-bit delta: %+v %v", r, err)
		}
	}
}

func TestComparisonLossyRequiresSpatialAndComponentMetrics(t *testing.T) {
	l := SampleLayout{Rows: 16, Columns: 16, Components: 3, BitsAllocated: 8, BitsStored: 8, HighBit: 7}
	want, got := make([]byte, 16*16*3), make([]byte, 16*16*3)
	for row := 8; row < 16; row++ {
		for col := 8; col < 16; col++ {
			got[(row*16+col)*3+2] = 8
		}
	}
	p := SamplePolicy{MaxAbsoluteError: 8, MinPSNR: 30, MaxTileMeanAbsoluteError: 4, Rationale: "test localized artifacts"}
	r, err := CompareSamples([][]byte{got}, l, [][]byte{want}, l, p)
	if err == nil || r.Components[2].MeanAbsoluteError != 2 || r.Components[2].WorstTileMeanAbsoluteError != 8 || r.Components[2].WorstTile != (SamplePosition{0, 8, 8, 2}) {
		t.Fatalf("localized corruption: %+v %v", r, err)
	}
	p.MaxTileMeanAbsoluteError, p.MinPSNR = 8, 40
	if _, err := CompareSamples([][]byte{got}, l, [][]byte{want}, l, p); err == nil {
		t.Fatal("per-component PSNR violation accepted")
	}
	p.MinPSNR = 30
	if r, err := CompareSamples([][]byte{got}, l, [][]byte{want}, l, p); err != nil || !r.Qualified {
		t.Fatalf("within lossy policy: %+v %v", r, err)
	}
	if r, err := CompareSamples([][]byte{got}, l, [][]byte{want}, l, SamplePolicy{MaxAbsoluteError: 8}); err != nil || r.Qualified {
		t.Fatalf("legacy tolerance falsely qualified: %+v %v", r, err)
	}
}

func TestComparisonLocatesLateDifferencesAndCountsEverySample(t *testing.T) {
	c := NativeMultiFrame()
	got := pixeldata.Frames{Rows: 2, Columns: 2, Data: cloneFrames(c.ExpectedFrames)}
	got.Data[1][2]++
	got.Data[1][3]++
	err := compareFrames(c, got)
	if err == nil {
		t.Fatal("late changed samples accepted")
	}
	for _, part := range []string{"frame=1", "row=1", "column=0", "component=0", "mismatches=2"} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("missing %s in %v", part, err)
		}
	}
}

func TestComparisonToleranceUsesSamplesRatherThanIndividualBytes(t *testing.T) {
	c := NativeSmall()
	c.Elements = replaceElement(c.Elements, uint16Element(tagRows, 1))
	c.Elements = replaceElement(c.Elements, uint16Element(tagColumns, 2))
	c.Elements = replaceElement(c.Elements, uint16Element(tagBitsAllocated, 16))
	c.Elements = replaceElement(c.Elements, uint16Element(tagBitsStored, 16))
	c.Elements = replaceElement(c.Elements, uint16Element(tagHighBit, 15))
	c.ExpectedFrames = [][]byte{{0, 0, 255, 255}}
	c.Tolerance = 8
	if err := compareFrames(c, pixeldata.Frames{Rows: 1, Columns: 2, Data: [][]byte{{0, 1, 255, 255}}}); err == nil {
		t.Fatal("256-sample error accepted because each byte differs by at most 1")
	}
}

func TestComparisonRejectsGeometryDriftWithIdenticalBytes(t *testing.T) {
	c := NativeSmall()
	if err := compareFrames(c, pixeldata.Frames{Rows: 1, Columns: 4, Data: cloneFrames(c.ExpectedFrames)}); err == nil {
		t.Fatal("wrong frame shape accepted")
	}
}

func TestComparisonFirstDifferenceUsesRasterOrderAcrossTiles(t *testing.T) {
	l := SampleLayout{Rows: 2, Columns: 16, Components: 1, BitsAllocated: 8, BitsStored: 8, HighBit: 7}
	want, got := make([]byte, 32), make([]byte, 32)
	got[9], got[16] = 1, 2
	r, err := CompareSamples([][]byte{got}, l, [][]byte{want}, l, SamplePolicy{})
	if err == nil || r.First == nil || *r.First != (SamplePosition{0, 0, 9, 0}) || r.Mismatches != 2 || r.OutsideLimit != 2 {
		t.Fatalf("raster ordering: %+v %v", r, err)
	}
}
