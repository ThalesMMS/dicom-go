package object

import "github.com/ThalesMMS/dicom-go/parser"

type Frame = parser.Frame
type FrameMetadata = parser.FrameMetadata
type FrameSink = parser.FrameSink
type FrameSinkFunc = parser.FrameSinkFunc

func NewFrameChannelSink(ch chan Frame) FrameSink {
	return parser.NewFrameChannelSink(ch)
}
