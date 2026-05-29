package render

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
)

func TestFreezeViewStateOwnsVariableMembers(t *testing.T) {
	state := validVRViewState()
	frozen, err := FreezeViewState(state)
	if err != nil {
		t.Fatal(err)
	}
	state.VR.ClippingPlanesLPS[0][0] = 9
	state.VR.TransferLUT.Samples[0].R = 1
	if frozen.VR.ClippingPlanesLPS[0][0] != 1 {
		t.Fatal("frozen clipping planes alias caller memory")
	}
	if frozen.VR.TransferLUT.Samples[0].R != 0 {
		t.Fatal("frozen transfer LUT aliases caller memory")
	}
}

func TestValidateViewStateBoundsAndCubic(t *testing.T) {
	state := validMPRViewState()
	state.MPR.Interpolation = InterpolationCubic
	if err := ValidateViewState(state); err != nil {
		t.Fatalf("cubic MPR rejected: %v", err)
	}
	state.OutputWidth = MaxViewOutputDimension + 1
	if err := ValidateViewState(state); !errors.Is(err, ErrInvalidViewState) {
		t.Fatalf("oversized output error = %v", err)
	}
}

func TestValidateVRGradientOpacityScale(t *testing.T) {
	state := validVRViewState()
	state.VR.GradientOpacityScale = 220
	if err := ValidateViewState(state); err != nil {
		t.Fatalf("gradient-opacity scale rejected: %v", err)
	}
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1)} {
		state.VR.GradientOpacityScale = invalid
		if err := ValidateViewState(state); !errors.Is(err, ErrInvalidViewState) {
			t.Fatalf("gradient-opacity scale %v error = %v", invalid, err)
		}
	}
}

func TestValidateViewStateRejectsFiniteInputsWithNonFiniteDerivedGeometry(t *testing.T) {
	mpr := validMPRViewState()
	mpr.MPR.PixelStepULPS = [3]float64{math.MaxFloat64, math.MaxFloat64, 0}
	mpr.MPR.PixelStepVLPS = [3]float64{math.MaxFloat64, -math.MaxFloat64, 0}
	mpr.OutputWidth, mpr.OutputHeight = 1, 1
	if err := ValidateViewState(mpr); err != nil {
		t.Fatalf("robustly normalizable single-pixel MPR basis rejected: %v", err)
	}
	mpr.OutputWidth, mpr.OutputHeight = 2, 2
	if err := ValidateViewState(mpr); !errors.Is(err, ErrInvalidViewState) {
		t.Fatalf("overflowing MPR derived extent error = %v", err)
	}

	mpr = validMPRViewState()
	mpr.OutputWidth, mpr.OutputHeight = 3, 1
	mpr.MPR.PixelOriginLPS[0] = math.Pow(2, 53) - 1
	if err := ValidateViewState(mpr); !errors.Is(err, ErrInvalidViewState) {
		t.Fatalf("last-pair float64 MPR collapse error = %v", err)
	}

	vr := validVRViewState()
	vr.VR.PositionLPS = [3]float64{-math.MaxFloat64, 0, 0}
	vr.VR.FocalPointLPS = [3]float64{math.MaxFloat64, 0, 0}
	if err := ValidateViewState(vr); !errors.Is(err, ErrInvalidViewState) {
		t.Fatalf("overflowing camera subtraction error = %v", err)
	}

	vr = validVRViewState()
	vr.VR.PositionLPS = [3]float64{}
	vr.VR.FocalPointLPS = [3]float64{1, 1, 1}
	vr.VR.ViewUpLPS = [3]float64{math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64}
	if err := ValidateViewState(vr); err != nil {
		t.Fatalf("robustly normalizable huge view-up rejected: %v", err)
	}
}

func TestValidateAffinePairRejectsNonFiniteIntermediateProducts(t *testing.T) {
	const huge = math.MaxFloat64
	forward := GeometryAffine{
		huge, huge, huge, 0,
		huge, huge, huge, 0,
		huge, huge, huge, 0,
		0, 0, 0, 1,
	}
	inverse := GeometryAffine{
		huge, -huge, huge, 0,
		huge, -huge, huge, 0,
		huge, -huge, huge, 0,
		0, 0, 0, 1,
	}
	if err := validateAffinePair(forward, inverse); err == nil {
		t.Fatal("affine pair with non-finite intermediate products was accepted")
	}
}

