package nifti_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/nifti"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	tagPatientName                = core.NewTag(0x0010, 0x0010)
	tagPatientID                  = core.NewTag(0x0010, 0x0020)
	tagImagePositionPatient       = core.NewTag(0x0020, 0x0032)
	tagImageOrientationPatient    = core.NewTag(0x0020, 0x0037)
	tagTemporalPositionIdentifier = core.NewTag(0x0020, 0x0100)
	tagNumberOfTemporalPositions  = core.NewTag(0x0020, 0x0105)
	tagPixelSpacing               = core.NewTag(0x0028, 0x0030)
	tagRescaleIntercept           = core.NewTag(0x0028, 0x1052)
	tagRescaleSlope               = core.NewTag(0x0028, 0x1053)
	tagFrameReferenceTime         = core.NewTag(0x0054, 0x1300)
	tagSharedFunctionalGroups     = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups   = core.NewTag(0x5200, 0x9230)
	tagPlanePositionSequence      = core.NewTag(0x0020, 0x9113)
	tagPlaneOrientationSequence   = core.NewTag(0x0020, 0x9116)
	tagPixelMeasuresSequence      = core.NewTag(0x0028, 0x9110)
)

const (
	testSeriesUID = "1.2.826.0.1.3680043.10.543.631.1"
	testFORUID    = "1.2.826.0.1.3680043.10.543.631.2"
	ctStorageUID  = "1.2.840.10008.5.1.4.1.1.2"
)

func TestWriteUnorderedMultiInstancePreservesVoxelOrderAndAffine(t *testing.T) {
	files := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{10, 20, 4}, pixels: []byte{50, 51}}),
		testSlice(t, imageSpec{position: [3]float64{10, 20, 0}, pixels: []byte{10, 11}}),
		testSlice(t, imageSpec{position: [3]float64{10, 20, 2}, pixels: []byte{30, 31}}),
	}

	var output bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFiles() error = %v", err)
	}
	if report.Dimensions != [4]int{2, 1, 3, 1} {
		t.Fatalf("dimensions = %v, want [2 1 3 1]", report.Dimensions)
	}
	if !report.InputReordered {
		t.Fatal("InputReordered = false, want true")
	}
	if report.Datatype != nifti.DatatypeUint8 || report.BitPix != 8 || report.VoxelOffset != 352 {
		t.Fatalf("encoding report = datatype %d bitpix %d offset %d", report.Datatype, report.BitPix, report.VoxelOffset)
	}
	if report.BytesWritten != int64(output.Len()) {
		t.Fatalf("BytesWritten = %d, output length = %d", report.BytesWritten, output.Len())
	}

	got := output.Bytes()
	requireNIfTIHeader(t, got, [4]int{2, 1, 3, 1}, nifti.DatatypeUint8, 8)
	wantAffine := [16]float64{
		-1, 0, 0, -10,
		0, -2, 0, -20,
		0, 0, 2, 0,
		0, 0, 0, 1,
	}
	requireAffine(t, report.IndexToRAS[:], wantAffine[:], 1e-6)
	sform := headerSForm(t, got)
	requireAffine(t, sform[:], wantAffine[:], 1e-6)
	if voxels := got[352:]; !bytes.Equal(voxels, []byte{10, 11, 30, 31, 50, 51}) {
		t.Fatalf("voxels = %v, want geometry-sorted [10 11 30 31 50 51]", voxels)
	}
}

func TestWritePatientOrientationsFromDICOMGeometry(t *testing.T) {
	for _, fixture := range patientOrientationFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			var output bytes.Buffer
			report, err := nifti.WriteFiles(context.Background(), &output, orientationVolume(t, fixture), nifti.DefaultOptions())
			if err != nil {
				t.Fatalf("WriteFiles(%s) error = %v", fixture.name, err)
			}
			requireAffine(t, report.IndexToRAS[:], fixture.affineRAS[:], 2e-6)
			sform := headerSForm(t, output.Bytes())
			requireAffine(t, sform[:], fixture.affineRAS[:], 2e-6)
			if got := output.Bytes()[352:]; !bytes.Equal(got, []byte{1, 2, 3, 4}) {
				t.Fatalf("%s voxel order = %v, want [1 2 3 4]", fixture.name, got)
			}
		})
	}
}

func TestWriteGZIPRoundTripsACompleteNIfTIStream(t *testing.T) {
	files := testUint8Volume(t, "MONOCHROME2")
	options := nifti.DefaultOptions()
	options.Compression = nifti.CompressionGZIP
	var compressed bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &compressed, files, options)
	if err != nil {
		t.Fatalf("WriteFiles(gzip) error = %v", err)
	}
	if report.Compression != nifti.CompressionGZIP || report.BytesWritten != int64(compressed.Len()) {
		t.Fatalf("gzip report = compression %v bytes %d, stream length %d", report.Compression, report.BytesWritten, compressed.Len())
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll(gzip) error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("gzip.Close() error = %v", err)
	}
	requireNIfTIHeader(t, decoded, [4]int{2, 1, 2, 1}, nifti.DatatypeUint8, 8)
	if !bytes.Equal(decoded[352:], []byte{1, 2, 3, 4}) {
		t.Fatalf("gzip voxels = %v, want [1 2 3 4]", decoded[352:])
	}
}

