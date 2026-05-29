package dimse

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	// StatusProcessingFailure is the generic DIMSE-N processing failure.
	StatusProcessingFailure uint16 = 0x0110
)

var (
	// ErrNormalizedPresentationContext marks a normalized request whose SOP
	// Class is not compatible with the selected presentation context.
	ErrNormalizedPresentationContext = errors.New("dicom dimse: incompatible normalized presentation context")
	// ErrNormalizedStatus marks a non-success normalized DIMSE response.
	ErrNormalizedStatus = errors.New("dicom dimse: normalized operation status")
)

// NormalizedStatusClass classifies a normalized DIMSE response status without
// discarding the original status code.
type NormalizedStatusClass int

const (
	NormalizedStatusSuccess NormalizedStatusClass = iota
	NormalizedStatusWarning
	NormalizedStatusFailure
)

func (c NormalizedStatusClass) String() string {
	switch c {
	case NormalizedStatusSuccess:
		return "success"
	case NormalizedStatusWarning:
		return "warning"
	case NormalizedStatusFailure:
		return "failure"
	default:
		return "unknown"
	}
}

// ClassifyNormalizedStatus applies the warning classes from PS3.7 Annex C and
// treats every other non-zero status as a failure. Service-specific callers can
// continue to inspect the raw Status value.
func ClassifyNormalizedStatus(status uint16) NormalizedStatusClass {
	switch {
	case status == StatusSuccess:
		return NormalizedStatusSuccess
	case status == 0x0001, status == 0x0107, status == 0x0116:
		return NormalizedStatusWarning
	case status >= 0xB000 && status <= 0xBFFF:
		return NormalizedStatusWarning
	default:
		return NormalizedStatusFailure
	}
}

// NormalizedStatusFields preserves conditional command fields defined by the
// DIMSE-N service and status tables. Nil means that none were present.
type NormalizedStatusFields struct {
	ErrorComment            string
	ErrorIDOrNil            *uint16
	OffendingElements       []core.Tag
	AttributeIdentifierList []core.Tag
}

// NormalizedStatusError exposes warning/failure status details while allowing
// callers to recover the typed error with errors.Is/As.
type NormalizedStatusError struct {
	Service string
	Status  uint16
	Class   NormalizedStatusClass
	Fields  *NormalizedStatusFields
}

func (e *NormalizedStatusError) Error() string {
	if e == nil {
		return ErrNormalizedStatus.Error()
	}
	service := strings.TrimSpace(e.Service)
	if service == "" {
		service = "normalized operation"
	}
	comment := ""
	if e.Fields != nil && e.Fields.ErrorComment != "" {
		comment = ": " + e.Fields.ErrorComment
	}
	return fmt.Sprintf("%s: %s returned status 0x%04X (%s)%s", ErrNormalizedStatus, service, e.Status, e.Class, comment)
}

func (e *NormalizedStatusError) Unwrap() error { return ErrNormalizedStatus }

// CheckNormalizedStatus returns a typed error for warnings and failures. The
// caller still owns the decoded response and optional response dataset.
func CheckNormalizedStatus(service string, status uint16, fields *NormalizedStatusFields) error {
	class := ClassifyNormalizedStatus(status)
	if class == NormalizedStatusSuccess {
		return nil
	}
	return &NormalizedStatusError{
		Service: service,
		Status:  status,
		Class:   class,
		Fields:  cloneNormalizedStatusFields(fields),
	}
}

// NormalizedPresentationContextError identifies the selected presentation
// context and the SOP Class carried by the normalized command.
type NormalizedPresentationContextError struct {
	PresentationContextID byte
	AbstractSyntaxUID     string
	CommandSOPClassUID    string
}

func (e *NormalizedPresentationContextError) Error() string {
	if e == nil {
		return ErrNormalizedPresentationContext.Error()
	}
	return fmt.Sprintf("%s: context %d abstract syntax %q, command SOP Class %q", ErrNormalizedPresentationContext, e.PresentationContextID, e.AbstractSyntaxUID, e.CommandSOPClassUID)
}

func (e *NormalizedPresentationContextError) Unwrap() error {
	return ErrNormalizedPresentationContext
}