func TestValidateFrameOutputRejectsCapacityBeyondSliceAndEmptyBackend(t *testing.T) {
	state := validMPRViewState()
	output := FrameOutput{
		ContractVersion:        FrameOutputContractVersion,
		StructSize:             FrameOutputHeaderSizeV1,
		Format:                 FrameFormatGrayF32TopLeft,
		Width:                  2,
		Height:                 2,
		StrideBytes:            8,
		CapacityBytes:          16,
		WrittenBytes:           16,
		VolumeGeneration:       state.VolumeGeneration,
		ViewGeneration:         state.ViewGeneration,
		PresentationGeneration: state.PresentationGeneration,
		BackendID:              "cpu-test",
		Pixels:                 make([]byte, 16),
	}
	if err := ValidateFrameOutput(output, state); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}
	output.CapacityBytes = 17
	if err := ValidateFrameOutput(output, state); err == nil {
		t.Fatal("capacity larger than slice accepted")
	}
	output.CapacityBytes = 16
	output.BackendID = ""
	if err := ValidateFrameOutput(output, state); err == nil {
		t.Fatal("empty backend accepted")
	}
	output.BackendID = "cpu-test"
	output.Width = MaxViewOutputDimension + 1
	if err := ValidateFrameOutput(output, ViewState{}); err == nil {
		t.Fatal("oversized backend-neutral frame accepted")
	}
	output.Width = 2
	output.BackendID = string(make([]byte, MaxFrameBackendIDBytes+1))
	if err := ValidateFrameOutput(output, ViewState{}); err == nil {
		t.Fatal("oversized backend id accepted")
	}
}

func TestFrameOutputV1HeaderSizeIncludesWarningBits(t *testing.T) {
	const frozenFrameOutputV1Bytes uint32 = 96
	if FrameOutputHeaderSizeV1 != frozenFrameOutputV1Bytes {
		t.Fatalf(
			"FrameOutputHeaderSizeV1 = %d, want frozen C++/Go V1 size %d",
			FrameOutputHeaderSizeV1,
			frozenFrameOutputV1Bytes,
		)
	}

	state := validMPRViewState()
	output := FrameOutput{
		ContractVersion:        FrameOutputContractVersion,
		StructSize:             FrameOutputHeaderSizeV1 - 1,
		Format:                 FrameFormatGrayF32TopLeft,
		Width:                  state.OutputWidth,
		Height:                 state.OutputHeight,
		StrideBytes:            uint64(state.OutputWidth) * 4,
		CapacityBytes:          uint64(state.OutputWidth) * uint64(state.OutputHeight) * 4,
		WrittenBytes:           uint64(state.OutputWidth) * uint64(state.OutputHeight) * 4,
		VolumeGeneration:       state.VolumeGeneration,
		ViewGeneration:         state.ViewGeneration,
		PresentationGeneration: state.PresentationGeneration,
		WarningBits:            FrameWarningHostRenderTiming,
		BackendID:              "header-size-regression",
		Pixels:                 make([]byte, int(state.OutputWidth*state.OutputHeight*4)),
	}
	if err := ValidateFrameOutput(output, state); err == nil {
		t.Fatal("FrameOutput accepted a V1 header that ends before WarningBits")
	}
}

func TestWriteModalityF32ContextStripsPaddingAndRescales(t *testing.T) {
	descriptor := testVolumeDescriptor(2, 2, 1)
	descriptor.ScalarFormat = VolumeScalarI16StoredLE
	descriptor.SampleDomain = VolumeSampleDomainStored
	descriptor.RescaleSlope = 2
	descriptor.RescaleIntercept = 10
	descriptor.RowStrideBytes = 8
	descriptor.SliceStrideBytes = 16
	descriptor.ByteLength = 16
	payload := make([]byte, 16)
	binary.LittleEndian.PutUint16(payload[0:], 1)
	binary.LittleEndian.PutUint16(payload[2:], 2)
	binary.LittleEndian.PutUint16(payload[8:], 3)
	binary.LittleEndian.PutUint16(payload[10:], 4)
	store := NewVolumeStore()
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Replace(VolumeInput{Descriptor: descriptor, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireCurrent()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release() //nolint:errcheck
	snapshot, err := lease.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var output byteCollector
	if err := snapshot.WriteModalityF32Context(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.data) != 16 {
		t.Fatalf("packed bytes = %d, want 16", len(output.data))
	}
	for index, want := range []float32{12, 14, 16, 18} {
		got := math.Float32frombits(binary.LittleEndian.Uint32(output.data[index*4:]))
		if got != want {
			t.Fatalf("sample %d = %v, want %v", index, got, want)
		}
	}
}

func TestWriteModalityF32ContextShortWriterAndCancellation(t *testing.T) {
	store := NewVolumeStore()
	t.Cleanup(func() { _ = store.Close() })
	descriptor := testVolumeDescriptor(8, 4, 2)
	if _, err := store.ReplaceFloat32(descriptor, make([]float32, 8*4*2)); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireCurrent()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release() //nolint:errcheck
	snapshot, _ := lease.Snapshot()
	if err := snapshot.WriteModalityF32To(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelWriter{cancel: cancel}
	if err := snapshot.WriteModalityF32Context(ctx, writer); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled stream error = %v", err)
	}
	if writer.writes != 1 {
		t.Fatalf("writes after cancellation = %d, want 1", writer.writes)
	}
}

func TestValidateModalityF32ConversionRejectsStoredRescaleOverflow(t *testing.T) {
	descriptor := testVolumeDescriptor(1, 1, 1)
	descriptor.ScalarFormat = VolumeScalarI16StoredLE
	descriptor.SampleDomain = VolumeSampleDomainStored
	descriptor.RowStrideBytes = 2
	descriptor.SliceStrideBytes = 2
	descriptor.ByteLength = 2
	descriptor.RescaleSlope = math.MaxFloat64
	if err := ValidateModalityF32Conversion(descriptor); !errors.Is(err, ErrInvalidVolumeSnapshot) {
		t.Fatalf("overflow conversion error = %v", err)
	}
}

func TestWriteModalityF32StreamsRawNonFiniteF32ForWorkerValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace func(*VolumeStore, VolumeDescriptor) error
	}{
		{
			name: "float32-payload",
			replace: func(store *VolumeStore, descriptor VolumeDescriptor) error {
				_, err := store.ReplaceFloat32(descriptor, []float32{float32(math.NaN())})
				return err
			},
		},
		{
			name: "byte-payload",
			replace: func(store *VolumeStore, descriptor VolumeDescriptor) error {
				payload := make([]byte, 4)
				binary.LittleEndian.PutUint32(payload, math.Float32bits(float32(math.Inf(1))))
				_, err := store.Replace(VolumeInput{Descriptor: descriptor, Payload: payload})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewVolumeStore()
			t.Cleanup(func() { _ = store.Close() })
			descriptor := testVolumeDescriptor(1, 1, 1)
			if err := test.replace(store, descriptor); err != nil {
				t.Fatal(err)
			}
			lease, err := store.AcquireCurrent()
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release() //nolint:errcheck
			snapshot, _ := lease.Snapshot()
			var output byteCollector
			if err := snapshot.WriteModalityF32To(&output); err != nil {
				t.Fatal(err)
			}
			value := math.Float32frombits(binary.LittleEndian.Uint32(output.data))
			if !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0) {
				t.Fatalf("raw non-finite payload became %v", value)
			}
		})
	}
}

