package dicomutil

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestRunJSONOutput(t *testing.T) {
	fixturePath := writeFixturePath(t, minimalFixture(t))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"-path", fixturePath, "-json"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0, stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("stdout is not valid JSON:\n%s", stdout.String())
	}

	var got fileJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax UID = %q, want %q", got.TransferSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	if _, ok := got.FileMeta["00020010"]; !ok {
		t.Fatalf("fileMeta missing TransferSyntaxUID: %#v", got.FileMeta)
	}
	if _, ok := got.DataSet["00080016"]; !ok {
		t.Fatalf("dataSet missing SOPClassUID: %#v", got.DataSet)
	}
}

func TestRunExtractImagesNativeFrames(t *testing.T) {
	fixturePath := writeFixturePath(t, nativeImageFixture(t, 2))
	outDir := filepath.Join(t.TempDir(), "images")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"-path", fixturePath, "-extract-images", "-out-dir", outDir}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0, stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	assertPNGDimensions(t, filepath.Join(outDir, "image_0001.png"), 2, 1)
	assertPNGDimensions(t, filepath.Join(outDir, "image_0002.png"), 2, 1)
	if _, err := os.Stat(filepath.Join(outDir, "image_0003.png")); !os.IsNotExist(err) {
		t.Fatalf("unexpected third image stat error: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "image_0001.png") || !strings.Contains(got, "image_0002.png") {
		t.Fatalf("stdout missing written image names: %q", got)
	}
}

func TestRunExtractImagesJPEGBaseline(t *testing.T) {
	fixturePath := writeFixturePath(t, jpegBaselineFixture(t))
	outDir := filepath.Join(t.TempDir(), "images")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"-path", fixturePath, "-extract-images", "-out-dir", outDir}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0, stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	assertPNGDimensions(t, filepath.Join(outDir, "image_0001.png"), 2, 1)
}

func TestRunExtractImagesVideoMediaReportsNonRenderable(t *testing.T) {
	fixturePath := writeFixturePath(t, videoMediaFixture(t))
	outDir := filepath.Join(t.TempDir(), "images")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"-path", fixturePath, "-extract-images", "-out-dir", outDir}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	for _, want := range []string{
		"dicomutil: media:",
		"video media payload",
		"not renderable as still-image frames",
		"external media pipeline",
		transfer.MPEG4HP41F.UID,
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
		}
	}
	if strings.Contains(stderr.String(), "RegisterCodec") {
		t.Fatalf("stderr = %q, want media diagnostic instead of codec registration hint", stderr.String())
	}
}

func TestRunUsageErrors(t *testing.T) {
	fixturePath := writeFixturePath(t, minimalFixture(t))

	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "missing path",
			args:       []string{"-json"},
			wantStderr: "-path is required",
		},
		{
			name:       "missing mode",
			args:       []string{"-path", fixturePath},
			wantStderr: "choose exactly one of -json or -extract-images",
		},
		{
			name:       "multiple modes",
			args:       []string{"-path", fixturePath, "-json", "-extract-images", "-out-dir", t.TempDir()},
			wantStderr: "choose exactly one of -json or -extract-images",
		},
		{
			name:       "missing output directory",
			args:       []string{"-path", fixturePath, "-extract-images"},
			wantStderr: "-out-dir is required with -extract-images",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := Run(tt.args, &stdout, &stderr)

			if exitCode != 2 {
				t.Fatalf("Run() exit code = %d, want 2", exitCode)
			}
			if stdout.Len() != 0 {
				t.Fatalf("unexpected stdout: %q", stdout.String())
			}
			if got := stderr.String(); !strings.Contains(got, tt.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", got, tt.wantStderr)
			}
		})
	}
}

func minimalFixture(t *testing.T) []byte {
	t.Helper()

	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func nativeImageFixture(t *testing.T, frames int) []byte {
	t.Helper()

	pixelData := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		pixelData[i*2] = byte(i * 32)
		pixelData[i*2+1] = 255 - byte(i*32)
	}

	dataset := append([]core.Element{}, dicomtest.MinimalDataset()...)
	dataset = append(dataset,
		imageMetadataElements(1, 2, 8, 8, 7, 0, "MONOCHROME2")...,
	)
	if frames > 1 {
		dataset = append(dataset, dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, strconv.Itoa(frames)))
	}
	dataset = append(dataset, dicomtest.NewOBElement(core.TagPixelData, pixelData))

	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dataset...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func jpegBaselineFixture(t *testing.T) []byte {
	t.Helper()

	img := image.NewGray(image.Rect(0, 0, 2, 1))
	img.SetGray(0, 0, color.Gray{Y: 0})
	img.SetGray(1, 0, color.Gray{Y: 255})

	var jpegData bytes.Buffer
	if err := stdjpeg.Encode(&jpegData, img, &stdjpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}

	dataset := append([]core.Element{}, dicomtest.MinimalDataset()...)
	dataset = append(dataset,
		imageMetadataElements(1, 2, 8, 8, 7, 0, "MONOCHROME2")...,
	)
	dataset = append(dataset, dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, jpegData.Bytes()))

	data, err := dicomtest.Part10File(transfer.JPEGBaseline, dataset...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func videoMediaFixture(t *testing.T) []byte {
	t.Helper()

	dataset := append([]core.Element{}, dicomtest.MinimalDataset()...)
	dataset = append(dataset,
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0002), core.VRUS, binary.LittleEndian, 3),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "YBR_PARTIAL_420"),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0006), core.VRUS, binary.LittleEndian, 0),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, "1"),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0010), core.VRUS, binary.LittleEndian, 2),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0011), core.VRUS, binary.LittleEndian, 2),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0100), core.VRUS, binary.LittleEndian, 8),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0101), core.VRUS, binary.LittleEndian, 8),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0102), core.VRUS, binary.LittleEndian, 7),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0103), core.VRUS, binary.LittleEndian, 0),
		dicomtest.NewFragmentSequenceElement(
			core.TagPixelData,
			nil,
			[]byte{0x00, 0x00, 0x01, 0x09, 0x10, 0x00},
			[]byte{0x00, 0x00, 0x01, 0x65, 0x88, 0x00},
		),
	)
	data, err := dicomtest.Part10File(transfer.MPEG4HP41F, dataset...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func imageMetadataElements(rows, columns, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16, photometric string) []core.Element {
	return []core.Element{
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0002), core.VRUS, binary.LittleEndian, 1),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, photometric),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0010), core.VRUS, binary.LittleEndian, rows),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0011), core.VRUS, binary.LittleEndian, columns),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0100), core.VRUS, binary.LittleEndian, bitsAllocated),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0101), core.VRUS, binary.LittleEndian, bitsStored),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0102), core.VRUS, binary.LittleEndian, highBit),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0103), core.VRUS, binary.LittleEndian, pixelRepresentation),
	}
}

func writeFixturePath(t *testing.T, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fixture.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertPNGDimensions(t *testing.T, path string, width, height int) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != width || bounds.Dy() != height {
		t.Fatalf("%s dimensions = %dx%d, want %dx%d", path, bounds.Dx(), bounds.Dy(), width, height)
	}
}

var tagNumberOfFrames = core.NewTag(0x0028, 0x0008)
