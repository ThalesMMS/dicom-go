package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadDataSetWithTransferSyntaxRecoverySelectsNativeSyntaxes(t *testing.T) {
	t.Parallel()

	for _, syntax := range []transfer.Syntax{
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	} {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()
			data := dicomtest.EncodeElements(syntax, dicomtest.MinimalDataset()...)
			obj, resolution, err := ReadDataSetWithTransferSyntaxRecovery(
				bytes.NewBuffer(data),
				ReadFileOptions{},
				TransferSyntaxRecoveryOptions{},
			)
			if err != nil {
				t.Fatalf("ReadDataSetWithTransferSyntaxRecovery() error = %v; resolution = %s", err, resolution.String())
			}
			if resolution.Syntax.UID != syntax.UID || resolution.Source != TransferSyntaxSourceInferredRawDataSet {
				t.Fatalf("resolution = %+v, want raw %q", resolution, syntax.UID)
			}
			stored, ok := obj.TransferSyntaxResolution()
			if !ok || stored.Syntax.UID != syntax.UID || stored.Source != resolution.Source {
				t.Fatalf("stored resolution = %+v ok=%v, want %+v", stored, ok, resolution)
			}
			requireRawDataSetWithoutFileMeta(t, obj)
		})
	}
}

func TestReadFileWithTransferSyntaxRecoveryRequiresExplicitPolicy(t *testing.T) {
	t.Parallel()

	raw := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	_, _, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(raw), ReadFileOptions{}, TransferSyntaxRecoveryOptions{})
	if !errors.Is(err, ErrMissingPreamble) {
		t.Fatalf("error = %v, want ErrMissingPreamble when policy is disabled", err)
	}

	file, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(raw), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
		AllowMissingPreamble: true,
	})
	if err != nil {
		t.Fatalf("ReadFileWithTransferSyntaxRecovery() error = %v; resolution = %s", err, resolution.String())
	}
	if file.Meta != nil || len(file.Preamble) != 0 {
		t.Fatalf("raw recovery synthesized Part 10 structures: meta=%v preamble=%d", file.Meta != nil, len(file.Preamble))
	}
	if file.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntax = %q, want %q", file.TransferSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	if resolution.Source != TransferSyntaxSourceInferredMissingPreamble {
		t.Fatalf("Source = %q, want missing preamble", resolution.Source)
	}
}

func TestReadFileWithTransferSyntaxRecoveryPreservesMetaWithoutPreamble(t *testing.T) {
	t.Parallel()

	meta := dicomtest.NewFileMetaBuilder().WithTransferSyntax(transfer.ExplicitVRBigEndian.UID).Encode()
	dataset := dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, dicomtest.MinimalDataset()...)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "DICM prefix at zero", data: append(append([]byte("DICM"), meta...), dataset...)},
		{name: "file meta at zero", data: append(append([]byte(nil), meta...), dataset...)},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(tc.data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
				AllowMissingPreamble: true,
			})
			if err != nil {
				t.Fatalf("ReadFileWithTransferSyntaxRecovery() error = %v; resolution=%s", err, resolution.String())
			}
			if file.Meta == nil || len(file.Preamble) != 0 {
				t.Fatalf("meta=%v preamble=%d, want preserved meta without synthesized preamble", file.Meta != nil, len(file.Preamble))
			}
			if got, _ := file.Meta.GetString(tagTransferSyntaxUID); got != transfer.ExplicitVRBigEndian.UID {
				t.Fatalf("meta UID = %q, want %q", got, transfer.ExplicitVRBigEndian.UID)
			}
			if resolution.Source != TransferSyntaxSourceDeclaredMissingPreamble || resolution.Inferred() {
				t.Fatalf("resolution = %+v, want declared missing-preamble recovery", resolution)
			}
		})
	}
}

