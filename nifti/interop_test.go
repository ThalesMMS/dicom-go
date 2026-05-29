package nifti_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/nifti"
	"github.com/ThalesMMS/dicom-go/object"
)

// TestWriteIndependentReaderFixture emits a deterministic synthetic file only
// when DICOM_GO_NIFTI_INTEROP_OUTPUT is set. It is the handoff point for the
// documented nibabel smoke check without making Python a Go test dependency.
func TestWriteIndependentReaderFixture(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DICOM_GO_NIFTI_INTEROP_OUTPUT"))
	if path == "" {
		t.Skip("set DICOM_GO_NIFTI_INTEROP_OUTPUT for the independent-reader fixture")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range patientOrientationFixtures() {
		fixturePath := path
		if fixture.name != "axial" {
			fixturePath = interopSiblingPath(path, fixture.name)
		}
		writeInteropFixture(t, fixturePath, orientationVolume(t, fixture))
	}
}

func writeInteropFixture(t *testing.T, path string, files []*object.File) {
	t.Helper()
	options := nifti.DefaultOptions()
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		options.Compression = nifti.CompressionGZIP
	}
	destination, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := nifti.WriteFiles(context.Background(), destination, files, options)
	closeErr := destination.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func interopSiblingPath(path, orientation string) string {
	extension := filepath.Ext(path)
	stem := strings.TrimSuffix(path, extension)
	if strings.EqualFold(extension, ".gz") {
		niiExtension := filepath.Ext(stem)
		stem = strings.TrimSuffix(stem, niiExtension)
		extension = niiExtension + extension
	}
	return stem + "-" + orientation + extension
}
