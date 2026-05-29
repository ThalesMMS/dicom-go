package encapdoc

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestNewPDFBuildsEncapsulatedPDFFile(t *testing.T) {
	file, err := NewPDF(PDFOptions{
		SOPInstanceUID:      "1.2.3.sop",
		StudyInstanceUID:    "1.2.3.study",
		SeriesInstanceUID:   "1.2.3.series",
		PatientName:         "PDF^PATIENT",
		PatientID:           "PID",
		SeriesDescription:   "Report",
		SeriesNumber:        "7",
		InstanceNumber:      "3",
		ContentDate:         "20260709",
		ContentTime:         "143000",
		AcquisitionDateTime: "20260709140000-0300",
		BurnedInAnnotation:  BurnedInAnnotationYes,
		DocumentTitle:       "Discharge report",
		Data:                []byte("%PDF-1.4\n"),
	})
	if err != nil {
		t.Fatalf("NewPDF() error = %v", err)
	}
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x0016), EncapsulatedPDFStorage)
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x0005), "ISO_IR 192")
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x0018), "1.2.3.sop")
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x0060), "DOC")
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x0064), "WSD")
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x103E), "Report")
	assertString(t, file.Dataset, core.NewTag(0x0010, 0x0010), "PDF^PATIENT")
	assertString(t, file.Dataset, core.NewTag(0x0010, 0x0020), "PID")
	assertString(t, file.Dataset, core.NewTag(0x0020, 0x000D), "1.2.3.study")
	assertString(t, file.Dataset, core.NewTag(0x0020, 0x000E), "1.2.3.series")
	assertString(t, file.Dataset, core.NewTag(0x0020, 0x0011), "7")
	assertString(t, file.Dataset, core.NewTag(0x0020, 0x0013), "3")
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x0023), "20260709")
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x0033), "143000")
	assertString(t, file.Dataset, core.NewTag(0x0008, 0x002A), "20260709140000-0300")
	assertString(t, file.Dataset, core.NewTag(0x0028, 0x0301), "YES")
	assertString(t, file.Dataset, core.NewTag(0x0042, 0x0010), "Discharge report")
	assertString(t, file.Dataset, core.NewTag(0x0042, 0x0012), "application/pdf")
	if concepts, ok := file.Dataset.GetSequence(core.NewTag(0x0040, 0xA043)); !ok || len(concepts) != 0 {
		t.Fatalf("ConceptNameCodeSequence = %d items, ok=%v; want present and empty", len(concepts), ok)
	}

	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	round, err := object.ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	assertString(t, round.Dataset, core.NewTag(0x0008, 0x0016), EncapsulatedPDFStorage)
	assertString(t, round.Dataset, tagDocumentTitle, "Discharge report")
	assertString(t, round.Dataset, tagBurnedInAnnotation, BurnedInAnnotationYes)
	if concepts, ok := round.Dataset.GetSequence(tagConceptNameCodeSequence); !ok || len(concepts) != 0 {
		t.Fatalf("round-trip ConceptNameCodeSequence = %d items, ok=%v; want present and empty", len(concepts), ok)
	}
}

func TestNewPDFEmitsSafeDefaultsAndEmptyType2Attributes(t *testing.T) {
	file, err := NewPDF(PDFOptions{SOPInstanceUID: "1.2.3.defaults", Data: []byte("%PDF-1.4\n")})
	if err != nil {
		t.Fatal(err)
	}
	assertString(t, file.Dataset, tagSeriesNumber, "1")
	assertString(t, file.Dataset, tagInstanceNumber, "1")
	assertString(t, file.Dataset, tagBurnedInAnnotation, BurnedInAnnotationYes)
	for _, tag := range []core.Tag{tagContentDate, tagContentTime, tagAcquisitionDateTime, tagDocumentTitle} {
		if got, ok := file.Dataset.GetString(tag); !ok || got != "" {
			t.Fatalf("%v = %q, ok=%v; want present and empty Type 2 attribute", tag, got, ok)
		}
	}
	if concepts, ok := file.Dataset.GetSequence(tagConceptNameCodeSequence); !ok || len(concepts) != 0 {
		t.Fatalf("ConceptNameCodeSequence = %d items, ok=%v; want present and empty", len(concepts), ok)
	}
}

func TestNewPDFRejectsInvalidBurnedInAnnotation(t *testing.T) {
	_, err := NewPDF(PDFOptions{
		SOPInstanceUID:     "1.2.3.invalid-burned-in",
		BurnedInAnnotation: "UNKNOWN",
		Data:               []byte("%PDF-1.4\n"),
	})
	if !errors.Is(err, ErrInvalidBurnedInAnnotation) {
		t.Fatalf("NewPDF() error = %v, want ErrInvalidBurnedInAnnotation", err)
	}
}

func TestNewPDFRoundTripsUnicodeMetadata(t *testing.T) {
	file, err := NewPDF(PDFOptions{
		SOPInstanceUID: "1.2.3.unicode", PatientName: "João^Silva",
		SeriesDescription: "Relatório clínico", Data: []byte("%PDF-1.4\n"),
	})
	if err != nil {
		t.Fatalf("NewPDF() error = %v", err)
	}
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	round, err := object.ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, err := round.Dataset.LookupString(tagPatientName); err != nil || got != "João^Silva" {
		t.Fatalf("PatientName = %q, %v; want João^Silva", got, err)
	}
	if got, err := round.Dataset.LookupString(tagSeriesDescription); err != nil || got != "Relatório clínico" {
		t.Fatalf("SeriesDescription = %q, %v; want Relatório clínico", got, err)
	}
}

func TestNewPDFRejectsBlankSOPInstanceUID(t *testing.T) {
	_, err := NewPDF(PDFOptions{SOPInstanceUID: " \t", Data: []byte("%PDF-1.4\n")})
	if !errors.Is(err, object.ErrMissingSOPInstanceUID) {
		t.Fatalf("NewPDF() error = %v, want ErrMissingSOPInstanceUID", err)
	}
}

func TestDocumentLengthRejectsUint32Overflow(t *testing.T) {
	if strconv.IntSize == 32 {
		t.Skip("cannot represent oversized document length on 32-bit int")
	}
	_, err := documentLengthElement(int(int64(math.MaxUint32) + 1))
	if !errors.Is(err, ErrEncapsulatedDocumentTooLarge) {
		t.Fatalf("documentLengthElement() error = %v, want ErrEncapsulatedDocumentTooLarge", err)
	}
}

func assertString(t *testing.T, obj *object.Object, tag core.Tag, want string) {
	t.Helper()
	got, _ := obj.GetString(tag)
	if got != want {
		t.Fatalf("%v = %q, want %q", tag, got, want)
	}
}
