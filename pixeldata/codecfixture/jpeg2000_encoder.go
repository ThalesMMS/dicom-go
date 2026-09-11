package codecfixture

import (
	"encoding/binary"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// JPEG2000EncoderCase is synthetic native input with exact original samples.
// It carries no assumption that a decoder wrapper is an independent oracle.
type JPEG2000EncoderCase struct {
	Case
	Metadata pixeldata.Metadata
}

// JPEG2000LosslessEncoderCases covers unsigned 8..16 stored bits, monochrome
// and RGB, both allocation widths, odd geometry, tiny dimensions and two frames.
func JPEG2000LosslessEncoderCases() []JPEG2000EncoderCase {
	var cases []JPEG2000EncoderCase
	for bits := 8; bits <= 16; bits++ {
		for _, components := range []int{1, 3} {
			allocated := 8
			if bits > 8 {
				allocated = 16
			}
			cases = append(cases, jpeg2000EncoderCase(bits, allocated, components, 17, 33))
		}
	}
	cases = append(cases, jpeg2000EncoderCase(8, 16, 1, 1, 1), jpeg2000EncoderCase(8, 16, 3, 1, 31), jpeg2000EncoderCase(16, 16, 3, 129, 131))
	return cases
}

func jpeg2000EncoderCase(bits, allocated, components, rows, cols int) JPEG2000EncoderCase {
	m := pixeldata.Metadata{Rows: uint16(rows), Columns: uint16(cols), SamplesPerPixel: uint16(components), BitsAllocated: uint16(allocated), BitsStored: uint16(bits), HighBit: uint16(bits - 1), NumberOfFrames: 2, PhotometricInterpretation: "MONOCHROME2"}
	if components == 3 {
		m.PhotometricInterpretation = "RGB"
		m.PlanarConfigurationPresent = true
	} else if bits%2 == 1 {
		m.PhotometricInterpretation = "MONOCHROME1"
	}
	frames := make([][]byte, 2)
	maxValue := (1 << bits) - 1
	for f := range frames {
		frames[f] = make([]byte, rows*cols*components*(allocated/8))
		for i := 0; i < rows*cols; i++ {
			for c := 0; c < components; c++ {
				v := (i*197 + c*701 + f*503) & maxValue
				switch i % 7 {
				case 0:
					v = 0
				case 1:
					v = maxValue
				case 2:
					v = maxValue / 2
				case 3:
					v = (i/17 + c) & maxValue
				}
				if f == 1 {
					v = maxValue - v
				}
				o := (i*components + c) * (allocated / 8)
				frames[f][o] = byte(v)
				if allocated == 16 {
					binary.LittleEndian.PutUint16(frames[f][o:], uint16(v))
				}
			}
		}
	}
	name := fmt.Sprintf("j2k-encode-%db-%da-%dc-%dx%d", bits, allocated, components, rows, cols)
	tc := nativeCase(name, SizeSmall, rows, cols, frames)
	for _, e := range []core.Element{uint16Element(tagSamplesPerPixel, m.SamplesPerPixel), uint16Element(tagBitsAllocated, m.BitsAllocated), uint16Element(tagBitsStored, m.BitsStored), uint16Element(tagHighBit, m.HighBit), stringElement(tagPhotometricInterpretation, core.VRCS, m.PhotometricInterpretation), stringElement(tagSOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.7"), stringElement(tagModality, core.VRCS, "OT")} {
		tc.Elements = replaceElement(tc.Elements, e)
	}
	if components == 3 {
		tc.Elements = replaceElement(tc.Elements, uint16Element(tagPlanarConfiguration, 0))
	}
	return JPEG2000EncoderCase{tc, m}
}