func TestReadFileWithTransferSyntaxRecoveryHandlesMissingAndUnknownUID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		uid        string
		allow      TransferSyntaxRecoveryOptions
		wantCause  error
		wantSource TransferSyntaxResolutionSource
	}{
		{
			name:       "missing",
			uid:        "",
			allow:      TransferSyntaxRecoveryOptions{AllowMissingTransferSyntaxUID: true},
			wantCause:  ErrMissingTransferSyntax,
			wantSource: TransferSyntaxSourceInferredMissingUID,
		},
		{
			name:       "unknown",
			uid:        "1.2.840.10008.999.630",
			allow:      TransferSyntaxRecoveryOptions{AllowUnknownTransferSyntaxUID: true},
			wantCause:  transfer.ErrUnknownTransferSyntax,
			wantSource: TransferSyntaxSourceInferredUnknownUID,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildRecoveryPart10(tc.uid, transfer.ExplicitVRBigEndian)
			_, _, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{})
			if !errors.Is(err, tc.wantCause) {
				t.Fatalf("disabled error = %v, want %v", err, tc.wantCause)
			}

			file, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data), ReadFileOptions{}, tc.allow)
			if err != nil {
				t.Fatalf("enabled error = %v; resolution = %s", err, resolution.String())
			}
			if file.TransferSyntax.UID != transfer.ExplicitVRBigEndian.UID || resolution.Source != tc.wantSource {
				t.Fatalf("resolution = %+v, want %q / %q", resolution, transfer.ExplicitVRBigEndian.UID, tc.wantSource)
			}
			if got, ok := file.Meta.GetString(tagTransferSyntaxUID); !ok || transfer.NormalizeUID(got) != transfer.NormalizeUID(tc.uid) {
				t.Fatalf("original meta UID = %q ok=%v, want %q preserved", got, ok, tc.uid)
			}
		})
	}
}

func TestReadFileWithTransferSyntaxRecoveryOnlyOverridesMismatchWhenAllowed(t *testing.T) {
	t.Parallel()

	data := buildRecoveryPart10(transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian)
	_, _, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{})
	if !errors.Is(err, ErrDataSet) {
		t.Fatalf("disabled error = %v, want ErrDataSet", err)
	}

	file, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
		AllowDeclaredMismatch: true,
	})
	if err != nil {
		t.Fatalf("enabled error = %v; resolution = %s", err, resolution.String())
	}
	if file.TransferSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("effective UID = %q, want %q", file.TransferSyntax.UID, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, _ := file.Meta.GetString(tagTransferSyntaxUID); got != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("declared UID was rewritten to %q", got)
	}
	if resolution.Source != TransferSyntaxSourceRecoveredMismatch || resolution.DeclaredUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("resolution = %+v, want audited mismatch", resolution)
	}
}

func TestReadFileWithTransferSyntaxRecoveryPreservesAmbiguousMismatchDiagnostics(t *testing.T) {
	t.Parallel()

	data := buildRecoveryPart10(transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian)
	_, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
		AllowDeclaredMismatch: true,
		Probe: parser.TransferSyntaxProbeOptions{
			MinimumConfidence: 1.01,
		},
	})
	if !errors.Is(err, ErrDataSet) || !errors.Is(err, parser.ErrTransferSyntaxProbeAmbiguous) {
		t.Fatalf("error = %v, want dataset and ambiguity errors", err)
	}
	if resolution.Source != TransferSyntaxSourceDeclared || resolution.Probe.Outcome != parser.TransferSyntaxProbeAmbiguous {
		t.Fatalf("resolution = %+v, want declared decision with ambiguous probe", resolution)
	}
}

