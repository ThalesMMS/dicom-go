package codecfixture

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// SampleLayout describes reconstructed samples, independently of the compressed
// input's planar/photometric encoding. The canonical order is frame, row, column,
// component; storage endianness and planar layout may differ between decoders.
type SampleLayout struct {
	Rows          int  `json:"rows"`
	Columns       int  `json:"columns"`
	Components    int  `json:"components"`
	BitsAllocated int  `json:"bits_allocated"`
	BitsStored    int  `json:"bits_stored"`
	HighBit       int  `json:"high_bit"`
	Signed        bool `json:"signed"`
	Planar        bool `json:"planar"`
	BigEndian     bool `json:"big_endian"`
}

// SamplePolicy is an explicit reconstruction comparison contract. Zero means
// exact samples. Lossy qualification additionally requires codec-specific PSNR
// and spatial limits with a rationale; maximum error alone is not qualification.
type SamplePolicy struct {
	MaxAbsoluteError         uint64  `json:"max_absolute_error"`
	MinPSNR                  float64 `json:"min_psnr,omitempty"`
	MaxTileMeanAbsoluteError float64 `json:"max_tile_mean_absolute_error,omitempty"`
	Rationale                string  `json:"rationale,omitempty"`
}

type SamplePosition struct {
	Frame     int `json:"frame"`
	Row       int `json:"row"`
	Column    int `json:"column"`
	Component int `json:"component"`
}

type ComponentMetrics struct {
	Samples           uint64  `json:"samples"`
	Changed           uint64  `json:"changed"`
	OutsideLimit      uint64  `json:"outside_limit"`
	MaxAbsoluteError  uint64  `json:"max_absolute_error"`
	MeanAbsoluteError float64 `json:"mean_absolute_error"`
	RMSE              float64 `json:"rmse"`
	// Nil PSNR means exact equality (infinite PSNR), avoiding non-JSON Inf.
	PSNR                       *float64       `json:"psnr,omitempty"`
	WorstTileMeanAbsoluteError float64        `json:"worst_tile_mean_absolute_error"`
	WorstTile                  SamplePosition `json:"worst_tile"`
}

type ComparisonReport struct {
	Samples      uint64             `json:"samples"`
	Mismatches   uint64             `json:"mismatches"`
	OutsideLimit uint64             `json:"outside_limit"`
	First        *SamplePosition    `json:"first,omitempty"`
	Components   []ComponentMetrics `json:"components,omitempty"`
	Qualified    bool               `json:"qualified"`
	Reason       string             `json:"reason,omitempty"`
}

func (l SampleLayout) validate() error {
	if l.Rows <= 0 || l.Rows > 65535 || l.Columns <= 0 || l.Columns > 65535 || l.Components < 1 || l.Components > 4 {
		return fmt.Errorf("invalid sample geometry")
	}
	if l.BitsAllocated != 1 && l.BitsAllocated != 8 && l.BitsAllocated != 16 && l.BitsAllocated != 32 {
		return fmt.Errorf("unsupported sample storage width %d", l.BitsAllocated)
	}
	if l.BitsStored <= 0 || l.BitsStored > l.BitsAllocated || l.HighBit < l.BitsStored-1 || l.HighBit >= l.BitsAllocated {
		return fmt.Errorf("invalid stored bits/high bit")
	}
	return nil
}

func (l SampleLayout) frameBytes() int64 {
	return (int64(l.Rows)*int64(l.Columns)*int64(l.Components)*int64(l.BitsAllocated) + 7) / 8
}

