package objectview

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestVisitRawProvidesReadOnlyAccess(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tag, core.VROW, []byte{0x34, 0x12, 0xCD, 0xAB}),
	}, nil)

	visited := VisitRaw(obj, tag, func(raw Raw) {
		if got := raw.Len(); got != 4 {
			t.Fatalf("Len() = %d, want 4", got)
		}
		if got := raw.Byte(2); got != 0xCD {
			t.Fatalf("Byte(2) = %#x, want 0xcd", got)
		}
		if got := raw.Uint16LE(1); got != 0xABCD {
			t.Fatalf("Uint16LE(1) = %#x, want 0xabcd", got)
		}
	})
	if !visited {
		t.Fatal("VisitRaw() = false, want true")
	}
}

func TestVisitRawRejectsMissingNonRawAndNilInputs(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0010)
	obj := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: tag, VR: core.VRPN},
		Value:  core.StringValue{"Example^Patient"},
	}}, nil)

	for name, visited := range map[string]bool{
		"nil object":    VisitRaw(nil, tag, func(Raw) {}),
		"nil visitor":   VisitRaw(obj, tag, nil),
		"non-raw value": VisitRaw(obj, tag, func(Raw) {}),
		"missing tag":   VisitRaw(obj, core.NewTag(0x0010, 0x0020), func(Raw) {}),
	} {
		if visited {
			t.Errorf("%s: VisitRaw() = true, want false", name)
		}
	}
}
