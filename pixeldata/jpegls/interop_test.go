package jpegls

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// TestIndependentDecoderAcceptsGeneratedCodestreams qualifies encoder output
// against CharLS or DCMTK. When DICOM_GO_JPEGLS_INDEPENDENT=1 the test fails
// closed if neither independent decoder is available; it does not Skip.
func TestIndependentDecoderAcceptsGeneratedCodestreams(t *testing.T) {
	if os.Getenv("DICOM_GO_JPEGLS_INDEPENDENT") != "1" {
		t.Skip("set DICOM_GO_JPEGLS_INDEPENDENT=1 to require CharLS or DCMTK qualification")
	}
	decoder, err := lookupIndependentJPEGLSDecoder()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		metadata pixeldata.Metadata
		frame    []byte
	}{
		{
			name:     "monochrome 8-bit",
			metadata: encoderMetadata(2, 3, 1, 8, "MONOCHROME2"),
			frame:    []byte{0, 0, 64, 128, 255, 255},
		},
		{
			name:     "monochrome 16-bit",
			metadata: encoderMetadata(1, 4, 1, 16, "MONOCHROME2"),
			frame:    []byte{0, 0, 1, 0, 0, 128, 255, 255},
		},
		{
			name:     "RGB 8-bit",
			metadata: encoderMetadata(1, 3, 3, 8, "RGB"),
			frame:    []byte{255, 0, 0, 0, 255, 0, 0, 0, 255},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := NewEncoder().EncodeFrame(context.Background(), test.frame, test.metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := decoder(t, encoded.Data, test.frame, test.metadata); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecoderMatchesIndependentPydicomReferences(t *testing.T) {
	fixtureDir := filepath.Join("..", "codecfixture", "testdata", "codecfull", "pydicom")
	for _, test := range []struct {
		name       string
		compressed string
		reference  string
	}{
		{name: "multiframe MR", compressed: "emri_small_jpeg_ls_lossless.dcm", reference: "emri_small.dcm"},
		{name: "signed MR", compressed: "MR_small_jpeg_ls_lossless.dcm", reference: "MR_small.dcm"},
	} {
		t.Run(test.name, func(t *testing.T) {
			compressed, err := object.OpenFile(filepath.Join(fixtureDir, test.compressed))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = compressed.Close() }()
			pixel, err := pixeldata.ExtractView(compressed.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			got, err := New().DecodeContext(context.Background(), pixel, compressed.Dataset)
			if err != nil {
				t.Fatal(err)
			}

			reference, err := object.OpenFile(filepath.Join(fixtureDir, test.reference))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reference.Close() }()
			want, err := pixeldata.ExtractNativeFramesView(reference.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			if got.Rows != int(want.Metadata.Rows) || got.Columns != int(want.Metadata.Columns) || len(got.Data) != len(want.Data) {
				t.Fatalf("decoded shape = %dx%d/%d, want %dx%d/%d", got.Columns, got.Rows, len(got.Data), want.Metadata.Columns, want.Metadata.Rows, len(want.Data))
			}
			for index := range want.Data {
				if !bytes.Equal(got.Data[index], want.Data[index]) {
					difference := firstDifferentByte(got.Data[index], want.Data[index])
					if difference == len(got.Data[index]) || difference == len(want.Data[index]) {
						t.Fatalf("frame %d length differs from independently generated native reference: got=%d want=%d", index, len(got.Data[index]), len(want.Data[index]))
					}
					t.Fatalf("frame %d differs from independently generated native reference at byte %d: got=%#02x want=%#02x", index, difference, got.Data[index][difference], want.Data[index][difference])
				}
			}
		})
	}
}

func firstDifferentByte(got, want []byte) int {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			return i
		}
	}
	return limit
}

func lookupIndependentJPEGLSDecoder() (func(*testing.T, []byte, []byte, pixeldata.Metadata) error, error) {
	if path, err := exec.LookPath("dcmdjpls"); err == nil {
		return func(t *testing.T, stream, want []byte, metadata pixeldata.Metadata) error {
			return decodeWithDCMTK(t, path, stream, want, metadata)
		}, nil
	}
	python := os.Getenv("DICOM_GO_PYTHON")
	if python == "" {
		python = "python"
	}
	if _, err := exec.LookPath(python); err == nil {
		if exec.Command(python, "-c", "import jpeg_ls").Run() == nil {
			return func(t *testing.T, stream, want []byte, metadata pixeldata.Metadata) error {
				return runPythonStreamCheck(t, python, pythonJpegLSScript, stream, want)
			}, nil
		}
		if exec.Command(python, "-c", "import charls").Run() == nil {
			return func(t *testing.T, stream, want []byte, metadata pixeldata.Metadata) error {
				return runPythonStreamCheck(t, python, pythonCharLSScript, stream, want)
			}, nil
		}
	}
	return nil, fmt.Errorf("independent JPEG-LS decoder missing: install DCMTK dcmdjpls or a Python jpeg_ls/charls module; CharLS/DCMTK qualification is blocked on this host")
}

const pythonJpegLSScript = `
import sys
import jpeg_ls
data = open(sys.argv[1], "rb").read()
decoded = jpeg_ls.decode(data)
want = bytes.fromhex(sys.argv[2])
if bytes(decoded) != want:
    raise SystemExit("python jpeg_ls mismatch")
`

const pythonCharLSScript = `
import sys
import charls
data = open(sys.argv[1], "rb").read()
decoded = charls.jpeg_ls_decode(data)
want = bytes.fromhex(sys.argv[2])
if bytes(decoded) != want:
    raise SystemExit("python charls mismatch")
`

func decodeWithDCMTK(t *testing.T, dcmdjpls string, stream, want []byte, metadata pixeldata.Metadata) error {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "jpegls.dcm")
	dst := filepath.Join(dir, "native.dcm")
	fileBytes, err := encodedPart10(t, stream, metadata)
	if err != nil {
		return err
	}
	if err := os.WriteFile(src, fileBytes, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(dcmdjpls, src, dst)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dcmdjpls: %w\n%s", err, output)
	}
	handle, err := os.Open(dst)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	decoded, err := object.ReadFile(handle)
	if err != nil {
		return err
	}
	frames, err := pixeldata.ExtractNativeFramesView(decoded.Dataset)
	if err != nil {
		return err
	}
	if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], want) {
		return fmt.Errorf("dcmdjpls pixels = % x, want % x", frames.Data, want)
	}
	return nil
}

func runPythonStreamCheck(t *testing.T, python, script string, stream, want []byte) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frame.jls")
	if err := os.WriteFile(path, stream, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(python, "-c", script, path, fmt.Sprintf("%x", want))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("independent python decoder: %w\n%s", err, output)
	}
	return nil
}

func encodedPart10(t *testing.T, fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	t.Helper()
	obj, _ := jpeglsObjectWithFragment(t, metadata, fragment)
	obj.Put(dicomtest.NewStringElement(core.NewTag(0x0008, 0x0016), core.VRUI, "1.2.840.10008.5.1.4.1.1.7"))
	obj.Put(dicomtest.NewStringElement(core.NewTag(0x0008, 0x0018), core.VRUI, "1.2.826.0.1.3680043.8.498.640"))
	file := &object.File{Dataset: obj, TransferSyntax: transfer.JPEGLSLossless}
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
