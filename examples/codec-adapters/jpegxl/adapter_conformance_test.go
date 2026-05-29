package jpegxladapter

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCodecFixtureConformanceJPEGXLDependencyUnavailable(t *testing.T) {
	tc := codecfixture.DependencyUnavailableJPEGXL()
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(transfer.JPEGXL.UID, NewWithDecoder(&fakeDecoder{err: ErrDjxlUnavailable})); err != nil {
		t.Fatal(err)
	}
	if err := codecfixture.ValidateCase(registry, tc); err != nil {
		t.Fatal(err)
	}
}