func TestReadFileWithTransferSyntaxRecoveryLetsPermissiveDeclaredSyntaxWin(t *testing.T) {
	t.Parallel()

	// These bytes are simultaneously a permissive Explicit VR Little Endian
	// Pixel Data element and a structurally plausible Implicit VR Little Endian
	// element. The strict probe rejects the declared explicit candidate because
	// its reserved bytes and value length are non-conformant, but the caller has
	// explicitly authorized both conditions for the normal parser.
	const explicitValueLength = 82507
	var dataset bytes.Buffer
	if err := binary.Write(&dataset, binary.LittleEndian, uint16(0x7FE0)); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&dataset, binary.LittleEndian, uint16(0x0010)); err != nil {
		t.Fatal(err)
	}
	dataset.WriteString("OB")
	dataset.Write([]byte{0x01, 0x00})
	if err := binary.Write(&dataset, binary.LittleEndian, uint32(explicitValueLength)); err != nil {
		t.Fatal(err)
	}
	dataset.Write(bytes.Repeat([]byte{0x00}, explicitValueLength))

	var data bytes.Buffer
	data.Write(make([]byte, 128))
	data.WriteString("DICM")
	data.Write(dicomtest.NewFileMetaBuilder().WithTransferSyntax(transfer.ExplicitVRLittleEndian.UID).Encode())
	data.Write(dataset.Bytes())

	file, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data.Bytes()), ReadFileOptions{
		OddLengthPolicy: parser.AcceptOddLength,
	}, TransferSyntaxRecoveryOptions{
		AllowDeclaredMismatch: true,
		Probe: parser.TransferSyntaxProbeOptions{
			Candidates: []transfer.Syntax{
				transfer.ImplicitVRLittleEndian,
				transfer.ExplicitVRLittleEndian,
			},
			MinimumConfidence: 0.50,
			MinimumElements:   1,
		},
	})
	if err != nil {
		t.Fatalf("ReadFileWithTransferSyntaxRecovery() error = %v; resolution=%s", err, resolution.String())
	}
	if file.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID || resolution.Source != TransferSyntaxSourceDeclared {
		t.Fatalf("resolution = %+v, want permissively accepted declared syntax", resolution)
	}
}

func TestReadFileWithTransferSyntaxRecoveryHandlesMissingFileMeta(t *testing.T) {
	t.Parallel()

	var data bytes.Buffer
	data.Write(make([]byte, 128))
	data.WriteString("DICM")
	data.Write(dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...))

	_, _, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data.Bytes()), ReadFileOptions{}, TransferSyntaxRecoveryOptions{})
	if !errors.Is(err, ErrFileMeta) {
		t.Fatalf("disabled error = %v, want the historical strict file-meta error", err)
	}

	file, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data.Bytes()), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
		AllowMissingFileMeta: true,
	})
	if err != nil {
		t.Fatalf("enabled error = %v; resolution = %s", err, resolution.String())
	}
	if file.Meta != nil || resolution.Source != TransferSyntaxSourceInferredMissingFileMeta {
		t.Fatalf("file/meta resolution = meta:%v %+v", file.Meta != nil, resolution)
	}
}

func TestTransferSyntaxRecoveryReturnsAmbiguousDiagnosticsWithoutPHI(t *testing.T) {
	t.Parallel()

	const canary = "SECRET-PATIENT-NAME"
	data := append([]byte{0x09, 0x09, 0x10, 0x10, 'L', 'O', 0x00, 0x00}, canary...)
	_, resolution, err := ReadDataSetWithTransferSyntaxRecovery(bytes.NewReader(data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
		Probe: parser.TransferSyntaxProbeOptions{
			Candidates:    []transfer.Syntax{transfer.ExplicitVRLittleEndian, transfer.ExplicitVRBigEndian},
			MaxProbeBytes: 8,
		},
	})
	if !errors.Is(err, parser.ErrTransferSyntaxProbeAmbiguous) {
		t.Fatalf("error = %v, want ErrTransferSyntaxProbeAmbiguous", err)
	}
	if bytes.Contains([]byte(resolution.String()), []byte(canary)) || bytes.Contains([]byte(err.Error()), []byte(canary)) {
		t.Fatalf("diagnostic exposed source value: resolution=%s err=%v", resolution.String(), err)
	}
	if resolution.Probe.Outcome != parser.TransferSyntaxProbeAmbiguous || resolution.Syntax.UID != "" {
		t.Fatalf("resolution = %+v, want ambiguous without syntax", resolution)
	}
}

