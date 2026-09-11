package qualification

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestClinicalFixtureCatalogIsFrozenDeterministicAndLoadable(t *testing.T) {
	wantIDs := []ClinicalFixtureID{
		ClinicalFixtureRampHU,
		ClinicalFixtureSteps,
		ClinicalFixtureConcentricSpheres,
		ClinicalFixtureContrastTubes,
		ClinicalFixtureAirStructure,
		ClinicalFixtureSliceImpulse,
		ClinicalFixtureUniform,
		ClinicalFixtureAnisotropic,
	}
	catalog := ClinicalFixtureCatalog()
	if len(catalog) != len(wantIDs) {
		t.Fatalf("catalog length = %d, want %d", len(catalog), len(wantIDs))
	}
	seenHashes := make(map[string]ClinicalFixtureID, len(catalog))
	for index, definition := range catalog {
		if definition.ID != wantIDs[index] {
			t.Fatalf("catalog[%d] = %q, want %q", index, definition.ID, wantIDs[index])
		}
		fixture, err := NewClinicalFixture(definition.ID, uint64(index+1))
		if err != nil {
			t.Fatalf("NewClinicalFixture(%q): %v", definition.ID, err)
		}
		if err := fixture.Verify(); err != nil {
			t.Fatalf("Verify(%q): %v", definition.ID, err)
		}
		if fixture.Name() != string(definition.ID) || fixture.Descriptor().Dimensions != definition.Dimensions ||
			fixture.Descriptor().SpacingMM != definition.SpacingMM {
			t.Fatalf("fixture %q metadata drifted", definition.ID)
		}
		frozenSHA, ok := ReferenceClinicalFixtureSHA256(definition.ID)
		if !ok {
			t.Errorf("fixture %q has no frozen SHA-256; generated %s", definition.ID, fixture.PayloadSHA256())
			continue
		}
		if fixture.PayloadSHA256() != frozenSHA {
			t.Errorf("fixture %q SHA-256 = %s, want %s", definition.ID, fixture.PayloadSHA256(), frozenSHA)
		}
		if previous, duplicate := seenHashes[frozenSHA]; duplicate {
			t.Errorf("fixtures %q and %q share payload hash %s", previous, definition.ID, frozenSHA)
		}
		seenHashes[frozenSHA] = definition.ID
		second, err := NewClinicalFixture(definition.ID, 99)
		if err != nil {
			t.Fatalf("second NewClinicalFixture(%q): %v", definition.ID, err)
		}
		if second.PayloadSHA256() != fixture.PayloadSHA256() {
			t.Errorf("fixture %q payload depends on generation", definition.ID)
		}
	}

	// Catalog callers cannot mutate the retained definitions.
	catalog[0].ID = "mutated"
	if ClinicalFixtureCatalog()[0].ID != ClinicalFixtureRampHU {
		t.Fatal("clinical fixture catalog leaked a mutable alias")
	}
}

func TestClinicalFixturesExerciseRequiredIntensityAndGeometryCases(t *testing.T) {
	ramp := mustClinicalFixture(t, ClinicalFixtureRampHU)
	rampPayload := ramp.CopyPayload()
	if got, want := fixtureValueAt(t, ramp, rampPayload, 0, 0, 0), int16(-1400); got != want {
		t.Fatalf("ramp lower = %d, want %d", got, want)
	}
	if got, want := fixtureValueAt(t, ramp, rampPayload, 31, 0, 0), int16(1800); got != want {
		t.Fatalf("ramp upper = %d, want %d", got, want)
	}

	steps := mustClinicalFixture(t, ClinicalFixtureSteps)
	stepsPayload := steps.CopyPayload()
	stepValues := make(map[int16]struct{})
	for x := uint32(0); x < steps.Descriptor().Dimensions[0]; x++ {
		stepValues[fixtureValueAt(t, steps, stepsPayload, x, 0, 0)] = struct{}{}
	}
	if len(stepValues) != 11 {
		t.Fatalf("step sentinel count = %d, want 11", len(stepValues))
	}

	uniform := mustClinicalFixture(t, ClinicalFixtureUniform)
	uniformPayload := uniform.CopyPayload()
	for z := uint32(0); z < uniform.Descriptor().Dimensions[2]; z++ {
		for y := uint32(0); y < uniform.Descriptor().Dimensions[1]; y++ {
			for x := uint32(0); x < uniform.Descriptor().Dimensions[0]; x++ {
				if got := fixtureValueAt(t, uniform, uniformPayload, x, y, z); got != 42 {
					t.Fatalf("uniform[%d,%d,%d] = %d, want 42", x, y, z, got)
				}
			}
		}
	}

	anisotropic := mustClinicalFixture(t, ClinicalFixtureAnisotropic)
	descriptor := anisotropic.Descriptor()
	if descriptor.SpacingMM != [3]float64{0.5, 0.8, 3} {
		t.Fatalf("anisotropic spacing = %v", descriptor.SpacingMM)
	}
	for axis, spacing := range descriptor.SpacingMM {
		if math.Abs(descriptor.IndexToPatientLPS[axis*5]-spacing) > 1e-12 ||
			math.Abs(descriptor.PatientLPSToIndex[axis*5]-1/spacing) > 1e-12 {
			t.Fatalf("anisotropic affine axis %d does not encode spacing", axis)
		}
	}
}

