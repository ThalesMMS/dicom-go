package dimse_test

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/net/dimse"
)

// Compile-time direct comparisons protect the legacy Storage Commitment
// command models from becoming non-comparable through added slices or maps.
// Existing callers used these public values before the generic N-service layer.
func TestLegacyStorageCommitmentCommandModelsRemainComparable(t *testing.T) {
	actionRequest := dimse.NActionRequest{}
	if actionRequest != (dimse.NActionRequest{}) {
		t.Fatal("zero NActionRequest is not equal")
	}
	actionResponse := dimse.NActionResponse{}
	if actionResponse != (dimse.NActionResponse{}) {
		t.Fatal("zero NActionResponse is not equal")
	}
	eventRequest := dimse.NEventReportRequest{}
	if eventRequest != (dimse.NEventReportRequest{}) {
		t.Fatal("zero NEventReportRequest is not equal")
	}
	eventResponse := dimse.NEventReportResponse{}
	if eventResponse != (dimse.NEventReportResponse{}) {
		t.Fatal("zero NEventReportResponse is not equal")
	}
}
