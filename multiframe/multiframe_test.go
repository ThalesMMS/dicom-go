package multiframe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	tagFrameVOILUTSequence  = core.NewTag(0x0028, 0x9132)
	tagFrameAnatomySequence = core.NewTag(0x0020, 0x9071)
	tagSliceThickness       = core.NewTag(0x0018, 0x0050)
	tagRescaleType          = core.NewTag(0x0028, 0x1054)
	tagWindowCenter         = core.NewTag(0x0028, 0x1050)
	tagWindowWidth          = core.NewTag(0x0028, 0x1051)
)

func TestConvertToClassicEnhancedCT(t *testing.T) {
	source := enhancedTestFile(t, EnhancedCTImageStorageUID, false)
	outputs, report, err := ConvertToClassic(context.Background(), source, Options{
		FrameIndices:      []int{1, 0},
		SeriesInstanceUID: "1.2.826.0.1.3680043.10.900.20",
		SOPInstanceUIDForFrame: func(frame int) (string, error) {
			return fmt.Sprintf("1.2.826.0.1.3680043.10.900.3%d", frame), nil
		},
		Equipment: ContributingEquipment{
			Manufacturer:          "dicom-go",
			ManufacturerModelName: "multiframe",
			SoftwareVersions:      []string{"1.0"},
		},
		Now: func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("ConvertToClassic: %v", err)
	}
	defer closeFiles(outputs)
	if report.SourceFrames != 2 || report.ConvertedFrames != 2 || report.TargetSOPClassUID != CTImageStorageUID || report.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID || report.OutputBytes == 0 {
		t.Fatalf("unexpected report: %+v", report)
	}

	wantPositions := []string{"10", "0"}
	wantPixels := [][]byte{{9, 0, 10, 0, 11, 0, 12, 0}, {1, 0, 2, 0, 3, 0, 4, 0}}
	for i, file := range outputs {
		if file.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
			t.Fatalf("output %d syntax = %s", i, file.TransferSyntax.UID)
		}
		if got, _ := file.Dataset.GetString(tagSOPClassUID); got != CTImageStorageUID {
			t.Fatalf("output %d SOP class = %q", i, got)
		}
		if got, _ := file.Dataset.GetString(tagSeriesInstanceUID); got != "1.2.826.0.1.3680043.10.900.20" {
			t.Fatalf("output %d series UID = %q", i, got)
		}
		if got, _ := file.Dataset.GetString(tagSOPInstanceUID); got != fmt.Sprintf("1.2.826.0.1.3680043.10.900.3%d", 1-i) {
			t.Fatalf("output %d SOP instance UID = %q", i, got)
		}
		if file.Dataset.Has(tagNumberOfFrames) || file.Dataset.Has(tagSharedFunctionalGroupsSequence) || file.Dataset.Has(tagPerFrameFunctionalGroupsSequence) {
			t.Fatalf("output %d retained enhanced attributes", i)
		}
		position, ok := file.Dataset.GetStrings(tagImagePositionPatient)
		if !ok || len(position) != 3 || position[2] != wantPositions[i] {
			t.Fatalf("output %d position = %#v", i, position)
		}
		imageType, ok := file.Dataset.GetStrings(tagImageType)
		if !ok || len(imageType) < 4 || imageType[0] != "ORIGINAL" || imageType[3] != fmt.Sprintf("FRAME%d", 1-i) {
			t.Fatalf("output %d ImageType = %#v", i, imageType)
		}
		native, err := pixeldata.ExtractNativeFramesView(file.Dataset)
		if err != nil {
			t.Fatalf("output %d native pixels: %v", i, err)
		}
		if len(native.Data) != 1 || !bytes.Equal(native.Data[0], wantPixels[i]) {
			t.Fatalf("output %d pixels = %v", i, native.Data)
		}
		assertConversionProvenance(t, file.Dataset, i, 2-i)
		if !file.Dataset.Has(tagKVP) || !file.Dataset.Has(tagAcquisitionNumber) {
			t.Fatalf("output %d did not emit missing CT Type 2 attributes", i)
		}
		assertPart10RoundTrip(t, file)
	}

	outputPixels, _ := outputs[0].Dataset.GetRaw(tagPixelData)
	outputPixels[0] = 0xFF
	sourcePixels, _ := source.Dataset.GetRaw(tagPixelData)
	otherPixels, _ := outputs[1].Dataset.GetRaw(tagPixelData)
	if sourcePixels[8] == 0xFF || otherPixels[0] == 0xFF {
		t.Fatal("output Pixel Data shares mutable storage with source or sibling")
	}
	if got, _ := source.Dataset.GetString(tagSeriesInstanceUID); got != "1.2.826.0.1.3680043.10.900.11" || !source.Dataset.Has(tagSharedFunctionalGroupsSequence) {
		t.Fatal("source identity or functional groups were mutated")
	}
}

