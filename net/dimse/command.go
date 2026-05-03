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
	ErrorComment              = core.NewTag(0x0000, 0x0902)

	// Query/Retrieve service command elements.
	MoveDestination = core.NewTag(0x0000, 0x0600)

	// Storage Commitment (Normalized DIMSE) command elements.
	RequestedSOPClassUID    = core.NewTag(0x0000, 0x0003)
	RequestedSOPInstanceUID = core.NewTag(0x0000, 0x1001)
	ActionTypeID            = core.NewTag(0x0000, 0x1008)
	EventTypeID             = core.NewTag(0x0000, 0x1002)
)

const (
	CStoreRQ  uint16 = 0x0001
	CStoreRSP uint16 = 0x8001
	CEchoRQ   uint16 = 0x0030
	CEchoRSP  uint16 = 0x8030
	CFindRQ   uint16 = 0x0020
	CFindRSP  uint16 = 0x8020

	// Query/Retrieve command fields.
	CGetRQ   uint16 = 0x0010
	CGetRSP  uint16 = 0x8010
	CMoveRQ  uint16 = 0x0021
	CMoveRSP uint16 = 0x8021

	// Storage Commitment / Normalized DIMSE command fields.
	NActionRQ       uint16 = 0x0130
	NActionRSP      uint16 = 0x8130
	NEventReportRQ  uint16 = 0x0100
	NEventReportRSP uint16 = 0x8100
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
	if len(raw) < 2 {
		return 0, fmt.Errorf("dicom dimse: command element %s has %d byte value, want at least 2", tag, len(raw))
	}
	return binary.LittleEndian.Uint16(raw[:2]), nil
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
