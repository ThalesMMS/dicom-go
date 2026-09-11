package object_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// TestPydicomISO2022Interop is an opt-in, bidirectional Part 10 gate. dicom-go
// authors each file, pydicom independently decodes and rewrites it, and
// dicom-go then decodes the rewritten file.
func TestPydicomISO2022Interop(t *testing.T) {
	if os.Getenv("DICOM_GO_PYDICOM_CHARSET") != "1" {
		t.Skip("set DICOM_GO_PYDICOM_CHARSET=1 to enable independent ISO 2022 validation")
	}
	python := strings.TrimSpace(os.Getenv("DICOM_GO_PYTHON"))
	if python == "" {
		python = "python3"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, python, "-c", "import pydicom; assert pydicom.__version__ == '3.0.2'").CombinedOutput(); err != nil {
		t.Fatalf("pydicom 3.0.2 is required: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	type fixture struct {
		name  string
		codes []string
		tag   core.Tag
		vr    core.VR
		text  string
	}
	fixtures := []fixture{
		{name: "japanese", codes: []string{"", "ISO 2022 IR 87"}, tag: tags.PatientName, vr: core.VRPN, text: "=山田^太郎"},
		{name: "korean", codes: []string{"", "ISO 2022 IR 149"}, tag: tags.PatientName, vr: core.VRPN, text: "=洪^吉洞=홍^길동"},
		{name: "mixed", codes: []string{"", "ISO 2022 IR 87", "ISO 2022 IR 13", "ISO 2022 IR 159"}, tag: core.NewTag(0x0008, 0x0080), vr: core.VRLO, text: "あaｱア齩"},
	}

	dir := t.TempDir()
	for index, fixture := range fixtures {
		path := filepath.Join(dir, fixture.name+".dcm")
		writeISO2022InteropFile(t, path, index+1, fixture.codes, fixture.tag, fixture.vr, fixture.text)
	}

	const script = `
import os
import sys
import pydicom

expected = {
    "japanese": ("PatientName", "=山田^太郎"),
    "korean": ("PatientName", "=洪^吉洞=홍^길동"),
    "mixed": ("InstitutionName", "あaｱア齩"),
}
root = sys.argv[1]
for name, (keyword, text) in expected.items():
    source = os.path.join(root, name + ".dcm")
    rewritten = os.path.join(root, name + "-pydicom.dcm")
    dataset = pydicom.dcmread(source)
    assert str(getattr(dataset, keyword)) == text, (name, getattr(dataset, keyword))
    # Replace the decoded value so PersonName.original_string cannot make the
    # writer preserve dicom-go's original bytes. This exercises pydicom's own
    # encoder on the return leg.
    setattr(dataset, keyword, text)
    dataset.save_as(rewritten, enforce_file_format=True)
`
	if output, err := exec.CommandContext(ctx, python, "-c", script, dir).CombinedOutput(); err != nil {
		t.Fatalf("pydicom ISO 2022 validation failed: %v\n%s", err, output)
	}

	for _, fixture := range fixtures {
		path := filepath.Join(dir, fixture.name+"-pydicom.dcm")
		file, err := object.OpenFile(path)
		if err != nil {
			t.Fatalf("open pydicom %s output: %v", fixture.name, err)
		}
		got, ok := file.GetString(fixture.tag)
		if !ok || got != fixture.text {
			t.Fatalf("pydicom %s round-trip = (%q, %t), want %q", fixture.name, got, ok, fixture.text)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close pydicom %s output: %v", fixture.name, err)
		}
	}
}

func writeISO2022InteropFile(t *testing.T, path string, suffix int, codes []string, textTag core.Tag, vr core.VR, text string) {
	t.Helper()
	const sopClassUID = "1.2.840.10008.5.1.4.1.1.7"
	sopInstanceUID := "1.2.826.0.1.3680043.10.543.862." + strconv.Itoa(suffix)
	element := func(tag core.Tag, vr core.VR, values ...string) core.Element {
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(values)}
	}
	meta := object.FromElements([]core.Element{
		element(tags.MediaStorageSOPClassUID, core.VRUI, sopClassUID),
		element(tags.MediaStorageSOPInstanceUID, core.VRUI, sopInstanceUID),
		element(tags.TransferSyntaxUID, core.VRUI, transfer.ExplicitVRLittleEndian.UID),
	}, std.Dictionary)
	dataset := object.FromElements([]core.Element{
		element(tags.SOPClassUID, core.VRUI, sopClassUID),
		element(tags.SOPInstanceUID, core.VRUI, sopInstanceUID),
		element(core.NewTag(0x0008, 0x0005), core.VRCS, codes...),
		element(textTag, vr, text),
	}, std.Dictionary)
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := object.WriteFile(output, &object.File{Meta: meta, Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}); err != nil {
		_ = output.Close()
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close %s: %v", filepath.Base(path), err)
	}
}
