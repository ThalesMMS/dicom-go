package index

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	testSOPClassUID    = "1.2.840.10008.5.1.4.1.1.2"
	testSOPInstanceUID = "1.2.826.0.1.3680043.10.543.626.1"
)

func TestReadPathProfilesAndPart10Meta(t *testing.T) {
	path := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		textIndexElement(tags.PatientName, core.VRPN, "SECRET^PATIENT"),
		textIndexElement(tags.PatientID, core.VRLO, "SECRET-ID"),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.4"),
		textIndexElement(tags.SeriesInstanceUID, core.VRUI, "1.2.3.4.5"),
		textIndexElement(tags.Modality, core.VRCS, "CT"),
		textIndexElement(tags.InstanceNumber, core.VRIS, "7"),
	})

	result, err := ReadPath(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.Patient != nil {
		t.Fatal("zero options unexpectedly populated patient PHI")
	}
	if result.Record.Study.InstanceUID != "1.2.3.4" || result.Record.Series.Modality != "CT" ||
		result.Record.Instance.SOPInstanceUID != testSOPInstanceUID {
		t.Fatalf("record = %+v", result.Record)
	}
	if result.Record.FileMeta.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID ||
		result.Record.FileMeta.MediaStorageSOPClassUID != testSOPClassUID ||
		result.Record.FileMeta.MediaStorageSOPInstanceUID != testSOPInstanceUID {
		t.Fatalf("file meta = %+v", result.Record.FileMeta)
	}

	patient, err := ReadPath(context.Background(), path, Options{Profile: ProfileCore | ProfilePatient})
	if err != nil {
		t.Fatal(err)
	}
	if patient.Record.Patient == nil || patient.Record.Patient.Name != "SECRET^PATIENT" || patient.Record.Patient.ID != "SECRET-ID" {
		t.Fatalf("patient = %+v", patient.Record.Patient)
	}
}

func TestReadFindsMetadataBeyond128KiBWithoutMaterializingBulk(t *testing.T) {
	privateBulk := core.NewTag(0x0019, 0x1010)
	path := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		core.NewRawElement(privateBulk, core.VROB, bytes.Repeat([]byte{0x5a}, 256<<10)),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.626"),
	})

	result, err := ReadPath(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.Study.InstanceUID != "1.2.3.626" {
		t.Fatalf("study UID = %q", result.Record.Study.InstanceUID)
	}
}

