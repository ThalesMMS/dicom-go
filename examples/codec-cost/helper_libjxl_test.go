package codeccost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestLibjxlHelperDecodesCodecfullGray16(t *testing.T) {
	if _, err := exec.LookPath("pkg-config"); err != nil {
		t.Skip("pkg-config not available")
	}
	if err := exec.Command("pkg-config", "--exists", "libjxl").Run(); err != nil {
		t.Skip("libjxl not available")
	}
	src := filepath.Join("helperc", "jxl_helper.c")
	out := filepath.Join(t.TempDir(), "jxl-helper")
	if err := compileJXLHelper(context.Background(), src, out); err != nil {
		t.Fatal(err)
	}
	helper, err := startHelperProcess(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = helper.Close() }()
	fragment, err := os.ReadFile(filepath.Join(discoverCorpusRoot(), "jxl", "gray16-lossless.jxl"))
	if err != nil {
		t.Skip(err.Error())
	}
	result, err := helper.Decode(context.Background(), DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "jpegxl-gray16",
		Mode:     ModeWarm,
		Fragment: fragment,
		Metadata: pixeldataMetadataGray16(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := 64 * 64 * 2
	if len(result.Pixels) != want {
		t.Fatalf("pixels = %d, want %d", len(result.Pixels), want)
	}
}

func pixeldataMetadataGray16() pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows: 64, Columns: 64, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 16, HighBit: 15, NumberOfFrames: 1,
		PhotometricInterpretation: "MONOCHROME2",
	}
}
