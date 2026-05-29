// Package waveform reads and queries calibrated, channel-multiplexed DICOM
// waveforms. It deliberately exposes recorded samples and annotations without
// diagnostic interpretation or inferred channels.
package waveform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	// ErrNotWaveform reports that the SOP Class is not a waveform storage class.
	ErrNotWaveform = errors.New("waveform: not a waveform storage SOP class")
	// ErrUnsupported reports a known waveform whose samples are intentionally
	// exposed only through raw-fallback metadata.
	ErrUnsupported = errors.New("waveform: unsupported waveform")
	// ErrClosed reports use of a Recording after Close.
	ErrClosed = errors.New("waveform: recording is closed")
)

const (
	defaultMaxIndexEntries = 1_000_000
	defaultMaxWidth        = 16_384
	defaultMaxGroups       = 256
	defaultMaxChannels     = 1_024
)

// Options bounds index memory and individual rendering queries.
type Options struct {
	// MaxIndexEntries is the global maximum number of min/max buckets retained
	// by this Recording across every group, channel, and resolution level.
	MaxIndexEntries int
	// MaxEnvelopeWidth is the maximum accepted viewport width.
	MaxEnvelopeWidth int
	// MaxGroups limits Waveform Sequence items accepted by Open.
	MaxGroups int
	// MaxChannelsPerGroup limits channels in one multiplex group.
	MaxChannelsPerGroup int
	// SourceFactory optionally supplies an owned deferred source for each
	// Waveform Data element. The default source reads the element's raw bytes.
	SourceFactory SourceFactory
}

// SampleSource is an owned random-access Waveform Data source. Size must remain
// stable until Close. Recording.Close closes every source exactly once.
type SampleSource interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// SourceFactory opens the Waveform Data source for one zero-based multiplex
// group. It may resolve an element whose value was intentionally deferred.
type SourceFactory func(groupIndex int, waveformData core.Element) (SampleSource, error)

// CalibrationStatus states whether physical-unit conversion is safe.
type CalibrationStatus uint8

const (
	CalibrationMissingSensitivity CalibrationStatus = iota
	CalibrationMissingUnits
	CalibrationMissingParameters
	CalibrationComplete
)

func (s CalibrationStatus) String() string {
	switch s {
	case CalibrationMissingSensitivity:
		return "missing sensitivity"
	case CalibrationMissingUnits:
		return "missing units"
	case CalibrationMissingParameters:
		return "missing correction factor or baseline"
	case CalibrationComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// CodedConcept preserves a DICOM coded entry without interpreting it.
type CodedConcept struct {
	Value   string
	Scheme  string
	Version string
	Meaning string
}

// Calibration contains the recorded channel conversion parameters. Values are
// converted only when Status is CalibrationComplete; Has fields preserve which
// parameters were actually encoded.
type Calibration struct {
	Status              CalibrationStatus
	Units               CodedConcept
	Sensitivity         float64
	CorrectionFactor    float64
	Baseline            float64
	HasSensitivity      bool
	HasCorrectionFactor bool
	HasBaseline         bool
}

// ChannelInfo is immutable parsed channel metadata.
type ChannelInfo struct {
	Index          int
	Number         int
	Label          string
	Status         []string
	Source         CodedConcept
	Calibration    Calibration
	TimeSkew       time.Duration
	SampleSkew     float64
	ChannelOffset  time.Duration
	BitsStored     int
	LowFrequency   float64
	HighFrequency  float64
	NotchFrequency float64
}

// GroupInfo describes one Waveform Sequence item.
type GroupInfo struct {
	Index                int
	Label                string
	UID                  string
	Originality          string
	TimeOffset           time.Duration
	SamplingFrequencyHz  float64
	SampleCount          int64
	Duration             time.Duration
	BitsAllocated        int
	SampleInterpretation string
	Channels             []ChannelInfo
	Supported            bool
	FallbackReason       string
	RawDataBytes         int64
}

// ChannelReference is the one-based (multiplex group, channel) pair encoded by
// Referenced Waveform Channels. ChannelNumber zero means all channels.
type ChannelReference struct {
	GroupNumber   int
	ChannelNumber int
}

// Annotation preserves recorded temporal and coded/textual annotation data.
// ReferencedSamplePositions remain one-based as encoded by DICOM.
type Annotation struct {
	AnnotationGroupNumber     int
	TemporalRangeType         string
	Channels                  []ChannelReference
	ReferencedSamplePositions []uint64
	ReferencedTimeOffsets     []time.Duration
	ReferencedDateTimes       []string
	ConceptName               CodedConcept
	ConceptValue              CodedConcept
	NumericValues             []float64
	NumericUnits              CodedConcept
	Text                      string
}

// RawFallback describes why decoded rendering is unavailable while retaining
// enough metadata for an explicit raw-data UI.
type RawFallback struct {
	SOPClassUID string
	GroupIndex  int
	Encoding    string
	RawBytes    int64
	Reason      string
}

// UnsupportedError carries raw-fallback metadata and unwraps to ErrUnsupported.
type UnsupportedError struct {
	Fallback RawFallback
}

func (e *UnsupportedError) Error() string {
	if e == nil {
		return ErrUnsupported.Error()
	}
	if e.Fallback.Reason == "" {
		return ErrUnsupported.Error()
	}
	return fmt.Sprintf("%s: %s", ErrUnsupported, e.Fallback.Reason)
}

func (e *UnsupportedError) Unwrap() error { return ErrUnsupported }

// Sample is one raw or calibrated waveform value. Time is relative to the
// multiplex group start and includes encoded channel skew and offset.
type Sample struct {
	SampleIndex int64
	Time        time.Duration
	Raw         float64
	Value       float64
	Valid       bool
	Calibrated  bool
}

// EnvelopeBucket preserves the extrema in one aligned index bucket. The
// extrema carry their original sample positions, so narrow spikes are not
// averaged away. StartSample is inclusive and EndSample is exclusive.
type EnvelopeBucket struct {
	StartSample int64
	EndSample   int64
	Min         Sample
	Max         Sample
	Valid       bool
}

// ChannelEnvelope is the bounded envelope for one selected channel.
type ChannelEnvelope struct {
	Channel ChannelInfo
	Buckets []EnvelopeBucket
}

// Recording owns deferred sample sources and lazy indexes.
type Recording struct {
	mu          sync.RWMutex
	closeOnce   sync.Once
	closeCh     chan struct{}
	closed      bool
	sopClassUID string
	groups      []*group
	annotations []Annotation
	options     normalizedOptions
}

type normalizedOptions struct {
	maxIndexEntries int
	maxWidth        int
	maxGroups       int
	maxChannels     int
	sourceFactory   SourceFactory
}

type group struct {
	info        GroupInfo
	source      SampleSource
	decoder     sampleDecoder
	paddingBits []uint64
	hasPadding  bool

	indexMu       sync.Mutex
	indexBuilding bool
	indexReady    chan struct{}
	index         *multiIndex
	indexErr      error
	indexBudget   int
}

// SOPClassUID returns the source SOP Class UID.
func (r *Recording) SOPClassUID() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sopClassUID
}

// Groups returns a deep metadata copy in encoded multiplex-group order.
func (r *Recording) Groups() []GroupInfo {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]GroupInfo, len(r.groups))
	for i, group := range r.groups {
		out[i] = cloneGroupInfo(group.info)
	}
	return out
}

