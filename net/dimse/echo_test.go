package dimse

import (
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestPeerMaxPDUWithHeaderCapsOutboundFragmentSize(t *testing.T) {
	defaultWithHeader := int(ul.DefaultMaxPDU + ul.PDUHeaderSize)
	tests := []struct {
		name  string
		assoc *ul.Association
		want  int
	}{
		{name: "nil association", want: defaultWithHeader},
		{name: "unspecified", assoc: &ul.Association{}, want: defaultWithHeader},
		{name: "smaller peer limit", assoc: &ul.Association{PeerMaxPDU: 4096}, want: 4096 + int(ul.PDUHeaderSize)},
		{name: "default peer limit", assoc: &ul.Association{PeerMaxPDU: ul.DefaultMaxPDU}, want: defaultWithHeader},
		{name: "larger peer limit", assoc: &ul.Association{PeerMaxPDU: ul.DefaultMaxPDU + 1}, want: defaultWithHeader},
		{name: "unlimited peer", assoc: &ul.Association{PeerMaxPDU: math.MaxUint32}, want: defaultWithHeader},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerMaxPDUWithHeader(tt.assoc); got != tt.want {
				t.Fatalf("peerMaxPDUWithHeader() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCEchoRequestCommandSet(t *testing.T) {
	req := CEchoRequest{MessageID: 9}
	reqObj, err := DecodeCommandSet(mustEncodeCommandSet(t, req.CommandSet()))
	if err != nil {
		t.Fatalf("DecodeCommandSet(request) error = %v", err)
	}
	field, err := CommandUint16(reqObj, CommandField)
	if err != nil {
		t.Fatalf("request CommandField error = %v", err)
	}
	if field != CEchoRQ {
		t.Fatalf("request CommandField = 0x%04X, want 0x%04X", field, CEchoRQ)
	}
	messageID, err := CommandUint16(reqObj, MessageID)
	if err != nil {
		t.Fatalf("request MessageID error = %v", err)
	}
	if messageID != 9 {
		t.Fatalf("request MessageID = %d, want 9", messageID)
	}
	dataSetType, err := CommandUint16(reqObj, CommandDataSetType)
	if err != nil {
		t.Fatalf("request CommandDataSetType error = %v", err)
	}
	if dataSetType != NoDataSet {
		t.Fatalf("request CommandDataSetType = 0x%04X, want 0x%04X", dataSetType, NoDataSet)
	}
	uid, ok := reqObj.GetUID(AffectedSOPClassUID)
	if !ok {
		t.Fatal("request missing AffectedSOPClassUID")
	}
	if uid != VerificationSOPClassUID {
		t.Fatalf("request AffectedSOPClassUID = %q, want %q", uid, VerificationSOPClassUID)
	}
}

func TestCEchoResponseCommandSet(t *testing.T) {
	resp := CEchoResponse{MessageIDBeingRespondedTo: 9, Status: StatusSuccess}
	respObj, err := DecodeCommandSet(mustEncodeCommandSet(t, resp.CommandSet()))
	if err != nil {
		t.Fatalf("DecodeCommandSet(response) error = %v", err)
	}
	field, err := CommandUint16(respObj, CommandField)
	if err != nil {
		t.Fatalf("response CommandField error = %v", err)
	}
	if field != CEchoRSP {
		t.Fatalf("response CommandField = 0x%04X, want 0x%04X", field, CEchoRSP)
	}
	messageID, err := CommandUint16(respObj, MessageIDBeingRespondedTo)
	if err != nil {
		t.Fatalf("response MessageIDBeingRespondedTo error = %v", err)
	}
	if messageID != 9 {
		t.Fatalf("response MessageIDBeingRespondedTo = %d, want 9", messageID)
	}
	status, err := CommandUint16(respObj, Status)
	if err != nil {
		t.Fatalf("response Status error = %v", err)
	}
	if status != StatusSuccess {
		t.Fatalf("response Status = 0x%04X, want 0x%04X", status, StatusSuccess)
	}
}

func TestParseCEchoResponse(t *testing.T) {
	resp := CEchoResponse{MessageIDBeingRespondedTo: 9, Status: StatusSuccess}
	respObj, err := DecodeCommandSet(mustEncodeCommandSet(t, resp.CommandSet()))
	if err != nil {
		t.Fatalf("DecodeCommandSet(response) error = %v", err)
	}
	parsed, err := ParseCEchoResponse(respObj)
	if err != nil {
		t.Fatalf("ParseCEchoResponse() error = %v", err)
	}
	if *parsed != resp {
		t.Fatalf("ParseCEchoResponse() = %#v, want %#v", parsed, resp)
	}
}

func mustEncodeCommandSet(t *testing.T, elements []core.Element) []byte {
	t.Helper()
	data, err := EncodeCommandSet(elements)
	if err != nil {
		t.Fatalf("EncodeCommandSet() error = %v", err)
	}
	return data
}
