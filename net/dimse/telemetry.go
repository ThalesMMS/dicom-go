package dimse

import (
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

func observeCommand(assoc *ul.Association, direction telemetry.Direction, pcID byte, command *object.Object, duration time.Duration) {
	if assoc == nil || command == nil {
		return
	}
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return
	}
	observation := telemetry.CommandObservation{
		Direction:             direction,
		PresentationContextID: pcID,
		CommandField:          field,
		Duration:              duration,
	}
	if dataSetType, err := optionalCommandUint16(command, CommandDataSetType); err == nil && dataSetType != nil {
		observation.DataSetPresent = *dataSetType != NoDataSet
	}
	if status, err := optionalCommandUint16(command, Status); err == nil && status != nil {
		observation.Status = *status
		observation.StatusSet = true
		observation.StatusCategory = commandStatusCategory(*status)
	}
	if uid, ok := command.GetString(AffectedSOPClassUID); ok {
		observation.AbstractSyntaxUID = uid
	} else if uid, ok := command.GetString(RequestedSOPClassUID); ok {
		observation.AbstractSyntaxUID = uid
	}
	assoc.RecordCommandObservation(observation)
}

func commandStatusCategory(status uint16) string {
	switch {
	case status == StatusSuccess:
		return telemetry.StatusCategorySuccess
	case status == 0xFE00:
		return telemetry.StatusCategoryCanceled
	case status == 0xFF00 || status == 0xFF01:
		return telemetry.StatusCategoryPending
	case status == 0x0001 || status == 0x0107 || status == 0x0116 || status&0xF000 == 0xB000:
		return telemetry.StatusCategoryWarning
	default:
		return telemetry.StatusCategoryFailure
	}
}
