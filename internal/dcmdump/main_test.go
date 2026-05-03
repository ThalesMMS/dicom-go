package dcmdump

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var update = flag.Bool("update", false, "update golden files")

func TestTextGoldenBasicPrimitiveElements(t *testing.T) {
	assertTextGolden(t, "basic_text.golden", primitiveFixture, defaultMaxValueLen)
}

func TestTextGoldenNestedSequences(t *testing.T) {
	assertTextGolden(t, "nested_sequences.golden", nestedSequenceFixture, defaultMaxValueLen)
}

func TestTextGoldenPixelDataSummary(t *testing.T) {
	assertTextGolden(t, "pixeldata_summary.golden", pixelDataFixture, defaultMaxValueLen)
}

func TestTextGoldenLongStringTruncation(t *testing.T) {
	assertTextGolden(t, "long_string_truncation.golden", longStringFixture, 24)
}

func TestJSONGoldenOutput(t *testing.T) {
	file := openFixtureFile(t, primitiveFixture(t))

	var out bytes.Buffer
	if err := dumpFileJSON(&out, file); err != nil {
		t.Fatal(err)
	}
	if !jsonLooksValid(out.Bytes()) {
		t.Fatalf("output is not valid JSON:\n%s", out.String())
	}

	var got fileJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantMeta, err := marshalJSONObject(file.Meta)
	if err != nil {
		t.Fatal(err)
	}
	wantDataSet, err := marshalJSONObject(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.FileMeta, wantMeta) {
		t.Fatalf("fileMeta JSON mismatch:\n%s", diffText(mustMarshalJSON(t, got.FileMeta), mustMarshalJSON(t, wantMeta)))
	}
	if !reflect.DeepEqual(got.DataSet, wantDataSet) {
		t.Fatalf("dataSet JSON mismatch:\n%s", diffText(mustMarshalJSON(t, got.DataSet), mustMarshalJSON(t, wantDataSet)))
	}
	assertGolden(t, "json_output.golden", out.Bytes())
}

func TestJSONMarshalObjectIncludesSequences(t *testing.T) {
	file := openFixtureFile(t, nestedSequenceFixture(t))

	got, err := marshalJSONObject(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := got["00081111"].(map[string]any)
	if !ok {
		t.Fatalf("sequence entry type = %T, want map[string]any", got["00081111"])
	}
	items, ok := entry["Value"].([]any)
	if !ok {
		t.Fatalf("sequence Value type = %T, want []any", entry["Value"])
	}
	if len(items) != 2 {
		t.Fatalf("sequence item count = %d, want 2", len(items))
	}
	firstItem, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first sequence item type = %T, want map[string]any", items[0])
	}
	if _, ok := firstItem["00081150"]; !ok {
		t.Fatalf("first sequence item missing nested SOPClassUID: %#v", firstItem)
	}
}

func TestJSONMarshalObjectIncludesKnownUIDNames(t *testing.T) {
	file := openFixtureFile(t, primitiveFixture(t))

	got, err := marshalJSONObject(file.Meta)
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := got["00020010"].(map[string]any)
	if !ok {
		t.Fatalf("transfer syntax entry type = %T, want map[string]any", got["00020010"])
	}
	names, ok := entry["ValueNames"].([]any)
	if !ok {
		t.Fatalf("ValueNames type = %T, want []any", entry["ValueNames"])
	}
	if len(names) != 1 || names[0] != "Explicit VR Little Endian" {
		t.Fatalf("ValueNames = %#v, want [Explicit VR Little Endian]", names)
	}
}

func TestJSONMarshalObjectOmitsUnknownUIDNames(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.NewUIElement(core.NewTag(0x0008, 0x0016), "9.8.7.6.5"),
		},
	}, std.Dictionary)

	got, err := marshalJSONObject(obj)
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := got["00080016"].(map[string]any)
	if !ok {
		t.Fatalf("entry type = %T, want map[string]any", got["00080016"])
	}
	if _, ok := entry["ValueNames"]; ok {
		t.Fatalf("unexpected ValueNames for unknown UID: %#v", entry["ValueNames"])
	}
}

func TestRunTextOutputMatchesGolden(t *testing.T) {
	fixturePath := writeFixturePath(t, primitiveFixture(t))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"-max-value=64", "-show-offsets", fixturePath}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0, stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	assertGolden(t, "basic_text.golden", stdout.Bytes())
}

func TestRunJSONOutputMatchesGolden(t *testing.T) {
	fixturePath := writeFixturePath(t, primitiveFixture(t))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"-json", fixturePath}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0, stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	assertGolden(t, "json_output.golden", stdout.Bytes())
}

