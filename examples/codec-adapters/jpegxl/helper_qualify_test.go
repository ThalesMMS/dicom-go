package jpegxladapter

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestProductionHelperDecodesCodecfullGray16WhenLibjxlAvailable(t *testing.T) {
	session := openCompiledHelper(t)
	fragment, err := os.ReadFile(codecfullJXL("gray16-lossless.jxl"))
	if err != nil {
		t.Skip(err.Error())
	}
	meta := pixeldata.Metadata{
		Rows: 64, Columns: 64, SamplesPerPixel: 1,
		BitsAllocated: 16, BitsStored: 16, HighBit: 15, NumberOfFrames: 1,
		PhotometricInterpretation: "MONOCHROME2",
	}
	pixels, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), fragment, meta)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(pixels), 64*64*2; got != want {
		t.Fatalf("helper pixels = %d, want %d", got, want)
	}
}

func TestProductionHelperRejectsInvalidCodestreamAsMalformed(t *testing.T) {
	session := openCompiledHelper(t)
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("not-jxl"), gray8Meta())
	if !errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("error = %v, want ErrMalformedCodestream", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after malformed helper decode", len(frame))
	}
}

func TestProductionHelperDrainsRejectedOpcodePayload(t *testing.T) {
	path := compileHelperBinary(t)
	conn, err := startHelperProcess(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Closer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writeHelperRequest(conn.Writer, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
		t.Fatal(err)
	}
	hello, err := waitHelperReply(ctx, conn.Reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Status != helperStatusOK {
		t.Fatalf("hello status = %d, want OK", hello.Status)
	}

	if err := writeHelperRequest(conn.Writer, helperRequest{
		Opcode:    99,
		RequestID: 2,
		Payload:   []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := waitHelperReply(ctx, conn.Reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Status != helperStatusProtocol {
		t.Fatalf("rejected opcode status = %d, want protocol", reply.Status)
	}

	if err := writeHelperRequest(conn.Writer, helperRequest{Opcode: helperOpcodeHello, RequestID: 3}); err != nil {
		t.Fatal(err)
	}
	hello, err = waitHelperReply(ctx, conn.Reader, 0)
	if err != nil {
		t.Fatalf("follow-up hello after rejected opcode: %v", err)
	}
	if hello.Status != helperStatusOK {
		t.Fatalf("follow-up hello status = %d, want OK", hello.Status)
	}
}

func TestProductionHelperDrainsHelloPayload(t *testing.T) {
	path := compileHelperBinary(t)
	conn, err := startHelperProcess(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Closer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writeHelperRequest(conn.Writer, helperRequest{
		Opcode:    helperOpcodeHello,
		RequestID: 1,
		Payload:   []byte{9, 8, 7, 6, 5, 4, 3, 2},
	}); err != nil {
		t.Fatal(err)
	}
	hello, err := waitHelperReply(ctx, conn.Reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Status != helperStatusOK {
		t.Fatalf("hello with payload status = %d, want OK", hello.Status)
	}

	if err := writeHelperRequest(conn.Writer, helperRequest{Opcode: helperOpcodeHello, RequestID: 2}); err != nil {
		t.Fatal(err)
	}
	hello, err = waitHelperReply(ctx, conn.Reader, 0)
	if err != nil {
		t.Fatalf("follow-up hello after payload drain: %v", err)
	}
	if hello.Status != helperStatusOK {
		t.Fatalf("follow-up hello status = %d, want OK", hello.Status)
	}
}

func TestProductionHelperRejectsEmptyDecodePayloadAsMalformed(t *testing.T) {
	path := compileHelperBinary(t)
	conn, err := startHelperProcess(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Closer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writeHelperRequest(conn.Writer, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := waitHelperReply(ctx, conn.Reader, 0); err != nil {
		t.Fatal(err)
	}

	if err := writeHelperRequest(conn.Writer, helperRequest{
		Opcode:        helperOpcodeDecode,
		RequestID:     2,
		Rows:          1,
		Columns:       2,
		Samples:       1,
		BitsAllocated: 8,
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := waitHelperReply(ctx, conn.Reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Status != helperStatusMalformed {
		t.Fatalf("empty decode status = %d, want malformed", reply.Status)
	}

	if err := writeHelperRequest(conn.Writer, helperRequest{Opcode: helperOpcodeHello, RequestID: 3}); err != nil {
		t.Fatal(err)
	}
	hello, err := waitHelperReply(ctx, conn.Reader, 0)
	if err != nil {
		t.Fatalf("follow-up hello after empty decode: %v", err)
	}
	if hello.Status != helperStatusOK {
		t.Fatalf("follow-up hello status = %d, want OK", hello.Status)
	}
}

func TestHorosPACSIsOptionalAndRecorded(t *testing.T) {
	if os.Getenv("DICOMGO_INTEGRATION") != "1" {
		t.Skip("set DICOMGO_INTEGRATION=1 to probe optional Horos PACS reachability; default tests stay offline")
	}
	host := os.Getenv("HOROS_HOST")
	if host == "" {
		host = "192.168.100.62"
	}
	port := os.Getenv("HOROS_PORT")
	if port == "" {
		port = "4007"
	}
	addr := net.JoinHostPort(host, port)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Logf("pacs-horos: %s unreachable; helper qualification uses local non-PHI fixtures only", addr)
		return
	}
	_ = conn.Close()
	t.Log("pacs-horos: reachable but unused; JPEG XL helper qualification does not retrieve from HOROS")
}

func openCompiledHelper(t *testing.T) *Session {
	t.Helper()
	session, err := OpenSession(context.Background(), SessionConfig{
		Executable: compileHelperBinary(t),
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func compileHelperBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("pkg-config"); err != nil {
		t.Skip("pkg-config not available")
	}
	if err := exec.Command("pkg-config", "--exists", "libjxl").Run(); err != nil {
		t.Skip("libjxl not available")
	}
	out := filepath.Join(t.TempDir(), HelperExecutableName)
	ctx, cancel := context.WithTimeout(context.Background(), helperCompileTimeout)
	defer cancel()
	if err := compileJPEGXLHelper(ctx, filepath.Join("helperc", "jpegxl_helper.c"), out); err != nil {
		t.Fatal(err)
	}
	return out
}

func codecfullJXL(name string) string {
	if env := os.Getenv("CODEC_COST_CORPUS"); env != "" {
		return filepath.Join(env, "jxl", name)
	}
	return filepath.Join("..", "..", "..", "pixeldata", "codecfixture", "testdata", "codecfull", "jxl", name)
}
