package jpegls

const (
	minC       = -128
	maxC       = 127
	basicT1    = 3
	basicT2    = 7
	basicT3    = 21
	basicReset = 64
)

var runJ = [32]int{
	0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3,
	4, 4, 5, 5, 6, 6, 7, 7, 8, 9, 10, 11, 12, 13, 14, 15,
}

type locoState struct {
	maxval int
	range_ int
	qbpp   int
	bpp    int
	limit  int
	reset  int
	near   int
	t1     int
	t2     int
	t3     int
	a      [367]int
	n      [367]int
	b      [365]int
	c      [365]int
	nn     [2]int
	runIdx int
}

func newLocoState(maxval int) *locoState {
	t1, t2, t3 := defaultThresholds(maxval)
	return newLocoStateWithPreset(maxval, t1, t2, t3, basicReset)
}

func newLocoStateWithPreset(maxval, t1, t2, t3, reset int) *locoState {
	return newLocoStateForPreset(presetCodingParameters{maxval: maxval, t1: t1, t2: t2, t3: t3, reset: reset})
}

func newLocoStateForPreset(p presetCodingParameters) *locoState {
	s := &locoState{maxval: p.maxval, near: p.near, reset: p.reset, t1: p.t1, t2: p.t2, t3: p.t3}
	s.range_ = (p.maxval+2*p.near)/(2*p.near+1) + 1
	s.qbpp = ceilLog2(s.range_)
	s.bpp = maxInt(2, ceilLog2(p.maxval+1))
	s.limit = 2 * (s.bpp + maxInt(8, s.bpp))
	initA := maxInt(2, (s.range_+32)/64)
	for i := 0; i < 367; i++ {
		s.a[i] = initA
		s.n[i] = 1
	}
	return s
}

func defaultThresholds(maxval int) (t1, t2, t3 int) {
	return defaultNearThresholds(maxval, 0)
}

func defaultNearThresholds(maxval, near int) (t1, t2, t3 int) {
	// T.87 C.2.4.1.1.1 CLAMP returns the lower bound for either violation,
	// including an expression above MAXVAL (unlike ordinary saturation).
	threshold := func(value, lower int) int {
		if value < lower || value > maxval {
			return lower
		}
		return value
	}
	if maxval >= 128 {
		factor := (minInt(maxval, 4095) + 128) / 256
		t1 = threshold(factor*(basicT1-2)+2+3*near, near+1)
		t2 = threshold(factor*(basicT2-3)+3+5*near, t1)
		t3 = threshold(factor*(basicT3-4)+4+7*near, t2)
		return t1, t2, t3
	}
	factor := 256 / (maxval + 1)
	t1 = threshold(maxInt(2, basicT1/factor+3*near), near+1)
	t2 = threshold(maxInt(3, basicT2/factor+5*near), t1)
	t3 = threshold(maxInt(4, basicT3/factor+7*near), t2)
	return t1, t2, t3
}

func (s *locoState) quantize(d int) int {
	if d <= -s.t3 {
		return -4
	}
	if d <= -s.t2 {
		return -3
	}
	if d <= -s.t1 {
		return -2
	}
	if d < -s.near {
		return -1
	}
	if d <= s.near {
		return 0
	}
	if d < s.t1 {
		return 1
	}
	if d < s.t2 {
		return 2
	}
	if d < s.t3 {
		return 3
	}
	return 4
}

func (s *locoState) modRange(err int) int {
	if err < 0 {
		err += s.range_
	}
	if err >= (s.range_+1)/2 {
		err -= s.range_
	}
	return err
}

func (s *locoState) mapRegular(err, q, k int) int {
	if s.near == 0 && k == 0 && 2*s.b[q] <= -s.n[q] {
		if err >= 0 {
			return 2*err + 1
		}
		return -2 * (err + 1)
	}
	if err >= 0 {
		return 2 * err
	}
	return -2*err - 1
}

func (s *locoState) unmapRegular(mapped, q, k int) int {
	if s.near == 0 && k == 0 && 2*s.b[q] <= -s.n[q] {
		if mapped%2 != 0 {
			return (mapped - 1) / 2
		}
		return -mapped/2 - 1
	}
	if mapped%2 == 0 {
		return mapped / 2
	}
	return -(mapped + 1) / 2
}

