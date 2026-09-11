package qualification

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"

	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

// ClinicalFixtureID identifies one small PHI-free qualification volume. The
// IDs and formulas are versioned because their payload hashes are used as
// evidence by CPU/WebGPU parity campaigns.
type ClinicalFixtureID string

const (
	ClinicalFixtureRampHU            ClinicalFixtureID = "clinical-ramp-hu-v1"
	ClinicalFixtureSteps             ClinicalFixtureID = "clinical-steps-v1"
	ClinicalFixtureConcentricSpheres ClinicalFixtureID = "clinical-concentric-spheres-v1"
	ClinicalFixtureContrastTubes     ClinicalFixtureID = "clinical-contrast-tubes-v1"
	ClinicalFixtureAirStructure      ClinicalFixtureID = "clinical-air-structure-v1"
	ClinicalFixtureSliceImpulse      ClinicalFixtureID = "clinical-slice-impulse-v1"
	ClinicalFixtureUniform           ClinicalFixtureID = "clinical-uniform-v1"
	ClinicalFixtureAnisotropic       ClinicalFixtureID = "clinical-anisotropic-v1"
)

// ClinicalFixtureDefinition describes a generated corpus without retaining
// its payload. Dimensions deliberately remain small enough for ordinary unit
// tests and race runs.
type ClinicalFixtureDefinition struct {
	ID          ClinicalFixtureID
	Description string
	Dimensions  [3]uint32
	SpacingMM   [3]float64
}

var clinicalFixtureCatalogV1 = []ClinicalFixtureDefinition{
	{ClinicalFixtureRampHU, "HU ramp below, within, and above every clinical window", [3]uint32{32, 8, 8}, [3]float64{1, 1, 1}},
	{ClinicalFixtureSteps, "constant sentinel-intensity regions", [3]uint32{32, 8, 8}, [3]float64{1, 1, 1}},
	{ClinicalFixtureConcentricSpheres, "nested densities for front-to-back accumulation and occlusion", [3]uint32{32, 32, 32}, [3]float64{1, 1, 1}},
	{ClinicalFixtureContrastTubes, "high-contrast vascular tubes", [3]uint32{32, 32, 24}, [3]float64{1, 1, 1}},
	{ClinicalFixtureAirStructure, "soft-tissue shell with an air cavity", [3]uint32{32, 32, 32}, [3]float64{1, 1, 1}},
	{ClinicalFixtureSliceImpulse, "one distinct impulse per source slice", [3]uint32{9, 9, 7}, [3]float64{1, 1, 1}},
	{ClinicalFixtureUniform, "degenerate constant-value domain", [3]uint32{16, 16, 16}, [3]float64{1, 1, 1}},
	{ClinicalFixtureAnisotropic, "physical-space structures with anisotropic spacing", [3]uint32{32, 24, 12}, [3]float64{0.5, 0.8, 3}},
}

// Frozen payload hashes are populated below after the formulas. Any change is
// a golden regeneration that must be reviewed explicitly.
var clinicalFixtureSHA256V1 = map[ClinicalFixtureID]string{
	ClinicalFixtureRampHU:            "bbe28543c7d99d88d0033efef6fe2bb5657827e84808d2537bc4f87ad0d256c8",
	ClinicalFixtureSteps:             "8ed3f63f25001cc8c1c29bc2cf17c72739805e128ec4ed8f4c7bc3f4c802eeb9",
	ClinicalFixtureConcentricSpheres: "e7e6e223e491643d65e5381d9b8c66793b0881a6639433c5ba6f4978d81f683f",
	ClinicalFixtureContrastTubes:     "3ec622a27ec5a00abfd65f313744812b6aadb9f2cb41cc12c671423bbf2397c2",
	ClinicalFixtureAirStructure:      "37a8d6616b23cd1e995abf9ae7c3e103aa473535a3e300a67f044d2ebc37a402",
	ClinicalFixtureSliceImpulse:      "b5223fcf924dfec259566884d7ba94a768d4d7fa2a3b313581d96a2b0aaa5ccc",
	ClinicalFixtureUniform:           "23c9c8514cd4f02ba3ef519dc461c3d56ce4639cd3b0a8ad1d5d2bb417ca0714",
	ClinicalFixtureAnisotropic:       "a70a1de7ea8af85dcb7da2c33d1c5acbf41f43b26e5396fb50245084b12c97ba",
}

// ClinicalFixtureCatalog returns detached definitions in contractual order.
func ClinicalFixtureCatalog() []ClinicalFixtureDefinition {
	return append([]ClinicalFixtureDefinition(nil), clinicalFixtureCatalogV1...)
}

