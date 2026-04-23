package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestTruncateString(t *testing.T) {
	if got := truncateString("ABCDEFGHIJ", 7); got != "ABCD..." {
		t.Fatalf("truncateString() = %q, want %q", got, "ABCD...")
	}
	if got := truncateString("ABC", 7); got != "ABC" {
		t.Fatalf("truncateString() short string = %q, want %q", got, "ABC")
	}
	if got := truncateString("áéíóú", 4); got != "á..." {
		t.Fatalf("truncateString() utf8 = %q, want %q", got, "á...")
	}
}

func TestEscapeStringValue(t *testing.T) {
	got := escapeStringValue("line1\nline2\rtab\tpath\\name\x01")
	want := `line1\nline2\rtab\tpath\\name\x01`
	if got != want {
		t.Fatalf("escapeStringValue() = %q, want %q", got, want)
	}
}

func TestEscapeStringValues(t *testing.T) {
	got := escapeStringValues([]string{"alpha", "beta"})
	want := `alpha\beta`
	if got != want {
		t.Fatalf("escapeStringValues() = %q, want %q", got, want)
	}
}

func TestFormatBinaryValue(t *testing.T) {
	if got := formatBinaryValue([]byte{0xA0, 0xB1, 0xC2}, 32); got != "A0 B1 C2" {
		t.Fatalf("formatBinaryValue() = %q, want %q", got, "A0 B1 C2")
	}
	if got := formatBinaryValue([]byte{0xA0, 0xB1, 0xC2, 0xD3}, 7); got != "A0 B..." {
		t.Fatalf("formatBinaryValue() truncated = %q, want %q", got, "A0 B...")
	}
	if got := formatBinaryValue([]byte{0xA0, 0xB1, 0xC2}, 7); got != "A0 B..." {
		t.Fatalf("formatBinaryValue() partial final segment = %q, want %q", got, "A0 B...")
	}
}

func TestFormatFixedWidthValuesShowsEllipsisWhenMoreValuesRemain(t *testing.T) {
	raw := []byte{1, 0, 2, 0, 3, 0}
	got, ok := formatNumericValue(core.VRUS, raw, binary.LittleEndian, 3)
	if !ok {
		t.Fatal("formatNumericValue() ok=false, want true")
	}
	if got != "..." {
		t.Fatalf("formatNumericValue() truncated = %q, want %q", got, "...")
	}
}

func TestFormatNumericValueShowsEllipsisWhenLastSegmentIsTruncated(t *testing.T) {
	got, ok := formatNumericValue(core.VRUS, []byte{0xD2, 0x04}, binary.LittleEndian, 3) // 1234
	if !ok {
		t.Fatal("formatNumericValue() ok=false, want true")
	}
	if got != "..." {
		t.Fatalf("formatNumericValue() truncated last segment = %q, want %q", got, "...")
	}
}

func TestNameForUID(t *testing.T) {
	if got := nameForUID("1.2.840.10008.1.2.1"); got != "Explicit VR Little Endian" {
		t.Fatalf("nameForUID() = %q, want %q", got, "Explicit VR Little Endian")
	}
	if got := nameForUID("9.9.9.9"); got != "" {
		t.Fatalf("nameForUID() unknown = %q, want empty string", got)
	}
}

func TestDumpFileShowsKnownUIDNames(t *testing.T) {
	const ctImageStorage = "1.2.840.10008.5.1.4.1.1.2"

	data, err := dicomtest.NewFileBuilder().
		WithMeta(dicomtest.NewFileMetaBuilder().WithSOPClass(ctImageStorage).WithTransferSyntax(transfer.ExplicitVRLittleEndian.UID)).
		AddElements(
			dicomtest.NewUIElement(core.NewTag(0x0008, 0x0016), ctImageStorage),
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST^PATIENT"),
			dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "TESTID001"),
			dicomtest.NewUIElement(core.NewTag(0x0008, 0x0018), dicomtest.TestSOPInstanceUID),
			dicomtest.NewUIElement(core.NewTag(0x0020, 0x000D), dicomtest.TestStudyInstanceUID),
		).
		Build()
	if err != nil {
		t.Fatal(err)
	}

	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := NewFormatter(&out, defaultMaxValueLen).DumpFile(file); err != nil {
		t.Fatal(err)
	}
	text := out.String()

	for _, want := range []string{
		`1.2.840.10008.5.1.4.1.1.2 = "CT Image Storage"`,
		`1.2.840.10008.1.2.1 = "Explicit VR Little Endian"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dump output is missing %q:\n%s", want, text)
		}
	}
}

func TestFormatUIDValueFallsBackForUnknownUID(t *testing.T) {
	const unknown = "9.8.7.6.5"

	if got := formatUIDValue(unknown); got != unknown {
		t.Fatalf("formatUIDValue() = %q, want %q", got, unknown)
	}
}