func TestReadFindsMetadataAfterLongSequenceWithoutMaterializingBulk(t *testing.T) {
	sequenceTag := core.NewTag(0x0040, 0xA730)
	privateBulk := core.NewTag(0x0019, 0x1010)
	sequence := core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(privateBulk, core.VROB, bytes.Repeat([]byte{0x5a}, 256<<10)),
		}}}},
	}
	encoded := encodeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		sequence,
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.after-sequence"),
	})
	counted := &countingReaderAt{data: encoded}
	result, err := ReadAt(context.Background(), "", counted, int64(len(encoded)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.Study.InstanceUID != "1.2.3.after-sequence" {
		t.Fatalf("study UID = %q", result.Record.Study.InstanceUID)
	}
	if counted.bytesRead >= int64(len(encoded)) {
		t.Fatalf("reader materialized nested bulk value: %d/%d", counted.bytesRead, len(encoded))
	}
}

func TestReadStopsBeforeNativeAndEncapsulatedPixelPayload(t *testing.T) {
	for _, syntax := range []transfer.Syntax{transfer.ExplicitVRLittleEndian, transfer.JPEGBaseline} {
		t.Run(syntax.Name, func(t *testing.T) {
			pixel := core.NewRawElement(tags.PixelData, core.VROB, bytes.Repeat([]byte{0x6d}, 128<<10))
			if syntax.Encapsulated {
				pixel = core.Element{Header: core.ElementHeader{Tag: tags.PixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true}, Value: core.FragmentSequence{Fragments: [][]byte{bytes.Repeat([]byte{0x6d}, 128<<10)}}}
			}
			encoded := encodeIndexFixture(t, syntax, []core.Element{
				textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
				textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
				pixel,
			})
			counted := &countingReaderAt{data: encoded}
			result, err := ReadAt(context.Background(), "SECRET-PATH", counted, int64(len(encoded)), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Record.Instance.SOPInstanceUID != testSOPInstanceUID {
				t.Fatalf("record = %+v", result.Record)
			}
			if counted.bytesRead >= int64(len(encoded)) {
				t.Fatalf("metadata reader consumed full pixel payload: %d/%d", counted.bytesRead, len(encoded))
			}
		})
	}
}

func TestReadSkipsNestedPixelPayloadAndContinuesTopLevelMetadata(t *testing.T) {
	iconImageSequence := core.NewTag(0x0088, 0x0200)
	for _, syntax := range []transfer.Syntax{transfer.ExplicitVRLittleEndian, transfer.JPEGBaseline} {
		t.Run(syntax.Name, func(t *testing.T) {
			pixel := core.NewRawElement(tags.PixelData, core.VROB, bytes.Repeat([]byte{0x6d}, 128<<10))
			if syntax.Encapsulated {
				pixel = core.Element{
					Header: core.ElementHeader{Tag: tags.PixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
					Value:  core.FragmentSequence{Fragments: [][]byte{bytes.Repeat([]byte{0x6d}, 128<<10)}},
				}
			}
			icon := core.Element{
				Header: core.ElementHeader{Tag: iconImageSequence, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
				Value:  core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{pixel}}}},
			}
			encoded := encodeIndexFixture(t, syntax, []core.Element{
				textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
				textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
				icon,
				textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.after-icon"),
			})
			counted := &countingReaderAt{data: encoded}
			result, err := ReadAt(context.Background(), "", counted, int64(len(encoded)), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if result.StoppedBeforePixelData || result.Record.Study.InstanceUID != "1.2.3.after-icon" {
				t.Fatalf("result = %+v", result)
			}
			if counted.bytesRead >= int64(len(encoded)) {
				t.Fatalf("metadata reader consumed nested pixel payload: %d/%d", counted.bytesRead, len(encoded))
			}
		})
	}
}

func TestReadSyntaxesAndRawOptIn(t *testing.T) {
	for _, syntax := range []transfer.Syntax{
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.DeflatedExplicitVRLittleEndian,
	} {
		t.Run(syntax.Name, func(t *testing.T) {
			path := writeIndexFixture(t, syntax, []core.Element{
				textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
				textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
				textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.626"),
			})
			result, err := ReadPath(context.Background(), path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Record.FileMeta.TransferSyntaxUID != syntax.UID || result.Record.Study.InstanceUID != "1.2.3.626" {
				t.Fatalf("record = %+v", result.Record)
			}
		})
	}

	var raw bytes.Buffer
	dataset := object.FromElements([]core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.raw"),
	}, std.Dictionary)
	if err := object.WriteDataSet(&raw, dataset, transfer.ExplicitVRBigEndian); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAt(context.Background(), "raw", bytes.NewReader(raw.Bytes()), int64(raw.Len()), Options{}); err == nil || !errors.Is(err, object.ErrMissingPreamble) {
		t.Fatalf("strict raw error = %v", err)
	}
	result, err := ReadAt(context.Background(), "raw", bytes.NewReader(raw.Bytes()), int64(raw.Len()), Options{
		RawDataSet: &RawDataSetOptions{TransferSyntax: transfer.ExplicitVRBigEndian},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.Study.InstanceUID != "1.2.3.raw" || len(result.Record.Diagnostics) == 0 || result.Record.Diagnostics[0].Code != CodeRawDataSet {
		t.Fatalf("raw result = %+v", result)
	}

	uidOnly := transfer.ExplicitVRBigEndian
	uidOnly.Name = "caller-provided fields are not authoritative"
	uidOnly.ExplicitVR = false
	result, err = ReadAt(context.Background(), "raw", bytes.NewReader(raw.Bytes()), int64(raw.Len()), Options{
		RawDataSet: &RawDataSetOptions{TransferSyntax: uidOnly},
	})
	if err != nil || result.Record.Study.InstanceUID != "1.2.3.raw" {
		t.Fatalf("canonical raw syntax result/error = %+v/%v", result, err)
	}
}

func TestRecordMatchesFullObjectExtraction(t *testing.T) {
	rows := make([]byte, 2)
	columns := make([]byte, 2)
	transfer.ExplicitVRLittleEndian.ByteOrder.PutUint16(rows, 512)
	transfer.ExplicitVRLittleEndian.ByteOrder.PutUint16(columns, 256)
	path := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		textIndexElement(tags.PatientName, core.VRPN, "DOE^JANE"),
		textIndexElement(tags.PatientID, core.VRLO, "ID-626"),
		textIndexElement(tags.PatientBirthDate, core.VRDA, "19700101"),
		textIndexElement(tags.PatientSex, core.VRCS, "F"),
		textIndexElement(tags.AccessionNumber, core.VRSH, "ACC-626"),
		textIndexElement(tags.StudyDate, core.VRDA, "20260808"),
		textIndexElement(tags.StudyTime, core.VRTM, "010203"),
		textIndexElement(tags.StudyDescription, core.VRLO, "INDEX STUDY"),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.4"),
		textIndexElement(tags.Modality, core.VRCS, "CT"),
		textIndexElement(tags.SeriesDate, core.VRDA, "20260808"),
		textIndexElement(tags.SeriesTime, core.VRTM, "020304"),
		textIndexElement(tags.SeriesDescription, core.VRLO, "INDEX SERIES"),
		textIndexElement(tags.SeriesNumber, core.VRIS, "6"),
		textIndexElement(tags.SeriesInstanceUID, core.VRUI, "1.2.3.4.5"),
		textIndexElement(tags.InstanceNumber, core.VRIS, "7"),
		textIndexElement(tags.NumberOfFrames, core.VRIS, "3"),
		core.NewRawElement(tags.Rows, core.VRUS, rows),
		core.NewRawElement(tags.Columns, core.VRUS, columns),
		textIndexElement(tags.PixelSpacing, core.VRDS, "0.5\\0.75"),
		textIndexElement(tags.ImagePositionPatient, core.VRDS, "1\\2\\3"),
		textIndexElement(tags.ImageOrientationPatient, core.VRDS, "1\\0\\0\\0\\1\\0"),
		textIndexElement(tags.SliceThickness, core.VRDS, "2.5"),
	})
	indexed, err := ReadPath(context.Background(), path, Options{Profile: ProfileAll})
	if err != nil {
		t.Fatal(err)
	}
	full, err := object.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	if got, _ := full.Dataset.GetString(tags.PatientName); indexed.Record.Patient == nil || indexed.Record.Patient.Name != got {
		t.Fatalf("patient = %+v, full = %q", indexed.Record.Patient, got)
	}
	if got, _ := full.Dataset.GetUID(tags.StudyInstanceUID); indexed.Record.Study.InstanceUID != got {
		t.Fatalf("study UID = %q, full = %q", indexed.Record.Study.InstanceUID, got)
	}
	if got, _ := full.Dataset.GetUID(tags.SeriesInstanceUID); indexed.Record.Series.InstanceUID != got {
		t.Fatalf("series UID = %q, full = %q", indexed.Record.Series.InstanceUID, got)
	}
	if got, _ := full.Dataset.GetInt(tags.NumberOfFrames); indexed.Record.Instance.NumberOfFrames != got {
		t.Fatalf("frames = %d, full = %d", indexed.Record.Instance.NumberOfFrames, got)
	}
	if indexed.Record.Geometry == nil || indexed.Record.Geometry.Rows != 512 || indexed.Record.Geometry.Columns != 256 {
		t.Fatalf("geometry = %+v", indexed.Record.Geometry)
	}
	if got, _ := full.Dataset.GetFloats(tags.PixelSpacing); !reflect.DeepEqual(indexed.Record.Geometry.PixelSpacing, got) {
		t.Fatalf("spacing = %v, full = %v", indexed.Record.Geometry.PixelSpacing, got)
	}
}

func TestRecordPreservesFullParserLastWinsAndCharacterSetSemantics(t *testing.T) {
	latin1Description := core.NewRawElement(tags.StudyDescription, core.VRLO, []byte{' ', ' ', 'C', 'a', 'f', 0xe9})
	encoded, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian,
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		textIndexElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 6"),
		textIndexElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 100"),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.first"),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.last"),
		latin1Description,
	)
	if err != nil {
		t.Fatal(err)
	}
	indexed, err := ReadAt(context.Background(), "", bytes.NewReader(encoded), int64(len(encoded)), Options{
		Profile: ProfileCore | ProfileDescriptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	full, err := object.ReadFile(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := full.Dataset.GetUID(tags.StudyInstanceUID); indexed.Record.Study.InstanceUID != got || got != "1.2.3.last" {
		t.Fatalf("last-wins UID indexed/full = %q/%q", indexed.Record.Study.InstanceUID, got)
	}
	if got, _ := full.Dataset.GetString(tags.StudyDescription); indexed.Record.Study.Description != got || got != "  Café" {
		t.Fatalf("description indexed/full = %q/%q", indexed.Record.Study.Description, got)
	}
}

func TestBigEndianGeometryMatchesFullObject(t *testing.T) {
	rows := make([]byte, 2)
	columns := make([]byte, 2)
	transfer.ExplicitVRBigEndian.ByteOrder.PutUint16(rows, 321)
	transfer.ExplicitVRBigEndian.ByteOrder.PutUint16(columns, 654)
	path := writeIndexFixture(t, transfer.ExplicitVRBigEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		core.NewRawElement(tags.Rows, core.VRUS, rows),
		core.NewRawElement(tags.Columns, core.VRUS, columns),
		textIndexElement(tags.PixelSpacing, core.VRDS, "0.4\\0.8"),
		textIndexElement(tags.ImagePositionPatient, core.VRDS, "-1\\2\\3"),
		textIndexElement(tags.ImageOrientationPatient, core.VRDS, "0\\1\\0\\0\\0\\-1"),
		textIndexElement(tags.SliceThickness, core.VRDS, "1.25"),
	})
	indexed, err := ReadPath(context.Background(), path, Options{Profile: ProfileCore | ProfileGeometry})
	if err != nil {
		t.Fatal(err)
	}
	full, err := object.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	spacing, _ := full.Dataset.GetFloats(tags.PixelSpacing)
	position, _ := full.Dataset.GetFloats(tags.ImagePositionPatient)
	orientation, _ := full.Dataset.GetFloats(tags.ImageOrientationPatient)
	thickness, _ := full.Dataset.GetFloats(tags.SliceThickness)
	if indexed.Record.Geometry == nil || indexed.Record.Geometry.Rows != 321 || indexed.Record.Geometry.Columns != 654 ||
		!reflect.DeepEqual(indexed.Record.Geometry.PixelSpacing, spacing) ||
		!reflect.DeepEqual(indexed.Record.Geometry.ImagePositionPatient, position) ||
		!reflect.DeepEqual(indexed.Record.Geometry.ImageOrientationPatient, orientation) ||
		len(thickness) != 1 || indexed.Record.Geometry.SliceThickness != thickness[0] {
		t.Fatalf("indexed/full geometry = %+v / spacing=%v position=%v orientation=%v thickness=%v",
			indexed.Record.Geometry, spacing, position, orientation, thickness)
	}
}

func TestOccurrenceFirstSkipsLaterOversizedDuplicates(t *testing.T) {
	privateTag := core.NewTag(0x0011, 0x1010)
	encoded, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian,
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		core.NewRawElement(privateTag, core.VROB, []byte("first")),
		core.NewRawElement(privateTag, core.VROB, bytes.Repeat([]byte{0x7a}, 128<<10)),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.after-duplicates"),
	)
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingReaderAt{data: encoded}
	result, err := ReadAt(context.Background(), "", counted, int64(len(encoded)), Options{
		Selectors: []Selector{{ID: "first", Tag: privateTag, Occurrence: OccurrenceFirst, MaxValueBytes: 8}},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := result.Record.Selected["first"]
	if len(values) != 1 {
		t.Fatalf("selected = %+v", values)
	}
	raw, _ := values[0].Element.RawBytes()
	if !bytes.Equal(bytes.TrimRight(raw, "\x00"), []byte("first")) {
		t.Fatalf("selected = %+v", values)
	}
	if result.Record.Study.InstanceUID != "1.2.3.after-duplicates" {
		t.Fatalf("study UID = %q", result.Record.Study.InstanceUID)
	}
	if counted.bytesRead >= int64(len(encoded)) {
		t.Fatalf("reader materialized later duplicate: %d/%d", counted.bytesRead, len(encoded))
	}
}

func TestReadNestedSelectorAndPrivateDictionary(t *testing.T) {
	sequenceTag := core.NewTag(0x0040, 0xA730)
	privateTag := core.NewTag(0x0011, 0x1010)
	sequence := core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			textIndexElement(privateTag, core.VRLO, "SELECTED"),
		}}}},
	}
	path := writeIndexFixture(t, transfer.ImplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		sequence,
	})
	overlay := dictionary.Chain{singleIndexDictionary{entry: dictionary.Entry{Tag: privateTag, VR: core.VRLO}}, std.Dictionary}
	result, err := ReadPath(context.Background(), path, Options{
		Dictionary: overlay,
		Selectors:  []Selector{{ID: "private", Path: []core.Tag{sequenceTag}, Tag: privateTag, Occurrence: OccurrenceAll}},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := result.Record.Selected["private"]
	if len(values) != 1 || values[0].Element.StringValue() != "SELECTED" || len(values[0].Path) != 2 || values[0].Path[0].ItemIndex != 0 {
		t.Fatalf("selected = %+v", values)
	}
}

func TestErrorsDoNotContainOriginOrValues(t *testing.T) {
	secret := "SECRET-PATIENT-PATH"
	_, err := ReadAt(context.Background(), secret, bytes.NewReader([]byte("not dicom")), 9, Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "not dicom") {
		t.Fatalf("error leaked input: %v", err)
	}
}

func TestErrorChainsPreserveOnlyRedactedClassifications(t *testing.T) {
	secretPath := t.TempDir() + "/SECRET-PATIENT-NAME.dcm"
	_, err := ReadPath(context.Background(), secretPath, Options{})
	var pathErr *os.PathError
	if err == nil || errors.As(err, &pathErr) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("path error classification = %v, pathErr=%v", err, pathErr)
	}

	privateTag := core.NewTag(0x0011, 0x1010)
	path := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		textIndexElement(privateTag, core.VRLO, "SECRET-VALUE"),
	})
	secretCallbackErr := errors.New("SECRET-CALLBACK-ERROR")
	_, err = ReadPath(context.Background(), path, Options{
		Profile: ProfileNone,
		Selectors: []Selector{{ID: "private", Tag: privateTag, Handle: func(context.Context, SelectedElement) error {
			return secretCallbackErr
		}}},
	})
	if err == nil || !errors.Is(err, ErrSelector) || errors.Is(err, secretCallbackErr) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("callback error classification = %v", err)
	}
}

