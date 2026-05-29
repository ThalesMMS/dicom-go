// Package qualification defines PHI-free source fixtures, pre-render transport
// adapters and evidence contracts shared by renderer qualification harnesses.
package qualification

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"

	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

const (
	// SyntheticCTName is the only retained name for the cross-backend synthetic
	// CT corpus. A name alone is never evidence: consumers must also record the
	// payload SHA-256 exposed by MaterializedVolume.
	SyntheticCTName = "synth-ct-i16le-v1"
	// ReferenceSyntheticCTSHA256 freezes the retained 512x512x300 payload.
	ReferenceSyntheticCTSHA256 = "1531f7807ee24b01a802eb6b0f75657d4faae6beb28ff7dd1b98682da5eca46b"
)

var referenceSyntheticCTDimensions = [3]uint32{512, 512, 300}

// ReferenceSyntheticCTDimensions returns the retained extent by value.
func ReferenceSyntheticCTDimensions() [3]uint32 {
	return referenceSyntheticCTDimensions
}

// MaterializedVolume owns one immutable descriptor/payload pair. Methods return
// value copies or fresh byte copies so a backend cannot mutate the corpus seen
// by a later backend.
type MaterializedVolume struct {
	name       string
	descriptor dicomrender.VolumeDescriptor
	payload    []byte
	sha256     [sha256.Size]byte
}

// NewSyntheticCT materializes the frozen int16 little-endian fixture formula.
// It is exported for bounded harness runs as well as the retained 512x512x300
// corpus; dimensions alter only the extent, never the voxel definition.
func NewSyntheticCT(dimensions [3]uint32, generation uint64) (MaterializedVolume, error) {
	if generation == 0 {
		return MaterializedVolume{}, fmt.Errorf("qualification: zero volume generation")
	}
	for axis, dimension := range dimensions {
		if dimension == 0 {
			return MaterializedVolume{}, fmt.Errorf("qualification: zero dimension at axis %d", axis)
		}
	}
	voxels := uint64(dimensions[0])
	for _, dimension := range dimensions[1:] {
		if voxels > math.MaxUint64/uint64(dimension) {
			return MaterializedVolume{}, fmt.Errorf("qualification: voxel count overflow")
		}
		voxels *= uint64(dimension)
	}
	if voxels > uint64(math.MaxInt)/2 {
		return MaterializedVolume{}, fmt.Errorf("qualification: payload exceeds Go slice capacity")
	}
	byteLength := voxels * 2
	payload := make([]byte, int(byteLength))
	descriptor, err := syntheticCTDescriptor(dimensions, generation)
	if err != nil {
		return MaterializedVolume{}, err
	}
	for z := uint32(0); z < dimensions[2]; z++ {
		for y := uint32(0); y < dimensions[1]; y++ {
			for x := uint32(0); x < dimensions[0]; x++ {
				// This is the single frozen cross-backend voxel definition.
				value := int16(int64((uint64(x)*3+uint64(y)*5+uint64(z)*7)%4096) - 1024)
				offset := uint64(z)*descriptor.SliceStrideBytes +
					uint64(y)*descriptor.RowStrideBytes + uint64(x)*2
				binary.LittleEndian.PutUint16(payload[offset:offset+2], uint16(value))
			}
		}
	}

	result := MaterializedVolume{
		name:       SyntheticCTName,
		descriptor: descriptor,
		payload:    payload,
		sha256:     sha256.Sum256(payload),
	}
	if dimensions == referenceSyntheticCTDimensions &&
		result.PayloadSHA256() != ReferenceSyntheticCTSHA256 {
		return MaterializedVolume{}, fmt.Errorf(
			"qualification: reference payload SHA-256 %s, want %s",
			result.PayloadSHA256(), ReferenceSyntheticCTSHA256,
		)
	}
	return result, nil
}

