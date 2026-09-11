package qualification

import (
	"encoding/binary"
	"fmt"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

// RenderStack adapts an immutable generated fixture to the ordinary dicom-go
// slice-stack input. Each frame owns its bytes, so later backend preparation
// cannot mutate the retained fixture or another renderer's input.
func (v MaterializedVolume) RenderStack() (*dicomrender.Stack, error) {
	if err := v.Verify(); err != nil {
		return nil, err
	}
	descriptor := v.Descriptor()
	if descriptor.ScalarFormat != dicomrender.VolumeScalarI16StoredLE ||
		descriptor.SampleDomain != dicomrender.VolumeSampleDomainStored ||
		descriptor.Components != 1 {
		return nil, fmt.Errorf("qualification: generated render stack requires stored mono int16")
	}
	if descriptor.Dimensions[0] > uint32(^uint16(0)) || descriptor.Dimensions[1] > uint32(^uint16(0)) {
		return nil, fmt.Errorf("qualification: fixture dimensions exceed DICOM frame fields")
	}
	frames := make([]*dicomrender.Frame, descriptor.Dimensions[2])
	payload := v.CopyPayload()
	for z := uint32(0); z < descriptor.Dimensions[2]; z++ {
		start := uint64(z) * descriptor.SliceStrideBytes
		end := start + descriptor.SliceStrideBytes
		if end > uint64(len(payload)) {
			return nil, fmt.Errorf("qualification: fixture slice %d exceeds payload", z)
		}
		origin := affineFixturePoint(descriptor.IndexToPatientLPS, 0, 0, float64(z))
		frames[z] = &dicomrender.Frame{
			SOPInstanceUID: fmt.Sprintf("1.2.826.0.1.3680043.10.5432.886.%d", z+1),
			FrameIndex:     int(z),
			Metadata: pixeldata.Metadata{
				Rows:                      uint16(descriptor.Dimensions[1]),
				Columns:                   uint16(descriptor.Dimensions[0]),
				SamplesPerPixel:           1,
				BitsAllocated:             16,
				BitsStored:                16,
				HighBit:                   15,
				PixelRepresentation:       1,
				NumberOfFrames:            1,
				PhotometricInterpretation: "MONOCHROME2",
			},
			ByteOrder:        binary.LittleEndian,
			PixelBytes:       append([]byte(nil), payload[start:end]...),
			DefaultWindow:    dicomrender.WindowLevel{Center: 200, Width: 244},
			Rescale:          dicomrender.Rescale{Slope: descriptor.RescaleSlope, Intercept: descriptor.RescaleIntercept},
			ImagePosition:    []float64{origin[0], origin[1], origin[2]},
			ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
			PixelSpacing:     []float64{descriptor.SpacingMM[1], descriptor.SpacingMM[0]},
			SliceThickness:   descriptor.SpacingMM[2],
			SliceLocation:    origin[2],
			SliceLocationOK:  true,
			InstanceNumber:   int(z + 1),
			Sort:             origin[2],
		}
	}
	return &dicomrender.Stack{
		UID:            "1.2.826.0.1.3680043.10.5432.886",
		StudyUID:       "1.2.826.0.1.3680043.10.5432.878",
		Modality:       "CT",
		BodyPart:       "SYNTHETIC",
		DefaultWindow:  dicomrender.WindowLevel{Center: 200, Width: 244},
		PixelSpacing:   []float64{descriptor.SpacingMM[1], descriptor.SpacingMM[0]},
		SliceThickness: descriptor.SpacingMM[2],
		Frames:         frames,
	}, nil
}

// BuildRenderVolume constructs the same CPU volume path used by the viewer.
// The caller owns the returned volume and must close it.
func (v MaterializedVolume) BuildRenderVolume() (*dicomrender.Volume, error) {
	stack, err := v.RenderStack()
	if err != nil {
		return nil, err
	}
	return dicomrender.BuildVolume(stack)
}

func affineFixturePoint(affine dicomrender.GeometryAffine, x, y, z float64) [3]float64 {
	return [3]float64{
		affine[0]*x + affine[1]*y + affine[2]*z + affine[3],
		affine[4]*x + affine[5]*y + affine[6]*z + affine[7],
		affine[8]*x + affine[9]*y + affine[10]*z + affine[11],
	}
}
