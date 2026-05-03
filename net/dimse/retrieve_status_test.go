package dimse

import (
	"testing"
)

func TestClassifyCMoveStatus(t *testing.T) {
	cases := []struct {
		name   string
		status uint16
		want   CMoveStatus
	}{
		{"pending_ff00", 0xFF00, CMoveStatusPending},
		{"pending_ff01", 0xFF01, CMoveStatusPending},
		{"success", 0x0000, CMoveStatusSuccess},
		{"cancel", 0xFE00, CMoveStatusCancel},
		{"warning_b000", 0xB000, CMoveStatusWarning},
		{"warning_b123", 0xB123, CMoveStatusWarning},
		// A common failure status for C-MOVE is "Move destination unknown" (0xA801)
		{"failure_a801", 0xA801, CMoveStatusFailure},
		{"failure_cxxx", 0xC000, CMoveStatusFailure},
		{"failure_random", 0x1234, CMoveStatusFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyCMoveStatus(tc.status)
			if got != tc.want {
				t.Fatalf("ClassifyCMoveStatus(0x%04x)=%v want %v", tc.status, got, tc.want)
			}
		})
	}
}
