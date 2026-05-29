package qualification

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

func TestSyntheticCTIsImmutableAndLoadable(t *testing.T) {
	fixture, err := NewSyntheticCT([3]uint32{8, 6, 4}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.Verify(); err != nil {
		t.Fatal(err)
	}
	if got, want := fixture.PayloadSHA256(), "9c744dad5a47c1159216bbfdcc4e96b9e7c0303a20f1f588db624fd68bb1a2cc"; got != want {
		t.Fatalf("payload SHA-256 = %s, want %s", got, want)
	}
	first := fixture.CopyPayload()
	first[0] ^= 0xff
	if bytes.Equal(first, fixture.CopyPayload()) {
		t.Fatal("CopyPayload leaked a mutable alias")
	}
	descriptor := fixture.Descriptor()
	if descriptor.Dimensions != [3]uint32{8, 6, 4} ||
		descriptor.ScalarFormat != dicomrender.VolumeScalarI16StoredLE {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}

	store := dicomrender.NewVolumeStore()
	lease, err := fixture.Load(store)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	snapshot, err := lease.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.VolumeGeneration != 7 {
		t.Fatalf("source descriptor generation = %d, want requested 7", descriptor.VolumeGeneration)
	}
	if snapshot.Generation() != 1 {
		t.Fatalf("authoritative store generation = %d, want 1", snapshot.Generation())
	}
	if got, ok := snapshot.ModalityAt(7, 5, 3); !ok || got != -957 {
		t.Fatalf("ModalityAt(7,5,3) = %v/%t, want -957/true", got, ok)
	}

	vtk, err := AdaptVTKI16(lease)
	if err != nil {
		t.Fatal(err)
	}
	cpu, err := AdaptCPUCanonicalF32(lease)
	if err != nil {
		t.Fatal(err)
	}
	webGPUTransport, err := AdaptWebGPUR32FloatTransport(lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []VolumeTransport{vtk, cpu, webGPUTransport} {
		if transport.Generation() != 1 {
			t.Fatalf("%s generation = %d, want authoritative 1", transport.Kind(), transport.Generation())
		}
		if err := transport.Verify(); err != nil {
			t.Fatalf("%s: %v", transport.Kind(), err)
		}
	}
	if vtk.Descriptor().ScalarFormat != dicomrender.VolumeScalarI16StoredLE ||
		vtk.PayloadSHA256() != fixture.PayloadSHA256() {
		t.Fatalf("VTK transport drifted from I16 source: %+v", vtk.Descriptor())
	}
	const expectedF32SHA256 = "e3063a489afd554005c6daa07ff57f7ffb3fc2319d59dd268651162baa8e4b44"
	if cpu.PayloadSHA256() != expectedF32SHA256 {
		t.Fatalf("CPU F32 SHA-256 = %s, want %s", cpu.PayloadSHA256(), expectedF32SHA256)
	}
	if webGPUTransport.PayloadSHA256() != cpu.PayloadSHA256() ||
		!bytes.Equal(webGPUTransport.CopyPayload(), cpu.CopyPayload()) {
		t.Fatal("WebGPU R32Float transport drifted from authoritative CPU F32 conversion")
	}
	f32 := cpu.CopyPayload()
	for z := uint32(0); z < 4; z++ {
		for y := uint32(0); y < 6; y++ {
			for x := uint32(0); x < 8; x++ {
				index := int((z*6*8 + y*8 + x) * 4)
				got := math.Float32frombits(binary.LittleEndian.Uint32(f32[index : index+4]))
				want, ok := snapshot.ModalityAt(x, y, z)
				if !ok || got != float32(want) {
					t.Fatalf("F32 voxel %d,%d,%d = %g, want %g/%t", x, y, z, got, want, ok)
				}
			}
		}
	}
	capability := WebGPUVolumeRendererCapability(true, "")
	if err := capability.Validate(); err != nil {
		t.Fatal(err)
	}
	if capability.Backend != "webgpu-wgpu-native-worker" ||
		capability.Feature != "volume-renderer" ||
		capability.Status != CapabilityAvailable ||
		capability.Reason != "" {
		t.Fatalf("WebGPU renderer capability = %+v, want accepted real worker", capability)
	}

	unboundTrace := positiveEvidenceBundle().Traces[1]
	unboundTrace.Generation.Volume = 7
	if _, err := BindTraceToLease(unboundTrace, lease); err == nil {
		t.Fatal("source generation 7 was silently accepted after store assigned generation 1")
	}
	unboundTrace.Generation.Volume = 0
	boundTrace, err := BindTraceToLease(unboundTrace, lease)
	if err != nil {
		t.Fatal(err)
	}
	if boundTrace.Generation.Volume != 1 {
		t.Fatalf("bound trace generation = %d, want authoritative 1", boundTrace.Generation.Volume)
	}
	unboundResource := positiveEvidenceBundle().Resources[1]
	unboundResource.Generation.Volume = 7
	if _, err := BindResourceSampleToLease(unboundResource, lease); err == nil {
		t.Fatal("resource source generation 7 was silently accepted after store assigned generation 1")
	}
	unboundResource.Generation.Volume = 0
	boundResource, err := BindResourceSampleToLease(unboundResource, lease)
	if err != nil {
		t.Fatal(err)
	}
	if boundResource.Generation.Volume != 1 {
		t.Fatalf("bound resource generation = %d, want authoritative 1", boundResource.Generation.Volume)
	}
}

func TestReferenceSyntheticCTHashIsFrozen(t *testing.T) {
	if _, err := NewReferenceSyntheticCT(1); err != nil {
		t.Fatal(err)
	}
	dimensions := ReferenceSyntheticCTDimensions()
	dimensions[0] = 1
	if ReferenceSyntheticCTDimensions()[0] != 512 {
		t.Fatal("reference dimensions leaked a mutable alias")
	}
}

func TestEvidenceJSONLIsDeterministicAndUnavailableIsExplicit(t *testing.T) {
	trace := TraceV1{
		Schema: TraceV1Schema, RunID: "run-001", Backend: "cpu",
		Sequence: "mpr", StepID: "mpr-01-axial", Operation: "mpr",
		Phase: "render", Outcome: "pass",
		Generation: Generation{Volume: 1, View: 1, Presentation: 1},
		SequenceNo: 1, TimestampNS: 1000, DurationNS: AvailableU64(250),
		FrameSHA256: strings.Repeat("a", 64),
	}
	var first, second bytes.Buffer
	if err := WriteTraceJSONL(&first, []TraceV1{trace}); err != nil {
		t.Fatal(err)
	}
	if err := WriteTraceJSONL(&second, []TraceV1{trace}); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() || !strings.HasSuffix(first.String(), "\n") {
		t.Fatalf("trace JSONL is not deterministic: %q / %q", first.String(), second.String())
	}

	unavailable := UnavailableU64("driver does not expose this counter")
	sample := ResourceSampleV1{
		Schema: ResourceSampleV1Schema, RunID: "run-001", Backend: "cpu",
		Sequence: "mpr", StepID: "mpr-01-axial",
		Generation: Generation{Volume: 1, View: 1, Presentation: 1},
		SequenceNo: 1, TimestampNS: 1000,
		PrivateBytes: unavailable, RSSBytes: AvailableU64(0),
		HeapBytes: AvailableU64(1024), GPUOwnedBytes: unavailable,
		CopiedBytes: AvailableU64(2048), PackageBytes: unavailable,
		Handles: unavailable, Threads: unavailable, ChildProcesses: AvailableU64(0),
		Goroutines: AvailableU64(3), Mappings: unavailable,
		VolumeUploads: AvailableU64(1), PipelineBuilds: AvailableU64(1),
		Renders: AvailableU64(1), Readbacks: unavailable,
	}
	var encoded bytes.Buffer
	if err := WriteResourceSampleJSONL(&encoded, []ResourceSampleV1{sample}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(encoded.Bytes()), &raw); err != nil {
		t.Fatal(err)
	}
	gpu := raw["gpu_owned_bytes"].(map[string]any)
	if gpu["status"] != "unavailable" || gpu["reason"] == "" {
		t.Fatalf("unavailable metric lost reason: %#v", gpu)
	}
	if _, exists := gpu["value"]; exists {
		t.Fatalf("unavailable metric fabricated a value: %#v", gpu)
	}
	rss := raw["rss_bytes"].(map[string]any)
	if rss["status"] != "available" || rss["value"] != float64(0) {
		t.Fatalf("observed zero was not preserved as available: %#v", rss)
	}
}

func TestEvidenceBundleJoinAndGoldenJSONL(t *testing.T) {
	bundle := positiveEvidenceBundle()
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	var traces, resources bytes.Buffer
	if err := WriteEvidenceBundleJSONL(&traces, &resources, bundle); err != nil {
		t.Fatal(err)
	}
	traceSHA := fmt.Sprintf("%x", sha256.Sum256(traces.Bytes()))
	resourceSHA := fmt.Sprintf("%x", sha256.Sum256(resources.Bytes()))
	const (
		expectedTraceSHA    = "fe3132bcc83d21753f62e4269e3773d4efb1a7ffe7e86f578b3704f26752496e"
		expectedResourceSHA = "004d27e7f32f20f7045ad4b00d00aec32088395a207c0e4018f22e09713afd3c"
	)
	if traceSHA != expectedTraceSHA || resourceSHA != expectedResourceSHA {
		t.Fatalf(
			"golden JSONL SHA trace/resource = %s/%s, want %s/%s\ntrace=%s\nresource=%s",
			traceSHA, resourceSHA, expectedTraceSHA, expectedResourceSHA,
			traces.String(), resources.String(),
		)
	}
}

func TestEvidenceBundleRejectsIdentityOrderAndGenerationDrift(t *testing.T) {
	tests := map[string]func(*EvidenceBundleV1){
		"run id": func(bundle *EvidenceBundleV1) {
			bundle.Resources[0].RunID = "different"
		},
		"backend": func(bundle *EvidenceBundleV1) {
			bundle.Resources[0].Backend = "vtk"
		},
		"sequence": func(bundle *EvidenceBundleV1) {
			bundle.Resources[0].Sequence = "vr"
		},
		"step": func(bundle *EvidenceBundleV1) {
			bundle.Resources[0].StepID = "different"
		},
		"generation tuple": func(bundle *EvidenceBundleV1) {
			bundle.Resources[1].Generation.View++
		},
		"resource sequence number": func(bundle *EvidenceBundleV1) {
			bundle.Resources[1].SequenceNo = 3
		},
		"duplicate sequence number": func(bundle *EvidenceBundleV1) {
			bundle.Traces[1].SequenceNo = 1
			bundle.Resources[1].SequenceNo = 1
		},
		"out of order": func(bundle *EvidenceBundleV1) {
			bundle.Traces[0].SequenceNo = 3
			bundle.Resources[0].SequenceNo = 3
		},
		"2D claims view": func(bundle *EvidenceBundleV1) {
			bundle.Traces[0].Generation.View = 1
			bundle.Resources[0].Generation.View = 1
		},
		"MPR render lacks view": func(bundle *EvidenceBundleV1) {
			bundle.Traces[1].Generation.View = 0
			bundle.Resources[1].Generation.View = 0
		},
		"MPR render lacks presentation": func(bundle *EvidenceBundleV1) {
			bundle.Traces[1].Generation.Presentation = 0
			bundle.Resources[1].Generation.Presentation = 0
		},
		"VR publish lacks view": func(bundle *EvidenceBundleV1) {
			bundle.Traces[2].Generation.View = 0
			bundle.Resources[2].Generation.View = 0
		},
		"implicit resource metric": func(bundle *EvidenceBundleV1) {
			bundle.Resources[0].GPUOwnedBytes = U64Measurement{}
		},
		"missing resource": func(bundle *EvidenceBundleV1) {
			bundle.Resources = bundle.Resources[:1]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			bundle := positiveEvidenceBundle()
			mutate(&bundle)
			if err := bundle.Validate(); err == nil {
				t.Fatal("mutated evidence bundle unexpectedly passed")
			}
		})
	}
}

func TestJSONLWritersRejectDeterministicShortWrites(t *testing.T) {
	bundle := positiveEvidenceBundle()
	t.Run("trace individual", func(t *testing.T) {
		if err := WriteTraceJSONL(deterministicShortWriter{}, bundle.Traces); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("WriteTraceJSONL error = %v, want io.ErrShortWrite", err)
		}
	})
	t.Run("resource individual", func(t *testing.T) {
		if err := WriteResourceSampleJSONL(
			deterministicShortWriter{},
			bundle.Resources,
		); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("WriteResourceSampleJSONL error = %v, want io.ErrShortWrite", err)
		}
	})
	t.Run("bundle trace destination", func(t *testing.T) {
		var resources bytes.Buffer
		if err := WriteEvidenceBundleJSONL(
			deterministicShortWriter{},
			&resources,
			bundle,
		); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("WriteEvidenceBundleJSONL error = %v, want io.ErrShortWrite", err)
		}
		if resources.Len() != 0 {
			t.Fatal("resource destination was written after trace destination failed")
		}
	})
	t.Run("bundle resource destination", func(t *testing.T) {
		var traces bytes.Buffer
		if err := WriteEvidenceBundleJSONL(
			&traces,
			deterministicShortWriter{},
			bundle,
		); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("WriteEvidenceBundleJSONL error = %v, want io.ErrShortWrite", err)
		}
		if traces.Len() == 0 {
			t.Fatal("trace destination was not written before resource destination failed")
		}
	})
	t.Run("real writer error is preserved", func(t *testing.T) {
		sentinel := errors.New("deterministic writer failure")
		if err := WriteTraceJSONL(
			deterministicErrorWriter{err: sentinel},
			bundle.Traces,
		); !errors.Is(err, sentinel) {
			t.Fatalf("WriteTraceJSONL error = %v, want sentinel", err)
		}
	})
}

