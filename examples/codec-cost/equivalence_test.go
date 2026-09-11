package codeccost

import "testing"

func TestComparePixelsRequiresLosslessIdentity(t *testing.T) {
	if err := ComparePixels([]byte{1, 2}, []byte{1, 2}, 8, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := ComparePixels([]byte{1, 2}, []byte{1, 3}, 8, false, 0); err == nil {
		t.Fatal("lossless mismatch accepted")
	}
}

func TestComparePixelsAllowsLossyWithinLimit(t *testing.T) {
	if err := ComparePixels([]byte{10}, []byte{15}, 8, true, 5); err != nil {
		t.Fatal(err)
	}
	if err := ComparePixels([]byte{10}, []byte{16}, 8, true, 5); err == nil {
		t.Fatal("lossy error above limit accepted")
	}
	if err := ComparePixels([]byte{10, 0}, []byte{15, 0}, 16, true, 5); err != nil {
		t.Fatal(err)
	}
}

func TestComparePixelsRejectsLengthNotMultipleOfSampleStep(t *testing.T) {
	err := ComparePixels([]byte{1}, []byte{1}, 16, true, 5)
	if err == nil {
		t.Fatal("odd 16-bit lossy buffer accepted")
	}
}
