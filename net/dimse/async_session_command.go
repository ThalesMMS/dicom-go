package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func commandFieldFromElements(elements []core.Element) (uint16, error) {
	return commandUint16FromElements(elements, CommandField)
}

func asyncCommandAllowsPending(commandField uint16) bool {
	switch commandField {
	case CFindRQ, CMoveRQ, CGetRQ:
		return true
	default:
		return false
	}
}

func asyncCommandStatusIsPending(commandField, status uint16) bool {
	switch commandField {
	case CFindRQ:
		return status == StatusPending || status == StatusPendingWarning
	case CMoveRQ, CGetRQ:
		return status == StatusPending
	default:
		return false
	}
}

func asyncResourceLimitStatus(commandField uint16) uint16 {
	switch commandField {
	case CStoreRQ, CFindRQ:
		return 0xA700
	case CMoveRQ, CGetRQ:
		return 0xA701
	default:
		return StatusProcessingFailure
	}
}

func commandUint16FromElements(elements []core.Element, tag core.Tag) (uint16, error) {
	return CommandUint16(object.FromElements(elements, nil), tag)
}

func commandElementsWithMessageID(elements []core.Element, tag core.Tag, messageID uint16) ([]core.Element, error) {
	cloned := append([]core.Element(nil), elements...)
	found := false
	for index := range cloned {
		if cloned[index].Header.Tag != tag {
			continue
		}
		if found {
			return nil, fmt.Errorf("dicom dimse: duplicate command element %s", tag)
		}
		cloned[index] = newUSCommandElement(tag, messageID)
		found = true
	}
	if !found {
		cloned = append(cloned, newUSCommandElement(tag, messageID))
	}
	return cloned, nil
}
