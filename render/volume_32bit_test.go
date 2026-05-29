package render

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestVolumePreparationDecodesSigned32BitModality(t *testing.T) {
	stored := [][]int32{
		{-2147483648, -16777216, 0, 2147483520},
		{-1073741824, -8388608, 16777216, 1073741824},
	}
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z, values := range stored {
		pixels := make([]byte, 4*len(values))
		for index, value := range values {
			binary.LittleEndian.PutUint32(pixels[4*index:], uint32(value))
		}
		stack.Frames = append(stack.Frames, &Frame{
			Metadata: pixeldata.Metadata{
				Rows: 2, Columns: 2, SamplesPerPixel: 1,
				BitsAllocated: 32, BitsStored: 32, HighBit: 31, PixelRepresentation: 1,
				PhotometricInterpretation: "MONOCHROME2",
			},
			ByteOrder:        binary.LittleEndian,
			PixelBytes:       pixels,
			Rescale:          Rescale{Slope: 0.5, Intercept: 128},
			ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
			ImagePosition:    []float64{0, 0, float64(z)},
		})
	}

	volume, err := BuildVolume(stack)
	if err != nil {
		t.Fatalf("BuildVolume() error = %v", err)
	}
	defer volume.Close()
	if _, err := volume.PrepareSnapshotContext(context.Background()); err != nil {
		t.Fatalf("PrepareSnapshotContext() error = %v", err)
	}
	lease, err := volume.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot() error = %v", err)
	}
	defer lease.Release()
	snapshot, err := lease.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	tests := []struct {
		x, y, z uint32
		want    float32
	}{
		{x: 0, y: 0, z: 0, want: -1073741696},
		{x: 1, y: 0, z: 0, want: -8388480},
		{x: 1, y: 1, z: 0, want: 1073741888},
		{x: 0, y: 1, z: 1, want: 8388736},
	}
	for _, test := range tests {
		got, ok := snapshot.ModalityAt(test.x, test.y, test.z)
		if !ok || got != float64(test.want) {
			t.Errorf("ModalityAt(%d,%d,%d) = %v/%t, want %v/true", test.x, test.y, test.z, got, ok, test.want)
		}
	}
}

func TestRegularGridResamplesUnsigned32BitModality(t *testing.T) {
	stored := []uint32{0, 2147483648, 4294967295}
	positions := []float64{0, 1, 3}
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for index, value := range stored {
		pixels := make([]byte, 2*2*4)
		for pixel := 0; pixel < 4; pixel++ {
			binary.BigEndian.PutUint32(pixels[4*pixel:], value)
		}
		stack.Frames = append(stack.Frames, &Frame{
			Metadata: pixeldata.Metadata{
				Rows: 2, Columns: 2, SamplesPerPixel: 1,
				BitsAllocated: 32, BitsStored: 32, HighBit: 31,
				PhotometricInterpretation: "MONOCHROME2",
			},
			ByteOrder:        binary.BigEndian,
			PixelBytes:       pixels,
			Rescale:          Rescale{Slope: 1},
			ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
			ImagePosition:    []float64{0, 0, positions[index]},
		})
	}

	volume, err := BuildVolume(stack)
	if err != nil {
		t.Fatalf("BuildVolume() error = %v", err)
	}
	defer volume.Close()
	if !volume.Geometry().RequiresResampling {
		t.Fatal("irregular source did not require resampling")
	}
	regular, err := volume.RegularGrid()
	if err != nil {
		t.Fatalf("RegularGrid() error = %v", err)
	}
	if regular == volume || regular.Depth != 4 {
		t.Fatalf("RegularGrid() = source:%t depth:%d, want distinct depth 4", regular == volume, regular.Depth)
	}
	lease, err := regular.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot() error = %v", err)
	}
	defer lease.Release()
	snapshot, err := lease.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	want := []float32{0, 2147483648, 3221225472, 4294967296}
	for z, expected := range want {
		got, ok := snapshot.ModalityAt(0, 0, uint32(z))
		if !ok || got != float64(expected) {
			t.Errorf("resampled ModalityAt(0,0,%d) = %v/%t, want %v/true", z, got, ok, expected)
		}
	}
}
