package codeccost

import (
	"context"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const (
	ModeCold         = "cold"
	ModeWarm         = "warm"
	ModeCacheHit     = "cache-hit"
	ModeCacheMiss    = "cache-miss"
	ModeSeriesSwitch = "series-switch"
	ModeCancel       = "cancel"
)

// DecodeRequest is one non-PHI frame decode. Fragment is borrowed and must
// not be retained after Decode returns.
type DecodeRequest struct {
	Codec         string
	Cohort        string
	Mode          string
	Fragment      []byte
	Metadata      pixeldata.Metadata
	WaitAdmission func(context.Context) error
}

// Backend decodes one frame and attributes exclusive stage cost.
type Backend interface {
	Decode(context.Context, DecodeRequest) (FrameResult, error)
}

// FrameResult is one measured decode. It holds pixels for equivalence checks
// but JSON reports omit them.
type FrameResult struct {
	Codec              string        `json:"codec"`
	Backend            string        `json:"backend"`
	Cohort             string        `json:"cohort"`
	Mode               string        `json:"mode"`
	StageRecord        StageRecord   `json:"timing"`
	PixelSHA256        string        `json:"pixelSHA256,omitempty"`
	Equivalence        string        `json:"equivalence,omitempty"`
	Launches           int           `json:"launches"`
	TempFilesCreated   int           `json:"tempFilesCreated"`
	IOBytes            int64         `json:"ioBytes"`
	IOOps              int           `json:"ioOps"`
	AllocBytes         uint64        `json:"allocBytes"`
	HeapInuseBytes     uint64        `json:"heapInuseBytes"`
	ProcessRSSBytes    uint64        `json:"processRSSBytes,omitempty"`
	SubprocessRSSBytes uint64        `json:"subprocessRSSBytes,omitempty"`
	HelperPID          int           `json:"helperPid,omitempty"`
	Cancelled          bool          `json:"cancelled"`
	CacheHit           bool          `json:"cacheHit"`
	Skip               string        `json:"skip,omitempty"`
	Notes              []string      `json:"notes,omitempty"`
	Pixels             []byte        `json:"-"`
	Elapsed            time.Duration `json:"-"`
}

// Stage returns the exclusive span for name, or a zero span.
func (r FrameResult) Stage(stage Stage) Span {
	for _, span := range r.StageRecord.Spans {
		if span.Stage == stage {
			return span
		}
	}
	return Span{Stage: stage}
}
