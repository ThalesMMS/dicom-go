package codecfixture

import (
	"encoding/binary"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// NearEncoderCase is synthetic native input for explicit JPEG-LS encode
// qualification. It carries no claim that a local roundtrip is independent.
type NearEncoderCase struct {
	Case
	Near     int
	Metadata pixeldata.Metadata
}

// JPEGLSNearEncoderCases covers unsigned 2..16-bit grayscale/RGB, odd geometry,
// two distinct frames, extrema, quantization boundaries and long/short runs.
// ExpectedFrames are original stored samples, not decoder oracle output.
func JPEGLSNearEncoderCases() []NearEncoderCase {
	var cases []NearEncoderCase
	for bits := 2; bits <= 16; bits++ {
		maxval := (1 << bits) - 1
		limit := min(255, maxval/2)
		seen := map[int]bool{}
		for _, near := range []int{1, 3, 7, limit} {
			if near > limit || seen[near] {
				continue
			}
			seen[near] = true
			for _, components := range []int{1, 3} {
				allocated := 8
				if bits > 8 {
					allocated = 16
				}
				m := pixeldata.Metadata{Rows: 33, Columns: 129, SamplesPerPixel: uint16(components),
					BitsAllocated: uint16(allocated), BitsStored: uint16(bits), HighBit: uint16(bits - 1),
					NumberOfFrames: 2, PhotometricInterpretation: "MONOCHROME2"}
				if components == 3 {
					m.PhotometricInterpretation = "RGB"
					m.PlanarConfigurationPresent = true
				} else if bits%2 == 1 {
					m.PhotometricInterpretation = "MONOCHROME1"
				}
				frames := make([][]byte, 2)
				for frame := range frames {
					frames[frame] = make([]byte, int(m.Rows)*int(m.Columns)*components*(allocated/8))
					for y := 0; y < int(m.Rows); y++ {
						for x := 0; x < int(m.Columns); x++ {
							for c := 0; c < components; c++ {
								v := (x*31 + y*197 + c*701 + frame*503) & maxval
								switch y % 6 {
								case 0:
									v = 0
								case 1:
									v = maxval
								case 2:
									v = min(maxval, maxval/2+(x%3-1)*near)
								case 3:
									v = (x/17*near + c*11) & maxval
								case 4:
									v = ((x%2)*maxval + frame) & maxval
								}
								if frame == 1 {
									v = maxval - v
								}
								i := ((y*int(m.Columns)+x)*components + c) * (allocated / 8)
								frames[frame][i] = byte(v)
								if allocated == 16 {
									binary.LittleEndian.PutUint16(frames[frame][i:], uint16(v))
								}
							}
						}
					}
				}
				name := fmt.Sprintf("jpegls-encode-near%d-%db-%dc", near, bits, components)
				tc := nativeCase(name, SizeSmall, int(m.Rows), int(m.Columns), frames)
				for _, e := range []core.Element{
					uint16Element(tagSamplesPerPixel, m.SamplesPerPixel), uint16Element(tagBitsAllocated, m.BitsAllocated),
					uint16Element(tagBitsStored, m.BitsStored), uint16Element(tagHighBit, m.HighBit),
					stringElement(tagPhotometricInterpretation, core.VRCS, m.PhotometricInterpretation),
					stringElement(tagSOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.7"),
					stringElement(tagModality, core.VRCS, "OT"),
				} {
					tc.Elements = replaceElement(tc.Elements, e)
				}
				if components == 3 {
					tc.Elements = replaceElement(tc.Elements, uint16Element(tagPlanarConfiguration, 0))
				}
				cases = append(cases, NearEncoderCase{Case: tc, Near: near, Metadata: m})
			}
		}
	}
	return cases
}
