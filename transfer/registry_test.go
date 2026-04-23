package transfer

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestMemoryRegistryNormalizesUIDs(t *testing.T) {
	r := NewRegistry(Syntax{
		UID:        "1.2.3 \x00",
		Name:       "Test Syntax",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
	})

	got, ok := r.Get("1.2.3 \x00")
	if !ok {
		t.Fatal("expected normalized UID lookup to succeed")
	}
	if got.UID != "1.2.3" {
		t.Fatalf("unexpected normalized UID: %q", got.UID)
	}
	if all := r.All(); len(all) != 1 || all[0].UID != "1.2.3" {
		t.Fatalf("registry stored unexpected UID set: %#v", all)
	}
}

func TestMemoryRegistryRegisterIgnoresEmptyNormalizedUID(t *testing.T) {
	r := NewRegistry()
	r.Register(Syntax{
		UID:        " \x00",
		Name:       "Invalid Syntax",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
	})

	if got := r.All(); len(got) != 0 {
		t.Fatalf("expected empty registry after invalid registration, got %#v", got)
	}

	if syntax, ok := r.Get(""); ok || syntax != (Syntax{}) {
		t.Fatalf("expected empty UID lookup to fail after invalid registration, got %#v ok=%v", syntax, ok)
	}
}

func TestMemoryRegistryRegisterNilReceiver(t *testing.T) {
	var r *MemoryRegistry
	r.Register(Syntax{
		UID:        "1.2.3",
		Name:       "Test Syntax",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
	})
}

func TestMemoryRegistryAllSortedByUIDAcrossCalls(t *testing.T) {
	r := NewRegistry(
		Syntax{UID: "1.2.840.10008.1.2.4.50"},
		Syntax{UID: "1.2.840.10008.1.2"},
		Syntax{UID: "1.2.840.10008.1.2.1"},
	)

	first := r.All()
	second := r.All()

	want := []string{
		"1.2.840.10008.1.2",
		"1.2.840.10008.1.2.1",
		"1.2.840.10008.1.2.4.50",
	}
	gotFirst := []string{first[0].UID, first[1].UID, first[2].UID}
	gotSecond := []string{second[0].UID, second[1].UID, second[2].UID}

	if !reflect.DeepEqual(gotFirst, want) {
		t.Fatalf("first All() call returned unexpected order: got %v want %v", gotFirst, want)
	}
	if !reflect.DeepEqual(gotSecond, want) {
		t.Fatalf("second All() call returned unexpected order: got %v want %v", gotSecond, want)
	}
}

func TestDefaultRegistryGetReturnsExpectedSyntaxes(t *testing.T) {
	tests := []Syntax{
		ImplicitVRLittleEndian,
		ExplicitVRLittleEndian,
		DeflatedExplicitVRLittleEndian,
		ExplicitVRBigEndian,
		JPEGBaseline,
		JPEGExtended,
		JPEGLosslessNonHierarchical,
		JPEGLosslessSV1,
		JPEGLSLossless,
		JPEGLSNearLossless,
		JPEG2000LosslessOnly,
		JPEG2000,
		JPEG2000Part2Lossless,
		JPEG2000Part2,
		RLELossless,
		EncapsulatedUncompressedExplicitVRLittleEndian,
	}

	for _, want := range tests {
		got, ok := DefaultRegistry.Get(want.UID + " \x00")
		if !ok {
			t.Fatalf("expected transfer syntax %q to be registered", want.UID)
		}
		if got.Name != want.Name {
			t.Fatalf("unexpected syntax name for %q: got %q want %q", want.UID, got.Name, want.Name)
		}
		if got.UID != want.UID {
			t.Fatalf("unexpected syntax UID for %q: got %q want %q", want.Name, got.UID, want.UID)
		}
	}
}

func TestDefaultRegistryGetUnknownUID(t *testing.T) {
	got, ok := DefaultRegistry.Get("1.2.840.10008.999.999")
	if ok {
		t.Fatalf("expected unknown UID lookup to fail, got %#v", got)
	}
	if got != (Syntax{}) {
		t.Fatalf("expected zero Syntax for unknown UID, got %#v", got)
	}
}

func TestDefaultRegistryIncludesKnownUnsupportedSyntaxes(t *testing.T) {
	tests := []struct {
		uid           string
		wantName      string
		wantSupported bool
	}{
		{
			uid:           "1.2.840.10008.1.2.4.95",
			wantName:      "JPIP Referenced Deflate",
			wantSupported: false,
		},
		{
			uid:           "1.2.840.10008.1.2.6.2",
			wantName:      "XML Encoding (Retired)",
			wantSupported: false,
		},
		{
			uid:           MPEG2MPML.UID,
			wantName:      MPEG2MPML.Name,
			wantSupported: false,
		},
		{
			uid:           SMPTEST211030PCMDigitalAudio.UID,
			wantName:      SMPTEST211030PCMDigitalAudio.Name,
			wantSupported: false,
		},
		{
			uid:           HTJ2KLossless.UID,
			wantName:      HTJ2KLossless.Name,
			wantSupported: false,
		},
		{
			uid:           JPEGXLLossless.UID,
			wantName:      JPEGXLLossless.Name,
			wantSupported: false,
		},
	}

	for _, tt := range tests {
		got, ok := DefaultRegistry.Get(tt.uid + " \x00")
		if !ok {
			t.Fatalf("expected known transfer syntax %q to be registered", tt.uid)
		}
		if got.Name != tt.wantName {
			t.Fatalf("unexpected syntax name for %q: got %q want %q", tt.uid, got.Name, tt.wantName)
		}
		if got.Supported != tt.wantSupported {
			t.Fatalf("unexpected supported flag for %q: got %v want %v", tt.uid, got.Supported, tt.wantSupported)
		}
	}
}