func syntheticCTDescriptor(
	dimensions [3]uint32,
	generation uint64,
) (dicomrender.VolumeDescriptor, error) {
	rowBytes := uint64(dimensions[0]) * 2
	sliceBytes := uint64(dimensions[1]) * rowBytes
	byteLength := uint64(dimensions[2]) * sliceBytes
	forward := dicomrender.GeometryAffine{
		0.7, 0, 0, 0,
		0, 0.7, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
	inverse := dicomrender.GeometryAffine{
		1 / 0.7, 0, 0, 0,
		0, 1 / 0.7, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
	descriptor := dicomrender.VolumeDescriptor{
		ContractVersion:   dicomrender.VolumeSnapshotContractVersion,
		HeaderSize:        dicomrender.VolumeSnapshotHeaderSizeV1,
		VolumeGeneration:  generation,
		Derivation:        dicomrender.VolumeDerivationNormalized,
		Dimensions:        dimensions,
		Components:        1,
		ScalarFormat:      dicomrender.VolumeScalarI16StoredLE,
		SampleDomain:      dicomrender.VolumeSampleDomainStored,
		RowStrideBytes:    rowBytes,
		SliceStrideBytes:  sliceBytes,
		ByteLength:        byteLength,
		RescaleSlope:      1,
		RescaleIntercept:  0,
		SpacingMM:         [3]float64{0.7, 0.7, 1},
		IndexToPatientLPS: forward,
		PatientLPSToIndex: inverse,
	}
	if err := dicomrender.ValidateVolumeDescriptor(descriptor); err != nil {
		return dicomrender.VolumeDescriptor{}, fmt.Errorf("qualification: synthetic CT descriptor: %w", err)
	}
	return descriptor, nil
}

// NewReferenceSyntheticCT materializes the retained 512x512x300 corpus.
func NewReferenceSyntheticCT(generation uint64) (MaterializedVolume, error) {
	fixture, err := NewSyntheticCT(referenceSyntheticCTDimensions, generation)
	if err != nil {
		return MaterializedVolume{}, err
	}
	if got := fixture.PayloadSHA256(); got != ReferenceSyntheticCTSHA256 {
		return MaterializedVolume{}, fmt.Errorf(
			"qualification: reference payload SHA-256 %s, want %s",
			got, ReferenceSyntheticCTSHA256,
		)
	}
	return fixture, nil
}

func (v MaterializedVolume) Name() string {
	return v.name
}

func (v MaterializedVolume) Descriptor() dicomrender.VolumeDescriptor {
	return v.descriptor
}

func (v MaterializedVolume) PayloadSHA256() string {
	return hex.EncodeToString(v.sha256[:])
}

func (v MaterializedVolume) PayloadSize() uint64 {
	return uint64(len(v.payload))
}

// CopyPayload returns a backend-owned copy of the exact frozen bytes.
func (v MaterializedVolume) CopyPayload() []byte {
	return append([]byte(nil), v.payload...)
}

// WritePayload streams the exact frozen bytes without exposing a mutable alias.
func (v MaterializedVolume) WritePayload(dst io.Writer) error {
	if dst == nil {
		return fmt.Errorf("qualification: nil payload writer")
	}
	written, err := dst.Write(v.payload)
	if err != nil {
		return err
	}
	if written != len(v.payload) {
		return io.ErrShortWrite
	}
	return nil
}

// VolumeInput returns a caller-owned copy suitable for VolumeStore.Replace.
func (v MaterializedVolume) VolumeInput() dicomrender.VolumeInput {
	return dicomrender.VolumeInput{
		Descriptor: v.descriptor,
		Payload:    v.CopyPayload(),
	}
}

// Load installs the source fixture and returns the authoritative store lease.
// The source descriptor's requested generation is deliberately not authority:
// VolumeStore assigns the generation and every downstream adapter must derive
// its descriptor, trace and wire tuple from the returned lease.
func (v MaterializedVolume) Load(store *dicomrender.VolumeStore) (*dicomrender.VolumeLease, error) {
	if store == nil {
		return nil, fmt.Errorf("qualification: nil volume store")
	}
	generation, err := store.Replace(v.VolumeInput())
	if err != nil {
		return nil, err
	}
	return store.Acquire(generation)
}

// Verify rejects descriptor or payload drift before a run is admitted.
func (v MaterializedVolume) Verify() error {
	if v.name != SyntheticCTName {
		return fmt.Errorf("qualification: unknown fixture name %q", v.name)
	}
	if err := dicomrender.ValidateVolumeDescriptor(v.descriptor); err != nil {
		return err
	}
	if uint64(len(v.payload)) != v.descriptor.ByteLength {
		return fmt.Errorf(
			"qualification: payload length %d, descriptor byte length %d",
			len(v.payload), v.descriptor.ByteLength,
		)
	}
	digest := sha256.Sum256(v.payload)
	if digest != v.sha256 {
		return fmt.Errorf("qualification: payload SHA-256 drift")
	}
	return nil
}
