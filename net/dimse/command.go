package dimse

import (
	"encoding/binary"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	CommandGroupLength        = core.NewTag(0x0000, 0x0000)
	AffectedSOPClassUID       = core.NewTag(0x0000, 0x0002)
	AffectedSOPInstanceUID    = core.NewTag(0x0000, 0x1000)
	CommandField              = core.NewTag(0x0000, 0x0100)
	MessageID                 = core.NewTag(0x0000, 0x0110)
	MessageIDBeingRespondedTo = core.NewTag(0x0000, 0x0120)
	Priority                  = core.NewTag(0x0000, 0x0700)
	CommandDataSetType        = core.NewTag(0x0000, 0x0800)
	Status                    = core.NewTag(0x0000, 0x0900)
	OffendingElement          = core.NewTag(0x0000, 0x0901)
	ErrorComment              = core.NewTag(0x0000, 0x0902)
	ErrorID                   = core.NewTag(0x0000, 0x0903)
	FailedSOPInstanceUIDList  = core.NewTag(0x0008, 0x0058)

	// Query/Retrieve service command elements.
	MoveDestination                      = core.NewTag(0x0000, 0x0600)
	NumberOfRemainingSuboperations       = core.NewTag(0x0000, 0x1020)
	NumberOfCompletedSuboperations       = core.NewTag(0x0000, 0x1021)
	NumberOfFailedSuboperations          = core.NewTag(0x0000, 0x1022)
	NumberOfWarningSuboperations         = core.NewTag(0x0000, 0x1023)
	MoveOriginatorApplicationEntityTitle = core.NewTag(0x0000, 0x1030)
	MoveOriginatorMessageID              = core.NewTag(0x0000, 0x1031)

	// Normalized DIMSE command elements.
	RequestedSOPClassUID    = core.NewTag(0x0000, 0x0003)
	RequestedSOPInstanceUID = core.NewTag(0x0000, 0x1001)
	AttributeIdentifierList = core.NewTag(0x0000, 0x1005)
	ActionTypeID            = core.NewTag(0x0000, 0x1008)
	EventTypeID             = core.NewTag(0x0000, 0x1002)
)

const (
	CStoreRQ  uint16 = 0x0001
	CStoreRSP uint16 = 0x8001
	CCancelRQ uint16 = 0x0FFF
	CEchoRQ   uint16 = 0x0030
	CEchoRSP  uint16 = 0x8030
	CFindRQ   uint16 = 0x0020
	CFindRSP  uint16 = 0x8020

	// Query/Retrieve command fields.
	CGetRQ   uint16 = 0x0010
	CGetRSP  uint16 = 0x8010
	CMoveRQ  uint16 = 0x0021
	CMoveRSP uint16 = 0x8021

	// Normalized DIMSE command fields.
	NActionRQ       uint16 = 0x0130
	NActionRSP      uint16 = 0x8130
	NEventReportRQ  uint16 = 0x0100
	NEventReportRSP uint16 = 0x8100
	NGetRQ          uint16 = 0x0110
	NGetRSP         uint16 = 0x8110
	NSetRQ          uint16 = 0x0120
	NSetRSP         uint16 = 0x8120
	NCreateRQ       uint16 = 0x0140
	NCreateRSP      uint16 = 0x8140
	NDeleteRQ       uint16 = 0x0150
	NDeleteRSP      uint16 = 0x8150
)

const (
	DataSetPresent uint16 = 0x0000 // CommandDataSetType value indicating a dataset follows.
	NoDataSet      uint16 = 0x0101 // CommandDataSetType value indicating no dataset follows.
	DataSetAbsent  uint16 = NoDataSet
)

const (
	StatusSuccess uint16 = 0x0000
	// Warning statuses are generally service-specific; C-STORE warnings often
	// live in 0xB000..0xBFFF.
	// Failure, cancel, and pending ranges are also service-specific and are
	// passed through by the DIMSE helpers instead of being normalized.
)

const VerificationSOPClassUID = "1.2.840.10008.1.1"

func CommandUint16(command *object.Object, tag core.Tag) (uint16, error) {
	if command == nil {
		return 0, fmt.Errorf("dicom dimse: nil command set")
	}
	raw, ok := command.GetRaw(tag)
	if !ok {
		return 0, fmt.Errorf("dicom dimse: missing command element %s", tag)
	}
	if len(raw) != 2 {
		return 0, fmt.Errorf("dicom dimse: command element %s has %d byte value, want exactly 2", tag, len(raw))
	}
	return binary.LittleEndian.Uint16(raw[:2]), nil
}

func optionalCommandUint16(command *object.Object, tag core.Tag) (*uint16, error) {
	if command == nil {
		return nil, fmt.Errorf("dicom dimse: nil command set")
	}
	if _, ok := command.Get(tag); !ok {
		return nil, nil
	}
	value, err := CommandUint16(command, tag)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func newUSCommandElement(tag core.Tag, value uint16) core.Element {
	var raw [2]byte
	binary.LittleEndian.PutUint16(raw[:], value)
	return core.NewRawElement(tag, core.VRUS, raw[:])
}

func newULCommandElement(tag core.Tag, value uint32) core.Element {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], value)
	return core.NewRawElement(tag, core.VRUL, raw[:])
}

func newUIElement(tag core.Tag, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRUI},
		Value:  core.StringValue{value},
	}
}

func newUIListElement(tag core.Tag, values []string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRUI},
		Value:  core.StringValue(append([]string(nil), values...)),
	}
}

func newATCommandElement(tag core.Tag, values ...core.Tag) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		offset := i * 4
		binary.LittleEndian.PutUint16(raw[offset:], value.Group)
		binary.LittleEndian.PutUint16(raw[offset+2:], value.Element)
	}
	return core.NewRawElement(tag, core.VRAT, raw)
}