func TestSliceImpulseFixtureHasOneIndependentImpulsePerSlice(t *testing.T) {
	fixture := mustClinicalFixture(t, ClinicalFixtureSliceImpulse)
	payload := fixture.CopyPayload()
	descriptor := fixture.Descriptor()
	positions := make(map[[2]uint32]struct{})
	for z := uint32(0); z < descriptor.Dimensions[2]; z++ {
		var nonZero int
		var value int16
		var position [2]uint32
		for y := uint32(0); y < descriptor.Dimensions[1]; y++ {
			for x := uint32(0); x < descriptor.Dimensions[0]; x++ {
				candidate := fixtureValueAt(t, fixture, payload, x, y, z)
				if candidate != 0 {
					nonZero++
					value = candidate
					position = [2]uint32{x, y}
				}
			}
		}
		if nonZero != 1 || value != int16(500+int(z)*350) {
			t.Fatalf("slice %d has %d impulses with value %d", z, nonZero, value)
		}
		positions[position] = struct{}{}
	}
	if len(positions) < 5 {
		t.Fatalf("only %d distinct impulse positions; fixture would not expose Z mixing", len(positions))
	}
}

func TestNewClinicalFixtureRejectsUnknownIDAndZeroGeneration(t *testing.T) {
	if _, err := NewClinicalFixture("unknown", 1); err == nil {
		t.Fatal("unknown fixture ID succeeded")
	}
	if _, err := NewClinicalFixture(ClinicalFixtureRampHU, 0); err == nil {
		t.Fatal("zero generation succeeded")
	}
}

func TestClinicalFixtureBuildsOrdinaryCPUVolumeWithoutMetadataFiles(t *testing.T) {
	fixture := mustClinicalFixture(t, ClinicalFixtureRampHU)
	stack, err := fixture.RenderStack()
	if err != nil {
		t.Fatal(err)
	}
	if len(stack.Frames) != 8 || stack.Frames[0].Metadata.PhotometricInterpretation != "MONOCHROME2" ||
		stack.Frames[0].SOPInstanceUID == "" {
		t.Fatalf("unexpected generated stack: %+v", stack)
	}
	volume, err := fixture.BuildRenderVolume()
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close()
	if volume.Cols != 32 || volume.Rows != 8 || volume.Depth != 8 ||
		volume.ColSpacing != 1 || volume.RowSpacing != 1 || volume.SliceSpacing != 1 {
		t.Fatalf("unexpected generated volume geometry: %+v", volume)
	}
	minimum, maximum, ok := volume.HURange()
	if !ok || minimum != -1400 || maximum != 1800 {
		t.Fatalf("generated volume range = %.0f..%.0f, ok=%v", minimum, maximum, ok)
	}
}

func mustClinicalFixture(t *testing.T, id ClinicalFixtureID) MaterializedVolume {
	t.Helper()
	fixture, err := NewClinicalFixture(id, 1)
	if err != nil {
		t.Fatalf("NewClinicalFixture(%q): %v", id, err)
	}
	return fixture
}

func fixtureValueAt(t *testing.T, fixture MaterializedVolume, payload []byte, x, y, z uint32) int16 {
	t.Helper()
	descriptor := fixture.Descriptor()
	if x >= descriptor.Dimensions[0] || y >= descriptor.Dimensions[1] || z >= descriptor.Dimensions[2] {
		t.Fatalf("fixture coordinate [%d,%d,%d] outside %v", x, y, z, descriptor.Dimensions)
	}
	offset := uint64(z)*descriptor.SliceStrideBytes + uint64(y)*descriptor.RowStrideBytes + uint64(x)*2
	return int16(binary.LittleEndian.Uint16(payload[offset : offset+2]))
}