func TestWriteMONOCHROME1DoesNotApplyDisplayInversion(t *testing.T) {
	var output bytes.Buffer
	_, err := nifti.WriteFiles(context.Background(), &output, testUint8Volume(t, "MONOCHROME1"), nifti.DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFiles(MONOCHROME1) error = %v", err)
	}
	if got := output.Bytes()[352:]; !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("MONOCHROME1 voxels = %v, want quantitative stored values [1 2 3 4]", got)
	}
}

func TestWriteNormalizesShiftedSignedBigEndianPixels(t *testing.T) {
	files := []*object.File{
		testSlice(t, imageSpec{
			position: [3]float64{0, 0, 0}, rows: 1, columns: 1,
			bitsAllocated: 16, bitsStored: 12, highBit: 14, pixelRepresentation: 1,
			syntax: transfer.ExplicitVRBigEndian, pixels: []byte{0x7f, 0xf0},
		}),
		testSlice(t, imageSpec{
			position: [3]float64{0, 0, 2}, rows: 1, columns: 1,
			bitsAllocated: 16, bitsStored: 12, highBit: 14, pixelRepresentation: 1,
			syntax: transfer.ExplicitVRBigEndian, pixels: []byte{0x00, 0x38},
		}),
	}
	var output bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFiles(big-endian signed shifted) error = %v", err)
	}
	if report.Datatype != nifti.DatatypeInt16 || report.BitPix != 16 {
		t.Fatalf("datatype/bitpix = %d/%d, want int16/16", report.Datatype, report.BitPix)
	}
	voxels := output.Bytes()[352:]
	if len(voxels) != 4 {
		t.Fatalf("voxel byte length = %d, want 4", len(voxels))
	}
	got := []int16{
		int16(binary.LittleEndian.Uint16(voxels[0:2])),
		int16(binary.LittleEndian.Uint16(voxels[2:4])),
	}
	if got[0] != -2 || got[1] != 7 {
		t.Fatalf("normalized voxels = %v, want [-2 7]", got)
	}
}

func TestWriteScalingPoliciesPreserveOrApplyExactlyOnce(t *testing.T) {
	files := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{5, 6}, slope: float64Pointer(2), intercept: float64Pointer(-10)}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{7, 8}, slope: float64Pointer(2), intercept: float64Pointer(-10)}),
	}
	t.Run("preserve uniform header scaling", func(t *testing.T) {
		var output bytes.Buffer
		report, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
		if err != nil {
			t.Fatalf("WriteFiles(preserve) error = %v", err)
		}
		if report.ScalingPolicy != nifti.ScalingPreserveUniform || report.ScalingSlope != 2 || report.ScalingIntercept != -10 {
			t.Fatalf("scaling report = policy %v slope %v intercept %v", report.ScalingPolicy, report.ScalingSlope, report.ScalingIntercept)
		}
		if got := headerFloat32(output.Bytes(), 112); got != 2 {
			t.Fatalf("scl_slope = %v, want 2", got)
		}
		if got := headerFloat32(output.Bytes(), 116); got != -10 {
			t.Fatalf("scl_inter = %v, want -10", got)
		}
		if got := output.Bytes()[352:]; !bytes.Equal(got, []byte{5, 6, 7, 8}) {
			t.Fatalf("preserved stored voxels = %v, want [5 6 7 8]", got)
		}
	})

	for _, test := range []struct {
		name     string
		policy   nifti.ScalingPolicy
		datatype int16
		bitpix   int16
		width    int
	}{
		{name: "float32", policy: nifti.ScalingApplyFloat32, datatype: nifti.DatatypeFloat32, bitpix: 32, width: 4},
		{name: "float64", policy: nifti.ScalingApplyFloat64, datatype: nifti.DatatypeFloat64, bitpix: 64, width: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := nifti.DefaultOptions()
			options.Scaling = test.policy
			var output bytes.Buffer
			report, err := nifti.WriteFiles(context.Background(), &output, files, options)
			if err != nil {
				t.Fatalf("WriteFiles(%s) error = %v", test.name, err)
			}
			if report.Datatype != test.datatype || report.BitPix != test.bitpix || report.ScalingSlope != 1 || report.ScalingIntercept != 0 {
				t.Fatalf("applied scaling report = datatype %d bitpix %d slope %v intercept %v", report.Datatype, report.BitPix, report.ScalingSlope, report.ScalingIntercept)
			}
			voxels := output.Bytes()[352:]
			want := []float64{0, 2, 4, 6}
			for index, expected := range want {
				var got float64
				if test.width == 4 {
					got = float64(math.Float32frombits(binary.LittleEndian.Uint32(voxels[index*test.width:])))
				} else {
					got = math.Float64frombits(binary.LittleEndian.Uint64(voxels[index*test.width:]))
				}
				if got != expected {
					t.Fatalf("voxel %d = %v, want %v", index, got, expected)
				}
			}
		})
	}
}

