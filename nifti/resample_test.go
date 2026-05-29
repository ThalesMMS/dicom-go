package nifti_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/nifti"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestWriteIrregularGeometryResamplesOnlyWhenExplicitlyEnabled(t *testing.T) {
	files := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1, 2}}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{3, 4}}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 5}, pixels: []byte{5, 6}}),
	}
	options := nifti.DefaultOptions()
	options.Geometry = nifti.GeometryResampleLinear
	var output bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &output, files, options)
	if err != nil {
		t.Fatalf("WriteFiles(resample) error = %v", err)
	}
	if !report.Resampled || report.Interpolation != "linear" || report.Datatype != nifti.DatatypeFloat32 {
		t.Fatalf("resample report = resampled %v interpolation %q datatype %d", report.Resampled, report.Interpolation, report.Datatype)
	}
	if !report.Sidecar.Resampled || report.Sidecar.Interpolation != "linear" {
		t.Fatalf("sidecar resample provenance = %+v", report.Sidecar)
	}
	if output.Len() <= 352 {
		t.Fatalf("resampled output length = %d", output.Len())
	}
}

func TestWriteResamplesUnsigned32BitStoredPixels(t *testing.T) {
	files := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, rows: 1, columns: 1, bitsAllocated: 32, bitsStored: 32, highBit: 31, pixels: uint32Pixel(3_000_000_000)}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, rows: 1, columns: 1, bitsAllocated: 32, bitsStored: 32, highBit: 31, pixels: uint32Pixel(3_500_000_000)}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 5}, rows: 1, columns: 1, bitsAllocated: 32, bitsStored: 32, highBit: 31, pixels: uint32Pixel(4_000_000_000)}),
	}
	options := nifti.DefaultOptions()
	options.Geometry = nifti.GeometryResampleLinear
	var output bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &output, files, options)
	if err != nil {
		t.Fatalf("WriteFiles(unsigned32 resample) error = %v", err)
	}
	if !report.Resampled || report.Datatype != nifti.DatatypeFloat32 || output.Len() <= 352 {
		t.Fatalf("unsigned32 resample report = %+v bytes=%d", report, output.Len())
	}
}

func TestWriteGantryTiltPlansTheExpandedRegularGrid(t *testing.T) {
	files := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, spacing: [2]float64{1, 1}, rows: 2, columns: 2, pixels: []byte{1, 2, 3, 4}}),
		testSlice(t, imageSpec{position: [3]float64{0, 1, 2}, spacing: [2]float64{1, 1}, rows: 2, columns: 2, pixels: []byte{5, 6, 7, 8}}),
		testSlice(t, imageSpec{position: [3]float64{0, 2, 4}, spacing: [2]float64{1, 1}, rows: 2, columns: 2, pixels: []byte{9, 10, 11, 12}}),
	}
	options := nifti.DefaultOptions()
	options.Geometry = nifti.GeometryResampleLinear
	var output bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &output, files, options)
	if err != nil {
		t.Fatalf("WriteFiles(gantry tilt) error = %v", err)
	}
	if report.Dimensions != [4]int{2, 4, 3, 1} {
		t.Fatalf("regular-grid dimensions = %v, want [2 4 3 1]", report.Dimensions)
	}
	if !report.Resampled || output.Len() != 352+2*4*3*4 {
		t.Fatalf("regular-grid report = %+v bytes=%d", report, output.Len())
	}
}

func uint32Pixel(value uint32) []byte {
	encoded := make([]byte, 4)
	binary.LittleEndian.PutUint32(encoded, value)
	return encoded
}
