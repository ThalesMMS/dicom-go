package object_test

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

const (
	part10InteropSOPClassUID      = "1.2.840.10008.5.1.4.1.1.7"
	part10InteropUIDRoot          = "1.2.826.0.1.3680043.10.543.867"
	part10InteropPatientName      = "SYNTHETIC^MÜLLER"
	part10InteropPatientID        = "NO-PHI-867"
	part10InteropCharacterSet     = "ISO_IR 192"
	part10InteropPythonEnv        = "DICOM_GO_PYTHON"
	part10InteropPydicomEnableEnv = "DICOM_GO_PYDICOM_PART10"
)

var (
	part10InteropCharacterSetTag = core.NewTag(0x0008, 0x0005)
	part10InteropSequenceTag     = core.NewTag(0x0008, 0x1140)
	part10InteropPixelValues     = []uint16{1, 256, 4095, 65535, 42, 1024}
)

type part10InteropCase struct {
	name              string
	syntax            transfer.Syntax
	uidSuffix         int
	undefinedSequence bool
}

func TestPart10SyntheticFixtureMatrix(t *testing.T) {
	directory := t.TempDir()
	for _, test := range part10InteropCases() {
		path := filepath.Join(directory, "go-"+test.name+".dcm")
		writePart10InteropFixture(t, path, test)
		assertPart10InteropFile(t, path, test)
	}
}

// TestPydicomPart10InteropMatrix is an opt-in, bidirectional independent-reader
// gate. Go first authors synthetic Part 10 files for pydicom to validate.
// Pydicom then independently authors the same five cases; Go reads and rewrites
// those files before pydicom verifies the rewritten semantics.
func TestPydicomPart10InteropMatrix(t *testing.T) {
	if os.Getenv(part10InteropPydicomEnableEnv) != "1" {
		t.Skip("set DICOM_GO_PYDICOM_PART10=1 to enable independent Part 10 validation")
	}
	python := strings.TrimSpace(os.Getenv(part10InteropPythonEnv))
	if python == "" {
		python = "python3"
	}
	script := part10InteropScriptPath(t)
	directory := t.TempDir()

	for _, test := range part10InteropCases() {
		path := filepath.Join(directory, "go-"+test.name+".dcm")
		writePart10InteropFixture(t, path, test)
		assertPart10InteropFile(t, path, test)
	}
	runPart10InteropScript(t, python, script, "prepare", directory)

	for _, test := range part10InteropCases() {
		source := filepath.Join(directory, "pydicom-"+test.name+".dcm")
		assertPart10InteropFile(t, source, test)
		destination := filepath.Join(directory, "go-rewrite-"+test.name+".dcm")
		rewritePart10InteropFile(t, source, destination)
		assertPart10InteropFile(t, destination, test)
	}
	runPart10InteropScript(t, python, script, "verify-rewrites", directory)
}

func part10InteropCases() []part10InteropCase {
	return []part10InteropCase{
		{name: "explicit-little", syntax: transfer.ExplicitVRLittleEndian, uidSuffix: 1},
		{name: "implicit-little", syntax: transfer.ImplicitVRLittleEndian, uidSuffix: 2},
		{name: "explicit-big", syntax: transfer.ExplicitVRBigEndian, uidSuffix: 3},
		{name: "undefined-sequence", syntax: transfer.ExplicitVRLittleEndian, uidSuffix: 4, undefinedSequence: true},
		{name: "native-pixel", syntax: transfer.ExplicitVRLittleEndian, uidSuffix: 5},
	}
}

func writePart10InteropFixture(t *testing.T, path string, test part10InteropCase) {
	t.Helper()
	file := &object.File{Dataset: object.FromElements(part10InteropElements(test), std.Dictionary), TransferSyntax: test.syntax}
	writePart10InteropFile(t, path, file)
}

func rewritePart10InteropFile(t *testing.T, source, destination string) {
	t.Helper()
	file, err := object.OpenFile(source)
	if err != nil {
		t.Fatalf("open %s for rewrite: %v", filepath.Base(source), err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close %s after rewrite: %v", filepath.Base(source), err)
		}
	}()
	writePart10InteropFile(t, destination, file)
}

func writePart10InteropFile(t *testing.T, path string, file *object.File) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := object.WriteFile(output, file); err != nil {
		_ = output.Close()
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close %s: %v", filepath.Base(path), err)
	}
}

