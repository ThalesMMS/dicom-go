package render

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	// VolumeSnapshotContractVersion is the frozen cross-backend volume contract.
	VolumeSnapshotContractVersion uint32 = 1
	// VolumeSnapshotHeaderSizeV1 is the minimum logical V1 header size. A larger
	// header is accepted so V1 readers can ignore trailing fields.
	VolumeSnapshotHeaderSizeV1 uint32 = 360
)

var (
	ErrInvalidVolumeSnapshot = errors.New("render: invalid volume snapshot")
	ErrVolumeStoreClosed     = errors.New("render: volume store closed")
	ErrVolumeNotFound        = errors.New("render: volume generation not found")
	ErrVolumeInUse           = errors.New("render: volume generation is in use")
	ErrVolumeLeaseReleased   = errors.New("render: volume lease released")
	ErrVolumeBudgetExceeded  = errors.New("render: volume store byte budget exceeded")
)

// VolumeScalarFormat identifies the little-endian scalar representation.
type VolumeScalarFormat uint32

const (
	VolumeScalarUnknown VolumeScalarFormat = iota
	VolumeScalarF32ModalityLE
	VolumeScalarI16StoredLE
	VolumeScalarU16StoredLE
)

func (f VolumeScalarFormat) String() string {
	switch f {
	case VolumeScalarF32ModalityLE:
		return "F32_MODALITY_LE"
	case VolumeScalarI16StoredLE:
		return "I16_STORED_LE"
	case VolumeScalarU16StoredLE:
		return "U16_STORED_LE"
	default:
		return "UNKNOWN"
	}
}

// VolumeSampleDomain distinguishes modality values from stored DICOM samples.
type VolumeSampleDomain uint32

const (
	VolumeSampleDomainUnknown VolumeSampleDomain = iota
	VolumeSampleDomainModality
	VolumeSampleDomainStored
)

func (d VolumeSampleDomain) String() string {
	switch d {
	case VolumeSampleDomainModality:
		return "MODALITY"
	case VolumeSampleDomainStored:
		return "STORED"
	default:
		return "UNKNOWN"
	}
}

// VolumeDerivation describes why a generation exists. Presentation-only
// changes are intentionally absent: VOI, LUT and transfer-function state do not
// change clinical voxel data and therefore do not create volume generations.
type VolumeDerivation uint32

const (
	VolumeDerivationUnknown VolumeDerivation = iota
	VolumeDerivationNormalized
	VolumeDerivationRegularized
)

func (d VolumeDerivation) String() string {
	switch d {
	case VolumeDerivationNormalized:
		return "normalized"
	case VolumeDerivationRegularized:
		return "regularized"
	default:
		return "unknown"
	}
}

// VolumeDescriptor is the value-only header of an immutable VolumeSnapshot.
// Arrays are returned by value so consumers cannot mutate store-owned geometry.
type VolumeDescriptor struct {
	ContractVersion   uint32
	HeaderSize        uint32
	VolumeGeneration  uint64
	ParentGeneration  uint64
	Derivation        VolumeDerivation
	Dimensions        [3]uint32 // X columns, Y rows, Z slices
	Components        uint32
	ScalarFormat      VolumeScalarFormat
	SampleDomain      VolumeSampleDomain
	RowStrideBytes    uint64
	SliceStrideBytes  uint64
	ByteLength        uint64
	RescaleSlope      float64
	RescaleIntercept  float64
	SpacingMM         [3]float64
	IndexToPatientLPS GeometryAffine
	PatientLPSToIndex GeometryAffine
}

// VolumeInput is copied by VolumeStore.Replace. Payload ownership remains with
// the caller, and mutations after Replace cannot affect the stored generation.
type VolumeInput struct {
	Descriptor VolumeDescriptor
	Payload    []byte
}

type volumePayload interface {
	byteLen() uint64
	copyBytes(io.Writer) error
	modalityAt(offset uint64, descriptor VolumeDescriptor) (float64, bool)
}

type byteVolumePayload struct {
	data []byte
}

func (p *byteVolumePayload) byteLen() uint64 {
	if p == nil {
		return 0
	}
	return uint64(len(p.data))
}

func (p *byteVolumePayload) copyBytes(dst io.Writer) error {
	if p == nil {
		return nil
	}
	written, err := dst.Write(p.data)
	if err == nil && written != len(p.data) {
		return io.ErrShortWrite
	}
	return err
}