func TestTransferSyntaxRecoveryDoesNotCopyUnknownUIDIntoDiagnostics(t *testing.T) {
	t.Parallel()

	const canary = "SECRET-PATIENT-NAME"
	data := buildRecoveryPart10(canary, transfer.ExplicitVRLittleEndian)
	file, resolution, err := ReadFileWithTransferSyntaxRecovery(bytes.NewReader(data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
		AllowUnknownTransferSyntaxUID: true,
	})
	if err != nil {
		t.Fatalf("ReadFileWithTransferSyntaxRecovery() error = %v", err)
	}
	if resolution.DeclaredUID != "" || bytes.Contains([]byte(resolution.String()), []byte(canary)) {
		t.Fatalf("resolution exposed unknown UID: %+v / %s", resolution, resolution.String())
	}
	if got, _ := file.Meta.GetString(tagTransferSyntaxUID); got != canary {
		t.Fatalf("source meta UID = %q, want original value preserved", got)
	}
}

func TestTransferSyntaxRecoveryBoundsNonSeekableReplay(t *testing.T) {
	t.Parallel()

	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	_, _, err := ReadDataSetWithTransferSyntaxRecovery(bytes.NewBuffer(data), ReadFileOptions{}, TransferSyntaxRecoveryOptions{
		MaxNonSeekableBytes: 16,
	})
	if !errors.Is(err, ErrTransferSyntaxRecoveryBufferExceeded) {
		t.Fatalf("error = %v, want ErrTransferSyntaxRecoveryBufferExceeded", err)
	}
}

func TestTransferSyntaxRecoveryHonorsMaxTotalBytesBeforeBuffering(t *testing.T) {
	t.Parallel()

	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	source := &recoveryCountingReader{reader: bytes.NewReader(data)}
	_, _, err := ReadDataSetWithTransferSyntaxRecovery(source, ReadFileOptions{MaxTotalBytes: 32}, TransferSyntaxRecoveryOptions{
		MaxNonSeekableBytes: 1 << 20,
	})
	if !errors.Is(err, ErrTransferSyntaxRecoveryBufferExceeded) || !errors.Is(err, parser.ErrMaxTotalBytesExceeded) {
		t.Fatalf("error = %v, want replay and parser total-byte limit errors", err)
	}
	if source.bytesRead > 32 {
		t.Fatalf("source read %d bytes, want <= MaxTotalBytes 32", source.bytesRead)
	}
}

func TestProbeTransferSyntaxAtHonorsMaxTotalBytesForSeekableSource(t *testing.T) {
	t.Parallel()

	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	source := &recoveryCountingReadSeeker{reader: bytes.NewReader(data)}
	_, _ = probeTransferSyntaxAt(source, 0, parser.TransferSyntaxProbeOptions{}, 32, 0)
	if source.bytesRead > 32 {
		t.Fatalf("source read %d bytes during probe, want <= MaxTotalBytes 32", source.bytesRead)
	}
}

func TestOpenFileWithTransferSyntaxRecoveryPreservesDeferredSource(t *testing.T) {
	t.Parallel()

	tag := core.NewTag(0x7777, 0x0010)
	want := bytes.Repeat([]byte{0xCD}, 256)
	elements := append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))
	data := buildRecoveryPart10Elements("1.2.840.10008.999.630", transfer.ExplicitVRLittleEndian, elements)
	path := filepath.Join(t.TempDir(), "unknown-syntax.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	file, resolution, err := OpenFileWithTransferSyntaxRecovery(path, ReadFileOptions{
		InlineValueBytesThreshold: 64,
	}, TransferSyntaxRecoveryOptions{AllowUnknownTransferSyntaxUID: true})
	if err != nil {
		t.Fatalf("OpenFileWithTransferSyntaxRecovery() error = %v; resolution = %s", err, resolution.String())
	}
	defer file.Close()
	var got bytes.Buffer
	n, err := file.Dataset.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo() = %d bytes, want %d", n, len(want))
	}
}