func TestIndividualEvidenceWritersValidateBeforePublishing(t *testing.T) {
	bundle := positiveEvidenceBundle()
	bundle.Traces[1].Schema = "invalid"
	var traces bytes.Buffer
	if err := WriteTraceJSONL(&traces, bundle.Traces); err == nil {
		t.Fatal("invalid trace stream unexpectedly passed")
	}
	if traces.Len() != 0 {
		t.Fatal("invalid trace stream was partially written")
	}

	bundle = positiveEvidenceBundle()
	bundle.Resources[1].Schema = "invalid"
	var resources bytes.Buffer
	if err := WriteResourceSampleJSONL(&resources, bundle.Resources); err == nil {
		t.Fatal("invalid resource stream unexpectedly passed")
	}
	if resources.Len() != 0 {
		t.Fatal("invalid resource stream was partially written")
	}
	if err := WriteTraceJSONL(io.Discard, nil); err == nil {
		t.Fatal("empty trace stream unexpectedly passed")
	}
	if err := WriteResourceSampleJSONL(io.Discard, nil); err == nil {
		t.Fatal("empty resource stream unexpectedly passed")
	}
}

func TestEvidenceRejectsImplicitOrAmbiguousMeasurements(t *testing.T) {
	for _, metric := range []U64Measurement{
		{},
		{Status: "available"},
		{Status: "unavailable"},
		{Status: "unavailable", Value: uint64Pointer(0), Reason: "not measured"},
		{Status: "available", Value: uint64Pointer(0), Reason: "ambiguous"},
	} {
		if err := metric.Validate(); err == nil {
			t.Fatalf("Validate(%+v) unexpectedly passed", metric)
		}
	}
}

