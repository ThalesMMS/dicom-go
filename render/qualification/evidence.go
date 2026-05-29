package qualification

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

const (
	TraceV1Schema          = "trace-v1"
	ResourceSampleV1Schema = "resource-sample-v1"
)

var ErrInvalidEvidence = errors.New("qualification: invalid evidence")

// Generation identifies the clinical and presentation state associated with
// one trace or resource sample.
type Generation struct {
	Volume       uint64 `json:"volume"`
	View         uint64 `json:"view"`
	Presentation uint64 `json:"presentation"`
}

// U64Measurement carries either an observed uint64 or an explicit reason that
// the metric is unavailable. An unavailable metric can never silently become
// a fabricated zero.
type U64Measurement struct {
	Status string  `json:"status"`
	Value  *uint64 `json:"value,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

func AvailableU64(value uint64) U64Measurement {
	return U64Measurement{Status: "available", Value: &value}
}

func UnavailableU64(reason string) U64Measurement {
	return U64Measurement{Status: "unavailable", Reason: strings.TrimSpace(reason)}
}

func (m U64Measurement) Validate() error {
	switch m.Status {
	case "available":
		if m.Value == nil || m.Reason != "" {
			return fmt.Errorf("%w: available metric must contain only a value", ErrInvalidEvidence)
		}
	case "unavailable":
		if m.Value != nil || strings.TrimSpace(m.Reason) == "" {
			return fmt.Errorf("%w: unavailable metric requires a reason and no value", ErrInvalidEvidence)
		}
	default:
		return fmt.Errorf("%w: metric status %q", ErrInvalidEvidence, m.Status)
	}
	return nil
}

// TraceV1 is one backend-neutral state transition or rendering observation.
// It intentionally contains no patient/study/series identifiers.
type TraceV1 struct {
	Schema      string         `json:"schema"`
	RunID       string         `json:"run_id"`
	Backend     string         `json:"backend"`
	Sequence    string         `json:"sequence"`
	StepID      string         `json:"step_id"`
	Operation   string         `json:"operation"`
	Phase       string         `json:"phase"`
	Outcome     string         `json:"outcome"`
	Generation  Generation     `json:"generation"`
	SequenceNo  uint64         `json:"sequence_no"`
	TimestampNS int64          `json:"timestamp_ns"`
	DurationNS  U64Measurement `json:"duration_ns"`
	FrameSHA256 string         `json:"frame_sha256,omitempty"`
}

func (record TraceV1) Validate() error {
	if record.Schema != TraceV1Schema {
		return fmt.Errorf("%w: trace schema %q", ErrInvalidEvidence, record.Schema)
	}
	for field, value := range map[string]string{
		"run_id": record.RunID, "backend": record.Backend, "sequence": record.Sequence,
		"step_id": record.StepID, "operation": record.Operation,
		"phase": record.Phase, "outcome": record.Outcome,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: empty trace %s", ErrInvalidEvidence, field)
		}
	}
	if record.Generation.Volume == 0 {
		return fmt.Errorf("%w: zero trace volume generation", ErrInvalidEvidence)
	}
	if record.SequenceNo == 0 {
		return fmt.Errorf("%w: zero trace sequence number", ErrInvalidEvidence)
	}
	if err := validateTraceGeneration(record); err != nil {
		return err
	}
	return record.DurationNS.Validate()
}

// ResourceSampleV1 records the same uniform resource vocabulary for every
// backend. Unsupported process/driver counters remain explicitly unavailable.
type ResourceSampleV1 struct {
	Schema      string     `json:"schema"`
	RunID       string     `json:"run_id"`
	Backend     string     `json:"backend"`
	Sequence    string     `json:"sequence"`
	StepID      string     `json:"step_id"`
	Generation  Generation `json:"generation"`
	SequenceNo  uint64     `json:"sequence_no"`
	TimestampNS int64      `json:"timestamp_ns"`

	PrivateBytes   U64Measurement `json:"private_bytes"`
	RSSBytes       U64Measurement `json:"rss_bytes"`
	HeapBytes      U64Measurement `json:"heap_bytes"`
	GPUOwnedBytes  U64Measurement `json:"gpu_owned_bytes"`
	CopiedBytes    U64Measurement `json:"copied_bytes"`
	PackageBytes   U64Measurement `json:"package_bytes"`
	Handles        U64Measurement `json:"handles"`
	Threads        U64Measurement `json:"threads"`
	ChildProcesses U64Measurement `json:"child_processes"`
	Goroutines     U64Measurement `json:"goroutines"`
	Mappings       U64Measurement `json:"mappings"`
	VolumeUploads  U64Measurement `json:"volume_uploads"`
	PipelineBuilds U64Measurement `json:"pipeline_builds"`
	Renders        U64Measurement `json:"renders"`
	Readbacks      U64Measurement `json:"readbacks"`
}

func (sample ResourceSampleV1) Validate() error {
	if sample.Schema != ResourceSampleV1Schema {
		return fmt.Errorf("%w: resource schema %q", ErrInvalidEvidence, sample.Schema)
	}
	for field, value := range map[string]string{
		"run_id": sample.RunID, "backend": sample.Backend,
		"sequence": sample.Sequence, "step_id": sample.StepID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: empty resource %s", ErrInvalidEvidence, field)
		}
	}
	if sample.Generation.Volume == 0 {
		return fmt.Errorf("%w: zero resource volume generation", ErrInvalidEvidence)
	}
	if sample.SequenceNo == 0 {
		return fmt.Errorf("%w: zero resource sequence number", ErrInvalidEvidence)
	}
	metrics := []U64Measurement{
		sample.PrivateBytes, sample.RSSBytes, sample.HeapBytes,
		sample.GPUOwnedBytes, sample.CopiedBytes, sample.PackageBytes,
		sample.Handles, sample.Threads, sample.ChildProcesses,
		sample.Goroutines, sample.Mappings, sample.VolumeUploads,
		sample.PipelineBuilds, sample.Renders, sample.Readbacks,
	}
	for index, metric := range metrics {
		if err := metric.Validate(); err != nil {
			return fmt.Errorf("resource metric %d: %w", index, err)
		}
	}
	return nil
}

func WriteTraceJSONL(dst io.Writer, records []TraceV1) error {
	if dst == nil {
		return fmt.Errorf("%w: nil trace writer", ErrInvalidEvidence)
	}
	if len(records) == 0 {
		return fmt.Errorf("%w: empty trace stream", ErrInvalidEvidence)
	}
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if err := record.Validate(); err != nil {
			return fmt.Errorf("trace record %d: %w", index, err)
		}
		if err := encoder.Encode(record); err != nil {
			return fmt.Errorf("trace record %d: %w", index, err)
		}
	}
	return writeEvidenceText(dst, buffer.String())
}

func WriteResourceSampleJSONL(dst io.Writer, samples []ResourceSampleV1) error {
	if dst == nil {
		return fmt.Errorf("%w: nil resource writer", ErrInvalidEvidence)
	}
	if len(samples) == 0 {
		return fmt.Errorf("%w: empty resource stream", ErrInvalidEvidence)
	}
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for index, sample := range samples {
		if err := sample.Validate(); err != nil {
			return fmt.Errorf("resource sample %d: %w", index, err)
		}
		if err := encoder.Encode(sample); err != nil {
			return fmt.Errorf("resource sample %d: %w", index, err)
		}
	}
	return writeEvidenceText(dst, buffer.String())
}

// EvidenceBundleV1 keeps trace and resource streams independently serializable
// while requiring a one-to-one identity/generation join before either stream
// is accepted as qualification evidence.
type EvidenceBundleV1 struct {
	Traces    []TraceV1
	Resources []ResourceSampleV1
}

func (bundle EvidenceBundleV1) Validate() error {
	if len(bundle.Traces) == 0 || len(bundle.Resources) == 0 {
		return fmt.Errorf("%w: evidence bundle requires trace and resource records", ErrInvalidEvidence)
	}
	if len(bundle.Traces) != len(bundle.Resources) {
		return fmt.Errorf(
			"%w: trace/resource count mismatch %d/%d",
			ErrInvalidEvidence, len(bundle.Traces), len(bundle.Resources),
		)
	}
	var previous uint64
	for index := range bundle.Traces {
		trace := bundle.Traces[index]
		resource := bundle.Resources[index]
		if err := trace.Validate(); err != nil {
			return fmt.Errorf("bundle trace %d: %w", index, err)
		}
		if err := resource.Validate(); err != nil {
			return fmt.Errorf("bundle resource %d: %w", index, err)
		}
		if trace.SequenceNo <= previous {
			return fmt.Errorf(
				"%w: sequence_no %d is duplicate or out of order after %d",
				ErrInvalidEvidence, trace.SequenceNo, previous,
			)
		}
		if resource.SequenceNo != trace.SequenceNo {
			return fmt.Errorf(
				"%w: trace/resource sequence_no mismatch %d/%d",
				ErrInvalidEvidence, trace.SequenceNo, resource.SequenceNo,
			)
		}
		if trace.RunID != resource.RunID ||
			trace.Backend != resource.Backend ||
			trace.Sequence != resource.Sequence ||
			trace.StepID != resource.StepID ||
			trace.Generation != resource.Generation {
			return fmt.Errorf(
				"%w: trace/resource identity mismatch at sequence_no %d",
				ErrInvalidEvidence, trace.SequenceNo,
			)
		}
		previous = trace.SequenceNo
	}
	return nil
}

// WriteEvidenceBundleJSONL validates and buffers the complete join before the
// first write. Callers that need atomic filesystem publication still write each
// returned stream through their normal temporary-file/rename boundary.
func WriteEvidenceBundleJSONL(
	traceDst io.Writer,
	resourceDst io.Writer,
	bundle EvidenceBundleV1,
) error {
	if err := bundle.Validate(); err != nil {
		return err
	}
	var traceBuffer, resourceBuffer strings.Builder
	if err := WriteTraceJSONL(&traceBuffer, bundle.Traces); err != nil {
		return err
	}
	if err := WriteResourceSampleJSONL(&resourceBuffer, bundle.Resources); err != nil {
		return err
	}
	if traceDst == nil || resourceDst == nil {
		return fmt.Errorf("%w: nil evidence bundle writer", ErrInvalidEvidence)
	}
	if err := writeEvidenceText(traceDst, traceBuffer.String()); err != nil {
		return err
	}
	return writeEvidenceText(resourceDst, resourceBuffer.String())
}

func writeEvidenceText(dst io.Writer, text string) error {
	written, err := io.WriteString(dst, text)
	if err != nil {
		return err
	}
	if written != len(text) {
		return io.ErrShortWrite
	}
	return nil
}

func validateTraceGeneration(record TraceV1) error {
	switch record.Operation {
	case "2d":
		if record.Generation.View != 0 || record.Generation.Presentation != 0 {
			return fmt.Errorf(
				"%w: 2D phase %q must not claim view/presentation generations",
				ErrInvalidEvidence, record.Phase,
			)
		}
	case "mpr", "vr":
		if record.Phase == "render" || record.Phase == "publish" {
			if record.Generation.View == 0 || record.Generation.Presentation == 0 {
				return fmt.Errorf(
					"%w: %s %s requires complete view/presentation generations",
					ErrInvalidEvidence, record.Operation, record.Phase,
				)
			}
		} else if (record.Generation.View == 0) != (record.Generation.Presentation == 0) {
			return fmt.Errorf(
				"%w: %s phase %q has partial view/presentation generation",
				ErrInvalidEvidence, record.Operation, record.Phase,
			)
		}
	default:
		return fmt.Errorf("%w: trace operation %q", ErrInvalidEvidence, record.Operation)
	}
	return nil
}

// BindTraceToLease derives the volume generation from the authoritative
// VolumeStore lease. A caller-supplied non-zero generation must match exactly;
// source-fixture generations are never silently rewritten after Load.
func BindTraceToLease(
	record TraceV1,
	lease *dicomrender.VolumeLease,
) (TraceV1, error) {
	_, descriptor, err := authoritativeSnapshot(lease)
	if err != nil {
		return TraceV1{}, err
	}
	if record.Generation.Volume != 0 &&
		record.Generation.Volume != descriptor.VolumeGeneration {
		return TraceV1{}, fmt.Errorf(
			"%w: trace volume generation %d differs from authoritative lease %d",
			ErrInvalidEvidence, record.Generation.Volume, descriptor.VolumeGeneration,
		)
	}
	record.Generation.Volume = descriptor.VolumeGeneration
	if err := record.Validate(); err != nil {
		return TraceV1{}, err
	}
	return record, nil
}

// BindResourceSampleToLease is the resource-stream counterpart of
// BindTraceToLease.
func BindResourceSampleToLease(
	sample ResourceSampleV1,
	lease *dicomrender.VolumeLease,
) (ResourceSampleV1, error) {
	_, descriptor, err := authoritativeSnapshot(lease)
	if err != nil {
		return ResourceSampleV1{}, err
	}
	if sample.Generation.Volume != 0 &&
		sample.Generation.Volume != descriptor.VolumeGeneration {
		return ResourceSampleV1{}, fmt.Errorf(
			"%w: resource volume generation %d differs from authoritative lease %d",
			ErrInvalidEvidence, sample.Generation.Volume, descriptor.VolumeGeneration,
		)
	}
	sample.Generation.Volume = descriptor.VolumeGeneration
	if err := sample.Validate(); err != nil {
		return ResourceSampleV1{}, err
	}
	return sample, nil
}
