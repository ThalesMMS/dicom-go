package nifti_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/nifti"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parametricmap"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestWriteParametricMapPreservesCodedUnitsAndCalibratedFloatValues(t *testing.T) {
	file := quantitativeParametricMap(t)
	var output bytes.Buffer
	report, err := nifti.WriteFiles(context.Background(), &output, []*object.File{file}, nifti.DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFiles(Parametric Map) error = %v", err)
	}
	if report.Datatype != nifti.DatatypeFloat64 || report.ScalingPolicy != nifti.ScalingApplyFloat64 {
		t.Fatalf("encoding = datatype %d scaling %v", report.Datatype, report.ScalingPolicy)
	}
	if report.Sidecar.Units != (nifti.CodedUnits{Code: "mm", Scheme: "UCUM"}) {
		t.Fatalf("coded units = %+v", report.Sidecar.Units)
	}
	if report.Sidecar.Quantity != (nifti.CodedUnits{Code: "ADC", Scheme: "DCM"}) {
		t.Fatalf("coded quantity = %+v", report.Sidecar.Quantity)
	}
	voxels := output.Bytes()[352:]
	if len(voxels) != 16 {
		t.Fatalf("voxel bytes = %d, want 16", len(voxels))
	}
	got := []float64{
		math.Float64frombits(binary.LittleEndian.Uint64(voxels[:8])),
		math.Float64frombits(binary.LittleEndian.Uint64(voxels[8:])),
	}
	if got[0] != 3 || got[1] != 5 {
		t.Fatalf("calibrated voxels = %v, want [3 5]", got)
	}
}

func TestWriteParametricMapRejectsMixedQuantitiesWithSameUnits(t *testing.T) {
	first := quantitativeParametricMap(t)
	second := quantitativeParametricMap(t, "T1")
	var output bytes.Buffer
	_, err := nifti.WriteFiles(context.Background(), &output, []*object.File{first, second}, nifti.DefaultOptions())
	var exportErr *nifti.ExportError
	if !errors.As(err, &exportErr) || exportErr.Code != nifti.CodePixels {
		t.Fatalf("WriteFiles(mixed quantities) error = %T %v, want CodePixels", err, err)
	}
	if output.Len() != 0 {
		t.Fatalf("mixed quantities wrote %d bytes", output.Len())
	}
}

func TestWriteParametricMapEnforcesCombinedPayloadAndFrameBufferLimit(t *testing.T) {
	file := quantitativeParametricMap(t)
	path := writePart10File(t, "parametric-map.dcm", file)
	options := nifti.DefaultOptions()
	// The fixture has eight payload bytes and needs one 8-byte value buffer plus
	// one 8-byte encoded buffer for each one-pixel frame.
	options.Limits.MaxInMemorySourceBytes = 23
	tests := []struct {
		name   string
		source nifti.Source
	}{
		{name: "borrowed file", source: nifti.NewFilesSource([]*object.File{file})},
		{name: "path source", source: nifti.NewPathSource([]string{path})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			_, err := nifti.Write(context.Background(), &output, test.source, options)
			var exportErr *nifti.ExportError
			if !errors.As(err, &exportErr) || exportErr.Code != nifti.CodeLimit || !errors.Is(err, nifti.ErrLimitExceeded) {
				t.Fatalf("Write() error = %T %v, want CodeLimit/ErrLimitExceeded", err, err)
			}
			if output.Len() != 0 {
				t.Fatalf("limited Parametric Map wrote %d bytes", output.Len())
			}
		})
	}
}

func quantitativeParametricMap(t *testing.T, quantityValues ...string) *object.File {
	t.Helper()
	quantity := "ADC"
	if len(quantityValues) > 0 {
		quantity = quantityValues[0]
	}
	var raw []byte
	for _, value := range []float32{1, 2} {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], math.Float32bits(value))
		raw = append(raw, encoded[:]...)
	}
	mappingTag := core.NewTag(0x0040, 0x9096)
	unitsTag := core.NewTag(0x0040, 0x08EA)
	quantityDefinitionTag := core.NewTag(0x0040, 0x9220)
	conceptNameCodeTag := core.NewTag(0x0040, 0xA043)
	slopeTag := core.NewTag(0x0040, 0x9225)
	interceptTag := core.NewTag(0x0040, 0x9224)
	codeValueTag := core.NewTag(0x0008, 0x0100)
	codeSchemeTag := core.NewTag(0x0008, 0x0102)
	perFrame := []core.DataSet{
		derivedio.DataSet(derivedio.Seq(tagPlanePositionSequence, derivedio.DataSet(derivedio.DS(tagImagePositionPatient, 0, 0, 0)))),
		derivedio.DataSet(derivedio.Seq(tagPlanePositionSequence, derivedio.DataSet(derivedio.DS(tagImagePositionPatient, 0, 0, 2)))),
	}
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, parametricmap.ParametricMapStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, testSeriesUID+".9000"),
		derivedio.UI(derivedio.TagStudyInstanceUID, testSeriesUID+".study"),
		derivedio.UI(derivedio.TagSeriesInstanceUID, testSeriesUID),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, testFORUID),
		derivedio.US(derivedio.TagRows, 1), derivedio.US(derivedio.TagColumns, 1),
		derivedio.IS(derivedio.TagNumberOfFrames, 2),
		derivedio.Seq(tagSharedFunctionalGroups, derivedio.DataSet(
			derivedio.Seq(tagPlaneOrientationSequence, derivedio.DataSet(derivedio.DS(tagImageOrientationPatient, 1, 0, 0, 0, 1, 0))),
			derivedio.Seq(tagPixelMeasuresSequence, derivedio.DataSet(derivedio.DS(tagPixelSpacing, 1, 1))),
		)),
		derivedio.Seq(tagPerFrameFunctionalGroups, perFrame...),
		derivedio.Seq(mappingTag, derivedio.DataSet(
			derivedio.FD(slopeTag, 2), derivedio.FD(interceptTag, 1),
			derivedio.Seq(unitsTag, derivedio.DataSet(
				derivedio.SH(codeValueTag, "mm"), derivedio.SH(codeSchemeTag, "UCUM"),
			)),
			derivedio.Seq(quantityDefinitionTag, derivedio.DataSet(
				derivedio.Seq(conceptNameCodeTag, derivedio.DataSet(
					derivedio.SH(codeValueTag, quantity), derivedio.SH(codeSchemeTag, "DCM"),
				)),
			)),
		)),
		derivedio.Raw(core.NewTag(0x7FE0, 0x0008), core.VROF, raw),
	)
	return &object.File{Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}
}
