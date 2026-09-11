//go:build jpegxl_djxl || codecfull

package jpegxladapter

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestProductionHelperPixelsMatchDjxlCLI(t *testing.T) {
	session := openCompiledHelper(t)
	fragment, err := os.ReadFile(codecfullJXL("gray16-lossless.jxl"))
	if err != nil {
		t.Skip(err.Error())
	}
	meta := pixeldata.Metadata{
		Rows: 64, Columns: 64, SamplesPerPixel: 1,
		BitsAllocated: 16, BitsStored: 16, HighBit: 15, NumberOfFrames: 1,
		PhotometricInterpretation: "MONOCHROME2",
	}
	helperPixels, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), fragment, meta)
	if err != nil {
		t.Fatal(err)
	}
	cli := newDjxlDecoder()
	if _, err := cli.resolveExecutable(); err != nil {
		t.Skipf("djxl runtime unavailable: %v", err)
	}
	cliPixels, err := cli.DecodeFrameContext(context.Background(), fragment, meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(helperPixels, cliPixels) {
		t.Fatalf("helper/CLI pixels differ: helper=%d cli=%d", len(helperPixels), len(cliPixels))
	}
}
