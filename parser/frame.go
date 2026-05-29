package parser

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrFrameSink             = errors.New("dicom: frame sink failed")
	ErrMissingFrameMetadata  = errors.New("dicom: missing required frame metadata")
	ErrInvalidFrameMetadata  = errors.New("dicom: invalid frame metadata")
	ErrFrameDataSizeMismatch = errors.New("dicom: native Pixel Data size does not match frame metadata")
	tagFrameSamplesPerPixel  = core.NewTag(0x0028, 0x0002)
	tagFramePhotometric      = core.NewTag(0x0028, 0x0004)
	tagFramePlanarConfig     = core.NewTag(0x0028, 0x0006)
	tagFrameNumberOfFrames   = core.NewTag(0x0028, 0x0008)
	tagFrameRows             = core.NewTag(0x0028, 0x0010)
	tagFrameColumns          = core.NewTag(0x0028, 0x0011)
	tagFrameBitsAllocated    = core.NewTag(0x0028, 0x0100)
	tagFrameBitsStored       = core.NewTag(0x0028, 0x0101)
	tagFrameHighBit          = core.NewTag(0x0028, 0x0102)
	tagFramePixelRep         = core.NewTag(0x0028, 0x0103)
)

// FrameMetadata contains the top-level image metadata needed to split native
// Pixel Data into frames while the parser is reading the value stream.
type FrameMetadata struct {
	Rows                       uint16
	Columns                    uint16
	SamplesPerPixel            uint16
	BitsAllocated              uint16
	BitsStored                 uint16
	HighBit                    uint16
	PixelRepresentation        uint16
	PlanarConfiguration        uint16
	PlanarConfigurationPresent bool
	NumberOfFrames             int
	PhotometricInterpretation  string
}

func (m FrameMetadata) BytesPerSample() int {
	return int((m.BitsAllocated + 7) / 8)
}

func (m FrameMetadata) FrameSize() int64 {
	if strings.EqualFold(strings.TrimSpace(m.PhotometricInterpretation), "YBR_FULL_422") && m.BitsAllocated >= 8 {
		samples := int64(m.Rows) * int64(m.Columns) * 2
		return samples * int64(m.BytesPerSample())
	}
	samples := int64(m.Rows) * int64(m.Columns) * int64(m.SamplesPerPixel)
	if m.BitsAllocated < 8 {
		return (samples*int64(m.BitsAllocated) + 7) / 8
	}
	return samples * int64(m.BytesPerSample())
}

// TotalSize returns zero when the frame count is invalid or the product would
// overflow int64.
func (m FrameMetadata) TotalSize() int64 {
	frameSize := m.FrameSize()
	frames := int64(m.NumberOfFrames)
	if frameSize <= 0 || frames <= 0 || frameSize > math.MaxInt64/frames {
		return 0
	}
	return frameSize * frames
}

// Frame is emitted by a FrameSink for each native Pixel Data frame read by the
// parser. Data is a per-frame buffer owned by the receiver.
type Frame struct {
	Index          int
	Data           []byte
	Metadata       FrameMetadata
	TransferSyntax transfer.Syntax
}

// FrameSink receives streamed native Pixel Data frames. Close is called once
// when high-level reader collection finishes, even if parsing fails.
type FrameSink interface {
	HandleFrame(Frame) error
	Close() error
}

// FrameSinkFunc adapts a function to FrameSink. Close is a no-op.
type FrameSinkFunc func(Frame) error

func (f FrameSinkFunc) HandleFrame(frame Frame) error {
	if f == nil {
		return nil
	}
	return f(frame)
}

func (f FrameSinkFunc) Close() error {
	return nil
}

type frameChannelSink struct {
	ch   chan Frame
	once sync.Once
}

// NewFrameChannelSink returns a sink that sends frames to ch and closes ch when
// parsing finalizes.
func NewFrameChannelSink(ch chan Frame) FrameSink {
	return &frameChannelSink{ch: ch}
}

func (s *frameChannelSink) HandleFrame(frame Frame) error {
	if s == nil || s.ch == nil {
		return fmt.Errorf("%w: nil frame channel", ErrFrameSink)
	}
	s.ch <- frame
	return nil
}

func (s *frameChannelSink) Close() error {
	if s == nil || s.ch == nil {
		return nil
	}
	s.once.Do(func() {
		close(s.ch)
	})
	return nil
}

type frameMetadataState struct {
	metadata FrameMetadata

	rowsSeen           bool
	columnsSeen        bool
	samplesSeen        bool
	bitsAllocatedSeen  bool
	bitsStoredSeen     bool
	highBitSeen        bool
	pixelRepSeen       bool
	numberOfFramesSeen bool
	photometricSeen    bool
}