func TestWriteEnhancedMultiframeMatchesEquivalentMultiInstanceVolume(t *testing.T) {
	enhanced := enhancedVolume(t,
		[][3]float64{{10, 20, 4}, {10, 20, 0}, {10, 20, 2}},
		[][]byte{{50, 51}, {10, 11}, {30, 31}},
	)
	multi := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{10, 20, 4}, pixels: []byte{50, 51}}),
		testSlice(t, imageSpec{position: [3]float64{10, 20, 0}, pixels: []byte{10, 11}}),
		testSlice(t, imageSpec{position: [3]float64{10, 20, 2}, pixels: []byte{30, 31}}),
	}
	var enhancedOutput, multiOutput bytes.Buffer
	enhancedReport, err := nifti.WriteFiles(context.Background(), &enhancedOutput, []*object.File{enhanced}, nifti.DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFiles(enhanced) error = %v", err)
	}
	multiReport, err := nifti.WriteFiles(context.Background(), &multiOutput, multi, nifti.DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFiles(multi-instance) error = %v", err)
	}
	if !bytes.Equal(enhancedOutput.Bytes(), multiOutput.Bytes()) {
		t.Fatalf("enhanced and multi-instance NIfTI streams differ\nenhanced=%v\nmulti=%v", enhancedOutput.Bytes(), multiOutput.Bytes())
	}
	if enhancedReport.Dimensions != multiReport.Dimensions || enhancedReport.IndexToRAS != multiReport.IndexToRAS {
		t.Fatalf("enhanced report differs from multi-instance: enhanced=%+v multi=%+v", enhancedReport, multiReport)
	}
}

func TestWriteExplicitUniform4DPreservesTemporalOrderAndSpacing(t *testing.T) {
	files := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{2}, rows: 1, columns: 1, temporalPosition: 1, frameReferenceMilliseconds: float64Pointer(0)}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{3}, rows: 1, columns: 1, temporalPosition: 2, frameReferenceMilliseconds: float64Pointer(1500)}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1}, rows: 1, columns: 1, temporalPosition: 1, frameReferenceMilliseconds: float64Pointer(0)}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{4}, rows: 1, columns: 1, temporalPosition: 2, frameReferenceMilliseconds: float64Pointer(1500)}),
	}
	var output bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFiles(4D) error = %v", err)
	}
	if report.Dimensions != [4]int{1, 1, 2, 2} {
		t.Fatalf("dimensions = %v, want [1 1 2 2]", report.Dimensions)
	}
	if got := headerInt16(output.Bytes(), 40); got != 4 {
		t.Fatalf("dim[0] = %d, want 4", got)
	}
	if got := headerFloat32(output.Bytes(), 92); got != 1.5 {
		t.Fatalf("pixdim[4] = %v seconds, want 1.5", got)
	}
	if units := output.Bytes()[123]; units != 10 {
		t.Fatalf("xyzt_units = %d, want mm|sec (10)", units)
	}
	if got := output.Bytes()[352:]; !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("4D voxels = %v, want time-major [t1z1 t1z2 t2z1 t2z2]", got)
	}
	if got := report.Sidecar.TemporalOffsetsSeconds; len(got) != 2 || got[0] != 0 || got[1] != 1.5 {
		t.Fatalf("sidecar temporal offsets = %v, want [0 1.5]", got)
	}
}

func TestWriteRejectsOccurrenceInferredAndIncompleteDeclaredTime(t *testing.T) {
	t.Run("mixed explicit position and occurrence fallback", func(t *testing.T) {
		files := []*object.File{
			testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1}, rows: 1, columns: 1, temporalPosition: 1, frameReferenceMilliseconds: float64Pointer(0)}),
			testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{2}, rows: 1, columns: 1, temporalPosition: 1, frameReferenceMilliseconds: float64Pointer(0)}),
			testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{3}, rows: 1, columns: 1, temporalPosition: 2, frameReferenceMilliseconds: float64Pointer(1500)}),
			testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{4}, rows: 1, columns: 1, frameReferenceMilliseconds: float64Pointer(2500)}),
		}
		var output bytes.Buffer
		_, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
		requireExportError(t, err, nifti.CodeTemporal, nifti.ErrUnsupportedTemporal)
		if output.Len() != 0 {
			t.Fatalf("occurrence-inferred time wrote %d bytes", output.Len())
		}
	})

	t.Run("declared positions are incomplete", func(t *testing.T) {
		files := []*object.File{
			testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1}, rows: 1, columns: 1, temporalPosition: 1}),
			testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{2}, rows: 1, columns: 1, temporalPosition: 1}),
		}
		var output bytes.Buffer
		_, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
		requireExportError(t, err, nifti.CodeTemporal, nifti.ErrUnsupportedTemporal)
		if output.Len() != 0 {
			t.Fatalf("incomplete declared time wrote %d bytes", output.Len())
		}
	})
}