func TestReadJoinsRedactedCloseFailureWithReadFailure(t *testing.T) {
	secretCloseErr := errors.New("SECRET-CLOSE-ERROR")
	source := SourceFunc(func(context.Context) (OpenedSource, error) {
		return OpenedSource{
			Reader: bytes.NewReader([]byte("not dicom")),
			Info:   SourceInfo{Size: 9},
			Close:  func() error { return secretCloseErr },
		}, nil
	})
	_, err := Read(context.Background(), source, Options{})
	if err == nil || !errors.Is(err, object.ErrMissingPreamble) || !errors.Is(err, ErrClose) ||
		errors.Is(err, secretCloseErr) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("joined read/close error = %v", err)
	}
}

func TestReadProtectsInvalidHandleCleanup(t *testing.T) {
	source := SourceFunc(func(context.Context) (OpenedSource, error) {
		return OpenedSource{Info: SourceInfo{Size: 1}, Close: func() error {
			panic("SECRET-INVALID-HANDLE-CLOSE")
		}}, nil
	})
	_, err := Read(context.Background(), source, Options{})
	if err == nil || !errors.Is(err, ErrInvalidOptions) || !errors.Is(err, ErrClose) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("invalid handle cleanup error = %v", err)
	}
}

func TestFileMetaAndPresentInvalidValuesProduceDiagnostics(t *testing.T) {
	encoded := encodeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		core.NewRawElement(tags.PatientName, core.VRUS, []byte{1, 0}),
		core.NewRawElement(tags.Rows, core.VRSS, []byte{1, 0}),
	})
	withoutGroupLength := append(append([]byte(nil), encoded[:132]...), encoded[144:]...)
	mediaSOPClassTag := []byte{0x02, 0x00, 0x02, 0x00}
	uidOffset := bytes.Index(withoutGroupLength[132:], mediaSOPClassTag)
	if uidOffset < 0 {
		t.Fatal("Media Storage SOP Class UID not found")
	}
	uidOffset += 132
	withoutGroupLength[uidOffset+4] = 'L'
	withoutGroupLength[uidOffset+5] = 'O'
	result, err := ReadAt(context.Background(), "", bytes.NewReader(withoutGroupLength), int64(len(withoutGroupLength)), Options{
		Profile:                            ProfileCore | ProfilePatient | ProfileGeometry,
		AllowMissingMetaElementGroupLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantInvalid := map[string]bool{
		"file_meta.media_storage_sop_class_uid": false,
		"patient.name":                          false,
		"geometry.rows":                         false,
	}
	groupLengthMissing := false
	for _, diagnostic := range result.Record.Diagnostics {
		if diagnostic.Code == CodeInvalidValue {
			if _, ok := wantInvalid[diagnostic.Field]; ok {
				wantInvalid[diagnostic.Field] = true
			}
		}
		if diagnostic.Code == CodeMissingField && diagnostic.Field == "file_meta.group_length" {
			groupLengthMissing = true
		}
	}
	if !wantInvalid["file_meta.media_storage_sop_class_uid"] || !wantInvalid["patient.name"] ||
		!wantInvalid["geometry.rows"] || !groupLengthMissing {
		t.Fatalf("diagnostics = %+v", result.Record.Diagnostics)
	}
}

func TestTokenAndFragmentLimitsAreExact(t *testing.T) {
	sequenceTag := core.NewTag(0x0040, 0xA730)
	sequence := core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{Items: []core.DataSet{{}, {}}},
	}
	var raw bytes.Buffer
	if err := object.WriteDataSet(&raw, object.FromElements([]core.Element{
		sequence,
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.after-items"),
	}, std.Dictionary), transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	readRaw := func(limits Limits) (Result, error) {
		return ReadAt(context.Background(), "", bytes.NewReader(raw.Bytes()), int64(raw.Len()), Options{
			Limits: limits, RawDataSet: &RawDataSetOptions{TransferSyntax: transfer.ExplicitVRLittleEndian},
		})
	}
	if result, err := readRaw(Limits{MaxTokens: 7}); err != nil || result.Record.Study.InstanceUID != "1.2.3.after-items" {
		t.Fatalf("exact token limit result/error = %+v/%v", result, err)
	}
	if _, err := readRaw(Limits{MaxTokens: 6}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("token +1 error = %v", err)
	}

	iconImageSequence := core.NewTag(0x0088, 0x0200)
	pixel := core.Element{
		Header: core.ElementHeader{Tag: tags.PixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: [][]byte{{1, 2}, {3, 4}}},
	}
	icon := core.Element{
		Header: core.ElementHeader{Tag: iconImageSequence, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{pixel}}}},
	}
	raw.Reset()
	if err := object.WriteDataSet(&raw, object.FromElements([]core.Element{
		icon,
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.after-fragments"),
	}, std.Dictionary), transfer.JPEGBaseline); err != nil {
		t.Fatal(err)
	}
	readFragments := func(max int) (Result, error) {
		return ReadAt(context.Background(), "", bytes.NewReader(raw.Bytes()), int64(raw.Len()), Options{
			Limits: Limits{MaxFragments: max}, RawDataSet: &RawDataSetOptions{TransferSyntax: transfer.JPEGBaseline},
		})
	}
	if result, err := readFragments(2); err != nil || result.Record.Study.InstanceUID != "1.2.3.after-fragments" {
		t.Fatalf("exact fragment limit result/error = %+v/%v", result, err)
	}
	if _, err := readFragments(1); !errors.Is(err, ErrResourceLimit) || !errors.Is(err, parser.ErrMaxFragmentsExceeded) {
		t.Fatalf("fragment +1 error = %v", err)
	}
}