func assertPart10InteropFile(t *testing.T, path string, test part10InteropCase) {
	t.Helper()
	file, err := object.OpenFile(path)
	if err != nil {
		t.Fatalf("open %s: %v", filepath.Base(path), err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close %s: %v", filepath.Base(path), err)
		}
	}()

	if file.TransferSyntax.UID != test.syntax.UID {
		t.Fatalf("%s transfer syntax=%q, want %q", filepath.Base(path), file.TransferSyntax.UID, test.syntax.UID)
	}
	if got, ok := file.Meta.GetString(tags.TransferSyntaxUID); !ok || got != test.syntax.UID {
		t.Fatalf("%s file meta transfer syntax=(%q,%t), want %q", filepath.Base(path), got, ok, test.syntax.UID)
	}
	for tag, want := range map[core.Tag]string{
		part10InteropCharacterSetTag: part10InteropCharacterSet,
		tags.SOPClassUID:             part10InteropSOPClassUID,
		tags.SOPInstanceUID:          part10InteropUID(test.uidSuffix, 3),
		tags.StudyInstanceUID:        part10InteropUID(test.uidSuffix, 1),
		tags.SeriesInstanceUID:       part10InteropUID(test.uidSuffix, 2),
		tags.Modality:                "OT",
		tags.PatientName:             part10InteropPatientName,
		tags.PatientID:               part10InteropPatientID,
		core.NewTag(0x0020, 0x4000):  "SYNTHETIC INTEROP " + test.name,
	} {
		if got, ok := file.Dataset.GetString(tag); !ok || got != want {
			t.Fatalf("%s %s=(%q,%t), want %q", filepath.Base(path), tag, got, ok, want)
		}
	}

	sequenceElement, ok := file.Dataset.Get(part10InteropSequenceTag)
	if !ok {
		t.Fatalf("%s referenced image sequence missing", filepath.Base(path))
	}
	if test.undefinedSequence && !sequenceElement.Length().IsUndefined() {
		t.Fatalf("%s sequence is not undefined-length", filepath.Base(path))
	}
	sequence, ok := sequenceElement.Value.(core.SequenceValue)
	if !ok || len(sequence.Items) != 1 {
		t.Fatalf("%s sequence value=%#v", filepath.Base(path), sequenceElement.Value)
	}
	item := object.FromDataSet(sequence.Items[0], std.Dictionary)
	for tag, want := range map[core.Tag]string{
		core.NewTag(0x0008, 0x1150): part10InteropSOPClassUID,
		core.NewTag(0x0008, 0x1155): part10InteropUID(test.uidSuffix, 4),
	} {
		if got, ok := item.GetString(tag); !ok || got != want {
			t.Fatalf("%s reference %s=(%q,%t), want %q", filepath.Base(path), tag, got, ok, want)
		}
	}

	pixel, ok := file.Dataset.GetRaw(tags.PixelData)
	if !ok || len(pixel) != 2*len(part10InteropPixelValues) {
		t.Fatalf("%s Pixel Data length=%d ok=%t", filepath.Base(path), len(pixel), ok)
	}
	for index, want := range part10InteropPixelValues {
		if got := test.syntax.ByteOrder.Uint16(pixel[index*2:]); got != want {
			t.Fatalf("%s pixel[%d]=%d, want %d", filepath.Base(path), index, got, want)
		}
	}
}

func part10InteropElements(test part10InteropCase) []core.Element {
	sequenceHeader := core.ElementHeader{Tag: part10InteropSequenceTag, VR: core.VRSQ}
	if test.undefinedSequence {
		sequenceHeader.Length = core.UndefinedLength
		sequenceHeader.LengthSet = true
	}
	return []core.Element{
		part10InteropStringElement(tags.SOPClassUID, core.VRUI, part10InteropSOPClassUID),
		part10InteropStringElement(tags.SOPInstanceUID, core.VRUI, part10InteropUID(test.uidSuffix, 3)),
		part10InteropStringElement(tags.Modality, core.VRCS, "OT"),
		part10InteropStringElement(part10InteropCharacterSetTag, core.VRCS, part10InteropCharacterSet),
		part10InteropStringElement(tags.PatientName, core.VRPN, part10InteropPatientName),
		part10InteropStringElement(tags.PatientID, core.VRLO, part10InteropPatientID),
		part10InteropStringElement(tags.StudyInstanceUID, core.VRUI, part10InteropUID(test.uidSuffix, 1)),
		part10InteropStringElement(tags.SeriesInstanceUID, core.VRUI, part10InteropUID(test.uidSuffix, 2)),
		part10InteropStringElement(core.NewTag(0x0020, 0x4000), core.VRLT, "SYNTHETIC INTEROP "+test.name),
		part10InteropUint16Element(tags.SamplesPerPixel, 1),
		part10InteropStringElement(tags.PhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		part10InteropUint16Element(tags.Rows, 2),
		part10InteropUint16Element(tags.Columns, 3),
		part10InteropUint16Element(tags.BitsAllocated, 16),
		part10InteropUint16Element(tags.BitsStored, 16),
		part10InteropUint16Element(tags.HighBit, 15),
		part10InteropUint16Element(tags.PixelRepresentation, 0),
		{
			Header: sequenceHeader,
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				part10InteropStringElement(core.NewTag(0x0008, 0x1150), core.VRUI, part10InteropSOPClassUID),
				part10InteropStringElement(core.NewTag(0x0008, 0x1155), core.VRUI, part10InteropUID(test.uidSuffix, 4)),
			}}}},
		},
		core.NewRawElement(tags.PixelData, core.VROW, part10InteropPixelBytes(test.syntax.ByteOrder)),
	}
}

func part10InteropStringElement(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(values)}
}

func part10InteropUint16Element(tag core.Tag, value uint16) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRUS}, Value: core.Uint16Value{value}}
}

func part10InteropPixelBytes(order binary.ByteOrder) []byte {
	pixel := make([]byte, 2*len(part10InteropPixelValues))
	for index, value := range part10InteropPixelValues {
		order.PutUint16(pixel[index*2:], value)
	}
	return pixel
}

func part10InteropUID(caseSuffix, valueSuffix int) string {
	return part10InteropUIDRoot + "." + strconv.Itoa(caseSuffix) + "." + strconv.Itoa(valueSuffix)
}

func part10InteropScriptPath(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Part 10 interoperability test source")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "scripts", "pydicom_part10_interop.py"))
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Fatalf("locate pydicom Part 10 script: %v", err)
	}
	return path
}

func runPart10InteropScript(t *testing.T, python, script, action, directory string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, python, script, action, directory).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("pydicom Part 10 %s timed out: %v", action, ctx.Err())
	}
	if err != nil {
		t.Fatalf("pydicom Part 10 %s failed: %v\n%s", action, err, output)
	}
}
