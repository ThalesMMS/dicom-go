package render

import (
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"io"
	"math"
)

// Orbit-movie support for the 3D renderer (3d-toolbar-plan.md §5.13). RenderVRMovie
// orbits the camera over an arc and renders one frame per step; EncodeGIF packs
// the frames into an animated GIF via the standard library.

// RenderVRMovie renders `frames` images orbiting the camera in yaw over arcDeg
// degrees (e.g. 360 for a full turntable, 180 for a half sweep).
func RenderVRMovie(vol *Volume, cam VRCamera, preset VRPreset, window WindowLevel, shading bool, clip *VRClip, arcDeg float64, frames int, quality VRQuality) []image.Image {
	if frames < 1 {
		frames = 1
	}
	arc := arcDeg * math.Pi / 180
	out := make([]image.Image, 0, frames)
	for i := 0; i < frames; i++ {
		c := cam
		c.Yaw = cam.Yaw + arc*float64(i)/float64(frames)
		out = append(out, RenderVR(vol, c, preset, window, shading, clip, quality))
	}
	return out
}

// EncodeGIF writes the frames as an animated GIF with the given per-frame delay
// in centiseconds (100/fps). Frames are quantized to the Plan9 256-color palette.
func EncodeGIF(w io.Writer, frames []image.Image, delayCs int) error {
	if len(frames) == 0 {
		return fmt.Errorf("render: no frames to encode")
	}
	if delayCs < 1 {
		delayCs = 1
	}
	g := &gif.GIF{}
	for _, frame := range frames {
		b := frame.Bounds()
		p := image.NewPaletted(b, palette.Plan9)
		draw.Draw(p, b, frame, b.Min, draw.Src)
		g.Image = append(g.Image, p)
		g.Delay = append(g.Delay, delayCs)
	}
	return gif.EncodeAll(w, g)
}
