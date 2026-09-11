package dimse

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestRetrieveRequestAcceptsEveryPresentDataSetType(t *testing.T) {
	get := CGetRequest{AffectedSOPClassUID: StudyRootGetSOPClassUID, MessageID: 1}
	move := CMoveRequest{AffectedSOPClassUID: StudyRootMoveSOPClassUID, MessageID: 1, MoveDestination: "DEST"}
	for _, datasetType := range []uint16{0x0000, 0x0001, 0x0100, 0x0102, 0xFFFF, NoDataSet} {
		for _, test := range []struct {
			name     string
			elements []core.Element
			parse    func(*object.Object) error
		}{
			{"get", get.CommandSet(), func(o *object.Object) error { _, err := ParseCGetRequest(o); return err }},
			{"move", move.CommandSet(), func(o *object.Object) error { _, err := ParseCMoveRequest(o); return err }},
		} {
			o := object.FromElements(test.elements, nil)
			o.Put(newUSCommandElement(CommandDataSetType, datasetType))
			err := test.parse(o)
			if (err != nil) != (datasetType == NoDataSet) {
				t.Errorf("%s type %04X error=%v", test.name, datasetType, err)
			}
		}
	}
}