func TestRunErrors(t *testing.T) {
	corruptPath := writeFixturePath(t, bytes.Repeat([]byte{0x00}, 132))
	missingPath := filepath.Join(t.TempDir(), "missing.dcm")

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "missing file argument",
			args:       nil,
			wantCode:   2,
			wantStdout: "",
			wantStderr: "Usage: dcmdump [flags] <file.dcm>",
		},
		{
			name:       "non-existent file path",
			args:       []string{missingPath},
			wantCode:   1,
			wantStdout: "",
			wantStderr: "no such file or directory",
		},
		{
			name:       "invalid corrupt dicom file",
			args:       []string{corruptPath},
			wantCode:   1,
			wantStdout: "",
			wantStderr: "missing Part 10 preamble",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := Run(tt.args, &stdout, &stderr)

			if exitCode != tt.wantCode {
				t.Fatalf("exit code = %d, want %d; stderr=%q", exitCode, tt.wantCode, stderr.String())
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Fatalf("stdout = %q, want %q", got, tt.wantStdout)
			}
			if got := stderr.String(); !strings.Contains(got, tt.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", got, tt.wantStderr)
			}
		})
	}
}

func TestRunUsageErrorWithInvalidMaxValue(t *testing.T) {
	fixturePath := writeFixturePath(t, primitiveFixture(t))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"-max-value=0", fixturePath}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "-max-value must be greater than zero") {
		t.Fatalf("stderr missing max-value validation message: %q", got)
	}
}

func assertTextGolden(t *testing.T, goldenName string, fixture func(*testing.T) []byte, maxValueLen int) {
	t.Helper()

	file := openFixtureFile(t, fixture(t))
	var out bytes.Buffer
	if err := NewFormatter(&out, maxValueLen).DumpFile(file); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, goldenName, out.Bytes())
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	got = normalizeNewlines(got)

	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want = normalizeNewlines(want)
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch for %s:\n%s", name, diffText(string(got), string(want)))
	}
}

func openFixtureFile(t *testing.T, data []byte) *object.File {
	t.Helper()

	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func writeFixturePath(t *testing.T, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fixture.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func primitiveFixture(t *testing.T) []byte {
	t.Helper()

	dataset := append([]core.Element{}, dicomtest.MinimalDataset()...)
	dataset = append(dataset,
		dicomtest.NewStringElement(core.NewTag(0x0008, 0x0060), core.VRCS, "OT"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), 8),
	)

	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dataset...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func nestedSequenceFixture(t *testing.T) []byte {
	t.Helper()

	sequenceTag := core.NewTag(0x0008, 0x1111)
	nestedSequenceTag := core.NewTag(0x0008, 0x1115)

	dataset := append([]core.Element{}, dicomtest.MinimalDataset()...)
	dataset = append(dataset,
		dicomtest.NewSequenceElement(
			sequenceTag,
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewStringElement(core.NewTag(0x0008, 0x1150), core.VRUI, dicomtest.TestSOPClassUID),
					dicomtest.NewSequenceElement(
						nestedSequenceTag,
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "NEST^PATIENT"),
							},
						},
					),
				},
			},
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewStringElement(core.NewTag(0x0008, 0x1155), core.VRUI, dicomtest.TestSOPInstanceUID),
				},
			},
		),
	)

	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dataset...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func pixelDataFixture(t *testing.T) []byte {
	t.Helper()

	pixelData := make([]byte, 4096)
	for i := range pixelData {
		pixelData[i] = byte((i * 17) % 256)
	}

	dataset := append([]core.Element{}, dicomtest.MinimalDataset()...)
	dataset = append(dataset,
		dicomtest.NewStringElement(core.NewTag(0x0008, 0x0060), core.VRCS, "OT"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 64),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), 64),
		dicomtest.NewOBElement(core.TagPixelData, pixelData),
	)

	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dataset...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func longStringFixture(t *testing.T) []byte {
	t.Helper()

	dataset := append([]core.Element{}, dicomtest.MinimalDataset()...)
	dataset = append(dataset,
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "VERY^LONG^PATIENT^NAME^FOR^TRUNCATION^TESTING"),
	)

	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dataset...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func normalizeNewlines(data []byte) []byte {
	return []byte(strings.ReplaceAll(string(data), "\r\n", "\n"))
}

func jsonLooksValid(data []byte) bool {
	return json.Valid(data)
}

func diffText(got, want string) string {
	got = strings.TrimSuffix(strings.ReplaceAll(got, "\r\n", "\n"), "\n")
	want = strings.TrimSuffix(strings.ReplaceAll(want, "\r\n", "\n"), "\n")
	if got == want {
		return ""
	}
	return "got:\n" + got + "\n\nwant:\n" + want
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