func normalizedDataSetType(hasDataSet bool) uint16 {
	if hasDataSet {
		return DataSetPresent
	}
	return NoDataSet
}

func normalizedHasDataSet(dataSetType uint16) bool {
	return dataSetType != NoDataSet
}

func appendOptionalUID(elements []core.Element, tag core.Tag, value string) []core.Element {
	if value == "" {
		return elements
	}
	return append(elements, newUIElement(tag, value))
}

func appendOptionalUS(elements []core.Element, tag core.Tag, value *uint16) []core.Element {
	if value == nil {
		return elements
	}
	return append(elements, newUSCommandElement(tag, *value))
}

func appendNormalizedStatusFields(elements []core.Element, fields *NormalizedStatusFields) []core.Element {
	if fields == nil {
		return elements
	}
	if fields.ErrorComment != "" {
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: ErrorComment, VR: core.VRLO},
			Value:  core.StringValue{fields.ErrorComment},
		})
	}
	elements = appendOptionalUS(elements, ErrorID, fields.ErrorIDOrNil)
	if len(fields.OffendingElements) > 0 {
		elements = append(elements, newATCommandElement(OffendingElement, fields.OffendingElements...))
	}
	if len(fields.AttributeIdentifierList) > 0 {
		elements = append(elements, newATCommandElement(AttributeIdentifierList, fields.AttributeIdentifierList...))
	}
	return elements
}

func parseNormalizedStatusFields(command *object.Object) (*NormalizedStatusFields, error) {
	if command == nil {
		return nil, fmt.Errorf("dicom dimse: nil command set")
	}
	fields := &NormalizedStatusFields{}
	present := false
	if _, ok := command.Get(ErrorComment); ok {
		comment, ok := command.GetString(ErrorComment)
		if !ok {
			return nil, fmt.Errorf("dicom dimse: command element %s is not a valid string", ErrorComment)
		}
		fields.ErrorComment = comment
		present = true
	}
	errorID, err := optionalCommandUint16(command, ErrorID)
	if err != nil {
		return nil, err
	}
	if errorID != nil {
		fields.ErrorIDOrNil = errorID
		present = true
	}
	offending, ok, err := optionalCommandTags(command, OffendingElement)
	if err != nil {
		return nil, err
	}
	if ok {
		fields.OffendingElements = append([]core.Tag(nil), offending...)
		present = true
	}
	attributes, ok, err := optionalCommandTags(command, AttributeIdentifierList)
	if err != nil {
		return nil, err
	}
	if ok {
		fields.AttributeIdentifierList = append([]core.Tag(nil), attributes...)
		present = true
	}
	if !present {
		return nil, nil
	}
	return fields, nil
}

func cloneNormalizedStatusFields(fields *NormalizedStatusFields) *NormalizedStatusFields {
	if fields == nil {
		return nil
	}
	clone := *fields
	if fields.ErrorIDOrNil != nil {
		value := *fields.ErrorIDOrNil
		clone.ErrorIDOrNil = &value
	}
	clone.OffendingElements = append([]core.Tag(nil), fields.OffendingElements...)
	clone.AttributeIdentifierList = append([]core.Tag(nil), fields.AttributeIdentifierList...)
	return &clone
}

func optionalCommandUID(command *object.Object, tag core.Tag) (string, bool, error) {
	if command == nil {
		return "", false, fmt.Errorf("dicom dimse: nil command set")
	}
	if _, ok := command.Get(tag); !ok {
		return "", false, nil
	}
	uid, ok := command.GetUID(tag)
	if !ok || uid == "" {
		return "", true, fmt.Errorf("dicom dimse: invalid command UID element %s", tag)
	}
	return uid, true, nil
}

func requireNormalizedUID(service, name, uid string) error {
	if strings.TrimSpace(uid) == "" {
		return fmt.Errorf("dicom dimse: %s %s is required", service, name)
	}
	return nil
}

func validateNormalizedCommandField(command *object.Object, service string, want uint16) error {
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return err
	}
	if field != want {
		return fmt.Errorf("dicom dimse: %s command field 0x%04X, want 0x%04X", service, field, want)
	}
	return nil
}