// Annotations returns a deep copy without deriving or interpreting content.
func (r *Recording) Annotations() []Annotation {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneAnnotations(r.annotations)
}

// BuildIndex eagerly builds all supported group indexes. Envelope also builds
// the relevant group lazily when this method is not called.
func (r *Recording) BuildIndex(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("waveform: nil recording")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrClosed
	}
	if err := operationErr(ctx, r.closeCh); err != nil {
		return err
	}
	for _, group := range r.groups {
		if !group.info.Supported {
			continue
		}
		if err := group.ensureIndex(ctx, r.closeCh); err != nil {
			return err
		}
	}
	return nil
}

// Close prevents future reads and releases all sample sources. It is
// idempotent and starts no background goroutines.
func (r *Recording) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.closeCh != nil {
			close(r.closeCh)
		}
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	for _, group := range r.groups {
		if group.source != nil {
			if err := group.source.Close(); err != nil {
				errs = append(errs, err)
			}
			group.source = nil
		}
	}
	return errors.Join(errs...)
}

func cloneGroupInfo(info GroupInfo) GroupInfo {
	out := info
	out.Channels = make([]ChannelInfo, len(info.Channels))
	for i := range info.Channels {
		out.Channels[i] = info.Channels[i]
		out.Channels[i].Status = append([]string(nil), info.Channels[i].Status...)
	}
	return out
}

func cloneAnnotations(in []Annotation) []Annotation {
	out := make([]Annotation, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Channels = append([]ChannelReference(nil), in[i].Channels...)
		out[i].ReferencedSamplePositions = append([]uint64(nil), in[i].ReferencedSamplePositions...)
		out[i].ReferencedTimeOffsets = append([]time.Duration(nil), in[i].ReferencedTimeOffsets...)
		out[i].ReferencedDateTimes = append([]string(nil), in[i].ReferencedDateTimes...)
		out[i].NumericValues = append([]float64(nil), in[i].NumericValues...)
	}
	return out
}

// Open parses waveform metadata and retains deferred sample sources.
func Open(file *object.File, opts Options) (*Recording, error) {
	return open(file, opts)
}

// Read is an alias for Open.
func Read(file *object.File, opts Options) (*Recording, error) {
	return Open(file, opts)
}
