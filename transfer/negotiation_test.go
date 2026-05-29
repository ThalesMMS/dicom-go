package transfer

import "testing"

func TestProposedStoreTransferSyntaxUIDsOrdersNativeSyntaxes(t *testing.T) {
	tests := []struct {
		name      string
		sourceUID string
		pref      NativeStorePreference
		want      []string
	}{
		{
			name:      "source first",
			sourceUID: ExplicitVRLittleEndian.UID,
			pref:      NativeStoreSourceFirst,
			want:      []string{ExplicitVRLittleEndian.UID, ImplicitVRLittleEndian.UID},
		},
		{
			name:      "explicit first",
			sourceUID: ImplicitVRLittleEndian.UID,
			pref:      NativeStoreExplicitLittleEndianFirst,
			want:      []string{ExplicitVRLittleEndian.UID, ImplicitVRLittleEndian.UID},
		},
		{
			name:      "implicit first",
			sourceUID: ExplicitVRLittleEndian.UID,
			pref:      NativeStoreImplicitLittleEndianFirst,
			want:      []string{ImplicitVRLittleEndian.UID, ExplicitVRLittleEndian.UID},
		},
		{
			name:      "compressed source only",
			sourceUID: JPEG2000.UID,
			pref:      NativeStoreExplicitLittleEndianFirst,
			want:      []string{JPEG2000.UID},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ProposedStoreTransferSyntaxUIDs(test.sourceUID, test.pref)
			if !sameStrings(got, test.want) {
				t.Fatalf("ProposedStoreTransferSyntaxUIDs() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestReceiveTransferSyntaxUIDsCombinesNativeAndPreservableCompressed(t *testing.T) {
	got := ReceiveTransferSyntaxUIDs(ExplicitVRLittleEndian, ImplicitVRLittleEndian)
	wantPrefix := []string{ExplicitVRLittleEndian.UID, ImplicitVRLittleEndian.UID}
	if len(got) < len(wantPrefix) || !sameStrings(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("ReceiveTransferSyntaxUIDs() prefix = %#v, want %#v", got, wantPrefix)
	}
	for _, uid := range []string{
		EncapsulatedUncompressedExplicitVRLittleEndian.UID,
		JPEGBaseline.UID,
		JPEGLosslessNonHierarchical.UID,
		JPEGLSLossless.UID,
		JPEG2000LosslessOnly.UID,
		JPEGXL.UID,
	} {
		if !hasUID(got, uid) {
			t.Fatalf("ReceiveTransferSyntaxUIDs() missing %q: %#v", uid, got)
		}
	}
	for _, uid := range []string{RLELossless.UID, MPEG4HP41F.UID, JPIPReferenced.UID, JPIPHTJ2KReferenced.UID} {
		if hasUID(got, uid) {
			t.Fatalf("ReceiveTransferSyntaxUIDs() included unsupported/media syntax %q: %#v", uid, got)
		}
	}
}

func TestPreservableCompressedReceiveSyntaxesReturnsCopy(t *testing.T) {
	first := PreservableCompressedReceiveSyntaxes()
	if len(first) == 0 {
		t.Fatal("PreservableCompressedReceiveSyntaxes() returned no syntaxes")
	}
	first[0] = Syntax{UID: "mutated"}

	second := PreservableCompressedReceiveSyntaxes()
	if second[0].UID == "mutated" {
		t.Fatal("PreservableCompressedReceiveSyntaxes() returned shared backing storage")
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func hasUID(uids []string, uid string) bool {
	for _, got := range uids {
		if got == uid {
			return true
		}
	}
	return false
}
