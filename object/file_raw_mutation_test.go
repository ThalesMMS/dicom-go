package object

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// GetRaw hands out defensive clones. With parsed values aliasing a shared
// dataset buffer, caller mutation of a returned slice must never alter what a
// subsequent read of the same element observes.
func TestGetRawMutationDoesNotCorruptSubsequentReads(t *testing.T) {
	pixels := make([]byte, 96<<10)
	for i := range pixels {
		pixels[i] = byte(i * 7)
	}
	elements := append([]core.Element{}, dicomtest.MinimalDataset()...)
	elements = append(elements, core.NewRawElement(core.TagPixelData, core.VROW, pixels))
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, elements...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mutation.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	first, ok := file.GetRaw(core.TagPixelData)
	if !ok {
		t.Fatal("pixel data not found")
	}
	if !bytes.Equal(first, pixels) {
		t.Fatal("first read does not match written pixels")
	}
	for i := range first {
		first[i] ^= 0xFF
	}

	second, ok := file.GetRaw(core.TagPixelData)
	if !ok {
		t.Fatal("pixel data not found on second read")
	}
	if !bytes.Equal(second, pixels) {
		t.Fatal("mutating a GetRaw result altered a subsequent read")
	}

	uid, ok := file.GetUID(core.Tag{Group: 0x0008, Element: 0x0018})
	if !ok || uid != dicomtest.TestSOPInstanceUID {
		t.Fatalf("unexpected SOP Instance UID after mutation: %q", uid)
	}
}
