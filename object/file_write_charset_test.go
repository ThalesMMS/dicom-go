package object

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestWriteDataSetHandlesDeclaredAndInheritedCharsets(t *testing.T) {
	nameTag := core.NewTag(0x0010, 0x0010)
	sequenceTag := core.NewTag(0x0008, 0x1111)
	encodedCharsets := FromElements([]core.Element{
		dicomtest.NewStringElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 100"),
		dicomtest.NewStringElement(nameTag, core.VRPN, "José^Silva"),
		dicomtest.NewSequenceElement(sequenceTag,
			core.DataSet{Elements: []core.Element{
				dicomtest.NewStringElement(nameTag, core.VRPN, "René^Parent"),
			}},
			core.DataSet{Elements: []core.Element{
				dicomtest.NewStringElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 192"),
				dicomtest.NewStringElement(nameTag, core.VRPN, "山田^太郎"),
			}},
		),
	}, nil)
	unrepresentable := FromElements([]core.Element{
		dicomtest.NewStringElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 100"),
		dicomtest.NewStringElement(nameTag, core.VRPN, "山田^太郎"),
	}, nil)

	tests := []struct {
		name           string
		obj            *Object
		wantErr        error
		checkEncoded   func(*testing.T, []byte)
		checkRoundTrip func(*testing.T, *Object)
	}{
		{
			name: "declared inherited and overridden charsets",
			obj:  encodedCharsets,
			checkEncoded: func(t *testing.T, encoded []byte) {
				if !bytes.Contains(encoded, []byte("Jos\xe9^Silva")) {
					t.Fatalf("encoded dataset does not contain ISO_IR 100 bytes for José: % X", encoded)
				}
				if bytes.Contains(encoded, []byte("Jos\xc3\xa9^Silva")) {
					t.Fatalf("encoded dataset contains raw UTF-8 under ISO_IR 100: % X", encoded)
				}
			},
			checkRoundTrip: func(t *testing.T, roundTrip *Object) {
				if got, err := roundTrip.LookupString(nameTag); err != nil || got != "José^Silva" {
					t.Fatalf("top-level name = %q, %v; want José^Silva", got, err)
				}
				items, ok := roundTrip.GetSequence(sequenceTag)
				if !ok || len(items) != 2 {
					t.Fatalf("sequence items = %d, %v; want 2", len(items), ok)
				}
				if got, err := items[0].LookupString(nameTag); err != nil || got != "René^Parent" {
					t.Fatalf("inherited item name = %q, %v; want René^Parent", got, err)
				}
				if got, err := items[1].LookupString(nameTag); err != nil || got != "山田^太郎" {
					t.Fatalf("override item name = %q, %v; want 山田^太郎", got, err)
				}
			},
		},
		{name: "unrepresentable value", obj: unrepresentable, wantErr: dicomenc.ErrUnrepresentableCharacter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var encoded bytes.Buffer
			err := WriteDataSet(&encoded, tt.obj, transfer.ExplicitVRLittleEndian)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("WriteDataSet() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteDataSet() error = %v", err)
			}
			if tt.checkEncoded != nil {
				tt.checkEncoded(t, encoded.Bytes())
			}
			roundTrip, err := ReadDataSet(bytes.NewReader(encoded.Bytes()), transfer.ExplicitVRLittleEndian)
			if err != nil {
				t.Fatalf("ReadDataSet() error = %v", err)
			}
			if tt.checkRoundTrip != nil {
				tt.checkRoundTrip(t, roundTrip)
			}
		})
	}
}