func (s *locoState) updateRegular(q, err int) {
	s.b[q] += err * (2*s.near + 1)
	s.a[q] += absInt(err)
	if s.n[q] == s.reset {
		s.a[q] >>= 1
		if s.b[q] >= 0 {
			s.b[q] >>= 1
		} else {
			s.b[q] = -((1 - s.b[q]) >> 1)
		}
		s.n[q] >>= 1
	}
	s.n[q]++
	if s.b[q] <= -s.n[q] {
		s.b[q] += s.n[q]
		if s.c[q] > minC {
			s.c[q]--
		}
		if s.b[q] <= -s.n[q] {
			s.b[q] = 1 - s.n[q]
		}
	} else if s.b[q] > 0 {
		s.b[q] -= s.n[q]
		if s.c[q] < maxC {
			s.c[q]++
		}
		if s.b[q] > 0 {
			s.b[q] = 0
		}
	}
}

func (s *locoState) reconstruct(prediction, err int) int {
	step := 2*s.near + 1
	rx := prediction + err*step
	if rx < -s.near {
		rx += s.range_ * step
	}
	if rx > s.maxval+s.near {
		rx -= s.range_ * step
	}
	return clampInt(rx, 0, s.maxval)
}

func predict(ra, rb, rc int) int {
	if rc >= maxInt(ra, rb) {
		return minInt(ra, rb)
	}
	if rc <= minInt(ra, rb) {
		return maxInt(ra, rb)
	}
	return ra + rb - rc
}

func (s *locoState) golombK(a, n int) int {
	k := 0
	// Shift the positive counter in a wider domain: valid 16-bit LSE states
	// can require a comparison above MaxInt32 even though A and N fit int.
	for uint64(n)<<uint(k) < uint64(a) {
		k++
	}
	return k
}

func writeLimitedGolomb(w *bitWriter, mapped, k, limit, qbpp int) {
	high := mapped >> k
	maxHigh := limit - qbpp - 1
	if high < maxHigh {
		w.writeZeros(high)
		w.writeBit(1)
		if k > 0 {
			w.writeBits(uint32(mapped)&uint32((1<<k)-1), k)
		}
		return
	}
	w.writeZeros(maxHigh)
	w.writeBit(1)
	w.writeBits(uint32(mapped-1), qbpp)
}

func readLimitedGolomb(r *bitReader, k, limit, qbpp int) (int, error) {
	maxHigh := limit - qbpp - 1
	zeros := 0
	for zeros < maxHigh {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if bit == 1 {
			if k == 0 {
				return zeros, nil
			}
			rem, err := r.readBits(k)
			if err != nil {
				return 0, err
			}
			return zeros<<k | int(rem), nil
		}
		zeros++
	}
	bit, err := r.readBit()
	if err != nil {
		return 0, err
	}
	if bit != 1 {
		return 0, fmtInvalid("limited Golomb terminator")
	}
	val, err := r.readBits(qbpp)
	if err != nil {
		return 0, err
	}
	return int(val) + 1, nil
}

func neighbors(recon []int, width, x, y int) (ra, rb, rc, rd int) {
	if y > 0 {
		rb = recon[(y-1)*width+x]
		if x+1 < width {
			rd = recon[(y-1)*width+x+1]
		} else {
			rd = rb
		}
	}
	if x > 0 {
		ra = recon[y*width+x-1]
		if y > 0 {
			rc = recon[(y-1)*width+x-1]
		}
		return ra, rb, rc, rd
	}
	ra = rb
	// ISO/IEC 14495-1 initializes the virtual pixel immediately left of the
	// current row from the first sample of the previous row. Consequently, at
	// x=0 Rc is the virtual-left value retained by the previous row: the first
	// sample two rows above (or zero for the first two rows), not Rb.
	if y > 1 {
		rc = recon[(y-2)*width]
	}
	return ra, rb, rc, rd
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ceilLog2(v int) int {
	if v <= 1 {
		return 0
	}
	n := 0
	x := v - 1
	for x > 0 {
		x >>= 1
		n++
	}
	return n
}
