package vps

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
)

func Test_VolumetricPresentationState_round_trips_supported_sop_classes(t *testing.T) {
	sopClasses := []string{
		GrayscalePlanarMPRVolumetricPresentationStateStorage,
		CompositingPlanarMPRVolumetricPresentationStateStorage,
		VolumeRenderingVolumetricPresentationStateStorage,
		SegmentedVolumeRenderingVolumetricPresentationStateStorage,
		MultipleVolumeRenderingVolumetricPresentationStateStorage,
	}
	for _, sopClass := range sopClasses {
		t.Run(sopClass, func(t *testing.T) {
			// Given: a VPS referencing one input set and a render preset.
			state := State{
				SOPClassUID:       sopClass,
				SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.9.1",
				StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.9.study",
				SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.9.series",
				Inputs: []Input{{
					Number:      1,
					InputSetUID: "1.2.826.0.1.3680043.9.7433.input.1",
					ReferencedInstances: []ReferencedInstance{{
						SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
						SOPInstanceUID: "1.2.3.ct.1",
					}},
				}},
				RenderPresetName: render.PresetBonesSkin1,
				Camera: Camera{
					Target:   render.Vec3{X: 1, Y: 2, Z: 3},
					Yaw:      0.25,
					Pitch:    0.5,
					Roll:     0.1,
					Distance: 400,
					FovY:     0.8,
				},
			}
			file, err := Write(&state)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if creator, ok := file.Dataset.GetString(tagPrivateCreator); !ok || creator != privateCreator {
				t.Fatalf("private creator = %q ok=%v, want %q", creator, ok, privateCreator)
			}
			inputs, ok := file.Dataset.GetSequence(tagVPSInputSequence)
			if !ok || len(inputs) != 1 {
				t.Fatalf("VPS input sequence ok=%v len=%d, want one item", ok, len(inputs))
			}
			inputNumber, ok := inputs[0].Get(tagVPSInputNumber)
			if !ok || inputNumber.VR() != core.VRUS {
				t.Fatalf("VPS input number VR = %s ok=%v, want US", inputNumber.VR(), ok)
			}
			if _, nested := inputs[0].GetSequence(tagVPSInputSetSequence); nested {
				t.Fatal("VPS Input Set Sequence nested inside input item, want root-level sequence")
			}
			inputSets, ok := file.Dataset.GetSequence(tagVPSInputSetSequence)
			if !ok || len(inputSets) != 1 {
				t.Fatalf("VPS input set sequence ok=%v len=%d, want one root item", ok, len(inputSets))
			}
			refs, ok := inputSets[0].GetSequence(tagReferencedImageSequence)
			if !ok || len(refs) != 1 {
				t.Fatalf("Referenced Image Sequence ok=%v len=%d, want one item", ok, len(refs))
			}
			if crop, ok := file.Dataset.GetString(tagGlobalCrop); !ok || crop != "NO" {
				t.Fatalf("Global Crop = %q ok=%v, want NO", crop, ok)
			}
			var encoded bytes.Buffer
			if err := object.WriteFile(&encoded, file); err != nil {
				t.Fatalf("object.WriteFile: %v", err)
			}

			// When: the VPS is read and applied to renderer state.
			readFile, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
			if err != nil {
				t.Fatalf("object.ReadFile: %v", err)
			}
			roundTrip, err := Read(readFile.Dataset)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			applied, err := Apply(roundTrip)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			// Then: the SOP class, inputs, preset, and camera survive.
			if roundTrip.SOPClassUID != sopClass {
				t.Fatalf("SOPClassUID = %q, want %q", roundTrip.SOPClassUID, sopClass)
			}
			if applied.Preset.Name != render.PresetBonesSkin1 {
				t.Fatalf("Preset = %q, want %q", applied.Preset.Name, render.PresetBonesSkin1)
			}
			if len(applied.Inputs) != 1 || len(applied.Inputs[0].ReferencedInstances) != 1 {
				t.Fatalf("Inputs = %+v, want one referenced instance", applied.Inputs)
			}
			if applied.Camera.Target != (render.Vec3{X: 1, Y: 2, Z: 3}) {
				t.Fatalf("Camera.Target = %+v, want 1/2/3", applied.Camera.Target)
			}
		})
	}
}

func TestVolumetricPresentationStateRejectsUnrepresentedStandardPayload(t *testing.T) {
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, VolumeRenderingVolumetricPresentationStateStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.826.0.1.3680043.9.7433.312.1"),
		derivedio.CS(core.NewTag(0x0070, 0x1602), "PERSPECTIVE"),
	)

	_, err := Read(dataset)
	if !errors.Is(err, ErrUnsupportedPayload) {
		t.Fatalf("Read standard VPS error = %v, want ErrUnsupportedPayload", err)
	}
}

func TestVolumetricPresentationStateReadsLegacyPrivatePayloadWithoutCreator(t *testing.T) {
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, VolumeRenderingVolumetricPresentationStateStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.826.0.1.3680043.9.7433.312.2"),
		derivedio.LO(tagRenderPresetName, render.PresetMIPBW),
		derivedio.DS(tagCameraTarget, 1, 2, 3),
	)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read legacy private VPS: %v", err)
	}
	if state.RenderPresetName != render.PresetMIPBW || state.Camera.Target != (render.Vec3{X: 1, Y: 2, Z: 3}) {
		t.Fatalf("legacy private state = %+v", state)
	}
}

func TestVolumetricPresentationStateRejectsPrivateCreatorWithoutPayload(t *testing.T) {
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, VolumeRenderingVolumetricPresentationStateStorage),
		derivedio.LO(tagPrivateCreator, privateCreator),
	)

	_, err := Read(dataset)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Read creator-only VPS error = %v, want ErrInvalidObject", err)
	}
}