func (l SampleLayout) sample(data []byte, row, column, component int) int64 {
	pixel := int64(row)*int64(l.Columns) + int64(column)
	index := pixel*int64(l.Components) + int64(component)
	if l.Planar {
		index = int64(component)*int64(l.Rows)*int64(l.Columns) + pixel
	}
	var word uint64
	if l.BitsAllocated == 1 {
		word = uint64(data[index/8]>>uint(index%8)) & 1
	} else {
		offset := index * int64(l.BitsAllocated/8)
		var order binary.ByteOrder = binary.LittleEndian
		if l.BigEndian {
			order = binary.BigEndian
		}
		switch l.BitsAllocated {
		case 8:
			word = uint64(data[offset])
		case 16:
			word = uint64(order.Uint16(data[offset:]))
		case 32:
			word = uint64(order.Uint32(data[offset:]))
		}
	}
	word = (word >> uint(l.HighBit-l.BitsStored+1)) & ((uint64(1) << uint(l.BitsStored)) - 1)
	if l.Signed && word&(uint64(1)<<uint(l.BitsStored-1)) != 0 {
		return int64(word) - int64(uint64(1)<<uint(l.BitsStored))
	}
	return int64(word)
}

// CompareSamples visits every sample and every frame. Spatial error is measured
// in 8x8 tiles independently per frame/component; no global average can hide a
// concentrated artifact. It allocates O(components), independent of image size.
func CompareSamples(got [][]byte, actual SampleLayout, want [][]byte, reference SampleLayout, policy SamplePolicy) (ComparisonReport, error) {
	r := ComparisonReport{}
	fail := func(reason string) (ComparisonReport, error) {
		r.Reason = reason
		return r, fmt.Errorf("%w: %s", ErrDecodeMismatch, reason)
	}
	if err := actual.validate(); err != nil {
		return fail("actual layout: " + err.Error())
	}
	if err := reference.validate(); err != nil {
		return fail("reference layout: " + err.Error())
	}
	if actual.Rows != reference.Rows || actual.Columns != reference.Columns || actual.Components != reference.Components || actual.Signed != reference.Signed || actual.BitsStored != reference.BitsStored {
		return fail("sample geometry/signedness/precision differs")
	}
	if len(got) != len(want) {
		return fail(fmt.Sprintf("frame count got=%d want=%d", len(got), len(want)))
	}
	if len(want) == 0 {
		return fail("no expected frames")
	}
	if math.IsNaN(policy.MinPSNR) || math.IsInf(policy.MinPSNR, 0) || policy.MinPSNR < 0 || math.IsNaN(policy.MaxTileMeanAbsoluteError) || math.IsInf(policy.MaxTileMeanAbsoluteError, 0) || policy.MaxTileMeanAbsoluteError < 0 {
		return fail("invalid comparison policy")
	}
	r.Components = make([]ComponentMetrics, reference.Components)
	sum := make([]float64, reference.Components)
	squares := make([]float64, reference.Components)
	firstIndex := uint64(math.MaxUint64)
	for frame := range got {
		for _, check := range []struct {
			data   []byte
			length int64
		}{{got[frame], actual.frameBytes()}, {want[frame], reference.frameBytes()}} {
			length := int64(len(check.data))
			if length != check.length && !(check.length%2 == 1 && length == check.length+1 && check.data[len(check.data)-1] == 0) {
				return fail(fmt.Sprintf("frame=%d byte length=%d expected=%d", frame, length, check.length))
			}
		}
		for tileRow := 0; tileRow < reference.Rows; tileRow += 8 {
			for tileColumn := 0; tileColumn < reference.Columns; tileColumn += 8 {
				tileSum := [4]float64{}
				var tileCount uint64
				for row := tileRow; row < min(tileRow+8, reference.Rows); row++ {
					for column := tileColumn; column < min(tileColumn+8, reference.Columns); column++ {
						tileCount++
						for component := 0; component < reference.Components; component++ {
							a, b := actual.sample(got[frame], row, column, component), reference.sample(want[frame], row, column, component)
							delta := a - b
							if delta < 0 {
								delta = -delta
							}
							abs := uint64(delta)
							m := &r.Components[component]
							m.Samples++
							r.Samples++
							if abs > 0 {
								m.Changed++
								r.Mismatches++
								index := (uint64(frame)*uint64(reference.Rows)*uint64(reference.Columns)+uint64(row)*uint64(reference.Columns)+uint64(column))*uint64(reference.Components) + uint64(component)
								if index < firstIndex {
									firstIndex = index
									r.First = &SamplePosition{frame, row, column, component}
								}
							}
							if abs > m.MaxAbsoluteError {
								m.MaxAbsoluteError = abs
							}
							sum[component] += float64(abs)
							squares[component] += float64(abs) * float64(abs)
							tileSum[component] += float64(abs)
							if abs > policy.MaxAbsoluteError {
								m.OutsideLimit++
								r.OutsideLimit++
							}
						}
					}
				}
				for component := range r.Components {
					mean := tileSum[component] / float64(tileCount)
					m := &r.Components[component]
					if mean > m.WorstTileMeanAbsoluteError {
						m.WorstTileMeanAbsoluteError = mean
						m.WorstTile = SamplePosition{frame, tileRow, tileColumn, component}
					}
				}
			}
		}
	}
	metricFailure := false
	peak := float64((uint64(1) << uint(reference.BitsStored)) - 1)
	for component := range r.Components {
		m := &r.Components[component]
		m.MeanAbsoluteError = sum[component] / float64(m.Samples)
		m.RMSE = math.Sqrt(squares[component] / float64(m.Samples))
		if m.RMSE > 0 {
			psnr := 20 * math.Log10(peak/m.RMSE)
			m.PSNR = &psnr
			if policy.MinPSNR > 0 && psnr < policy.MinPSNR {
				metricFailure = true
			}
		}
		if policy.MaxTileMeanAbsoluteError > 0 && m.WorstTileMeanAbsoluteError > policy.MaxTileMeanAbsoluteError {
			metricFailure = true
		}
	}
	if r.OutsideLimit > 0 {
		p := r.First
		return fail(fmt.Sprintf("frame=%d row=%d column=%d component=%d mismatches=%d outside_limit=%d", p.Frame, p.Row, p.Column, p.Component, r.Mismatches, r.OutsideLimit))
	}
	if metricFailure {
		return fail("per-component PSNR or spatial tile error exceeds policy")
	}
	r.Qualified = policy.MaxAbsoluteError == 0 || (policy.MinPSNR > 0 && policy.MaxTileMeanAbsoluteError > 0 && policy.Rationale != "")
	if !r.Qualified {
		r.Reason = "absolute tolerance alone is not lossy qualification"
	}
	return r, nil
}