func TestWriteStrictlyRejectsUnsafeGeometryAndMixedIdentity(t *testing.T) {
	t.Run("irregular spacing", func(t *testing.T) {
		files := []*object.File{
			testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1, 2}}),
			testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{3, 4}}),
			testSlice(t, imageSpec{position: [3]float64{0, 0, 5}, pixels: []byte{5, 6}}),
		}
		var output bytes.Buffer
		_, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
		requireExportError(t, err, nifti.CodeGeometry, nifti.ErrInvalidGeometry)
		if output.Len() != 0 {
			t.Fatalf("destination length = %d after planning rejection, want 0", output.Len())
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*imageSpec)
	}{
		{name: "series", mutate: func(spec *imageSpec) { spec.seriesUID = testSeriesUID + ".99" }},
		{name: "frame of reference", mutate: func(spec *imageSpec) { spec.frameOfReferenceUID = testFORUID + ".99" }},
	} {
		t.Run("mixed "+test.name, func(t *testing.T) {
			first := imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1, 2}}
			second := imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{3, 4}}
			test.mutate(&second)
			var output bytes.Buffer
			_, err := nifti.WriteFiles(context.Background(), &output, []*object.File{testSlice(t, first), testSlice(t, second)}, nifti.DefaultOptions())
			exportErr := requireExportError(t, err, nifti.CodeIdentity, nifti.ErrMixedIdentity)
			if exportErr.SourceIndex != 1 {
				t.Fatalf("SourceIndex = %d, want 1", exportErr.SourceIndex)
			}
			if output.Len() != 0 {
				t.Fatalf("destination length = %d after identity rejection, want 0", output.Len())
			}
		})
	}
}

func TestPlanWriteCancellationAndShortWriterCleanTemporarySpool(t *testing.T) {
	files := testUint8Volume(t, "MONOCHROME2")

	t.Run("cancellation during destination write", func(t *testing.T) {
		tempDir := t.TempDir()
		options := nifti.DefaultOptions()
		options.TempDir = tempDir
		plan, err := nifti.PlanVolume(context.Background(), nifti.NewFilesSource(files), options)
		if err != nil {
			t.Fatalf("PlanVolume() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		writer := &cancelAfterFirstWrite{cancel: cancel}
		_, err = plan.Write(ctx, writer)
		requireExportError(t, err, nifti.CodeCanceled, context.Canceled)
		requireEmptyDirectory(t, tempDir)
	})

	t.Run("short destination writer", func(t *testing.T) {
		tempDir := t.TempDir()
		options := nifti.DefaultOptions()
		options.TempDir = tempDir
		writer := &shortWriter{maximum: 17}
		report, err := nifti.WriteFiles(context.Background(), writer, files, options)
		requireExportError(t, err, nifti.CodeWrite, io.ErrShortWrite)
		if !reflect.DeepEqual(report, nifti.Report{}) {
			t.Fatalf("partial-write report = %+v, want zero report", report)
		}
		requireEmptyDirectory(t, tempDir)
	})
}

func TestWriteRedactsPHIFromOutputSidecarAndReplayErrors(t *testing.T) {
	const (
		patientName = "SECRET^PATIENT"
		patientID   = "MRN-631-SECRET"
		secretPath  = "/private/phi/SECRET-PATIENT.dcm"
	)
	files := []*object.File{
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1, 2}, patientName: patientName, patientID: patientID}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{3, 4}, patientName: patientName, patientID: patientID}),
	}

	t.Run("successful export and sidecar", func(t *testing.T) {
		var output bytes.Buffer
		report, err := nifti.WriteFiles(context.Background(), &output, files, nifti.DefaultOptions())
		if err != nil {
			t.Fatalf("WriteFiles() error = %v", err)
		}
		sidecar, err := report.Sidecar.Marshal(64 << 10)
		if err != nil {
			t.Fatalf("Sidecar.Marshal() error = %v", err)
		}
		for label, payload := range map[string][]byte{"NIfTI": output.Bytes(), "sidecar": sidecar} {
			for _, secret := range []string{patientName, patientID} {
				if bytes.Contains(payload, []byte(secret)) {
					t.Fatalf("%s leaked %q", label, secret)
				}
			}
		}
	})

	t.Run("replay failure", func(t *testing.T) {
		tempDir := t.TempDir()
		secretCause := errors.New("decoder failed for " + patientName + " at " + secretPath)
		opens := make([]int, len(files))
		source := nifti.SourceFunc{
			Count: len(files),
			OpenFile: func(_ context.Context, index int, _ object.ReadFileOptions) (nifti.OpenedFile, error) {
				opens[index]++
				if index == 0 && opens[index] > 1 {
					return nifti.OpenedFile{}, secretCause
				}
				return nifti.OpenedFile{File: files[index]}, nil
			},
		}
		options := nifti.DefaultOptions()
		options.TempDir = tempDir
		var output bytes.Buffer
		_, err := nifti.Write(context.Background(), &output, source, options)
		requireExportError(t, err, nifti.CodeSource, nifti.ErrInvalidSource)
		if !errors.Is(err, secretCause) {
			t.Fatalf("errors.Is(error, underlying cause) = false")
		}
		for _, secret := range []string{patientName, secretPath} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("public error %q leaked %q", err, secret)
			}
		}
		if output.Len() != 0 {
			t.Fatalf("destination length = %d after replay failure, want 0", output.Len())
		}
		requireEmptyDirectory(t, tempDir)
	})
}

