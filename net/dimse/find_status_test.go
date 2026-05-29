package dimse

import (
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/object"
)

func TestCategorizeCFindStatus(t *testing.T) {
	cases := []struct {
		status uint16
		want   CFindStatusCategory
	}{
		{0xFF00, CFindStatusPending},
		{0xFF01, CFindStatusPending},
		{0x0000, CFindStatusSuccess},
		{CFindStatusCancel, CFindStatusFailure},
		{0xA700, CFindStatusFailure},
		{0xA901, CFindStatusFailure},
		{0xB000, CFindStatusFailure},
		{0xC123, CFindStatusFailure},
		{0x0001, CFindStatusFailure},
		{0x0122, CFindStatusFailure},
		{0x0124, CFindStatusFailure},
		{0x0210, CFindStatusFailure},
		{0x0211, CFindStatusFailure},
		{0x0212, CFindStatusFailure},
	}
	for _, tc := range cases {
		if got := CategorizeCFindStatus(tc.status); got != tc.want {
			t.Fatalf("status 0x%04X: got %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestCFindStatusError(t *testing.T) {
	r := &CFindResponse{Status: 0xFF00}
	if err := CFindStatusError(r); err != nil {
		t.Fatalf("pending: got err %v", err)
	}

	r = &CFindResponse{Status: 0x0000}
	if err := CFindStatusError(r); err != nil {
		t.Fatalf("success: got err %v", err)
	}

	r = &CFindResponse{Status: 0xA700, ErrorComment: "bad"}
	if err := CFindStatusError(r); err == nil {
		t.Fatalf("failure: expected err")
	}

	r = &CFindResponse{Status: 0x0122}
	err := CFindStatusError(r)
	if err == nil || errors.Is(err, ErrCFindInvalidStatus) {
		t.Fatalf("general failure: got err %v", err)
	}
}

func TestCFindStatusCategoryStringAndNilError(t *testing.T) {
	tests := map[CFindStatusCategory]string{
		CFindStatusPending:     "pending",
		CFindStatusSuccess:     "success",
		CFindStatusFailure:     "failure",
		CFindStatusInvalid:     "invalid",
		CFindStatusCategory(9): "invalid",
	}
	for category, want := range tests {
		if got := category.String(); got != want {
			t.Fatalf("%v.String() = %q, want %q", int(category), got, want)
		}
	}

	var invalid *CFindInvalidStatusError
	if got := invalid.Error(); got != ErrCFindInvalidStatus.Error() {
		t.Fatalf("nil CFindInvalidStatusError.Error() = %q", got)
	}
	if err := CFindStatusError(nil); err == nil || !strings.Contains(err.Error(), "nil C-FIND response") {
		t.Fatalf("CFindStatusError(nil) = %v, want nil response error", err)
	}
	err := CFindStatusError(&CFindResponse{Status: 0xA700})
	if err == nil || !strings.Contains(err.Error(), "0xA700") {
		t.Fatalf("CFindStatusError(failure) = %v, want status in message", err)
	}
}

func TestFindResultAccessorsAndString(t *testing.T) {
	var zero FindResult
	if zero.Status() != 0 || zero.ErrorComment() != "" || zero.String() != "C-FIND-RSP <nil>" {
		t.Fatalf("zero FindResult = status %d comment %q string %q", zero.Status(), zero.ErrorComment(), zero.String())
	}

	result := FindResult{Response: &CFindResponse{Status: StatusPending, ErrorComment: "warning"}}
	if result.Status() != StatusPending || result.ErrorComment() != "warning" {
		t.Fatalf("FindResult accessors = status 0x%04X comment %q", result.Status(), result.ErrorComment())
	}
	if got := result.String(); got != "C-FIND-RSP status=0xFF00" {
		t.Fatalf("FindResult.String() = %q", got)
	}

	result.Identifier = object.FromElements(nil, nil)
	if got := result.String(); got != "C-FIND-RSP status=0xFF00 identifier_present" {
		t.Fatalf("FindResult.String() with identifier = %q", got)
	}
}