func TestTransportAdaptersRejectWrongAuthorityOrFormat(t *testing.T) {
	if _, err := AdaptVTKI16(nil); err == nil {
		t.Fatal("nil VTK lease unexpectedly passed")
	}
	store := dicomrender.NewVolumeStore()
	fixture, err := NewSyntheticCT([3]uint32{2, 2, 2}, 9)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.Load(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := AdaptCPUCanonicalF32(lease); err == nil {
		t.Fatal("released authoritative lease unexpectedly passed")
	}

	f32Store := dicomrender.NewVolumeStore()
	descriptor := fixture.Descriptor()
	descriptor.Dimensions = [3]uint32{2, 2, 2}
	descriptor.RowStrideBytes = 8
	descriptor.SliceStrideBytes = 16
	descriptor.ByteLength = 32
	descriptor.SpacingMM = [3]float64{0.7, 0.7, 1}
	generation, err := f32Store.ReplaceFloat32(descriptor, make([]float32, 8))
	if err != nil {
		t.Fatal(err)
	}
	f32Lease, err := f32Store.Acquire(generation)
	if err != nil {
		t.Fatal(err)
	}
	defer f32Lease.Release()
	if _, err := AdaptVTKI16(f32Lease); err == nil {
		t.Fatal("VTK I16 adapter accepted F32 source")
	}
}

func TestFrozenSequencesAreValidDeterministicCopies(t *testing.T) {
	for name, build := range map[string]func() ([]StateStep, error){
		"mpr": FrozenMPRSequence, "vr": FrozenVRSequence, "mixed": FrozenMixedSequence,
	} {
		sequence, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := ValidateStateSequence(sequence); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	first, err := FrozenVRSequence()
	if err != nil {
		t.Fatal(err)
	}
	second, err := FrozenVRSequence()
	if err != nil {
		t.Fatal(err)
	}
	first[0].View.VR.TransferLUT.Samples[0].A = 1
	if second[0].View.VR.TransferLUT.Samples[0].A != 0 {
		t.Fatal("frozen sequence leaked a mutable LUT alias")
	}
	mixed, err := FrozenMixedSequence()
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed) < 8 {
		t.Fatalf("frozen mixed sequence shortened to %d steps; want at least 8", len(mixed))
	}
	if mixed[6].VolumeGeneration != 2 || mixed[7].VolumeGeneration != 2 {
		t.Fatalf("replacement generation not frozen: %+v %+v", mixed[6], mixed[7])
	}
}

func TestUnitCrossRejectsDegenerateInputs(t *testing.T) {
	if _, err := unitCross([3]float64{1, 0, 0}, [3]float64{2, 0, 0}); err == nil {
		t.Fatal("parallel vectors unexpectedly produced a unit cross product")
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

type deterministicShortWriter struct{}

func (deterministicShortWriter) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	return len(payload) - 1, nil
}

type deterministicErrorWriter struct {
	err error
}

func (writer deterministicErrorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func positiveEvidenceBundle() EvidenceBundleV1 {
	unavailable := UnavailableU64("counter is not exposed by this backend")
	resource := func(
		sequenceNo uint64,
		sequence, step string,
		generation Generation,
	) ResourceSampleV1 {
		return ResourceSampleV1{
			Schema: ResourceSampleV1Schema, RunID: "run-001", Backend: "cpu",
			Sequence: sequence, StepID: step, Generation: generation,
			SequenceNo: sequenceNo, TimestampNS: int64(sequenceNo) * 1000,
			PrivateBytes: unavailable, RSSBytes: AvailableU64(4096),
			HeapBytes: AvailableU64(2048), GPUOwnedBytes: unavailable,
			CopiedBytes: AvailableU64(384), PackageBytes: unavailable,
			Handles: unavailable, Threads: unavailable,
			ChildProcesses: AvailableU64(0), Goroutines: AvailableU64(3),
			Mappings: unavailable, VolumeUploads: AvailableU64(1),
			PipelineBuilds: AvailableU64(1), Renders: AvailableU64(1),
			Readbacks: unavailable,
		}
	}
	twoDGeneration := Generation{Volume: 1}
	mprGeneration := Generation{Volume: 1, View: 11, Presentation: 3}
	vrGeneration := Generation{Volume: 1, View: 22, Presentation: 4}
	return EvidenceBundleV1{
		Traces: []TraceV1{
			{
				Schema: TraceV1Schema, RunID: "run-001", Backend: "cpu",
				Sequence: "mixed", StepID: "mixed-01-2d-center",
				Operation: "2d", Phase: "publish", Outcome: "pass",
				Generation: twoDGeneration, SequenceNo: 1, TimestampNS: 1000,
				DurationNS:  AvailableU64(100),
				FrameSHA256: strings.Repeat("1", 64),
			},
			{
				Schema: TraceV1Schema, RunID: "run-001", Backend: "cpu",
				Sequence: "mixed", StepID: "mixed-02-mpr-axial",
				Operation: "mpr", Phase: "render", Outcome: "pass",
				Generation: mprGeneration, SequenceNo: 2, TimestampNS: 2000,
				DurationNS:  AvailableU64(250),
				FrameSHA256: strings.Repeat("2", 64),
			},
			{
				Schema: TraceV1Schema, RunID: "run-001", Backend: "cpu",
				Sequence: "mixed", StepID: "mixed-03-vr-fit",
				Operation: "vr", Phase: "publish", Outcome: "pass",
				Generation: vrGeneration, SequenceNo: 3, TimestampNS: 3000,
				DurationNS:  AvailableU64(500),
				FrameSHA256: strings.Repeat("3", 64),
			},
		},
		Resources: []ResourceSampleV1{
			resource(1, "mixed", "mixed-01-2d-center", twoDGeneration),
			resource(2, "mixed", "mixed-02-mpr-axial", mprGeneration),
			resource(3, "mixed", "mixed-03-vr-fit", vrGeneration),
		},
	}
}
