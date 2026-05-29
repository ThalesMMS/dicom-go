package main

import (
	"encoding/binary"
	"fmt"
	"image"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/roi"
)

func main() {
	frame := &render.Frame{
		Metadata: pixeldata.Metadata{
			Rows:                      2,
			Columns:                   2,
			SamplesPerPixel:           1,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PhotometricInterpretation: "MONOCHROME2",
		},
		ByteOrder:        binary.LittleEndian,
		PixelBytes:       []byte{0, 64, 128, 255},
		DefaultWindow:    render.WindowLevel{Center: 128, Width: 256},
		Rescale:          render.Rescale{Slope: 1},
		ImagePosition:    []float64{0, 0, 0},
		ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
	}

	img, err := render.RenderFrame(frame, frame.DefaultWindow)
	if err != nil {
		panic(err)
	}
	stack := &render.Stack{DefaultWindow: frame.DefaultWindow, PixelSpacing: []float64{1, 1}, Frames: []*render.Frame{frame}}
	vol, err := render.BuildVolume(stack)
	if err != nil {
		panic(err)
	}

	mask := roi.VectorROI{Shape: roi.ROIRectangle, Points: []image.Point{image.Pt(0, 0), image.Pt(1, 1)}}.Rasterize(2, 2)
	stats := roi.Stats2D(mask, func(x, y int) (float64, bool) {
		value, ok := render.PixelValueAt(frame, x, y)
		return value.Rescaled, ok
	})

	fmt.Printf("%dx%d depth=%d roi=%d mean=%.1f\n", img.Bounds().Dx(), img.Bounds().Dy(), vol.Depth, mask.Count(), stats.Mean)
}