func TestPathSourceStreamsNativePart10PixelsBelowInMemorySourceLimit(t *testing.T) {
	const (
		rows          = 128
		columns       = 128
		perFrameBytes = rows * columns
		inMemoryLimit = 1024
		uncompressed  = 2 * perFrameBytes
	)
	paths := []string{
		writePart10File(t, "slice-2.dcm", testSlice(t, imageSpec{
			position: [3]float64{0, 0, 2}, rows: rows, columns: columns,
			pixels: bytes.Repeat([]byte{0x22}, perFrameBytes),
		})),
		writePart10File(t, "slice-1.dcm", testSlice(t, imageSpec{
			position: [3]float64{0, 0, 0}, rows: rows, columns: columns,
			pixels: bytes.Repeat([]byte{0x11}, perFrameBytes),
		})),
	}
	options := nifti.DefaultOptions()
	options.Limits.MaxInMemorySourceBytes = inMemoryLimit
	options.TempDir = t.TempDir()
	var output bytes.Buffer
	report, err := nifti.Write(context.Background(), &output, nifti.NewPathSource(paths), options)
	if err != nil {
		t.Fatalf("Write(path source with %d-byte in-memory limit) error = %v", inMemoryLimit, err)
	}
	if uncompressed <= inMemoryLimit {
		t.Fatalf("invalid fixture: uncompressed bytes %d must exceed limit %d", uncompressed, inMemoryLimit)
	}
	if report.Dimensions != [4]int{columns, rows, 2, 1} || report.BytesWritten != int64(352+uncompressed) {
		t.Fatalf("streaming report = dimensions %v bytes %d, want [%d %d 2 1] and %d", report.Dimensions, report.BytesWritten, columns, rows, 352+uncompressed)
	}
	voxels := output.Bytes()[352:]
	if len(voxels) != uncompressed || voxels[0] != 0x11 || voxels[perFrameBytes-1] != 0x11 ||
		voxels[perFrameBytes] != 0x22 || voxels[len(voxels)-1] != 0x22 {
		t.Fatalf("streamed voxel boundaries = len %d first/last slice1 %#x/%#x slice2 %#x/%#x",
			len(voxels), voxels[0], voxels[perFrameBytes-1], voxels[perFrameBytes], voxels[len(voxels)-1])
	}
	requireEmptyDirectory(t, options.TempDir)
}

func TestPlanWriteIsReusableAndConcurrent(t *testing.T) {
	options := nifti.DefaultOptions()
	options.TempDir = t.TempDir()
	plan, err := nifti.PlanVolume(context.Background(), nifti.NewFilesSource(testUint8Volume(t, "MONOCHROME2")), options)
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	var baseline bytes.Buffer
	baselineReport, err := plan.Write(context.Background(), &baseline)
	if err != nil {
		t.Fatalf("first Plan.Write() error = %v", err)
	}

	const writers = 12
	type result struct {
		index  int
		data   []byte
		report nifti.Report
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, writers)
	for index := 0; index < writers; index++ {
		go func(index int) {
			<-start
			var output bytes.Buffer
			report, writeErr := plan.Write(context.Background(), &output)
			results <- result{index: index, data: append([]byte(nil), output.Bytes()...), report: report, err: writeErr}
		}(index)
	}
	close(start)
	for completed := 0; completed < writers; completed++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Plan.Write(%d) error = %v", result.index, result.err)
		}
		if !bytes.Equal(result.data, baseline.Bytes()) {
			t.Fatalf("concurrent Plan.Write(%d) bytes differ from baseline", result.index)
		}
		if !reflect.DeepEqual(result.report, baselineReport) {
			t.Fatalf("concurrent Plan.Write(%d) report differs\ngot=%+v\nwant=%+v", result.index, result.report, baselineReport)
		}
	}
	requireEmptyDirectory(t, options.TempDir)
}