func compareCaseSamples(c Case, got pixeldata.Frames) (ComparisonReport, error) {
	if len(c.ExpectedFrames) == 0 {
		return ComparisonReport{Reason: "smoke case without full expected reconstruction"}, nil
	}
	meta, err := pixeldata.ExtractMetadata(c.Object())
	if err != nil {
		return ComparisonReport{}, err
	}
	layout := SampleLayout{Rows: int(meta.Rows), Columns: int(meta.Columns), Components: int(meta.SamplesPerPixel), BitsAllocated: int(meta.BitsAllocated), BitsStored: int(meta.BitsStored), HighBit: int(meta.HighBit), Signed: meta.PixelRepresentation == 1, Planar: meta.PlanarConfiguration == 1}
	if got.Rows != layout.Rows || got.Columns != layout.Columns {
		return ComparisonReport{}, fmt.Errorf("%w: decoded geometry got=%dx%d want=%dx%d", ErrDecodeMismatch, got.Rows, got.Columns, layout.Rows, layout.Columns)
	}
	actual := layout
	if c.ReferenceLayout != nil {
		layout = *c.ReferenceLayout
	}
	if c.DecodedLayout != nil {
		actual = *c.DecodedLayout
	}
	policy := SamplePolicy{MaxAbsoluteError: uint64(c.Tolerance)}
	if c.SamplePolicy != nil {
		policy = *c.SamplePolicy
	}
	return CompareSamples(got.Data, actual, c.ExpectedFrames, layout, policy)
}
