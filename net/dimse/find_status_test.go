package dimse

import "testing"

func TestCategorizeCFindStatus(t *testing.T) {
	cases := []struct {
		status uint16
		want   CFindStatusCategory
	}{
		{0xFF00, CFindStatusPending},
		{0xFF01, CFindStatusPending},
		{0x0000, CFindStatusSuccess},
		{0xA700, CFindStatusFailure},
		{0x0001, CFindStatusFailure},
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
}
