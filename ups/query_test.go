package ups_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/ups"
)

func TestQueryIdentifierPreservesReturnAndMatchingKeys(t *testing.T) {
	t.Parallel()
	identifier, err := ups.BuildQueryIdentifier(ups.Query{Keys: map[core.Tag]ups.QueryKey{
		ups.TagPatientName:        ups.Match("DOE*"),
		ups.TagSOPInstanceUID:     ups.ReturnKey(),
		ups.TagProcedureStepState: ups.Match(string(ups.StateScheduled)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ups.ParseQueryIdentifier(identifier)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.HasMatchingKey() || len(parsed.Keys[ups.TagSOPInstanceUID].Values) != 0 || parsed.Keys[ups.TagPatientName].Values[0] != "DOE*" {
		t.Fatalf("parsed = %#v", parsed)
	}
	if _, err := ups.BuildQueryIdentifier(ups.Query{Keys: map[core.Tag]ups.QueryKey{ups.TagTransactionUID: ups.ReturnKey()}}); !errors.Is(err, ups.ErrQueryIdentifier) {
		t.Fatalf("Transaction UID query error = %v", err)
	}
}

func TestQueryRouteStreamsOnlyMatchingRequestedAttributes(t *testing.T) {
	t.Parallel()
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	createScheduledStep(t, service, "1.2.826.0.1.3680043.10.543.301", "DOE^ALPHA")
	createScheduledStep(t, service, "1.2.826.0.1.3680043.10.543.302", "OTHER^PATIENT")

	routes, err := service.QueryRoutes(ups.QuerySCPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := ups.BuildQueryIdentifier(ups.Query{Keys: map[core.Tag]ups.QueryKey{
		ups.TagPatientName: ups.Match("DOE*"), ups.TagSOPInstanceUID: ups.ReturnKey(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var matches []*object.Object
	handler := routes[0].Handler
	err = handler.Find(context.Background(), dimse.StreamingCFindRequest{Identifier: identifier}, func(status uint16, match *object.Object) error {
		if status != dimse.StatusPending {
			t.Fatalf("status = 0x%04X", status)
		}
		matches = append(matches, match)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d", len(matches))
	}
	if _, ok := matches[0].Get(ups.TagPatientName); !ok {
		t.Fatal("matching Patient Name was not projected")
	}
	if _, ok := matches[0].Get(ups.TagProcedureStepState); ok {
		t.Fatal("unrequested state was projected")
	}
}

func TestQueryWithoutMatchingKeyReturnsZeroMatches(t *testing.T) {
	t.Parallel()
	store, _ := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	service, _ := ups.NewService(store, ups.ServiceOptions{})
	createScheduledStep(t, service, "1.2.826.0.1.3680043.10.543.303", "DOE^ALPHA")
	routes, _ := service.QueryRoutes(ups.QuerySCPOptions{})
	identifier, _ := ups.BuildQueryIdentifier(ups.Query{Keys: map[core.Tag]ups.QueryKey{ups.TagSOPInstanceUID: ups.ReturnKey()}})
	count := 0
	if err := routes[0].Handler.Find(context.Background(), dimse.StreamingCFindRequest{Identifier: identifier}, func(uint16, *object.Object) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("matches = %d, want 0", count)
	}
}

func createScheduledStep(t *testing.T, service *ups.Service, uid, patientName string) {
	t.Helper()
	attributes := scheduledAttributes(t)
	attributes.Put(ups.StringElement(ups.TagPatientName, core.VRPN, patientName))
	if _, err := service.Create(context.Background(), ups.CreateRequest{SOPInstanceUID: uid, Attributes: attributes}); err != nil {
		t.Fatal(err)
	}
}
