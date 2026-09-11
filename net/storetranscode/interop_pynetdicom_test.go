package storetranscode

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestStoreTranscodeLossyAgainstPynetdicomAndPillow(t *testing.T) {
	if os.Getenv("DICOMGO_PYNETDICOM_INTEGRATION") == "" {
		t.Skip("set DICOMGO_PYNETDICOM_INTEGRATION=1 with the pinned store transcode interop profile")
	}
	python := os.Getenv("DICOMGO_PYTHON")
	if python == "" {
		python = "python3"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, python, "-c", "import pydicom,pynetdicom,PIL; print(pydicom.__version__+'/'+pynetdicom.__version__+'/'+PIL.__version__)").Output()
	if err != nil || strings.TrimSpace(string(version)) != "3.0.2/3.0.4/12.3.0" {
		t.Fatal("install scripts/requirements-store-transcode-interop.txt into DICOMGO_PYTHON")
	}
	dir := t.TempDir()
	fixture := codecfixture.NativeLarge()
	file := &object.File{Dataset: fixture.Object(), TransferSyntax: transfer.ExplicitVRLittleEndian}
	var wire bytes.Buffer
	if err := object.WriteFile(&wire, file); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(dir, "synthetic-source.dcm")
	outputPath := filepath.Join(dir, "decoded.raw")
	if err := os.WriteFile(inputPath, wire.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, python, "-u", "-c", lossyStorePeer, inputPath, outputPath)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("independent peer did not stop")
		}
	})
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(line) }()
	var address string
	select {
	case address = <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatalf("independent peer readiness: %q", address)
	}
	opts := testOptions(t, transfer.JPEGBaseline)
	opts.Transcode.AllowLossy = true
	if err := jpeg.RegisterEncoder(opts.Transcode.EncoderRegistry, 95); err != nil {
		t.Fatal(err)
	}
	source, err := NewSource(dimse.NewPathStoreSource(inputPath), opts)
	if err != nil {
		t.Fatal(err)
	}
	session, err := dimse.NewStoreSession(address, dimse.StoreSessionOptions{DialOptions: ul.DialOptions{CalledAETitle: "PY_STORE", CallingAETitle: "GO_STORE", NegotiationTimeout: 5 * time.Second, ReadProgressTimeout: 5 * time.Second, WriteProgressTimeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	result, sendErr := session.StoreBatch(ctx, []dimse.StoreSource{source})
	closeErr := session.Close(ctx)
	if sendErr != nil || closeErr != nil || result.Succeeded != 1 {
		t.Fatalf("independent C-STORE: send=%v close=%v result=%+v", sendErr, closeErr, result.Items)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	layout := codecfixture.SampleLayout{Rows: 256, Columns: 256, Components: 1, BitsAllocated: 8, BitsStored: 8, HighBit: 7}
	policy := codecfixture.SamplePolicy{MaxAbsoluteError: 8, MinPSNR: 40, MaxTileMeanAbsoluteError: 2, Rationale: "JPEG Baseline quality 95 on the synthetic unsigned 8-bit horizontal ramp; Pillow/libjpeg reconstruction must preserve full-frame and local contrast within explicit bounds"}
	report, err := codecfixture.CompareSamples([][]byte{got}, layout, fixture.ExpectedFrames, layout, policy)
	if err != nil || !report.Qualified {
		t.Fatalf("independent lossy pixels: %+v %v", report, err)
	}
	t.Logf("Pillow independently checked %d samples; complete metadata checked by pydicom peer", report.Samples)
	assertEmpty(t, opts.SpoolDirectory)
	after, err := os.ReadFile(inputPath)
	if err != nil || !bytes.Equal(after, wire.Bytes()) {
		t.Fatal("origin changed")
	}
}

const lossyStorePeer = `
import io, sys
import pydicom
from PIL import Image
from pydicom.encaps import generate_frames
from pynetdicom import AE, evt

source = pydicom.dcmread(sys.argv[1])
expected = source.to_json_dict()
ignored = ['7FE00010', '00080018', '00080008', '00282110', '00282112', '00282114']
for tag in ignored:
    expected.pop(tag, None)

def store(event):
    try:
        ds = event.dataset
        assert str(event.context.transfer_syntax) == '1.2.840.10008.1.2.4.50'
        assert ds.SOPClassUID == source.SOPClassUID == event.request.AffectedSOPClassUID
        assert ds.SOPInstanceUID == event.request.AffectedSOPInstanceUID
        assert ds.SOPInstanceUID != source.SOPInstanceUID
        assert ds.LossyImageCompression == '01'
        assert str(ds.ImageType[0]) == 'DERIVED'
        assert ds.LossyImageCompressionMethod == 'ISO_10918_1'
        assert float(ds.LossyImageCompressionRatio) > 0
        actual = ds.to_json_dict()
        for tag in ignored:
            actual.pop(tag, None)
        assert actual == expected
        reconstructed = []
        for frame in generate_frames(ds.PixelData, number_of_frames=int(ds.get('NumberOfFrames', 1))):
            image = Image.open(io.BytesIO(frame))
            assert image.mode == 'L' and image.size == (256, 256)
            reconstructed.append(image.tobytes())
        assert len(reconstructed) == 1
        with open(sys.argv[2], 'wb') as output:
            output.write(b''.join(reconstructed))
        return 0x0000
    except Exception:
        return 0xC000

ae = AE(ae_title='PY_STORE')
ae.add_supported_context(source.SOPClassUID, ['1.2.840.10008.1.2.4.50'])
server = ae.start_server(('127.0.0.1', 0), block=False, evt_handlers=[(evt.EVT_C_STORE, store)])
print('127.0.0.1:' + str(server.server_address[1]), flush=True)
sys.stdin.read()
server.shutdown()
`