func TestConvertToClassicEnhancedMR(t *testing.T) {
	source := enhancedTestFile(t, EnhancedMRImageStorageUID, false)
	outputs, report, err := ConvertToClassic(context.Background(), source, Options{
		SeriesInstanceUID: "1.2.826.0.1.3680043.10.900.40",
		SOPInstanceUIDForFrame: func(frame int) (string, error) {
			return fmt.Sprintf("1.2.826.0.1.3680043.10.900.5%d", frame), nil
		},
		Equipment: ContributingEquipment{Manufacturer: "dicom-go"},
	})
	if err != nil {
		t.Fatalf("ConvertToClassic: %v", err)
	}
	defer closeFiles(outputs)
	if report.TargetSOPClassUID != MRImageStorageUID || len(outputs) != 2 {
		t.Fatalf("unexpected MR result report=%+v files=%d", report, len(outputs))
	}
	for i, file := range outputs {
		if got, _ := file.Dataset.GetString(tagSOPClassUID); got != MRImageStorageUID {
			t.Fatalf("output %d SOP class = %q", i, got)
		}
		if got, _ := file.Dataset.GetString(tagScanningSequence); got != "SE" {
			t.Fatalf("output %d ScanningSequence = %q", i, got)
		}
		if !file.Dataset.Has(tagScanOptions) || !file.Dataset.Has(tagMRAcquisitionType) ||
			!file.Dataset.Has(tagEchoTime) || !file.Dataset.Has(tagEchoTrainLength) {
			t.Fatalf("output %d did not emit missing MR Type 2 attributes", i)
		}
		imageType, _ := file.Dataset.GetStrings(tagImageType)
		if len(imageType) < 4 || imageType[0] != "ORIGINAL" {
			t.Fatalf("output %d ImageType = %#v", i, imageType)
		}
		assertPart10RoundTrip(t, file)
	}
}

func TestConvertToClassicLegacyConvertedCTAndMR(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		target string
	}{
		{name: "CT", source: LegacyConvertedEnhancedCTImageStorageUID, target: CTImageStorageUID},
		{name: "MR", source: LegacyConvertedEnhancedMRImageStorageUID, target: MRImageStorageUID},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputs, report, err := ConvertToClassic(context.Background(), enhancedTestFile(t, test.source, false), validOptions())
			if err != nil {
				t.Fatalf("ConvertToClassic: %v", err)
			}
			defer closeFiles(outputs)
			if len(outputs) != 2 || report.TargetSOPClassUID != test.target {
				t.Fatalf("outputs=%d report=%+v", len(outputs), report)
			}
			for _, output := range outputs {
				if got, _ := output.Dataset.GetString(tagSOPClassUID); got != test.target {
					t.Fatalf("target SOP Class UID = %q, want %q", got, test.target)
				}
				assertPart10RoundTrip(t, output)
			}
		})
	}
}

func TestConvertToClassicRejectsConflictingFlattenedLeaves(t *testing.T) {
	source := enhancedTestFile(t, EnhancedCTImageStorageUID, false)
	element, _ := source.Dataset.Get(tagPerFrameFunctionalGroupsSequence)
	sequence := element.Value.(core.SequenceValue)
	items := append([]core.DataSet(nil), sequence.Items...)
	items[0].Elements = append([]core.Element(nil), items[0].Elements...)
	items[0].Elements = append(items[0].Elements, seq(tagFrameAnatomySequence, dataSet(
		stringsElement(tagImageOrientationPatient, core.VRDS, []string{"0", "1", "0", "1", "0", "0"}),
	)))
	source.Dataset.Put(seq(tagPerFrameFunctionalGroupsSequence, items...))
	_, _, err := ConvertToClassic(context.Background(), source, validOptions())
	if !errors.Is(err, ErrNonconformant) {
		t.Fatalf("error = %v, want ErrNonconformant", err)
	}
}