func BenchmarkWriteSyntheticVolume(b *testing.B) {
	const (
		columns = 256
		rows    = 256
		slices  = 32
	)
	files := make([]*object.File, slices)
	for z := 0; z < slices; z++ {
		pixels := make([]byte, rows*columns)
		for index := range pixels {
			pixels[index] = byte((index*31 + z*17) & 0xff)
		}
		files[z] = testSlice(b, imageSpec{
			position: [3]float64{0, 0, float64(z)}, pixels: pixels,
			rows: rows, columns: columns,
		})
	}
	source := nifti.NewFilesSource(files)
	for _, compression := range []nifti.Compression{nifti.CompressionNone, nifti.CompressionGZIP} {
		b.Run(compression.String(), func(b *testing.B) {
			options := nifti.DefaultOptions()
			options.Compression = compression
			options.TempDir = b.TempDir()
			b.ReportAllocs()
			b.SetBytes(rows * columns * slices)
			for iteration := 0; iteration < b.N; iteration++ {
				if _, err := nifti.Write(context.Background(), io.Discard, source, options); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type imageSpec struct {
	position                   [3]float64
	orientation                [6]float64
	spacing                    [2]float64
	rows                       uint16
	columns                    uint16
	bitsAllocated              uint16
	bitsStored                 uint16
	highBit                    uint16
	pixelRepresentation        uint16
	photometric                string
	pixels                     []byte
	syntax                     transfer.Syntax
	seriesUID                  string
	frameOfReferenceUID        string
	temporalPosition           int
	frameReferenceMilliseconds *float64
	slope                      *float64
	intercept                  *float64
	patientName                string
	patientID                  string
}

type patientOrientationFixture struct {
	name        string
	orientation [6]float64
	sliceStep   [3]float64
	affineRAS   [16]float64
}

func patientOrientationFixtures() []patientOrientationFixture {
	const angle = math.Pi / 6
	c, s := math.Cos(angle), math.Sin(angle)
	return []patientOrientationFixture{
		{
			name: "axial", orientation: [6]float64{1, 0, 0, 0, 1, 0}, sliceStep: [3]float64{0, 0, 2},
			affineRAS: [16]float64{-1, 0, 0, 0, 0, -2, 0, 0, 0, 0, 2, 0, 0, 0, 0, 1},
		},
		{
			name: "coronal", orientation: [6]float64{1, 0, 0, 0, 0, -1}, sliceStep: [3]float64{0, 2, 0},
			affineRAS: [16]float64{-1, 0, 0, 0, 0, 0, -2, 0, 0, -2, 0, 0, 0, 0, 0, 1},
		},
		{
			name: "sagittal", orientation: [6]float64{0, 1, 0, 0, 0, -1}, sliceStep: [3]float64{-2, 0, 0},
			affineRAS: [16]float64{0, 0, 2, 0, -1, 0, 0, 0, 0, -2, 0, 0, 0, 0, 0, 1},
		},
		{
			name: "oblique", orientation: [6]float64{c, s, 0, -s, c, 0}, sliceStep: [3]float64{0, 0, 2},
			affineRAS: [16]float64{-c, 2 * s, 0, 0, -s, -2 * c, 0, 0, 0, 0, 2, 0, 0, 0, 0, 1},
		},
	}
}

func orientationVolume(tb testing.TB, fixture patientOrientationFixture) []*object.File {
	tb.Helper()
	high := fixture.sliceStep
	return []*object.File{
		testSlice(tb, imageSpec{position: high, orientation: fixture.orientation, pixels: []byte{3, 4}}),
		testSlice(tb, imageSpec{orientation: fixture.orientation, pixels: []byte{1, 2}}),
	}
}

func testSlice(tb testing.TB, spec imageSpec) *object.File {
	tb.Helper()
	if spec.orientation == ([6]float64{}) {
		spec.orientation = [6]float64{1, 0, 0, 0, 1, 0}
	}
	if spec.spacing == ([2]float64{}) {
		spec.spacing = [2]float64{2, 1}
	}
	if spec.rows == 0 {
		spec.rows = 1
	}
	if spec.columns == 0 {
		spec.columns = 2
	}
	if spec.bitsAllocated == 0 {
		spec.bitsAllocated = 8
	}
	if spec.bitsStored == 0 {
		spec.bitsStored = spec.bitsAllocated
	}
	if spec.highBit == 0 && spec.bitsStored > 1 {
		spec.highBit = spec.bitsStored - 1
	}
	if spec.photometric == "" {
		spec.photometric = "MONOCHROME2"
	}
	if spec.syntax.UID == "" {
		spec.syntax = transfer.ExplicitVRLittleEndian
	}
	if spec.seriesUID == "" {
		spec.seriesUID = testSeriesUID
	}
	if spec.frameOfReferenceUID == "" {
		spec.frameOfReferenceUID = testFORUID
	}
	order := spec.syntax.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	elements := []core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, ctStorageUID),
		derivedio.UI(derivedio.TagSOPInstanceUID, fmt.Sprintf("%s.%d", testSeriesUID, int(math.Round(spec.position[2]*10))+1000+spec.temporalPosition*10000)),
		derivedio.UI(derivedio.TagSeriesInstanceUID, spec.seriesUID),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, spec.frameOfReferenceUID),
		rawUS(order, derivedio.TagSamplesPerPixel, 1),
		derivedio.CS(derivedio.TagPhotometricInterpretation, spec.photometric),
		derivedio.IS(derivedio.TagNumberOfFrames, 1),
		rawUS(order, derivedio.TagRows, spec.rows),
		rawUS(order, derivedio.TagColumns, spec.columns),
		rawUS(order, derivedio.TagBitsAllocated, spec.bitsAllocated),
		rawUS(order, derivedio.TagBitsStored, spec.bitsStored),
		rawUS(order, derivedio.TagHighBit, spec.highBit),
		rawUS(order, derivedio.TagPixelRepresentation, spec.pixelRepresentation),
		derivedio.DS(tagImagePositionPatient, spec.position[:]...),
		derivedio.DS(tagImageOrientationPatient, spec.orientation[:]...),
		derivedio.DS(tagPixelSpacing, spec.spacing[:]...),
	}
	if spec.temporalPosition > 0 {
		elements = append(elements,
			derivedio.IS(tagTemporalPositionIdentifier, spec.temporalPosition),
			derivedio.IS(tagNumberOfTemporalPositions, 2),
		)
	}
	if spec.frameReferenceMilliseconds != nil {
		elements = append(elements, derivedio.DS(tagFrameReferenceTime, *spec.frameReferenceMilliseconds))
	}
	if spec.slope != nil {
		elements = append(elements, derivedio.DS(tagRescaleSlope, *spec.slope))
	}
	if spec.intercept != nil {
		elements = append(elements, derivedio.DS(tagRescaleIntercept, *spec.intercept))
	}
	if spec.patientName != "" {
		elements = append(elements, derivedio.Str(tagPatientName, core.VRPN, spec.patientName))
	}
	if spec.patientID != "" {
		elements = append(elements, derivedio.LO(tagPatientID, spec.patientID))
	}
	pixelVR := core.VROB
	if spec.bitsAllocated > 8 {
		pixelVR = core.VROW
	}
	elements = append(elements, derivedio.Raw(derivedio.TagPixelData, pixelVR, append([]byte(nil), spec.pixels...)))
	dataset := derivedio.Object(elements...)
	dataset.SetValueByteOrder(order)
	return &object.File{Dataset: dataset, TransferSyntax: spec.syntax}
}

func enhancedVolume(t *testing.T, positions [][3]float64, frames [][]byte) *object.File {
	t.Helper()
	if len(positions) != len(frames) || len(frames) == 0 {
		t.Fatal("invalid enhanced test fixture")
	}
	shared := derivedio.DataSet(
		derivedio.Seq(tagPlaneOrientationSequence, derivedio.DataSet(
			derivedio.DS(tagImageOrientationPatient, 1, 0, 0, 0, 1, 0),
		)),
		derivedio.Seq(tagPixelMeasuresSequence, derivedio.DataSet(
			derivedio.DS(tagPixelSpacing, 2, 1),
		)),
	)
	perFrame := make([]core.DataSet, len(frames))
	var pixels []byte
	for index := range frames {
		perFrame[index] = derivedio.DataSet(
			derivedio.Seq(tagPlanePositionSequence, derivedio.DataSet(
				derivedio.DS(tagImagePositionPatient, positions[index][:]...),
			)),
		)
		pixels = append(pixels, frames[index]...)
	}
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, "1.2.840.10008.5.1.4.1.1.2.1"),
		derivedio.UI(derivedio.TagSOPInstanceUID, testSeriesUID+".5000"),
		derivedio.UI(derivedio.TagSeriesInstanceUID, testSeriesUID),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, testFORUID),
		derivedio.US(derivedio.TagSamplesPerPixel, 1),
		derivedio.CS(derivedio.TagPhotometricInterpretation, "MONOCHROME2"),
		derivedio.IS(derivedio.TagNumberOfFrames, len(frames)),
		derivedio.US(derivedio.TagRows, 1),
		derivedio.US(derivedio.TagColumns, 2),
		derivedio.US(derivedio.TagBitsAllocated, 8),
		derivedio.US(derivedio.TagBitsStored, 8),
		derivedio.US(derivedio.TagHighBit, 7),
		derivedio.US(derivedio.TagPixelRepresentation, 0),
		derivedio.Seq(tagSharedFunctionalGroups, shared),
		derivedio.Seq(tagPerFrameFunctionalGroups, perFrame...),
		derivedio.Raw(derivedio.TagPixelData, core.VROB, pixels),
	)
	return &object.File{Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}
}