func BenchmarkWriteModalityF32Streaming512x512x32(b *testing.B) {
	store := NewVolumeStore()
	defer store.Close() //nolint:errcheck
	descriptor := testVolumeDescriptor(512, 512, 32)
	if _, err := store.ReplaceFloat32(descriptor, make([]float32, 512*512*32)); err != nil {
		b.Fatal(err)
	}
	lease, _ := store.AcquireCurrent()
	defer lease.Release() //nolint:errcheck
	snapshot, _ := lease.Snapshot()
	b.SetBytes(512 * 512 * 32 * 4)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if err := snapshot.WriteModalityF32To(io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

type byteCollector struct{ data []byte }

func (w *byteCollector) Write(data []byte) (int, error) {
	w.data = append(w.data, data...)
	return len(data), nil
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

type cancelWriter struct {
	cancel context.CancelFunc
	writes int
}

func (w *cancelWriter) Write(data []byte) (int, error) {
	w.writes++
	w.cancel()
	return len(data), nil
}

func validMPRViewState() ViewState {
	return ViewState{
		ContractVersion:        ViewStateContractVersion,
		StructSize:             ViewStateHeaderSizeV1,
		Kind:                   ViewKindMPR,
		VolumeGeneration:       1,
		ViewGeneration:         1,
		PresentationGeneration: 1,
		OutputWidth:            2,
		OutputHeight:           2,
		MPR: MPRViewState{
			PixelStepULPS: [3]float64{1, 0, 0},
			PixelStepVLPS: [3]float64{0, 1, 0},
			Interpolation: InterpolationLinear,
			SlabMode:      SlabNone,
		},
	}
}

func validVRViewState() ViewState {
	return ViewState{
		ContractVersion:        ViewStateContractVersion,
		StructSize:             ViewStateHeaderSizeV1,
		Kind:                   ViewKindVR,
		VolumeGeneration:       1,
		ViewGeneration:         1,
		PresentationGeneration: 1,
		OutputWidth:            2,
		OutputHeight:           2,
		VR: VRViewState{
			PositionLPS:         [3]float64{0, 0, -10},
			FocalPointLPS:       [3]float64{0, 0, 0},
			ViewUpLPS:           [3]float64{0, 1, 0},
			Projection:          ProjectionPerspective,
			VerticalFOVRadians:  math.Pi / 4,
			NearMM:              0.1,
			FarMM:               100,
			Mode:                VRModeDVR,
			SampleDistanceMM:    1,
			ImageSampleDistance: 1,
			Ambient:             0.2,
			Diffuse:             0.7,
			Specular:            0.1,
			SpecularPower:       8,
			ClippingPlanesLPS:   []ClippingPlaneLPS{{1, 0, 0, 0}},
			TransferLUT: TransferLUT{
				DomainMin: -1000,
				DomainMax: 3000,
				Samples:   []TransferSample{{}, {R: 1, G: 1, B: 1, A: 1}},
			},
		},
	}
}
