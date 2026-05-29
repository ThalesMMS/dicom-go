package qualification

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

type TransportKind string

const (
	TransportCPUCanonicalF32 TransportKind = "cpu-canonical-f32le"
	TransportVTKI16          TransportKind = "vtk-i16le"
	// TransportWebGPUR32Float proves only the deterministic upload conversion.
	// It is not evidence that a WebGPU volume renderer exists or rendered.
	TransportWebGPUR32Float TransportKind = "webgpu-r32float-transport"
)

// VolumeTransport is a backend-owned payload derived from an authoritative
// VolumeStore lease. The source descriptor generation is never reused after
// Load: Descriptor always carries the generation observed through the lease.
type VolumeTransport struct {
	kind       TransportKind
	descriptor dicomrender.VolumeDescriptor
	payload    []byte
	sha256     [sha256.Size]byte
}

func (t VolumeTransport) Kind() TransportKind {
	return t.kind
}

func (t VolumeTransport) Descriptor() dicomrender.VolumeDescriptor {
	return t.descriptor
}

func (t VolumeTransport) Generation() uint64 {
	return t.descriptor.VolumeGeneration
}

func (t VolumeTransport) PayloadSHA256() string {
	return hex.EncodeToString(t.sha256[:])
}

func (t VolumeTransport) CopyPayload() []byte {
	return append([]byte(nil), t.payload...)
}

func (t VolumeTransport) Verify() error {
	if t.kind != TransportCPUCanonicalF32 &&
		t.kind != TransportVTKI16 &&
		t.kind != TransportWebGPUR32Float {
		return fmt.Errorf("qualification: unknown transport %q", t.kind)
	}
	if err := dicomrender.ValidateVolumeDescriptor(t.descriptor); err != nil {
		return err
	}
	if uint64(len(t.payload)) != t.descriptor.ByteLength {
		return fmt.Errorf(
			"qualification: transport payload length %d, descriptor byte length %d",
			len(t.payload), t.descriptor.ByteLength,
		)
	}
	if sha256.Sum256(t.payload) != t.sha256 {
		return fmt.Errorf("qualification: transport SHA-256 drift")
	}
	return nil
}

// AdaptCPUCanonicalF32 emits tightly packed modality-domain float32 bytes from
// the authoritative lease used by the canonical CPU volume path.
func AdaptCPUCanonicalF32(lease *dicomrender.VolumeLease) (VolumeTransport, error) {
	return adaptModalityF32(lease, TransportCPUCanonicalF32)
}

// AdaptVTKI16 emits the exact stored-value I16 payload and authoritative
// descriptor generation accepted by the VTK wire contract.
func AdaptVTKI16(lease *dicomrender.VolumeLease) (VolumeTransport, error) {
	snapshot, descriptor, err := authoritativeSnapshot(lease)
	if err != nil {
		return VolumeTransport{}, err
	}
	if descriptor.ScalarFormat != dicomrender.VolumeScalarI16StoredLE ||
		descriptor.SampleDomain != dicomrender.VolumeSampleDomainStored {
		return VolumeTransport{}, fmt.Errorf(
			"qualification: VTK I16 transport requires I16_STORED_LE, got %s/%s",
			descriptor.ScalarFormat, descriptor.SampleDomain,
		)
	}
	var output bytes.Buffer
	if descriptor.ByteLength > uint64(math.MaxInt) {
		return VolumeTransport{}, fmt.Errorf("qualification: VTK transport exceeds Go buffer capacity")
	}
	output.Grow(int(descriptor.ByteLength))
	if err := snapshot.WritePayloadTo(&output); err != nil {
		return VolumeTransport{}, err
	}
	return newTransport(TransportVTKI16, descriptor, output.Bytes())
}

// AdaptWebGPUR32FloatTransport proves the deterministic R32Float upload bytes
// derived from the authoritative lease.
func AdaptWebGPUR32FloatTransport(lease *dicomrender.VolumeLease) (VolumeTransport, error) {
	return adaptModalityF32(lease, TransportWebGPUR32Float)
}