// ClinicalFixtureDefinitionByID resolves one versioned generated corpus.
func ClinicalFixtureDefinitionByID(id ClinicalFixtureID) (ClinicalFixtureDefinition, bool) {
	for _, definition := range clinicalFixtureCatalogV1 {
		if definition.ID == id {
			return definition, true
		}
	}
	return ClinicalFixtureDefinition{}, false
}

// ReferenceClinicalFixtureSHA256 returns the frozen V1 payload hash.
func ReferenceClinicalFixtureSHA256(id ClinicalFixtureID) (string, bool) {
	value, ok := clinicalFixtureSHA256V1[id]
	return value, ok
}

// NewClinicalFixture materializes one deterministic little-endian int16
// volume. It contains no DICOM metadata and therefore cannot contain PHI.
func NewClinicalFixture(id ClinicalFixtureID, generation uint64) (MaterializedVolume, error) {
	definition, ok := ClinicalFixtureDefinitionByID(id)
	if !ok {
		return MaterializedVolume{}, fmt.Errorf("qualification: unknown clinical fixture %q", id)
	}
	if generation == 0 {
		return MaterializedVolume{}, fmt.Errorf("qualification: zero volume generation")
	}
	descriptor, err := clinicalFixtureDescriptor(definition, generation)
	if err != nil {
		return MaterializedVolume{}, err
	}
	voxelCount := uint64(definition.Dimensions[0]) *
		uint64(definition.Dimensions[1]) * uint64(definition.Dimensions[2])
	if voxelCount > uint64(math.MaxInt)/2 {
		return MaterializedVolume{}, fmt.Errorf("qualification: clinical fixture exceeds Go slice capacity")
	}
	payload := make([]byte, int(voxelCount*2))
	for z := uint32(0); z < definition.Dimensions[2]; z++ {
		for y := uint32(0); y < definition.Dimensions[1]; y++ {
			for x := uint32(0); x < definition.Dimensions[0]; x++ {
				value := clinicalFixtureValue(id, definition, x, y, z)
				offset := uint64(z)*descriptor.SliceStrideBytes +
					uint64(y)*descriptor.RowStrideBytes + uint64(x)*2
				binary.LittleEndian.PutUint16(payload[offset:offset+2], uint16(value))
			}
		}
	}
	result := MaterializedVolume{
		name:       string(id),
		descriptor: descriptor,
		payload:    payload,
		sha256:     sha256.Sum256(payload),
	}
	if expected, frozen := ReferenceClinicalFixtureSHA256(id); frozen && result.PayloadSHA256() != expected {
		return MaterializedVolume{}, fmt.Errorf(
			"qualification: clinical fixture %q SHA-256 %s, want %s",
			id, result.PayloadSHA256(), expected,
		)
	}
	return result, nil
}

func clinicalFixtureDescriptor(
	definition ClinicalFixtureDefinition,
	generation uint64,
) (dicomrender.VolumeDescriptor, error) {
	dimensions := definition.Dimensions
	for axis, dimension := range dimensions {
		if dimension == 0 {
			return dicomrender.VolumeDescriptor{}, fmt.Errorf("qualification: zero dimension at axis %d", axis)
		}
		if !positiveFinite(definition.SpacingMM[axis]) {
			return dicomrender.VolumeDescriptor{}, fmt.Errorf("qualification: invalid spacing at axis %d", axis)
		}
	}
	rowBytes := uint64(dimensions[0]) * 2
	sliceBytes := uint64(dimensions[1]) * rowBytes
	byteLength := uint64(dimensions[2]) * sliceBytes
	sx, sy, sz := definition.SpacingMM[0], definition.SpacingMM[1], definition.SpacingMM[2]
	descriptor := dicomrender.VolumeDescriptor{
		ContractVersion:  dicomrender.VolumeSnapshotContractVersion,
		HeaderSize:       dicomrender.VolumeSnapshotHeaderSizeV1,
		VolumeGeneration: generation,
		Derivation:       dicomrender.VolumeDerivationNormalized,
		Dimensions:       dimensions,
		Components:       1,
		ScalarFormat:     dicomrender.VolumeScalarI16StoredLE,
		SampleDomain:     dicomrender.VolumeSampleDomainStored,
		RowStrideBytes:   rowBytes,
		SliceStrideBytes: sliceBytes,
		ByteLength:       byteLength,
		RescaleSlope:     1,
		RescaleIntercept: 0,
		SpacingMM:        definition.SpacingMM,
		IndexToPatientLPS: dicomrender.GeometryAffine{
			sx, 0, 0, 0,
			0, sy, 0, 0,
			0, 0, sz, 0,
			0, 0, 0, 1,
		},
		PatientLPSToIndex: dicomrender.GeometryAffine{
			1 / sx, 0, 0, 0,
			0, 1 / sy, 0, 0,
			0, 0, 1 / sz, 0,
			0, 0, 0, 1,
		},
	}
	if err := dicomrender.ValidateVolumeDescriptor(descriptor); err != nil {
		return dicomrender.VolumeDescriptor{}, fmt.Errorf("qualification: clinical fixture descriptor: %w", err)
	}
	return descriptor, nil
}