func (p *byteVolumePayload) modalityAt(offset uint64, descriptor VolumeDescriptor) (float64, bool) {
	if p == nil {
		return 0, false
	}
	switch descriptor.ScalarFormat {
	case VolumeScalarF32ModalityLE:
		end, ok := checkedAdd64(offset, 4)
		if !ok || end > uint64(len(p.data)) {
			return 0, false
		}
		value := math.Float32frombits(binary.LittleEndian.Uint32(p.data[offset:end]))
		return float64(value), true
	case VolumeScalarI16StoredLE:
		end, ok := checkedAdd64(offset, 2)
		if !ok || end > uint64(len(p.data)) {
			return 0, false
		}
		stored := int16(binary.LittleEndian.Uint16(p.data[offset:end]))
		return float64(stored)*descriptor.RescaleSlope + descriptor.RescaleIntercept, true
	case VolumeScalarU16StoredLE:
		end, ok := checkedAdd64(offset, 2)
		if !ok || end > uint64(len(p.data)) {
			return 0, false
		}
		stored := binary.LittleEndian.Uint16(p.data[offset:end])
		return float64(stored)*descriptor.RescaleSlope + descriptor.RescaleIntercept, true
	default:
		return 0, false
	}
}

// float32VolumePayload is used by the dicom-go ingest path to transfer the one
// exact normalized allocation into the store without a raw-byte mirror.
type float32VolumePayload struct {
	data []float32
}

func (p *float32VolumePayload) byteLen() uint64 {
	if p == nil {
		return 0
	}
	return uint64(len(p.data)) * 4
}