func TestPart10PreambleHonorsMaxTotalBeforeReading(t *testing.T) {
	encoded := encodeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	counted := &countingReaderAt{data: encoded}
	_, err := ReadAt(context.Background(), "", counted, int64(len(encoded)), Options{
		Limits: Limits{MaxTotalBytes: 1},
	})
	if err == nil || !errors.Is(err, ErrResourceLimit) || counted.bytesRead > 1 {
		t.Fatalf("error/bytes read = %v/%d, want bounded failure", err, counted.bytesRead)
	}
}

func TestDeflatedLogicalBytesAndMalformedLengthRemainBounded(t *testing.T) {
	encoded := encodeIndexFixture(t, transfer.DeflatedExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		core.NewRawElement(core.NewTag(0x0019, 0x1010), core.VROB, bytes.Repeat([]byte{0}, 64<<10)),
	})
	_, err := ReadAt(context.Background(), "", bytes.NewReader(encoded), int64(len(encoded)), Options{
		Limits: Limits{MaxTotalBytes: 1024},
	})
	if !errors.Is(err, parser.ErrMaxTotalBytesExceeded) {
		t.Fatalf("deflated limit error = %v", err)
	}

	var malformed bytes.Buffer
	_ = binary.Write(&malformed, binary.LittleEndian, tags.StudyInstanceUID.Group)
	_ = binary.Write(&malformed, binary.LittleEndian, tags.StudyInstanceUID.Element)
	malformed.WriteString("UI")
	_ = binary.Write(&malformed, binary.LittleEndian, uint16(128))
	malformed.WriteString("1\x00")
	_, err = ReadAt(context.Background(), "", bytes.NewReader(malformed.Bytes()), int64(malformed.Len()), Options{
		RawDataSet: &RawDataSetOptions{TransferSyntax: transfer.ExplicitVRLittleEndian},
	})
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("malformed length error = %v", err)
	}
}

