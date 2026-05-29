package parser

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReaderSmallValueArenaRetainsDistinctValues(t *testing.T) {
	for _, tt := range []struct {
		name  string
		count int
	}{
		{name: "baseline", count: 32},
		{name: "arena rollover", count: 300},
	} {
		t.Run(tt.name, func(t *testing.T) {
			elements := make([]core.Element, tt.count)
			for i := range elements {
				var value [4]byte
				binary.LittleEndian.PutUint32(value[:], uint32(i+1))
				elements[i] = core.NewRawElement(core.NewTag(0x7777, uint16(i+1)), core.VRUL, value[:])
			}
			data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, elements...)
			reader := NewReader(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{})

			dataset, err := reader.ReadDataSet()
			if err != nil {
				t.Fatal(err)
			}
			if len(dataset.Elements) != tt.count {
				t.Fatalf("element count = %d, want %d", len(dataset.Elements), tt.count)
			}
			for i, element := range dataset.Elements {
				raw, ok := element.Value.(core.RawValue)
				if !ok || len(raw) != 4 {
					t.Fatalf("element %d value = %T/%d bytes, want RawValue/4", i, element.Value, len(raw))
				}
				if got := binary.LittleEndian.Uint32(raw); got != uint32(i+1) {
					t.Fatalf("element %d = %d, want %d", i, got, i+1)
				}
			}
			first := dataset.Elements[0].Value.(core.RawValue)
			second := dataset.Elements[1].Value.(core.RawValue)
			appended := append(first, 0xFF, 0xFF, 0xFF, 0xFF)
			if len(appended) != 8 {
				t.Fatalf("appended first value length = %d, want 8", len(appended))
			}
			if got := binary.LittleEndian.Uint32(second); got != 2 {
				t.Fatalf("appending to first arena value changed second to %d", got)
			}
			first[0] = 0xFF
			if got := binary.LittleEndian.Uint32(second); got != 2 {
				t.Fatalf("mutating first arena value changed second to %d", got)
			}
		})
	}
}