func testUint8Volume(t *testing.T, photometric string) []*object.File {
	t.Helper()
	return []*object.File{
		testSlice(t, imageSpec{position: [3]float64{0, 0, 0}, pixels: []byte{1, 2}, photometric: photometric}),
		testSlice(t, imageSpec{position: [3]float64{0, 0, 2}, pixels: []byte{3, 4}, photometric: photometric}),
	}
}

func writePart10File(t *testing.T, name string, file *object.File) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile fixture %q error = %v", name, err)
	}
	return path
}

func rawUS(order binary.ByteOrder, tag core.Tag, values ...uint16) core.Element {
	raw := make([]byte, 2*len(values))
	for index, value := range values {
		order.PutUint16(raw[index*2:], value)
	}
	return derivedio.Raw(tag, core.VRUS, raw)
}

func requireNIfTIHeader(t *testing.T, data []byte, dimensions [4]int, datatype, bitpix int16) {
	t.Helper()
	if len(data) < 352 {
		t.Fatalf("NIfTI length = %d, want at least 352", len(data))
	}
	if got := int32(binary.LittleEndian.Uint32(data[0:4])); got != 348 {
		t.Fatalf("sizeof_hdr = %d, want 348", got)
	}
	wantRank := int16(3)
	if dimensions[3] > 1 {
		wantRank = 4
	}
	if got := headerInt16(data, 40); got != wantRank {
		t.Fatalf("dim[0] = %d, want %d", got, wantRank)
	}
	for index, want := range dimensions {
		if got := headerInt16(data, 42+index*2); got != int16(want) {
			t.Fatalf("dim[%d] = %d, want %d", index+1, got, want)
		}
	}
	if got := headerInt16(data, 70); got != datatype {
		t.Fatalf("datatype = %d, want %d", got, datatype)
	}
	if got := headerInt16(data, 72); got != bitpix {
		t.Fatalf("bitpix = %d, want %d", got, bitpix)
	}
	if got := headerFloat32(data, 108); got != 352 {
		t.Fatalf("vox_offset = %v, want 352", got)
	}
	if string(data[344:348]) != "n+1\x00" {
		t.Fatalf("magic = %q, want n+1\\0", data[344:348])
	}
	if !bytes.Equal(data[348:352], []byte{0, 0, 0, 0}) {
		t.Fatalf("extension flag = %v, want all zero", data[348:352])
	}
}

