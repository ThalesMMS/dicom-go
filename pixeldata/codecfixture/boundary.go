package codecfixture

import (
	"encoding/binary"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// CorpusCases is the deterministic synthetic portion of the shared corpus.
func CorpusCases() []Case {
	cases := []Case{JPEGBaselineSmall(), JPEGExtendedSmall(), JPEGExtendedProcess4Mono12(), JPEGLosslessSV1RGB8Interleaved()}
	c := nativeCase("encapsulated-uncompressed-multiframe", SizeSmall, 1, 2, [][]byte{{1, 2}, {3, 4}})
	c.Syntax = transfer.EncapsulatedUncompressedExplicitVRLittleEndian
	c.Elements = replaceElement(c.Elements, core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 0x0010), VR: core.VROB, Length: core.UndefinedLength}, Value: core.FragmentSequence{Fragments: [][]byte{{1, 2}, {3, 4}}}})
	cases = append(cases, c)
	return append(cases, BoundaryCases()...)
}

// BoundaryCases extends the same Case corpus with deterministic odd-size,
// two-frame sample extremes. RLE planes are independently assembled as literal
// PackBits segments, not produced by the decoder or its encoder.
func BoundaryCases() []Case {
	var cases []Case
	for _, bits := range []int{8, 12, 16, 32} {
		for _, signed := range []bool{false, true} {
			allocated := bits
			if bits == 12 {
				allocated = 16
			}
			width := allocated / 8
			frames := make([][]byte, 2)
			mask := uint64(1)<<uint(bits) - 1
			for f := range frames {
				frames[f] = make([]byte, 3*129*width)
				for i := 0; i < 3*129; i++ {
					// Long zero runs, sign boundary, extrema and row transitions.
					v := []uint64{0, mask, mask / 2, mask/2 + 1, 1, uint64(i*13 + f)}[(i/128+f)%6] & mask
					switch allocated {
					case 8:
						frames[f][i] = byte(v)
					case 16:
						binary.LittleEndian.PutUint16(frames[f][i*width:], uint16(v))
					case 32:
						binary.LittleEndian.PutUint32(frames[f][i*width:], uint32(v))
					}
				}
			}
			c := nativeCase(fmt.Sprintf("native-boundary-%d-signed-%t", bits, signed), SizeSmall, 3, 129, frames)
			c.Elements = replaceElement(c.Elements, uint16Element(tagBitsAllocated, uint16(allocated)))
			c.Elements = replaceElement(c.Elements, uint16Element(tagBitsStored, uint16(bits)))
			c.Elements = replaceElement(c.Elements, uint16Element(tagHighBit, uint16(bits-1)))
			if signed {
				c.Elements = replaceElement(c.Elements, uint16Element(tagPixelRepresentation, 1))
			}
			cases = append(cases, c)
			c.Name = fmt.Sprintf("rle-boundary-%d-signed-%t", bits, signed)
			c.Syntax, c.RegisterCodecs = transfer.RLELossless, RegisterBuiltinCodecs
			var fragments [][]byte
			for _, frame := range frames {
				planes := make([][]byte, width)
				for plane := range planes {
					planes[plane] = make([]byte, 3*129)
					for i := range planes[plane] {
						planes[plane][i] = frame[i*width+width-1-plane]
					}
				}
				fragments = append(fragments, rleFragment(planes...))
			}
			c.Elements = replaceElement(c.Elements, core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 0x0010), VR: core.VROB, Length: core.UndefinedLength}, Value: core.FragmentSequence{Fragments: fragments}})
			if bits == 32 {
				c.ExpectedError, c.ExpectedFrames = ErrorUnsupportedMetadata, nil
			}
			cases = append(cases, c)
		}
	}
	return append(cases, colorBoundaryCases()...)
}

func colorBoundaryCases() []Case {
	var cases []Case
	for _, photo := range []string{"RGB", "YBR_FULL"} {
		for _, planar := range []bool{false, true} {
			frame := make([]byte, 3*5*3)
			for i := range frame {
				frame[i] = byte(i * 17)
			}
			c := nativeCase(fmt.Sprintf("native-color-%s-planar-%t", photo, planar), SizeSmall, 3, 5, [][]byte{frame})
			c.Elements = replaceElement(c.Elements, uint16Element(tagSamplesPerPixel, 3))
			c.Elements = replaceElement(c.Elements, stringElement(tagPhotometricInterpretation, core.VRCS, photo))
			plane := uint16(0)
			if planar {
				plane = 1
			}
			c.Elements = replaceElement(c.Elements, uint16Element(tagPlanarConfiguration, plane))
			planes := make([][]byte, 3)
			for comp := range planes {
				planes[comp] = make([]byte, 15)
				for i := range planes[comp] {
					planes[comp][i] = frame[i*3+comp]
				}
			}
			if planar {
				raw := append(append(append([]byte(nil), planes[0]...), planes[1]...), planes[2]...)
				c.Elements = replaceElement(c.Elements, rawElement(append(raw, 0)))
				c.ReferenceLayout = &SampleLayout{Rows: 3, Columns: 5, Components: 3, BitsAllocated: 8, BitsStored: 8, HighBit: 7}
			} else {
				c.Elements = replaceElement(c.Elements, rawElement(append(append([]byte(nil), frame...), 0)))
			}
			cases = append(cases, c)
			c.Name = fmt.Sprintf("rle-color-%s-planar-%t", photo, planar)
			c.Syntax, c.RegisterCodecs = transfer.RLELossless, RegisterBuiltinCodecs
			c.Elements = replaceElement(c.Elements, fragmentElement(rleFragment(planes...)))
			if planar || photo != "RGB" {
				c.ExpectedError, c.ExpectedFrames = ErrorUnsupportedMetadata, nil
			}
			cases = append(cases, c)
		}
	}
	return cases
}
