package pixeldata_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestPydicomRLETranscodeInterop(t *testing.T) {
	if os.Getenv("DICOM_GO_PYDICOM_RLE") != "1" {
		t.Skip("set DICOM_GO_PYDICOM_RLE=1 to enable independent pydicom RLE validation")
	}
	python := os.Getenv("DICOM_GO_PYTHON")
	if python == "" {
		python = "python3"
	}
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "native.dcm")
	rlePath := filepath.Join(dir, "rle.dcm")
	tc := codecfixture.NativeSmall()
	source, err := tc.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := rle.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	decoders := pixeldata.NewMemoryRegistry()
	if err := rle.Register(decoders); err != nil {
		t.Fatal(err)
	}
	if _, err := pixeldata.TranscodePath(context.Background(), sourcePath, rlePath, transfer.RLELossless, pixeldata.TranscodeOptions{
		DecoderRegistry: decoders,
		EncoderRegistry: encoders,
	}); err != nil {
		t.Fatal(err)
	}
	script := `
import sys
import pydicom
ds = pydicom.dcmread(sys.argv[1])
assert str(ds.file_meta.TransferSyntaxUID) == "1.2.840.10008.1.2.5"
arr = ds.pixel_array
assert tuple(arr.shape) == (2, 2), arr.shape
assert arr.astype("uint8").tobytes() == bytes([0, 64, 128, 255])
`
	command := exec.CommandContext(context.Background(), python, "-c", script, rlePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pydicom RLE validation failed: %v\n%s", err, output)
	}
}