func (p *float32VolumePayload) copyBytes(dst io.Writer) error {
	if p == nil {
		return nil
	}
	var scratch [4]byte
	for _, value := range p.data {
		binary.LittleEndian.PutUint32(scratch[:], math.Float32bits(value))
		written, err := dst.Write(scratch[:])
		if err != nil {
			return err
		}
		if written != len(scratch) {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (p *float32VolumePayload) modalityAt(offset uint64, descriptor VolumeDescriptor) (float64, bool) {
	if p == nil || descriptor.ScalarFormat != VolumeScalarF32ModalityLE || offset%4 != 0 {
		return 0, false
	}
	index := offset / 4
	if index >= uint64(len(p.data)) {
		return 0, false
	}
	return float64(p.data[index]), true
}

// VolumeSnapshot is a read-only view. It is valid until its VolumeLease is
// released; methods fail closed after release instead of exposing a mutable
// payload slice or retaining a backend-specific object.
type VolumeSnapshot struct {
	lease *VolumeLease
}

// Descriptor returns a value copy of the immutable snapshot header.
func (s VolumeSnapshot) Descriptor() (VolumeDescriptor, error) {
	record, err := s.record()
	if err != nil {
		return VolumeDescriptor{}, err
	}
	return record.descriptor, nil
}

// Generation returns the volume generation, or zero after release.
func (s VolumeSnapshot) Generation() uint64 {
	record, err := s.record()
	if err != nil {
		return 0
	}
	return record.descriptor.VolumeGeneration
}

// ResidencyKey returns the composite store/generation identity protected by
// this snapshot's lease, or zero after release.
func (s VolumeSnapshot) ResidencyKey() VolumeResidencyKey {
	if s.lease == nil {
		return VolumeResidencyKey{}
	}
	return s.lease.ResidencyKey()
}

// ModalityAt returns one sample in modality domain. It applies the supplied
// slope/intercept for the optional stored-value formats.
func (s VolumeSnapshot) ModalityAt(x, y, z uint32) (float64, bool) {
	record, err := s.record()
	if err != nil {
		return 0, false
	}
	descriptor := record.descriptor
	if x >= descriptor.Dimensions[0] || y >= descriptor.Dimensions[1] || z >= descriptor.Dimensions[2] {
		return 0, false
	}
	scalarSize, ok := descriptor.ScalarFormat.scalarSize()
	if !ok {
		return 0, false
	}
	zOffset, ok := checkedMul64(uint64(z), descriptor.SliceStrideBytes)
	if !ok {
		return 0, false
	}
	yOffset, ok := checkedMul64(uint64(y), descriptor.RowStrideBytes)
	if !ok {
		return 0, false
	}
	xOffset, ok := checkedMul64(uint64(x), scalarSize)
	if !ok {
		return 0, false
	}
	offset, ok := checkedAdd64(zOffset, yOffset)
	if !ok {
		return 0, false
	}
	offset, ok = checkedAdd64(offset, xOffset)
	if !ok {
		return 0, false
	}
	return record.payload.modalityAt(offset, descriptor)
}

// WritePayloadTo copies the immutable little-endian payload to a caller-owned
// transport buffer. This is the explicit copy boundary for native/GPU upload.
func (s VolumeSnapshot) WritePayloadTo(dst io.Writer) error {
	if dst == nil {
		return fmt.Errorf("%w: nil payload writer", ErrInvalidVolumeSnapshot)
	}
	record, err := s.record()
	if err != nil {
		return err
	}
	return record.payload.copyBytes(dst)
}

// WriteModalityF32To streams tightly packed little-endian modality-domain
// float32 rows. It is the bounded upload boundary for backends that require one
// R32Float texture: stored-value inputs are rescaled while streaming and source
// row/slice padding is not copied.
func (s VolumeSnapshot) WriteModalityF32To(dst io.Writer) error {
	return s.WriteModalityF32Context(context.Background(), dst)
}

// WriteModalityF32Context is the cancellable form of WriteModalityF32To. It
// writes one reusable packed row at a time (or a direct source row for packed
// F32 byte payloads), so staging memory is O(width), never O(volume).
func (s VolumeSnapshot) WriteModalityF32Context(ctx context.Context, dst io.Writer) error {
	if dst == nil {
		return fmt.Errorf("%w: nil modality writer", ErrInvalidVolumeSnapshot)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, err := s.record()
	if err != nil {
		return err
	}
	descriptor := record.descriptor
	if err := ValidateModalityF32Conversion(descriptor); err != nil {
		return err
	}
	scalarSize, ok := descriptor.ScalarFormat.scalarSize()
	if !ok {
		return fmt.Errorf("%w: unknown scalar format", ErrInvalidVolumeSnapshot)
	}
	rowBytes64, ok := checkedMul64(uint64(descriptor.Dimensions[0]), 4)
	if !ok || rowBytes64 > uint64(math.MaxInt) {
		return fmt.Errorf("%w: modality row size overflow", ErrInvalidVolumeSnapshot)
	}
	row := make([]byte, int(rowBytes64))
	for z := uint32(0); z < descriptor.Dimensions[2]; z++ {
		zOffset := uint64(z) * descriptor.SliceStrideBytes
		for y := uint32(0); y < descriptor.Dimensions[1]; y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			sourceOffset := zOffset + uint64(y)*descriptor.RowStrideBytes
			if bytesPayload, direct := record.payload.(*byteVolumePayload); direct &&
				descriptor.ScalarFormat == VolumeScalarF32ModalityLE {
				end := sourceOffset + rowBytes64
				if end > uint64(len(bytesPayload.data)) {
					return fmt.Errorf("%w: modality row unavailable at %d,%d", ErrInvalidVolumeSnapshot, y, z)
				}
				if err := writeExact(dst, bytesPayload.data[sourceOffset:end]); err != nil {
					return err
				}
				continue
			}
			for x := uint32(0); x < descriptor.Dimensions[0]; x++ {
				if x&255 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				offset := sourceOffset + uint64(x)*scalarSize
				value, available := record.payload.modalityAt(offset, descriptor)
				if !available {
					return fmt.Errorf("%w: modality sample unavailable at %d,%d,%d", ErrInvalidVolumeSnapshot, x, y, z)
				}
				value32 := float32(value)
				if descriptor.ScalarFormat != VolumeScalarF32ModalityLE &&
					(math.IsNaN(value) || math.IsInf(value, 0) ||
						math.IsNaN(float64(value32)) || math.IsInf(float64(value32), 0)) {
					return fmt.Errorf(
						"%w: modality sample out of finite float32 range at %d,%d,%d",
						ErrInvalidVolumeSnapshot, x, y, z,
					)
				}
				binary.LittleEndian.PutUint32(row[int(x)*4:], math.Float32bits(value32))
			}
			if err := writeExact(dst, row); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateModalityF32Conversion proves in O(1) that every possible stored
// integer sample maps into finite float32 modality space. Direct F32 payloads
// are validated by the consuming GPU worker during their single upload pass.
func ValidateModalityF32Conversion(descriptor VolumeDescriptor) error {
	if err := ValidateVolumeDescriptor(descriptor); err != nil {
		return err
	}
	var minimum, maximum float64
	switch descriptor.ScalarFormat {
	case VolumeScalarF32ModalityLE:
		return nil
	case VolumeScalarI16StoredLE:
		minimum, maximum = math.MinInt16, math.MaxInt16
	case VolumeScalarU16StoredLE:
		minimum, maximum = 0, math.MaxUint16
	default:
		return fmt.Errorf("%w: unknown scalar format", ErrInvalidVolumeSnapshot)
	}
	for _, stored := range []float64{minimum, maximum} {
		value := stored*descriptor.RescaleSlope + descriptor.RescaleIntercept
		value32 := float32(value)
		if math.IsNaN(value) || math.IsInf(value, 0) ||
			math.IsNaN(float64(value32)) || math.IsInf(float64(value32), 0) {
			return fmt.Errorf(
				"%w: stored-value rescale exceeds finite float32 modality range",
				ErrInvalidVolumeSnapshot,
			)
		}
	}
	return nil
}

func writeExact(dst io.Writer, payload []byte) error {
	written, err := dst.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func (s VolumeSnapshot) record() (*volumeRecord, error) {
	if s.lease == nil {
		return nil, ErrVolumeLeaseReleased
	}
	return s.lease.record()
}

func (f VolumeScalarFormat) scalarSize() (uint64, bool) {
	switch f {
	case VolumeScalarF32ModalityLE:
		return 4, true
	case VolumeScalarI16StoredLE, VolumeScalarU16StoredLE:
		return 2, true
	default:
		return 0, false
	}
}

// ValidateVolumeDescriptor applies the frozen VolumeSnapshot V1 rejection
// rules to a fully versioned descriptor.
func ValidateVolumeDescriptor(descriptor VolumeDescriptor) error {
	return validateVolumeDescriptor(descriptor, false)
}

func validateVolumeDescriptor(descriptor VolumeDescriptor, generationPending bool) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidVolumeSnapshot, fmt.Sprintf(format, args...))
	}
	if descriptor.ContractVersion != VolumeSnapshotContractVersion {
		return fail("contract version %d", descriptor.ContractVersion)
	}
	if descriptor.HeaderSize < VolumeSnapshotHeaderSizeV1 {
		return fail("header size %d below V1 minimum %d", descriptor.HeaderSize, VolumeSnapshotHeaderSizeV1)
	}
	if !generationPending && descriptor.VolumeGeneration == 0 {
		return fail("zero volume generation")
	}
	if descriptor.Derivation != VolumeDerivationUnknown &&
		descriptor.Derivation != VolumeDerivationNormalized &&
		descriptor.Derivation != VolumeDerivationRegularized {
		return fail("unknown derivation %d", descriptor.Derivation)
	}
	if descriptor.Derivation == VolumeDerivationRegularized && descriptor.ParentGeneration == 0 {
		return fail("regularized generation lacks parent")
	}
	if (descriptor.Derivation == VolumeDerivationUnknown || descriptor.Derivation == VolumeDerivationNormalized) &&
		descriptor.ParentGeneration != 0 {
		return fail("normalized generation has parent %d", descriptor.ParentGeneration)
	}
	if descriptor.Components != 1 {
		return fail("components %d, want 1", descriptor.Components)
	}
	for axis, dimension := range descriptor.Dimensions {
		if dimension == 0 {
			return fail("zero dimension at axis %d", axis)
		}
	}
	scalarSize, ok := descriptor.ScalarFormat.scalarSize()
	if !ok {
		return fail("unknown scalar format %d", descriptor.ScalarFormat)
	}
	switch descriptor.ScalarFormat {
	case VolumeScalarF32ModalityLE:
		if descriptor.SampleDomain != VolumeSampleDomainModality ||
			descriptor.RescaleSlope != 1 || descriptor.RescaleIntercept != 0 {
			return fail("F32 modality format requires MODALITY domain and identity rescale")
		}
	case VolumeScalarI16StoredLE, VolumeScalarU16StoredLE:
		if descriptor.SampleDomain != VolumeSampleDomainStored {
			return fail("%s requires STORED domain", descriptor.ScalarFormat)
		}
	}
	if !finiteScalar(descriptor.RescaleSlope) || !finiteScalar(descriptor.RescaleIntercept) {
		return fail("non-finite rescale")
	}
	packedRow, ok := checkedMul64(uint64(descriptor.Dimensions[0]), scalarSize)
	if !ok {
		return fail("packed row overflow")
	}
	if descriptor.RowStrideBytes < packedRow {
		return fail("row stride %d below packed row %d", descriptor.RowStrideBytes, packedRow)
	}
	minimumSlice, ok := checkedMul64(uint64(descriptor.Dimensions[1]), descriptor.RowStrideBytes)
	if !ok {
		return fail("slice stride overflow")
	}
	if descriptor.SliceStrideBytes < minimumSlice {
		return fail("slice stride %d below rows*row_stride %d", descriptor.SliceStrideBytes, minimumSlice)
	}
	lastSlice, ok := checkedMul64(uint64(descriptor.Dimensions[2]-1), descriptor.SliceStrideBytes)
	if !ok {
		return fail("byte length overflow at final slice")
	}
	lastRow, ok := checkedMul64(uint64(descriptor.Dimensions[1]-1), descriptor.RowStrideBytes)
	if !ok {
		return fail("byte length overflow at final row")
	}
	finalAddress, ok := checkedAdd64(lastSlice, lastRow)
	if !ok {
		return fail("byte length overflow")
	}
	finalAddress, ok = checkedAdd64(finalAddress, packedRow)
	if !ok || descriptor.ByteLength < finalAddress {
		return fail("byte length %d below final addressed byte %d", descriptor.ByteLength, finalAddress)
	}
	for axis, spacing := range descriptor.SpacingMM {
		if !finitePositiveSpacing(spacing) {
			return fail("invalid spacing at axis %d", axis)
		}
	}
	if err := validateAffinePair(descriptor.IndexToPatientLPS, descriptor.PatientLPSToIndex); err != nil {
		return fail("%v", err)
	}
	const spacingTolerance = 1e-9
	for axis := 0; axis < 3; axis++ {
		columnNorm := math.Hypot(
			math.Hypot(
				descriptor.IndexToPatientLPS[axis],
				descriptor.IndexToPatientLPS[4+axis],
			),
			descriptor.IndexToPatientLPS[8+axis],
		)
		if !finiteScalar(columnNorm) {
			return fail("non-finite affine column norm at axis %d", axis)
		}
		tolerance := spacingTolerance * math.Max(1, descriptor.SpacingMM[axis])
		difference := columnNorm - descriptor.SpacingMM[axis]
		if !finiteScalar(difference) || math.Abs(difference) > tolerance {
			return fail("spacing axis %d is %.12g but affine column norm is %.12g", axis, descriptor.SpacingMM[axis], columnNorm)
		}
	}
	return nil
}

func finiteScalar(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateAffinePair(forward, inverse GeometryAffine) error {
	if !forward.Finite() {
		return fmt.Errorf("index_to_patient_lps is non-finite or has an invalid final row")
	}
	if !inverse.Finite() {
		return fmt.Errorf("patient_lps_to_index is non-finite or has an invalid final row")
	}
	const tolerance = 1e-9
	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			want := 0.0
			if row == col {
				want = 1
			}
			a, b := 0.0, 0.0
			for k := 0; k < 4; k++ {
				productA := forward[row*4+k] * inverse[k*4+col]
				productB := inverse[row*4+k] * forward[k*4+col]
				if !finiteScalar(productA) || !finiteScalar(productB) {
					return fmt.Errorf("affine product is non-finite")
				}
				a += productA
				b += productB
				if !finiteScalar(a) || !finiteScalar(b) {
					return fmt.Errorf("affine product accumulator is non-finite")
				}
			}
			differenceA := a - want
			differenceB := b - want
			if !finiteScalar(differenceA) || !finiteScalar(differenceB) ||
				math.Abs(differenceA) > tolerance || math.Abs(differenceB) > tolerance {
				return fmt.Errorf("affines are not mutual inverses within %.0e", tolerance)
			}
		}
	}
	return nil
}

func checkedMul64(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}

func checkedAdd64(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}
