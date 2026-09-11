package dimse

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestCStoreCommandRejectsMultipleUIDsBeforeIdentityIsLost(t *testing.T) {
	for _, tag := range []core.Tag{AffectedSOPClassUID, AffectedSOPInstanceUID} {
		request := CStoreRequest{AffectedSOPClassUID: "1.2.3", AffectedSOPInstanceUID: "1.2.4", MessageID: 1}
		command := object.FromElements(request.CommandSet(), nil)
		command.Put(core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRUI}, Value: core.StringValue{"1.2.3", "1.2.4"}})
		_, err := ParseCStoreRequest(command)
		if err == nil {
			t.Fatal("silently accepted first UID in command")
		}
		if strings.Contains(err.Error(), "1.2.3") || strings.Contains(err.Error(), "1.2.4") {
			t.Fatalf("UID exposed in error: %v", err)
		}
	}
}