func (s frameMetadataState) complete() (FrameMetadata, error) {
	var missing []string
	if !s.rowsSeen {
		missing = append(missing, fmt.Sprintf("Rows (%s)", tagFrameRows))
	}
	if !s.columnsSeen {
		missing = append(missing, fmt.Sprintf("Columns (%s)", tagFrameColumns))
	}
	if !s.samplesSeen {
		missing = append(missing, fmt.Sprintf("SamplesPerPixel (%s)", tagFrameSamplesPerPixel))
	}
	if !s.bitsAllocatedSeen {
		missing = append(missing, fmt.Sprintf("BitsAllocated (%s)", tagFrameBitsAllocated))
	}
	if !s.bitsStoredSeen {
		missing = append(missing, fmt.Sprintf("BitsStored (%s)", tagFrameBitsStored))
	}
	if !s.highBitSeen {
		missing = append(missing, fmt.Sprintf("HighBit (%s)", tagFrameHighBit))
	}
	if !s.pixelRepSeen {
		missing = append(missing, fmt.Sprintf("PixelRepresentation (%s)", tagFramePixelRep))
	}
	if !s.photometricSeen {
		missing = append(missing, fmt.Sprintf("PhotometricInterpretation (%s)", tagFramePhotometric))
	}
	if len(missing) > 0 {
		return FrameMetadata{}, fmt.Errorf("%w: %s", ErrMissingFrameMetadata, strings.Join(missing, ", "))
	}

	metadata := s.metadata
	if !s.numberOfFramesSeen {
		metadata.NumberOfFrames = 1
	}
	if metadata.Rows == 0 || metadata.Columns == 0 || metadata.SamplesPerPixel == 0 ||
		metadata.BitsAllocated == 0 || metadata.NumberOfFrames <= 0 || metadata.FrameSize() <= 0 {
		return FrameMetadata{}, fmt.Errorf(
			"%w: rows=%d columns=%d samples_per_pixel=%d bits_allocated=%d number_of_frames=%d",
			ErrInvalidFrameMetadata,
			metadata.Rows,
			metadata.Columns,
			metadata.SamplesPerPixel,
			metadata.BitsAllocated,
			metadata.NumberOfFrames,
		)
	}
	return metadata, nil
}

func (r *Reader) captureFrameMetadata(header core.ElementHeader, data []byte) error {
	if r.frameSink == nil || len(r.seqDelimiters) != 0 {
		return nil
	}

	switch header.Tag {
	case tagFrameRows:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.Rows = value
		r.frameMetadata.rowsSeen = true
	case tagFrameColumns:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.Columns = value
		r.frameMetadata.columnsSeen = true
	case tagFrameSamplesPerPixel:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.SamplesPerPixel = value
		r.frameMetadata.samplesSeen = true
	case tagFrameBitsAllocated:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.BitsAllocated = value
		r.frameMetadata.bitsAllocatedSeen = true
	case tagFrameBitsStored:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.BitsStored = value
		r.frameMetadata.bitsStoredSeen = true
	case tagFrameHighBit:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.HighBit = value
		r.frameMetadata.highBitSeen = true
	case tagFramePixelRep:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.PixelRepresentation = value
		r.frameMetadata.pixelRepSeen = true
	case tagFramePlanarConfig:
		value, err := r.frameMetadataUint16(header, data)
		if err != nil {
			return err
		}
		r.frameMetadata.metadata.PlanarConfiguration = value
		r.frameMetadata.metadata.PlanarConfigurationPresent = true
	case tagFrameNumberOfFrames:
		value := strings.TrimSpace(core.TrimTextValue(header.VR, string(data)))
		if value == "" {
			r.frameMetadata.metadata.NumberOfFrames = 1
			r.frameMetadata.numberOfFramesSeen = true
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 || n > math.MaxInt32 {
			return fmt.Errorf("%w: NumberOfFrames=%q", ErrInvalidFrameMetadata, value)
		}
		r.frameMetadata.metadata.NumberOfFrames = n
		r.frameMetadata.numberOfFramesSeen = true
	case tagFramePhotometric:
		value := strings.TrimSpace(core.TrimTextValue(header.VR, string(data)))
		if value == "" {
			return fmt.Errorf("%w: PhotometricInterpretation is empty", ErrInvalidFrameMetadata)
		}
		r.frameMetadata.metadata.PhotometricInterpretation = value
		r.frameMetadata.photometricSeen = true
	}
	return nil
}

func (r *Reader) frameMetadataUint16(header core.ElementHeader, data []byte) (uint16, error) {
	if len(data) < 2 {
		return 0, fmt.Errorf("%w: %s has %d byte(s)", ErrInvalidFrameMetadata, header.Tag, len(data))
	}
	return frameMetadataByteOrder(r.syntax).Uint16(data[:2]), nil
}

func frameMetadataByteOrder(syntax transfer.Syntax) binary.ByteOrder {
	if syntax.ByteOrder == nil {
		return binary.LittleEndian
	}
	return syntax.ByteOrder
}
