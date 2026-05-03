package dimse

import (
	"errors"
	"testing"
)

func TestStorageCommitment_NActionResponse_Statuses(t *testing.T) {
	tests := []struct {
		name     string
		status   uint16
		wantNil  bool
		wantType StorageCommitmentStatus
	}{
		{"success", 0x0000, true, StorageCommitmentStatusSuccess},
		{"warning_as_failure", 0xB000, false, StorageCommitmentStatusFailure},
		{"failure", 0xA700, false, StorageCommitmentStatusFailure},
		{"unsupported_unknown", 0x0123, false, StorageCommitmentStatusFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rsp := NActionResponse{Status: tt.status}
			err := CheckStorageCommitmentStatus("N-ACTION-RSP", rsp.Status)
			if tt.wantNil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error")
			}
			var se *StorageCommitmentStatusError
			if !errors.As(err, &se) {
				t.Fatalf("expected StorageCommitmentStatusError")
			}
			if se.Status != tt.status {
				t.Fatalf("status=0x%04X want 0x%04X", se.Status, tt.status)
			}
			if se.Class != tt.wantType {
				t.Fatalf("class=%v want %v", se.Class, tt.wantType)
			}
		})
	}
}

func TestStorageCommitment_NEventReportResponse_Statuses(t *testing.T) {
	tests := []struct {
		name     string
		status   uint16
		wantNil  bool
		wantType StorageCommitmentStatus
	}{
		{"success", 0x0000, true, StorageCommitmentStatusSuccess},
		{"warning_as_failure", 0xB006, false, StorageCommitmentStatusFailure},
		{"failure", 0xC001, false, StorageCommitmentStatusFailure},
		{"unsupported_unknown", 0x2222, false, StorageCommitmentStatusFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rsp := NEventReportResponse{Status: tt.status}
			err := CheckStorageCommitmentStatus("N-EVENT-REPORT-RSP", rsp.Status)
			if tt.wantNil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error")
			}
			var se *StorageCommitmentStatusError
			if !errors.As(err, &se) {
				t.Fatalf("expected StorageCommitmentStatusError")
			}
			if se.Status != tt.status {
				t.Fatalf("status=0x%04X want 0x%04X", se.Status, tt.status)
			}
			if se.Class != tt.wantType {
				t.Fatalf("class=%v want %v", se.Class, tt.wantType)
			}
		})
	}
}