func TestOpenDataSetWithTransferSyntaxRecoveryPreservesDeferredSource(t *testing.T) {
	t.Parallel()

	tag := core.NewTag(0x7777, 0x0020)
	want := bytes.Repeat([]byte{0xAB}, 256)
	elements := append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))
	data := dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, elements...)
	path := filepath.Join(t.TempDir(), "raw-explicit-be.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	obj, resolution, err := OpenDataSetWithTransferSyntaxRecovery(path, ReadFileOptions{
		InlineValueBytesThreshold: 64,
	}, TransferSyntaxRecoveryOptions{})
	if err != nil {
		t.Fatalf("OpenDataSetWithTransferSyntaxRecovery() error = %v; resolution=%s", err, resolution.String())
	}
	defer obj.Close()
	var got bytes.Buffer
	n, err := obj.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo() = %d bytes, want %d", n, len(want))
	}
}

func TestReadDataSetWithTransferSyntaxRecoveryPreservesInitialOffsetForDeferredValue(t *testing.T) {
	t.Parallel()

	tag := core.NewTag(0x7777, 0x0030)
	want := bytes.Repeat([]byte{0xBC}, 256)
	elements := append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, elements...)
	prefix := bytes.Repeat([]byte{0xEF}, 41)
	source := bytes.NewReader(append(append([]byte(nil), prefix...), data...))
	if _, err := source.Seek(int64(len(prefix)), io.SeekStart); err != nil {
		t.Fatal(err)
	}

	obj, resolution, err := ReadDataSetWithTransferSyntaxRecovery(source, ReadFileOptions{
		InlineValueBytesThreshold: 64,
	}, TransferSyntaxRecoveryOptions{})
	if err != nil {
		t.Fatalf("ReadDataSetWithTransferSyntaxRecovery() error = %v; resolution=%s", err, resolution.String())
	}
	var got bytes.Buffer
	n, err := obj.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo() = %d bytes, want %d", n, len(want))
	}
}

func TestReadFileWithTransferSyntaxRecoveryPreservesInitialOffsetAndEmitsFramesOnce(t *testing.T) {
	t.Parallel()

	pixel := sequentialFrameBytes(12)
	elements := nativeFrameStreamingElements(pixel, "3")
	data := buildRecoveryPart10Elements(transfer.ImplicitVRLittleEndian.UID, transfer.ExplicitVRLittleEndian, elements)
	containerPrefix := bytes.Repeat([]byte{0xEE}, 37)
	source := bytes.NewReader(append(append([]byte(nil), containerPrefix...), data...))
	if _, err := source.Seek(int64(len(containerPrefix)), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	sink := &recordingFrameSink{}
	file, resolution, err := ReadFileWithTransferSyntaxRecovery(source, ReadFileOptions{FrameSink: sink}, TransferSyntaxRecoveryOptions{
		AllowDeclaredMismatch: true,
	})
	if err != nil {
		t.Fatalf("ReadFileWithTransferSyntaxRecovery() error = %v; resolution=%s", err, resolution.String())
	}
	if file.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("effective syntax = %q, want explicit little endian", file.TransferSyntax.UID)
	}
	if sink.closeCount != 1 || len(sink.frames) != 3 {
		t.Fatalf("FrameSink closes=%d frames=%d, want 1 close / 3 frames", sink.closeCount, len(sink.frames))
	}
}

func buildRecoveryPart10(uid string, syntax transfer.Syntax) []byte {
	return buildRecoveryPart10Elements(uid, syntax, dicomtest.MinimalDataset())
}

func buildRecoveryPart10Elements(uid string, syntax transfer.Syntax, elements []core.Element) []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(dicomtest.NewFileMetaBuilder().WithTransferSyntax(uid).Encode())
	buf.Write(dicomtest.EncodeElements(syntax, elements...))
	return buf.Bytes()
}

type recoveryCountingReader struct {
	reader    io.Reader
	bytesRead int64
}

func (r *recoveryCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

type recoveryCountingReadSeeker struct {
	reader    *bytes.Reader
	bytesRead int64
}

func (r *recoveryCountingReadSeeker) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func (r *recoveryCountingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return r.reader.Seek(offset, whence)
}
