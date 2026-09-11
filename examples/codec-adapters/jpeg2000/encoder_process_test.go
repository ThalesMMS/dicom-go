//go:build jpeg2000_openjpeg || codecfull

package jpeg2000

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func encoderProcessFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	binary := filepath.Join(dir, "peer")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	code := `package main
import("bytes";"fmt";"os";"strings";"time")
func main(){
 mode:=os.Getenv("DICOMGO_TEST_OPJ_MODE")
 if len(os.Args)>1 && os.Args[1]=="-h" {version:="2.5.4";if mode=="version"{version="2.5.3"};fmt.Printf("opj_compress utility from the OpenJPEG project\nopenjp2 library v%s.\n",version);return}
 output:="";for i:=1;i<len(os.Args)-1;i++ {if os.Args[i]=="-o"{output=os.Args[i+1]}}
 switch mode {
 case "wait":os.WriteFile(os.Getenv("DICOMGO_TEST_OPJ_STARTED"),[]byte("started"),0600);for{time.Sleep(time.Hour)}
 case "invalid":os.WriteFile(output,[]byte{255,79,255,217},0600)
 case "large":os.WriteFile(output,bytes.Repeat([]byte{1},4096),0600)
 default:fmt.Fprint(os.Stderr,strings.Repeat("NATIVE_CANARY",10000));os.Exit(7)
 }
}
`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v %s", err, out)
	}
	return binary
}

func TestOpenJPEGEncoderSubprocessFailuresCancelAndCleanup(t *testing.T) {
	helper := encoderProcessFixture(t)
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, root)
	}
	t.Setenv("DICOMGO_TEST_OPJ_STARTED", marker)
	opts := OpenJPEGEncoderOptions{Executable: helper, MaxOutputBytes: 128}
	t.Setenv("DICOMGO_TEST_OPJ_MODE", "version")
	if _, err := NewOpenJPEGLosslessEncoder(context.Background(), opts); !errors.Is(err, ErrOpenJPEGEncoderUnavailable) {
		t.Fatal("wrong runtime accepted")
	}
	t.Setenv("DICOMGO_TEST_OPJ_MODE", "failure")
	e, err := NewOpenJPEGLosslessEncoder(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	frame := []byte{1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6, 0}
	before := bytes.Clone(frame)
	m := encoderMetadata()
	for _, tc := range []struct {
		mode string
		want error
	}{{"failure", pixeldata.ErrEncoderFailed}, {"invalid", pixeldata.ErrEncoderOutputInvalid}, {"large", ErrOpenJPEGEncoderLimit}} {
		t.Setenv("DICOMGO_TEST_OPJ_MODE", tc.mode)
		result, err := e.EncodeFrame(context.Background(), frame, m)
		if !errors.Is(err, tc.want) || len(result.Data) != 0 || strings.Contains(err.Error(), "CANARY") {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
			t.Fatalf("owned resources leaked: %v %v", entries, err)
		}
	}
	t.Setenv("DICOMGO_TEST_OPJ_MODE", "wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.EncodeFrame(ctx, frame, m); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	queued, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = e.EncodeFrame(queued, frame, m)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queue cancellation: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native process did not terminate")
	}
	if !bytes.Equal(before, frame) {
		t.Fatal("input changed")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("canceled process leaked files: %v %v", entries, err)
	}
	t.Setenv("DICOMGO_TEST_OPJ_MODE", "failure")
	registry := pixeldata.NewMemoryEncoderRegistry()
	if err := registry.RegisterEncoder(transfer.JPEG2000LosslessOnly.UID, e); err != nil {
		t.Fatal(err)
	}
	tc := codecfixture.JPEG2000LosslessEncoderCases()[0]
	source := tc.Object()
	nativeBefore, _ := pixeldata.ExtractNativeFrames(source)
	output, _, err := pixeldata.TranscodeDataSet(context.Background(), source, tc.Syntax, transfer.JPEG2000LosslessOnly, pixeldata.TranscodeOptions{EncoderRegistry: registry})
	if err == nil || output != nil {
		t.Fatal("native error published transcode output")
	}
	nativeAfter, _ := pixeldata.ExtractNativeFrames(source)
	for i := range nativeBefore.Data {
		if !bytes.Equal(nativeBefore.Data[i], nativeAfter.Data[i]) {
			t.Fatal("failed transcode changed source")
		}
	}
}
