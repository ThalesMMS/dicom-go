package core

import (
	"bytes"
	"testing"
)

var allKnownVRs = []VR{
	VRAE, VRAS, VRAT, VRCS, VRDA, VRDS, VRDT, VRFL, VRFD, VRIS, VRLO, VRLT,
	VROB, VROD, VROF, VROL, VROV, VROW, VRPN, VRSH, VRSL, VRSQ, VRSS, VRSV, VRST,
	VRTM, VRUC, VRUI, VRUL, VRUN, VRUR, VRUS, VRUT, VRUV,
}

func TestParseVRKnownValues(t *testing.T) {
	for _, vr := range allKnownVRs {
		t.Run(vr.String(), func(t *testing.T) {
			got, err := ParseVR(vr.String())
			if err != nil {
				t.Fatalf("ParseVR(%q) returned error: %v", vr, err)
			}
			if got != vr {
				t.Fatalf("ParseVR(%q) = %q, want %q", vr, got, vr)
			}
		})
	}
}

func TestParseVRRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", "A", "ZZ", "PN ", "123"} {
		if _, err := ParseVR(input); err == nil {
			t.Fatalf("ParseVR(%q) unexpectedly succeeded", input)
		}
	}
}

func TestVRString(t *testing.T) {
	if got := VR("").String(); got != string(VRUnknown) {
		t.Fatalf("empty VR String() = %q, want %q", got, VRUnknown)
	}
	if got := VRUnknown.String(); got != "UN" {
		t.Fatalf("VRUnknown.String() = %q, want %q", got, "UN")
	}
	if got := VR("ZZ").String(); got != "ZZ" {
		t.Fatalf("custom VR String() = %q, want %q", got, "ZZ")
	}
}

func TestVRUsesLongExplicitLength(t *testing.T) {
	longVRs := map[VR]bool{
		VROB: true, VROD: true, VROF: true, VROL: true, VROV: true, VROW: true,
		VRSQ: true, VRUC: true, VRUR: true, VRUT: true, VRUN: true,
	}

	for _, vr := range allKnownVRs {
		want := longVRs[vr]
		if got := vr.UsesLongExplicitLength(); got != want {
			t.Fatalf("%s.UsesLongExplicitLength() = %v, want %v", vr, got, want)
		}
	}
}

func TestVRIsStringLike(t *testing.T) {
	stringLikeVRs := map[VR]bool{
		VRAE: true, VRAS: true, VRCS: true, VRDA: true, VRDS: true, VRDT: true,
		VRIS: true, VRLO: true, VRLT: true, VRPN: true, VRSH: true, VRST: true,
		VRTM: true, VRUC: true, VRUI: true, VRUR: true, VRUT: true,
	}

	for _, vr := range allKnownVRs {
		want := stringLikeVRs[vr]
		if got := vr.IsStringLike(); got != want {
			t.Fatalf("%s.IsStringLike() = %v, want %v", vr, got, want)
		}
	}
}

func TestVRPaddingByte(t *testing.T) {
	spacePaddedVRs := map[VR]bool{
		VRAE: true, VRAS: true, VRCS: true, VRDA: true, VRDS: true, VRDT: true,
		VRIS: true, VRLO: true, VRLT: true, VRPN: true, VRSH: true, VRST: true,
		VRTM: true, VRUC: true, VRUR: true, VRUT: true,
	}

	for _, vr := range allKnownVRs {
		want := byte(0x00)
		if spacePaddedVRs[vr] {
			want = ' '
		}
		if got := vr.PaddingByte(); got != want {
			t.Fatalf("%s.PaddingByte() = %#x, want %#x", vr, got, want)
		}
	}
}

func TestVRPadToEvenLength(t *testing.T) {
	oddSpace := []byte("ABC")
	paddedSpace := VRPN.PadToEvenLength(oddSpace)
	if !bytes.Equal(paddedSpace, []byte{'A', 'B', 'C', ' '}) {
		t.Fatalf("VRPN.PadToEvenLength(%q) = %v", oddSpace, paddedSpace)
	}

	oddNUL := []byte("1.2")
	paddedNUL := VRUI.PadToEvenLength(oddNUL)
	if !bytes.Equal(paddedNUL, []byte{'1', '.', '2', 0x00}) {
		t.Fatalf("VRUI.PadToEvenLength(%q) = %v", oddNUL, paddedNUL)
	}

	even := []byte("AB")
	cloned := VROB.PadToEvenLength(even)
	if !bytes.Equal(cloned, even) {
		t.Fatalf("expected even-length data to remain unchanged, got %v", cloned)
	}
	cloned[0] = 'Z'
	if even[0] != 'A' {
		t.Fatalf("PadToEvenLength should not alias the input slice")
	}
}