func TestSelectorHandlersAreBoundedRedactedAndContextAware(t *testing.T) {
	privateTag := core.NewTag(0x0011, 0x1010)
	path := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		textIndexElement(privateTag, core.VRLO, "SECRET-SELECTED-VALUE"),
	})
	_, err := ReadPath(context.Background(), path, Options{
		Profile: ProfileNone,
		Selectors: []Selector{{
			ID: "panic", Tag: privateTag,
			Handle: func(context.Context, SelectedElement) error { panic("SECRET-PANIC") },
		}},
	})
	if err == nil || !errors.Is(err, ErrSelector) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("panic error = %v", err)
	}
	callbackCtx, callbackCancel := context.WithCancel(context.Background())
	_, err = ReadPath(callbackCtx, path, Options{
		Profile: ProfileNone,
		Selectors: []Selector{{
			ID: "cancel-panic", Tag: privateTag,
			Handle: func(context.Context, SelectedElement) error {
				callbackCancel()
				panic("SECRET-CANCELED-PANIC")
			},
		}},
	})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("cancel+panic error = %v", err)
	}
	callbackCtx, callbackCancel = context.WithCancel(context.Background())
	_, err = ReadPath(callbackCtx, path, Options{
		Profile: ProfileNone,
		Selectors: []Selector{{
			ID: "cancel-error", Tag: privateTag,
			Handle: func(context.Context, SelectedElement) error {
				callbackCancel()
				return errors.New("SECRET-CANCELED-ERROR")
			},
		}},
	})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("cancel+error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ReadPath(ctx, path, Options{Profile: ProfileNone})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}

	_, err = ReadPath(context.Background(), path, Options{
		Profile: ProfileNone,
		Limits:  Limits{MaxSelectedValues: 1},
		Selectors: []Selector{
			{ID: "first", Tag: privateTag},
			{ID: "second", Tag: privateTag},
		},
	})
	if err == nil {
		t.Fatal("selected-value limit error = nil")
	}
}

