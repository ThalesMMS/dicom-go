package jpegls

import (
	"context"
	"fmt"
)

// T.87 B.1 shares adaptive contexts across a scan, while each component keeps
// its own reconstructed neighbors. B.2 retains one run index per component;
// B.3 instead codes one vector run and uses RItype=0 for every interruption.
func decodeInterleavedScan(ctx context.Context, entropy []byte, width, height, components, ilv int, preset presetCodingParameters) ([][]int, error) {
	r := &bitReader{buf: entropy}
	st := newLocoStateForPreset(preset)
	planes := make([][]int, components)
	for c := range planes {
		planes[c] = make([]int, width*height)
	}
	var runIndices [3]int
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ilv == 1 {
			for c, plane := range planes {
				st.runIdx = runIndices[c]
				if err := decodeLine(ctx, r, st, plane, width, y); err != nil {
					return nil, fmt.Errorf("component %d: %w", c, err)
				}
				runIndices[c] = st.runIdx
			}
		} else if err := decodeSampleLine(ctx, r, st, planes, width, y); err != nil {
			return nil, err
		}
	}
	if err := validateEntropyPadding(r); err != nil {
		return nil, err
	}
	return planes, nil
}

func decodeSampleLine(ctx context.Context, r *bitReader, st *locoState, planes [][]int, width, y int) error {
	for x := 0; x < width; {
		if x&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var a, b, c, q1, q2, q3 [3]int
		run := true
		for component, plane := range planes {
			ra, rb, rc, rd := neighbors(plane, width, x, y)
			a[component], b[component], c[component] = ra, rb, rc
			q1[component], q2[component], q3[component] = st.quantize(rd-rb), st.quantize(rb-rc), st.quantize(rc-ra)
			run = run && q1[component] == 0 && q2[component] == 0 && q3[component] == 0
		}
		if run {
			next, err := decodeVectorRun(r, st, planes, width, x, y, a)
			if err != nil {
				return fmt.Errorf("vector run at (%d,%d): %w", x, y, err)
			}
			x = next
			continue
		}
		for component, plane := range planes {
			value, err := decodeRegular(r, st, a[component], b[component], c[component], q1[component], q2[component], q3[component])
			if err != nil {
				return fmt.Errorf("sample (%d,%d), component %d: %w", x, y, component, err)
			}
			plane[y*width+x] = value
		}
		x++
	}
	return nil
}

func decodeVectorRun(r *bitReader, st *locoState, planes [][]int, width, x, y int, values [3]int) (int, error) {
	start := x
	for x < width {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if bit == 0 {
			remaining, err := r.readBits(runJ[st.runIdx])
			if err != nil {
				return 0, err
			}
			x += int(remaining)
			if x >= width {
				return 0, fmtInvalid("vector run exceeds line or lacks interruption")
			}
			break
		}
		segment := 1 << runJ[st.runIdx]
		remaining := width - x
		x += minInt(segment, remaining)
		if segment <= remaining && st.runIdx < 31 {
			st.runIdx++
		}
	}
	for component, plane := range planes {
		for i := start; i < x; i++ {
			plane[y*width+i] = values[component]
		}
	}
	if x == width {
		return width, nil
	}
	for component, plane := range planes {
		_, rb, _, _ := neighbors(plane, width, x, y)
		value, err := decodeRunInterruptionType(r, st, values[component], rb, st.runIdx, 0)
		if err != nil {
			return 0, err
		}
		plane[y*width+x] = value
	}
	if st.runIdx > 0 {
		st.runIdx--
	}
	return x + 1, nil
}
