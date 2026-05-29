package dimse

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestCCancelRequestCommandSetRoundTrip(t *testing.T) {
	req := CCancelRequest{MessageIDBeingRespondedTo: 7}
	obj := object.FromElements(req.CommandSet(), nil)

	parsed, err := ParseCCancelRequest(obj)
	if err != nil {
		t.Fatalf("ParseCCancelRequest() error = %v", err)
	}
	if parsed.MessageIDBeingRespondedTo != req.MessageIDBeingRespondedTo {
		t.Fatalf("MessageIDBeingRespondedTo = %d, want %d", parsed.MessageIDBeingRespondedTo, req.MessageIDBeingRespondedTo)
	}
}

func TestParseCCancelRequestRejectsWrongCommandField(t *testing.T) {
	obj := object.FromElements(CEchoRequest{MessageID: 7}.CommandSet(), nil)
	if _, err := ParseCCancelRequest(obj); err == nil {
		t.Fatal("ParseCCancelRequest() expected wrong command field error")
	}
}

func TestParseCCancelRequestRejectsDatasetPresent(t *testing.T) {
	obj := object.FromElements([]core.Element{
		newUSCommandElement(CommandField, CCancelRQ),
		newUSCommandElement(MessageIDBeingRespondedTo, 7),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
	}, nil)
	if _, err := ParseCCancelRequest(obj); err == nil {
		t.Fatal("ParseCCancelRequest() expected dataset-present error")
	}
}

func TestParseCCancelRequestAcceptsZeroMessageIDBeingRespondedTo(t *testing.T) {
	obj := object.FromElements(CCancelRequest{}.CommandSet(), nil)
	parsed, err := ParseCCancelRequest(obj)
	if err != nil {
		t.Fatalf("ParseCCancelRequest() error = %v", err)
	}
	if parsed.MessageIDBeingRespondedTo != 0 {
		t.Fatalf("MessageIDBeingRespondedTo = %d, want 0", parsed.MessageIDBeingRespondedTo)
	}
}
