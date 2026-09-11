package codeccost

import (
	"fmt"
	"math"
)

// Equivalence is the comparison policy used before timing is trusted.
const (
	EquivalenceExact      = "exact"
	EquivalenceLossyLimit = "lossy-limit"
	EquivalenceSkipped    = "skipped"
)

// ComparePixels checks lossless identity or a max-abs-error limit for lossy.
func ComparePixels(got, want []byte, bitsAllocated int, lossy bool, maxAbsError int) error {
	if len(got) != len(want) {
		return fmt.Errorf("codeccost: pixel bytes %d != %d", len(got), len(want))
	}
	if !lossy {
		for i := range got {
			if got[i] != want[i] {
				return fmt.Errorf("codeccost: lossless mismatch at byte %d", i)
			}
		}
		return nil
	}
	if bitsAllocated != 8 && bitsAllocated != 16 {
		return fmt.Errorf("codeccost: unsupported BitsAllocated %d", bitsAllocated)
	}
	step := bitsAllocated / 8
	if step <= 0 || len(got)%step != 0 {
		return fmt.Errorf("codeccost: pixel bytes %d is not a multiple of %d", len(got), step)
	}
	for i := 0; i < len(got); i += step {
		var a, b int
		if step == 1 {
			a = int(got[i])
			b = int(want[i])
		} else {
			a = int(got[i]) | int(got[i+1])<<8
			b = int(want[i]) | int(want[i+1])<<8
		}
		if int(math.Abs(float64(a-b))) > maxAbsError {
			return fmt.Errorf("codeccost: lossy abs error %d exceeds %d at sample %d", int(math.Abs(float64(a-b))), maxAbsError, i/step)
		}
	}
	return nil
}
