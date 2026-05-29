// Package encapdoc builds DICOM Encapsulated Document objects.
package encapdoc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	EncapsulatedPDFStorage = "1.2.840.10008.5.1.4.1.1.104.1"
	BurnedInAnnotationYes  = "YES"
	BurnedInAnnotationNo   = "NO"
)

var (
	ErrEncapsulatedDocumentTooLarge = errors.New("encapdoc: encapsulated document is too large")
	ErrInvalidBurnedInAnnotation    = errors.New("encapdoc: Burned In Annotation must be YES or NO")
)

var (
	tagSpecificCharacterSet       = core.NewTag(0x0008, 0x0005)
	tagContentDate                = core.NewTag(0x0008, 0x0023)
	tagAcquisitionDateTime        = core.NewTag(0x0008, 0x002A)
	tagContentTime                = core.NewTag(0x0008, 0x0033)
	tagModality                   = core.NewTag(0x0008, 0x0060)
	tagPatientName                = core.NewTag(0x0010, 0x0010)
	tagPatientID                  = core.NewTag(0x0010, 0x0020)
	tagStudyInstanceUID           = core.NewTag(0x0020, 0x000D)
	tagSeriesInstanceUID          = core.NewTag(0x0020, 0x000E)
	tagSeriesNumber               = core.NewTag(0x0020, 0x0011)
	tagInstanceNumber             = core.NewTag(0x0020, 0x0013)
	tagBurnedInAnnotation         = core.NewTag(0x0028, 0x0301)
	tagConceptNameCodeSequence    = core.NewTag(0x0040, 0xA043)
	tagDocumentTitle              = core.NewTag(0x0042, 0x0010)
	tagEncapsulatedDocument       = core.NewTag(0x0042, 0x0011)
	tagMIMETypeOfEncapsulatedDoc  = core.NewTag(0x0042, 0x0012)
	tagEncapsulatedDocumentLength = core.NewTag(0x0042, 0x0015)
	tagConversionType             = core.NewTag(0x0008, 0x0064)
	tagSeriesDescription          = core.NewTag(0x0008, 0x103E)
	tagSOPClassUID                = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID             = core.NewTag(0x0008, 0x0018)
)

type PDFOptions struct {
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	PatientName         string
	PatientID           string
	SeriesDescription   string
	SeriesNumber        string
	InstanceNumber      string
	ContentDate         string
	ContentTime         string
	AcquisitionDateTime string
	// BurnedInAnnotation accepts YES or NO. Empty defaults to YES because an
	// arbitrary PDF may contain identifying text that DICOM treats as burned in.
	BurnedInAnnotation string
	DocumentTitle      string
	Data               []byte
}

// NewPDF builds a DICOM Encapsulated PDF object using Explicit VR Little Endian.
// Empty SeriesNumber and InstanceNumber default to "1"; Type 2 document
// attributes are emitted even when their values are unknown.
func NewPDF(opts PDFOptions) (*object.File, error) {
	if strings.TrimSpace(opts.SOPInstanceUID) == "" {
		return nil, object.ErrMissingSOPInstanceUID
	}
	burnedInAnnotation := strings.ToUpper(strings.TrimSpace(opts.BurnedInAnnotation))
	if burnedInAnnotation == "" {
		burnedInAnnotation = BurnedInAnnotationYes
	}
	if burnedInAnnotation != BurnedInAnnotationYes && burnedInAnnotation != BurnedInAnnotationNo {
		return nil, fmt.Errorf("%w: %q", ErrInvalidBurnedInAnnotation, opts.BurnedInAnnotation)
	}
	lengthElement, err := documentLengthElement(len(opts.Data))
	if err != nil {
		return nil, err
	}
	elements := []core.Element{
		stringElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 192"),
		stringElement(tagSOPClassUID, core.VRUI, EncapsulatedPDFStorage),
		stringElement(tagSOPInstanceUID, core.VRUI, opts.SOPInstanceUID),
		stringElement(tagContentDate, core.VRDA, opts.ContentDate),
		stringElement(tagAcquisitionDateTime, core.VRDT, opts.AcquisitionDateTime),
		stringElement(tagContentTime, core.VRTM, opts.ContentTime),
		stringElement(tagModality, core.VRCS, "DOC"),
		stringElement(tagConversionType, core.VRCS, "WSD"),
		stringElement(tagSeriesDescription, core.VRLO, opts.SeriesDescription),
		stringElement(tagStudyInstanceUID, core.VRUI, opts.StudyInstanceUID),
		stringElement(tagSeriesInstanceUID, core.VRUI, opts.SeriesInstanceUID),
		stringElement(tagSeriesNumber, core.VRIS, defaultString(opts.SeriesNumber, "1")),
		stringElement(tagInstanceNumber, core.VRIS, defaultString(opts.InstanceNumber, "1")),
		stringElement(tagPatientName, core.VRPN, opts.PatientName),
		stringElement(tagPatientID, core.VRLO, opts.PatientID),
		stringElement(tagBurnedInAnnotation, core.VRCS, burnedInAnnotation),
		emptySequenceElement(tagConceptNameCodeSequence),
		stringElement(tagDocumentTitle, core.VRST, opts.DocumentTitle),
		stringElement(tagMIMETypeOfEncapsulatedDoc, core.VRLO, "application/pdf"),
		core.NewRawElement(tagEncapsulatedDocument, core.VROB, append([]byte(nil), opts.Data...)),
		lengthElement,
	}
	return &object.File{
		Dataset:        object.FromElements(elements, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}, nil
}

func defaultString(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func emptySequenceElement(tag core.Tag) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{},
	}
}

func documentLengthElement(length int) (core.Element, error) {
	if length > math.MaxUint32 {
		return core.Element{}, fmt.Errorf("%w: %d bytes", ErrEncapsulatedDocumentTooLarge, length)
	}
	return ulRawElement(tagEncapsulatedDocumentLength, uint32(length)), nil
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue{strings.TrimSpace(value)},
	}
}

func ulRawElement(tag core.Tag, value uint32) core.Element {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], value)
	return core.NewRawElement(tag, core.VRUL, raw[:])
}
