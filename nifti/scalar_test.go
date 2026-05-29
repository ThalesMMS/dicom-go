package nifti

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestEncodeStoredFrameCoversSignedAndUnsignedIntegerDatatypes(t *testing.T) {
	for _, test := range []struct {
		name           string
		bits           uint16
		signed         bool
		datatype       int16
		raw            []byte
		wantLittleData []byte
	}{
		{name: "uint8", bits: 8, datatype: DatatypeUint8, raw: []byte{1, 255}, wantLittleData: []byte{1, 255}},
		{name: "int8", bits: 8, signed: true, datatype: DatatypeInt8, raw: []byte{0xff, 0x7f}, wantLittleData: []byte{0xff, 0x7f}},
		{name: "uint16", bits: 16, datatype: DatatypeUint16, raw: []byte{0x12, 0x34, 0xab, 0xcd}, wantLittleData: []byte{0x34, 0x12, 0xcd, 0xab}},
		{name: "int16", bits: 16, signed: true, datatype: DatatypeInt16, raw: []byte{0xff, 0xfe, 0x00, 0x07}, wantLittleData: []byte{0xfe, 0xff, 0x07, 0x00}},
		{name: "uint32", bits: 32, datatype: DatatypeUint32, raw: []byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}, wantLittleData: []byte{0x78, 0x56, 0x34, 0x12, 0xf0, 0xde, 0xbc, 0x9a}},
		{name: "int32", bits: 32, signed: true, datatype: DatatypeInt32, raw: []byte{0xff, 0xff, 0xff, 0xfe, 0x00, 0x00, 0x00, 0x07}, wantLittleData: []byte{0xfe, 0xff, 0xff, 0xff, 0x07, 0x00, 0x00, 0x00}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pixelRepresentation := uint16(0)
			if test.signed {
				pixelRepresentation = 1
			}
			metadata := pixeldata.Metadata{
				Rows: 1, Columns: 2, SamplesPerPixel: 1,
				BitsAllocated: test.bits, BitsStored: test.bits, HighBit: test.bits - 1,
				PixelRepresentation: pixelRepresentation, NumberOfFrames: 1,
				PhotometricInterpretation: "MONOCHROME2",
			}
			got, err := encodeStoredFrame(context.Background(), test.raw, metadata, binary.BigEndian, linearTransform{slope: 1}, test.datatype)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(test.wantLittleData) {
				t.Fatalf("encoded bytes = % X, want % X", got, test.wantLittleData)
			}
		})
	}
}

func TestEncodeStoredFrameRejectsInvalidStoredBitLayout(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata pixeldata.Metadata
	}{
		{name: "zero bits stored", metadata: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 0, HighBit: 0}},
		{name: "high bit below stored range", metadata: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 12, HighBit: 10}},
		{name: "high bit equals bits allocated", metadata: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 12, HighBit: 16}},
		{name: "high bit exceeds bits allocated", metadata: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 12, HighBit: 17}},
		{name: "bits stored exceeds bits allocated", metadata: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 17, HighBit: 16}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := encodeStoredFrame(context.Background(), []byte{0, 0}, test.metadata, binary.LittleEndian, linearTransform{slope: 1}, DatatypeUint16)
			if !errors.Is(err, ErrUnsupportedPixels) {
				t.Fatalf("encodeStoredFrame(%+v) error = %v, want ErrUnsupportedPixels", test.metadata, err)
			}
		})
	}
}
