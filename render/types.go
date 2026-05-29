package render

import (
	"encoding/binary"
	"fmt"
	"image"
	"sync"

	"github.com/ThalesMMS/dicom-go/dynamic"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

const (
	defaultWindowCenter = 40.0
	defaultWindowWidth  = 400.0
)

// WindowLevel is a DICOM window center/width pair used for display rendering.
type WindowLevel struct {
	Center   float64
	Width    float64
	Function display.VOIFunction
	LUT      *display.LUT
}

// Rescale applies DICOM modality LUT slope/intercept to stored pixel values.
type Rescale struct {
	Slope     float64
	Intercept float64
}

// Frame is the headless render input for one decoded DICOM image frame.
type Frame struct {
	SOPInstanceUID string
	FrameIndex     int

	Metadata pixeldata.Metadata

	TransferSyntaxUID  string
	TransferSyntaxName string
	ByteOrder          binary.ByteOrder
	PixelBytes         []byte

	DefaultWindow WindowLevel
	Rescale       Rescale

	ImagePosition    []float64
	ImageOrientation []float64
	PixelSpacing     []float64
	SliceThickness   float64
	SliceLocation    float64
	SliceLocationOK  bool
	InstanceNumber   int
	Sort             float64
	// Temporal preserves the frame's independent temporal/spatial identity.
	// Geometry guardrails use it to reject an unsplit 4D acquisition before it
	// can be mistaken for one 3D volume.
	Temporal dynamic.FrameMetadata

	DecodeErr    error
	Encapsulated bool
}

// Stack is the headless render input for a sorted frame stack.
type Stack struct {
	UID       string
	StudyUID  string
	Modality  string
	BodyPart  string
	Thumbnail image.Image

	DefaultWindow WindowLevel

	PixelSpacing   []float64
	SliceThickness float64
	Frames         []*Frame

	volumeMu     sync.Mutex
	volumeVoxels *volumeVoxels
	mprVolume    *Volume
	volumeClosed bool
	volumeStore  *VolumeStore
}

// SetThumbnail updates presentation-only preview state without invalidating or
// regenerating clinical voxel data.
func (s *Stack) SetThumbnail(thumbnail image.Image) {
	if s == nil {
		return
	}
	s.volumeMu.Lock()
	s.Thumbnail = thumbnail
	s.volumeMu.Unlock()
}

// ThumbnailImage returns the presentation-only preview under the stack lock.
func (s *Stack) ThumbnailImage() image.Image {
	if s == nil {
		return nil
	}
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	return s.Thumbnail
}

// Close releases normalized/regularized volume generations owned by this
// stack. Source Frame.PixelBytes remain caller-owned so the independent 2D
// path can define its own study lifecycle.
func (s *Stack) Close() error {
	if s == nil {
		return nil
	}
	s.volumeMu.Lock()
	volume := s.mprVolume
	store := s.volumeStore
	s.mprVolume = nil
	s.volumeVoxels = nil
	s.volumeClosed = true
	s.volumeMu.Unlock()
	if volume != nil {
		return volume.Close()
	}
	if store != nil {
		return store.Close()
	}
	return nil
}

// VolumeStoreStats reports canonical volume residency without building a
// volume when one has not yet been requested.
func (s *Stack) VolumeStoreStats() VolumeStoreStats {
	if s == nil {
		return VolumeStoreStats{}
	}
	s.volumeMu.Lock()
	volume := s.mprVolume
	store := s.volumeStore
	s.volumeMu.Unlock()
	if volume == nil {
		if store == nil {
			return VolumeStoreStats{}
		}
		return store.Stats()
	}
	return volume.VolumeStoreStats()
}

// SetVolumeStore transfers a caller-supplied store to the Stack before the
// first volume build. Stack.Close closes it. This is the integration point for
// a study-level hard memory budget.
func (s *Stack) SetVolumeStore(store *VolumeStore) error {
	if s == nil || store == nil {
		return fmt.Errorf("%w: nil volume store", ErrInvalidVolumeSnapshot)
	}
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	if s.volumeClosed {
		return ErrVolumeStoreClosed
	}
	if s.mprVolume != nil || s.volumeStore != nil {
		return ErrVolumeInUse
	}
	s.volumeStore = store
	return nil
}

// FirstDisplayFrame returns the first frame that can be displayed.
func (s *Stack) FirstDisplayFrame() *Frame {
	if s == nil {
		return nil
	}
	for _, frame := range s.Frames {
		if frame != nil && frame.DecodeErr == nil && !frame.Encapsulated {
			return frame
		}
	}
	return nil
}

// Displayable reports whether the stack has at least one renderable frame.
func (s *Stack) Displayable() bool {
	return s.FirstDisplayFrame() != nil
}
