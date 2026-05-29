package dimse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestStoreSessionIndependentSCPInterop(t *testing.T) {
	address := os.Getenv("DICOM_STORE_INTEROP_ADDRESS")
	if address == "" {
		t.Skip("set DICOM_STORE_INTEROP_ADDRESS to an independent Storage SCP")
	}
	called := os.Getenv("DICOM_STORE_INTEROP_CALLED_AE")
	if called == "" {
		called = "ANY-SCP"
	}
	calling := os.Getenv("DICOM_STORE_INTEROP_CALLING_AE")
	if calling == "" {
		calling = "STORESCU"
	}
	paths := filepath.SplitList(os.Getenv("DICOM_STORE_INTEROP_FILES"))
	if len(paths) == 0 {
		data, err := dicomtest.ExplicitVRFile()
		if err != nil {
			t.Fatalf("ExplicitVRFile() error = %v", err)
		}
		path := filepath.Join(t.TempDir(), "interop.dcm")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		paths = []string{path}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := NewStoreSession(address, StoreSessionOptions{
		DialOptions: ul.DialOptions{
			CalledAETitle: called, CallingAETitle: calling,
			NegotiationTimeout: 10 * time.Second, ReadProgressTimeout: 10 * time.Second, WriteProgressTimeout: 10 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewStoreSession() error = %v", err)
	}
	sources := make([]StoreSource, len(paths))
	for i := range paths {
		sources[i] = NewPathStoreSource(paths[i])
	}
	result, err := session.StoreBatch(ctx, sources)
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	for i, item := range result.Items {
		if item.Outcome != StoreOutcomeSuccess && item.Outcome != StoreOutcomeWarning {
			t.Fatalf("source %d outcome = %s", i+1, item.Outcome)
		}
	}
}
