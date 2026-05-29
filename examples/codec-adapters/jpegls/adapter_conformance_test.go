package jpegls

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
)

func TestCodecFixtureConformanceJPEGLSDependencyUnavailable(t *testing.T) {
	tc := codecfixture.DependencyUnavailableJPEGLS()
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry, nil); err != nil {
		t.Fatal(err)
	}
	if err := codecfixture.ValidateCase(registry, tc); err != nil {
		t.Fatal(err)
	}
}