func adaptModalityF32(
	lease *dicomrender.VolumeLease,
	kind TransportKind,
) (VolumeTransport, error) {
	snapshot, descriptor, err := authoritativeSnapshot(lease)
	if err != nil {
		return VolumeTransport{}, err
	}
	voxels := uint64(descriptor.Dimensions[0]) *
		uint64(descriptor.Dimensions[1]) *
		uint64(descriptor.Dimensions[2])
	if voxels > uint64(math.MaxInt)/4 {
		return VolumeTransport{}, fmt.Errorf("qualification: F32 transport exceeds Go buffer capacity")
	}
	var output bytes.Buffer
	output.Grow(int(voxels * 4))
	if err := snapshot.WriteModalityF32To(&output); err != nil {
		return VolumeTransport{}, err
	}
	descriptor.ScalarFormat = dicomrender.VolumeScalarF32ModalityLE
	descriptor.SampleDomain = dicomrender.VolumeSampleDomainModality
	descriptor.RowStrideBytes = uint64(descriptor.Dimensions[0]) * 4
	descriptor.SliceStrideBytes =
		uint64(descriptor.Dimensions[1]) * descriptor.RowStrideBytes
	descriptor.ByteLength = voxels * 4
	descriptor.RescaleSlope = 1
	descriptor.RescaleIntercept = 0
	return newTransport(kind, descriptor, output.Bytes())
}

func authoritativeSnapshot(
	lease *dicomrender.VolumeLease,
) (dicomrender.VolumeSnapshot, dicomrender.VolumeDescriptor, error) {
	if lease == nil {
		return dicomrender.VolumeSnapshot{}, dicomrender.VolumeDescriptor{},
			fmt.Errorf("qualification: nil authoritative lease")
	}
	snapshot, err := lease.Snapshot()
	if err != nil {
		return dicomrender.VolumeSnapshot{}, dicomrender.VolumeDescriptor{}, err
	}
	descriptor, err := snapshot.Descriptor()
	if err != nil {
		return dicomrender.VolumeSnapshot{}, dicomrender.VolumeDescriptor{}, err
	}
	if descriptor.VolumeGeneration == 0 ||
		descriptor.VolumeGeneration != snapshot.Generation() {
		return dicomrender.VolumeSnapshot{}, dicomrender.VolumeDescriptor{},
			fmt.Errorf(
				"qualification: authoritative generation mismatch descriptor=%d snapshot=%d",
				descriptor.VolumeGeneration, snapshot.Generation(),
			)
	}
	return snapshot, descriptor, nil
}

func newTransport(
	kind TransportKind,
	descriptor dicomrender.VolumeDescriptor,
	payload []byte,
) (VolumeTransport, error) {
	// Adapter-local buffers transfer ownership here. CopyPayload remains the
	// only public payload accessor and continues to return a defensive copy.
	result := VolumeTransport{
		kind:       kind,
		descriptor: descriptor,
		payload:    payload,
		sha256:     sha256.Sum256(payload),
	}
	if err := result.Verify(); err != nil {
		return VolumeTransport{}, err
	}
	return result, nil
}

type CapabilityStatus string

const (
	CapabilityAvailable   CapabilityStatus = "available"
	CapabilityUnavailable CapabilityStatus = "unavailable"
)

// BackendCapability distinguishes a proven transport from a real renderer.
type BackendCapability struct {
	Backend string           `json:"backend"`
	Feature string           `json:"feature"`
	Status  CapabilityStatus `json:"status"`
	Reason  string           `json:"reason,omitempty"`
}

func (capability BackendCapability) Validate() error {
	if strings.TrimSpace(capability.Backend) == "" ||
		strings.TrimSpace(capability.Feature) == "" {
		return fmt.Errorf("qualification: capability lacks backend or feature")
	}
	switch capability.Status {
	case CapabilityAvailable:
		if capability.Reason != "" {
			return fmt.Errorf("qualification: available capability has unavailable reason")
		}
	case CapabilityUnavailable:
		if strings.TrimSpace(capability.Reason) == "" {
			return fmt.Errorf("qualification: unavailable capability lacks reason")
		}
	default:
		return fmt.Errorf("qualification: invalid capability status %q", capability.Status)
	}
	return nil
}

func WebGPUVolumeRendererCapability(
	workerAvailable bool,
	unavailableReason string,
) BackendCapability {
	capability := BackendCapability{
		Backend: "webgpu-wgpu-native-worker",
		Feature: "volume-renderer",
	}
	if workerAvailable {
		capability.Status = CapabilityAvailable
		return capability
	}
	capability.Status = CapabilityUnavailable
	capability.Reason = strings.TrimSpace(unavailableReason)
	if capability.Reason == "" {
		capability.Reason = "worker start/handshake probe failed"
	}
	return capability
}