func TestConvertToClassicRejectsNonClassicPixelProfile(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*object.Object)
	}{
		{
			name: "bits_stored_below_12",
			mutate: func(dataset *object.Object) {
				dataset.Put(us(tagBitsStored, 11))
				dataset.Put(us(tagHighBit, 10))
			},
		},
		{
			name: "non_standard_monochrome_value",
			mutate: func(dataset *object.Object) {
				dataset.Put(str(tagPhotometricInterpretation, core.VRCS, "MONOCHROME3"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := enhancedTestFile(t, EnhancedCTImageStorageUID, false)
			test.mutate(source.Dataset)
			files, _, err := ConvertToClassic(context.Background(), source, validOptions())
			if !errors.Is(err, ErrNonconformant) || files != nil {
				t.Fatalf("files=%v error=%v, want nil/ErrNonconformant", files, err)
			}
		})
	}
}

func TestConvertToClassicRejectsSharedPerFrameMacroDuplication(t *testing.T) {
	source := enhancedTestFile(t, EnhancedCTImageStorageUID, true)
	_, _, err := ConvertToClassic(context.Background(), source, validOptions())
	if !errors.Is(err, ErrNonconformant) {
		t.Fatalf("error = %v, want ErrNonconformant", err)
	}
}

func TestConvertToClassicRejectsDuplicateUIDsAtomically(t *testing.T) {
	source := enhancedTestFile(t, EnhancedCTImageStorageUID, false)
	opts := validOptions()
	opts.SOPInstanceUIDForFrame = func(int) (string, error) { return "1.2.826.0.1.3680043.10.900.99", nil }
	files, _, err := ConvertToClassic(context.Background(), source, opts)
	if !errors.Is(err, ErrInvalidOptions) || files != nil {
		t.Fatalf("files=%v error=%v, want nil/ErrInvalidOptions", files, err)
	}
}

func TestConvertToClassicHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files, _, err := ConvertToClassic(ctx, enhancedTestFile(t, EnhancedCTImageStorageUID, false), validOptions())
	if !errors.Is(err, context.Canceled) || files != nil {
		t.Fatalf("files=%v error=%v, want nil/context.Canceled", files, err)
	}
}

func TestConvertToClassicHonorsFrameLimit(t *testing.T) {
	opts := validOptions()
	opts.Limits.MaxFrames = 1
	files, _, err := ConvertToClassic(context.Background(), enhancedTestFile(t, EnhancedCTImageStorageUID, false), opts)
	if !errors.Is(err, ErrResourceLimit) || files != nil {
		t.Fatalf("files=%v error=%v, want nil/ErrResourceLimit", files, err)
	}
}

func TestTargetSOPClassUID(t *testing.T) {
	for _, test := range []struct{ source, target string }{
		{EnhancedCTImageStorageUID, CTImageStorageUID},
		{LegacyConvertedEnhancedCTImageStorageUID, CTImageStorageUID},
		{EnhancedMRImageStorageUID, MRImageStorageUID},
		{LegacyConvertedEnhancedMRImageStorageUID, MRImageStorageUID},
	} {
		got, ok := TargetSOPClassUID(test.source)
		if !ok || got != test.target || !CanConvertSOPClass(test.source) {
			t.Fatalf("source %q => %q,%v", test.source, got, ok)
		}
	}
	if CanConvertSOPClass(CTImageStorageUID) {
		t.Fatal("classic CT must not be reported as convertible")
	}
}

func validOptions() Options {
	return Options{
		SeriesInstanceUID: "1.2.826.0.1.3680043.10.900.20",
		SOPInstanceUIDForFrame: func(frame int) (string, error) {
			return fmt.Sprintf("1.2.826.0.1.3680043.10.900.3%d", frame), nil
		},
		Equipment: ContributingEquipment{Manufacturer: "dicom-go"},
	}
}

