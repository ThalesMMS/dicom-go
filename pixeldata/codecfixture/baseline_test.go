package codecfixture

import (
	"reflect"
	"sort"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/builtin"
)

func TestRegisterBuiltinCodecsBaselineMatrix(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()
	if err := RegisterBuiltinCodecs(registry); err != nil {
		t.Fatal(err)
	}
	want := builtin.TransferSyntaxUIDs()
	sort.Strings(want)
	if got := registry.RegisteredCodecUIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registered codecs = %#v, want baseline %#v", got, want)
	}
}
