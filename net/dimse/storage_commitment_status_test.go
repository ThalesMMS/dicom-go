package dimse

import (
	"errors"
	"testing"
)

func TestClassifyStorageCommitmentStatus(t *testing.T) {
	tests := []struct {
		name   string
		status uint16
		want   StorageCommitmentStatus
	}{
		{"success", 0x0000, StorageCommitmentStatusSuccess},
		{"warning_b000_as_failure", 0xB000, StorageCommitmentStatusFailure},
		{"warning_b007_as_failure", 0xB007, StorageCommitmentStatusFailure},
		{"failure_a700", 0xA700, StorageCommitmentStatusFailure},
		{"failure_c001", 0xC001, StorageCommitmentStatusFailure},
		{"failure_misc", 0x0110, StorageCommitmentStatusFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyStorageCommitmentStatus(tt.status); got != tt.want {
				t.Fatalf("ClassifyStorageCommitmentStatus(0x%04X)=%v want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestCheckStorageCommitmentStatus_TypedError(t *testing.T) {
	err := CheckStorageCommitmentStatus("N-ACTION", 0xB000)
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *StorageCommitmentStatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageCommitmentStatusError via errors.As")
	}
	if se.Status != 0xB000 {
		t.Fatalf("status=%#x", se.Status)
	}
	if se.Class != StorageCommitmentStatusFailure {
		t.Fatalf("class=%v", se.Class)
	}

	if err := CheckStorageCommitmentStatus("N-ACTION", 0x0000); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