func headerSForm(t *testing.T, data []byte) [16]float64 {
	t.Helper()
	if len(data) < 328 {
		t.Fatalf("header length = %d, want at least 328", len(data))
	}
	if headerInt16(data, 254) != 1 {
		t.Fatalf("sform_code = %d, want 1", headerInt16(data, 254))
	}
	affine := [16]float64{15: 1}
	for row, offset := range []int{280, 296, 312} {
		for column := 0; column < 4; column++ {
			affine[row*4+column] = headerFloat32(data, offset+column*4)
		}
	}
	return affine
}

func headerInt16(data []byte, offset int) int16 {
	return int16(binary.LittleEndian.Uint16(data[offset : offset+2]))
}

func headerFloat32(data []byte, offset int) float64 {
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4])))
}

func requireAffine(t *testing.T, got, want []float64, tolerance float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("affine length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if math.Abs(got[index]-want[index]) > tolerance {
			t.Fatalf("affine[%d] = %v, want %v (got=%v want=%v)", index, got[index], want[index], got, want)
		}
	}
}

func requireExportError(t *testing.T, err error, code nifti.ErrorCode, sentinel error) *nifti.ExportError {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", code)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is(error, %v) = false; error = %v", sentinel, err)
	}
	var exportErr *nifti.ExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("errors.As(error, *ExportError) = false; error = %T %v", err, err)
	}
	if exportErr.Code != code {
		t.Fatalf("error code = %q, want %q (error = %v)", exportErr.Code, code, err)
	}
	return exportErr
}

func requireEmptyDirectory(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", directory, err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		t.Fatalf("temporary directory contains %v, want empty", names)
	}
}

type shortWriter struct {
	maximum int
	written int
}

func (w *shortWriter) Write(payload []byte) (int, error) {
	remaining := w.maximum - w.written
	if remaining <= 0 {
		return 0, nil
	}
	if remaining > len(payload) {
		remaining = len(payload)
	}
	w.written += remaining
	return remaining, nil
}

type cancelAfterFirstWrite struct {
	cancel context.CancelFunc
	writes int
}

func (w *cancelAfterFirstWrite) Write(payload []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		w.cancel()
	}
	return len(payload), nil
}

func float64Pointer(value float64) *float64 { return &value }
