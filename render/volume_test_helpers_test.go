package render

import (
	"encoding/binary"
	"image"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func gradientColumnStack(rows, cols, depth int) *Stack {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z := 0; z < depth; z++ {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				data[y*cols+x] = byte(x)
			}
		}
		stack.Frames = append(stack.Frames, volumeTestFrame(rows, cols, data, z))
	}
	return stack
}

func gradientXZStack(rows, cols, depth int) *Stack {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z := 0; z < depth; z++ {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				data[y*cols+x] = byte(x + z*50)
			}
		}
		stack.Frames = append(stack.Frames, volumeTestFrame(rows, cols, data, z))
	}
	return stack
}

func sphereStack(n int, radius float64) *Stack {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	center := float64(n-1) / 2
	for z := 0; z < n; z++ {
		data := make([]byte, n*n)
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				dx := float64(x) - center
				dy := float64(y) - center
				dz := float64(z) - center
				if dx*dx+dy*dy+dz*dz <= radius*radius {
					data[y*n+x] = 255
				}
			}
		}
		stack.Frames = append(stack.Frames, volumeTestFrame(n, n, data, z))
	}
	return stack
}

func singleBrightVoxelStack(n, brightX, brightY, brightZ int) *Stack {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z := 0; z < n; z++ {
		data := make([]byte, n*n)
		if z == brightZ {
			data[brightY*n+brightX] = 255
		}
		stack.Frames = append(stack.Frames, volumeTestFrame(n, n, data, z))
	}
	return stack
}

func volumeTestFrame(rows, cols int, data []byte, z int) *Frame {
	return &Frame{
		Metadata: pixeldata.Metadata{
			Rows:                      uint16(rows),
			Columns:                   uint16(cols),
			SamplesPerPixel:           1,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PhotometricInterpretation: "MONOCHROME2",
		},
		ByteOrder:        binary.LittleEndian,
		PixelBytes:       data,
		Rescale:          Rescale{Slope: 1},
		ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
		ImagePosition:    []float64{0, 0, float64(z)},
	}
}

func obliqueInteriorPlane(vol *Volume) Plane {
	center := vol.Center()
	angle := 30.0 * math.Pi / 180
	u := vol.AxisX.Scale(math.Cos(angle)).Add(vol.Normal.Scale(math.Sin(angle)))
	v := vol.AxisY
	const half = 3.0
	return Plane{
		Origin: center.Sub(u.Scale(half)).Sub(v.Scale(half)),
		U:      u.Scale(2 * half),
		V:      v.Scale(2 * half),
	}
}

func opaqueAbovePreset() VRPreset {
	tf := NewVRTransferFunction(
		[]TFColorStop{{HU: 0, R: 1, G: 1, B: 1}, {HU: 255, R: 1, G: 1, B: 1}},
		[]TFAlphaStop{{HU: 0, A: 0}, {HU: 127, A: 0}, {HU: 128, A: 1}, {HU: 255, A: 1}},
	)
	return VRPreset{Name: "test", TF: tf, Mode: VRModeDVR}
}

func vrBrightness(img *image.NRGBA, x, y int) int {
	c := img.NRGBAAt(x, y)
	return int(c.R) + int(c.G) + int(c.B)
}