func BenchmarkReadMetadataIndex(b *testing.B) {
	encoded := encodeIndexFixture(b, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		core.NewRawElement(core.NewTag(0x0019, 0x1010), core.VROB, bytes.Repeat([]byte{1}, 256<<10)),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.626"),
		core.NewRawElement(tags.PixelData, core.VROW, bytes.Repeat([]byte{2}, 1<<20)),
	})
	b.ReportAllocs()
	b.Run("metadata", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := ReadAt(context.Background(), "", bytes.NewReader(encoded), int64(len(encoded)), Options{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("full-object", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := object.ReadFileWithOptions(bytes.NewReader(encoded), object.ReadFileOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkReadMetadataIndexSeries(b *testing.B) {
	encoded := encodeIndexFixture(b, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		core.NewRawElement(core.NewTag(0x0019, 0x1010), core.VROB, bytes.Repeat([]byte{1}, 4<<10)),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.626"),
		core.NewRawElement(tags.PixelData, core.VROW, bytes.Repeat([]byte{2}, 256<<10)),
	})
	for _, count := range []int{100, 1000} {
		for _, mode := range []string{"metadata", "full-object"} {
			b.Run(fmt.Sprintf("%s/%d-slices", mode, count), func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(count), "slices/op")
				for i := 0; i < b.N; i++ {
					for range count {
						if mode == "metadata" {
							if _, err := ReadAt(context.Background(), "", bytes.NewReader(encoded), int64(len(encoded)), Options{}); err != nil {
								b.Fatal(err)
							}
							continue
						}
						if _, err := object.ReadFileWithOptions(bytes.NewReader(encoded), object.ReadFileOptions{}); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

type testingTB interface {
	Helper()
	Fatal(...any)
	TempDir() string
}

func writeIndexFixture(t testingTB, syntax transfer.Syntax, elements []core.Element) string {
	t.Helper()
	encoded := encodeIndexFixture(t, syntax, elements)
	path := t.TempDir() + "/fixture.dcm"
	if err := writeTestFile(path, encoded); err != nil {
		t.Fatal(err)
	}
	return path
}

func encodeIndexFixture(t interface {
	Helper()
	Fatal(...any)
}, syntax transfer.Syntax, elements []core.Element) []byte {
	t.Helper()
	file := &object.File{Dataset: object.FromElements(elements, std.Dictionary), TransferSyntax: syntax}
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func textIndexElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.NewRawElement(tag, vr, []byte(value))
}

type countingReaderAt struct {
	data      []byte
	bytesRead int64
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	r.bytesRead += int64(n)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type singleIndexDictionary struct{ entry dictionary.Entry }

func (d singleIndexDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	return d.entry, tag == d.entry.Tag
}
func (d singleIndexDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}
