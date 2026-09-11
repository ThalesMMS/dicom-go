package dimse

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// This qualifies local N-DIMSE multiplexing, not an independent printer service.
func TestAsyncNormalizedOutOfOrderFullDataSet(t *testing.T) {
	const class = "1.2.840.10008.5.1.1.16"
	client, server := newAsyncSessionPair(t, 2, 2, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: class, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID}})
	ctx := testContext(t)
	type arrival struct {
		message AsyncMessage
		release chan struct{}
	}
	arrivals := make(chan arrival, 2)
	payload := func(id uint16) core.DataSet {
		raw := make(core.RawValue, 128*1024)
		for i := range raw {
			raw[i] = byte(i*17 + int(id)*79)
		}
		return core.DataSet{Elements: []core.Element{{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0010), VR: core.VRLO}, Value: core.StringValue{"DICOMGO_TEST"}}, {Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x1010), VR: core.VROB}, Value: raw}}}
	}
	server.Handle(NGetRQ, func(ctx context.Context, s *AsyncSession, m AsyncMessage) error {
		req, err := ParseNormalizedGetRequest(m.Command)
		if err != nil {
			return err
		}
		a := arrival{m, make(chan struct{})}
		select {
		case arrivals <- a:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return s.Respond(ctx, m, (NormalizedGetResponse{AffectedSOPClassUID: class, AffectedSOPInstanceUID: req.RequestedSOPInstanceUID, Status: StatusSuccess}).CommandSet(), object.FromDataSet(payload(m.MessageID), std.Dictionary))
	})
	ops := make([]*AsyncOperation, 2)
	accepted := make([]arrival, 2)
	for i := range ops {
		var err error
		ops[i], err = client.StartNormalized(ctx, 1, (NormalizedGetRequest{RequestedSOPClassUID: class, RequestedSOPInstanceUID: fmt.Sprintf("1.2.826.0.1.3680043.10.543.903.%d", i+1)}).CommandSet(), nil)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case accepted[i] = <-arrivals:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if m := client.Snapshot(); m.ActiveInvoked != 2 || m.PeakInvoked != 2 {
		t.Fatalf("invoked: %+v", m)
	}
	if m := server.Snapshot(); m.ActivePerformed != 2 || m.PeakPerformed != 2 {
		t.Fatalf("performed: %+v", m)
	}
	for _, i := range []int{1, 0} {
		close(accepted[i].release)
		response, err := ops[i].Wait(ctx) // Response 2 must finish while request 1 remains withheld.
		if err != nil {
			t.Fatal(err)
		}
		if response.MessageID != ops[i].MessageID() || response.DataSet == nil {
			t.Fatal("normalized response correlation or dataset missing")
		}
		got, err := dicomtest.SemanticDataSet(response.DataSet.ToDataSet(), binary.LittleEndian)
		if err != nil {
			t.Fatal(err)
		}
		want, err := dicomtest.SemanticDataSet(payload(ops[i].MessageID()), binary.LittleEndian)
		if err != nil {
			t.Fatal(err)
		}
		if diff := dicomtest.DiffSemantic(got, want); diff != "" {
			t.Fatal(diff)
		}
	}
}
