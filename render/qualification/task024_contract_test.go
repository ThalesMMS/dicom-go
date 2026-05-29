package qualification

import (
	"encoding/binary"
	"reflect"
	"testing"

	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

func TestTask024FrozenMixedContract(t *testing.T) {
	if got, want := ReferenceSyntheticCTDimensions(), ([3]uint32{512, 512, 300}); got != want {
		t.Fatalf("reference dimensions=%v want=%v", got, want)
	}
	if ReferenceSyntheticCTSHA256 !=
		"1531f7807ee24b01a802eb6b0f75657d4faae6beb28ff7dd1b98682da5eca46b" {
		t.Fatalf("reference payload hash drifted: %s", ReferenceSyntheticCTSHA256)
	}
	sequence, err := FrozenMixedSequence()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateSequence(sequence); err != nil {
		t.Fatal(err)
	}
	gotIDs := make([]string, len(sequence))
	gotOperations := make([]Operation, len(sequence))
	gotGenerations := make([]uint64, len(sequence))
	for index, step := range sequence {
		gotIDs[index] = step.ID
		gotOperations[index] = step.Operation
		gotGenerations[index] = step.VolumeGeneration
	}
	wantIDs := []string{
		"mixed-01-2d-center",
		"mixed-02-mpr-axial",
		"mixed-03-vr-fit",
		"mixed-04-2d-jump",
		"mixed-05-mpr-oblique",
		"mixed-06-vr-transfer",
		"mixed-07-2d-replacement",
		"mixed-08-mpr-replacement",
	}
	wantOperations := []Operation{
		Operation2D,
		OperationMPR,
		OperationVR,
		Operation2D,
		OperationMPR,
		OperationVR,
		Operation2D,
		OperationMPR,
	}
	wantGenerations := []uint64{1, 1, 1, 1, 1, 1, 2, 2}
	if !reflect.DeepEqual(gotIDs, wantIDs) ||
		!reflect.DeepEqual(gotOperations, wantOperations) ||
		!reflect.DeepEqual(gotGenerations, wantGenerations) {
		t.Fatalf(
			"FrozenMixed drift:\nids=%v\noperations=%v\ngenerations=%v",
			gotIDs,
			gotOperations,
			gotGenerations,
		)
	}
	transfer := sequence[5].View.VR.TransferLUT
	for _, index := range []int{2, 5} {
		if got := sequence[index].View.VR.GradientOpacityScale; got != 220 {
			t.Fatalf("VR step %s gradient-opacity scale=%v want=220", sequence[index].ID, got)
		}
	}
	if transfer.DomainMin != -1024 ||
		transfer.DomainMax != 3071 ||
		!reflect.DeepEqual(
			transfer.Samples,
			[]dicomrender.TransferSample{
				{R: 0, G: 0, B: 0, A: 0},
				{R: 0.18, G: 0.08, B: 0.04, A: 0.08},
				{R: 0.72, G: 0.49, B: 0.32, A: 0.34},
				{R: 1, G: 0.92, B: 0.82, A: 0.82},
			},
		) {
		t.Fatalf("literal four-point transfer drifted: %+v", transfer)
	}
}

func TestTask024SyntheticFormulaAndWebGPUCapability(t *testing.T) {
	fixture, err := NewSyntheticCT([3]uint32{4, 3, 2}, 99)
	if err != nil {
		t.Fatal(err)
	}
	payload := fixture.CopyPayload()
	for z := uint32(0); z < 2; z++ {
		for y := uint32(0); y < 3; y++ {
			for x := uint32(0); x < 4; x++ {
				offset := ((z * 3 * 4) + (y * 4) + x) * 2
				got := int16(binary.LittleEndian.Uint16(payload[offset:]))
				want := int16(int64((uint64(x)*3+uint64(y)*5+uint64(z)*7)%4096) - 1024)
				if got != want {
					t.Fatalf("voxel (%d,%d,%d)=%d want=%d", x, y, z, got, want)
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
		t.Fatalf("real WebGPU capability not advertised exactly: %+v", capability)
	}
	unavailable := WebGPUVolumeRendererCapability(false, "worker handshake rejected")
	if err := unavailable.Validate(); err != nil {
		t.Fatal(err)
	}
	if unavailable.Status != CapabilityUnavailable ||
		unavailable.Reason != "worker handshake rejected" {
		t.Fatalf("failed worker probe capability = %+v", unavailable)
	}
}