func enhancedTestFile(t *testing.T, sopClassUID string, duplicateMacro bool) *object.File {
	t.Helper()
	isMR := sopClassUID == EnhancedMRImageStorageUID || sopClassUID == LegacyConvertedEnhancedMRImageStorageUID
	frameTypeSequence := tagCTImageFrameTypeSequence
	if isMR {
		frameTypeSequence = tagMRImageFrameTypeSequence
	}
	sharedMacros := []core.Element{
		seq(tagPixelMeasuresSequence, dataSet(
			stringsElement(tagPixelSpacing, core.VRDS, []string{"0.5", "0.5"}),
			str(tagSliceThickness, core.VRDS, "1"),
		)),
		seq(tagPlaneOrientationSequence, dataSet(
			stringsElement(tagImageOrientationPatient, core.VRDS, []string{"1", "0", "0", "0", "1", "0"}),
		)),
	}
	perFrame := make([]core.DataSet, 2)
	for frame := range perFrame {
		macros := []core.Element{
			seq(tagPlanePositionSequence, dataSet(stringsElement(tagImagePositionPatient, core.VRDS, []string{"0", "0", fmt.Sprint(frame * 10)}))),
			seq(frameTypeSequence, dataSet(stringsElement(tagFrameType, core.VRCS, []string{"ORIGINAL", "PRIMARY", "AXIAL", fmt.Sprintf("FRAME%d", frame)}))),
		}
		if !isMR {
			macros = append(macros,
				seq(tagPixelValueTransformationSequence, dataSet(
					str(tagRescaleIntercept, core.VRDS, "-1024"),
					str(tagRescaleSlope, core.VRDS, "1"),
					str(tagRescaleType, core.VRLO, "HU"),
				)),
				seq(tagFrameVOILUTSequence, dataSet(
					str(tagWindowCenter, core.VRDS, "40"),
					str(tagWindowWidth, core.VRDS, "400"),
				)),
			)
		}
		if duplicateMacro && frame == 0 {
			macros = append(macros, seq(tagPlaneOrientationSequence, dataSet(
				stringsElement(tagImageOrientationPatient, core.VRDS, []string{"1", "0", "0", "0", "1", "0"}),
			)))
		}
		perFrame[frame] = dataSet(macros...)
	}

	elements := []core.Element{
		ui(tagSOPClassUID, sopClassUID),
		ui(tagSOPInstanceUID, "1.2.826.0.1.3680043.10.900.1"),
		ui(tagStudyInstanceUID, "1.2.826.0.1.3680043.10.900.10"),
		ui(tagSeriesInstanceUID, "1.2.826.0.1.3680043.10.900.11"),
		ui(tagFrameOfReferenceUID, "1.2.826.0.1.3680043.10.900.12"),
		str(tagModality, core.VRCS, map[bool]string{true: "MR", false: "CT"}[isMR]),
		str(tagNumberOfFrames, core.VRIS, "2"),
		us(tagRows, 2), us(tagColumns, 2), us(tagSamplesPerPixel, 1),
		str(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		us(tagBitsAllocated, 16), us(tagBitsStored, 12), us(tagHighBit, 11), us(tagPixelRepresentation, 1),
		seq(tagSharedFunctionalGroupsSequence, dataSet(sharedMacros...)),
		seq(tagPerFrameFunctionalGroupsSequence, perFrame...),
		core.NewRawElement(tagPixelData, core.VROW, []byte{1, 0, 2, 0, 3, 0, 4, 0, 9, 0, 10, 0, 11, 0, 12, 0}),
	}
	if isMR {
		elements = append(elements,
			str(tagScanningSequence, core.VRCS, "SE"),
			str(tagSequenceVariant, core.VRCS, "NONE"),
		)
	}
	dataset := object.FromElements(elements, std.Dictionary)
	file := &object.File{Dataset: dataset, Meta: object.New(std.Dictionary), TransferSyntax: transfer.ExplicitVRLittleEndian}
	if err := file.RebuildFileMeta(); err != nil {
		t.Fatalf("RebuildFileMeta: %v", err)
	}
	return file
}

func assertConversionProvenance(t *testing.T, dataset *object.Object, outputIndex, referencedFrame int) {
	t.Helper()
	items, ok := dataset.GetSequence(tagConversionSourceAttributesSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("output %d ConversionSourceAttributesSequence count=%d ok=%v", outputIndex, len(items), ok)
	}
	if got, _ := items[0].GetString(tagReferencedSOPClassUID); got != EnhancedCTImageStorageUID {
		t.Fatalf("output %d referenced class = %q", outputIndex, got)
	}
	if got, _ := items[0].GetString(tagReferencedSOPInstanceUID); got != "1.2.826.0.1.3680043.10.900.1" {
		t.Fatalf("output %d referenced instance = %q", outputIndex, got)
	}
	if got, _ := items[0].GetString(tagReferencedFrameNumber); got != fmt.Sprint(referencedFrame) {
		t.Fatalf("output %d referenced frame = %q", outputIndex, got)
	}
	equipment, ok := dataset.GetSequence(tagContributingEquipmentSequence)
	if !ok || len(equipment) != 1 {
		t.Fatalf("output %d contributing equipment count=%d ok=%v", outputIndex, len(equipment), ok)
	}
	if got, _ := equipment[0].GetString(tagContributionDescription); got != conversionDescription {
		t.Fatalf("output %d contribution description = %q", outputIndex, got)
	}
	purpose, ok := equipment[0].GetSequence(tagPurposeOfReferenceCodeSequence)
	if !ok || len(purpose) != 1 {
		t.Fatalf("output %d equipment purpose count=%d ok=%v", outputIndex, len(purpose), ok)
	}
	if got, _ := purpose[0].GetString(tagCodeValue); got != "109106" {
		t.Fatalf("output %d equipment purpose code = %q", outputIndex, got)
	}
}

func assertPart10RoundTrip(t *testing.T, file *object.File) {
	t.Helper()
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	roundTrip, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	defer func() { _ = roundTrip.Close() }()
	if roundTrip.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("round-trip syntax = %q", roundTrip.TransferSyntax.UID)
	}
}

func us(tag core.Tag, values ...uint16) core.Element {
	raw := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(raw[i*2:], value)
	}
	return core.NewRawElement(tag, core.VRUS, raw)
}

func closeFiles(files []*object.File) {
	for _, file := range files {
		_ = file.Close()
	}
}