func clinicalFixtureValue(
	id ClinicalFixtureID,
	definition ClinicalFixtureDefinition,
	x, y, z uint32,
) int16 {
	w, h, d := definition.Dimensions[0], definition.Dimensions[1], definition.Dimensions[2]
	switch id {
	case ClinicalFixtureRampHU:
		if w <= 1 {
			return -1400
		}
		return int16(-1400 + int64(x)*3200/int64(w-1))
	case ClinicalFixtureSteps:
		sentinels := [...]int16{-1200, -1000, -800, -550, -300, 0, 200, 350, 700, 1200, 2500}
		index := int(uint64(x) * uint64(len(sentinels)) / uint64(w))
		if index >= len(sentinels) {
			index = len(sentinels) - 1
		}
		return sentinels[index]
	case ClinicalFixtureConcentricSpheres:
		distance := normalizedRadiusSquared(x, y, z, w, h, d, definition.SpacingMM)
		switch {
		case distance <= 0.10*0.10:
			return 1350
		case distance <= 0.22*0.22:
			return 420
		case distance <= 0.38*0.38:
			return 60
		default:
			return -1000
		}
	case ClinicalFixtureContrastTubes:
		cx, cy := float64(w-1)/2, float64(h-1)/2
		dx1, dy1 := float64(x)-cx*0.55, float64(y)-cy
		dx2, dy2 := float64(x)-cx*1.45, float64(y)-cy
		if dx1*dx1+dy1*dy1 <= 2.4*2.4 {
			return 1100
		}
		if dx2*dx2+dy2*dy2 <= 3.2*3.2 {
			return 520
		}
		// A transverse branch intersects both longitudinal tubes.
		if math.Abs(float64(y)-cy) <= 1.25 && math.Abs(float64(z)-float64(d-1)/2) <= 1.25 {
			return 760
		}
		return -850
	case ClinicalFixtureAirStructure:
		distance := normalizedRadiusSquared(x, y, z, w, h, d, definition.SpacingMM)
		if distance > 0.40*0.40 {
			return -1000
		}
		airDX := (float64(x) - float64(w)*0.57) / float64(w)
		airDY := (float64(y) - float64(h)*0.48) / float64(h)
		airDZ := (float64(z) - float64(d)*0.50) / float64(d)
		airDistance := airDX*airDX + airDY*airDY + airDZ*airDZ
		if airDistance <= 0.16*0.16 {
			return -980
		}
		if airDistance <= 0.20*0.20 {
			return 720
		}
		return 65
	case ClinicalFixtureSliceImpulse:
		impulseX := uint32(1 + (int(z)*3)%int(w-2))
		impulseY := uint32(1 + (int(z)*5)%int(h-2))
		if x == impulseX && y == impulseY {
			return int16(500 + int(z)*350)
		}
		return 0
	case ClinicalFixtureUniform:
		return 42
	case ClinicalFixtureAnisotropic:
		distance := normalizedRadiusSquared(x, y, z, w, h, d, definition.SpacingMM)
		switch {
		case distance <= 0.16*0.16:
			return 920
		case distance <= 0.36*0.36:
			return 240
		default:
			return -1000
		}
	default:
		return 0
	}
}

// normalizedRadiusSquared measures in physical space and normalizes by the
// largest physical extent, making the anisotropic fixture geometrically
// meaningful rather than merely changing descriptor metadata.
func normalizedRadiusSquared(
	x, y, z, w, h, d uint32,
	spacing [3]float64,
) float64 {
	extents := [3]float64{
		float64(w-1) * spacing[0],
		float64(h-1) * spacing[1],
		float64(d-1) * spacing[2],
	}
	maximum := math.Max(extents[0], math.Max(extents[1], extents[2]))
	if maximum <= 0 {
		return 0
	}
	dx := (float64(x)*spacing[0] - extents[0]/2) / maximum
	dy := (float64(y)*spacing[1] - extents[1]/2) / maximum
	dz := (float64(z)*spacing[2] - extents[2]/2) / maximum
	return dx*dx + dy*dy + dz*dz
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
