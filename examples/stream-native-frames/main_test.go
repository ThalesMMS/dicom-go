package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
)

func TestStreamFramesEarlyExitAndMalformedHeader(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		data, err := codecfixture.NativeMultiFrame().Part10Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if malformed {
			data = []byte("bad header")
		}
		path := filepath.Join(t.TempDir(), "synthetic.dcm")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- streamFrames(ctx, path, 1, &out) }()
		select {
		case err := <-done:
			cancel()
			if (err != nil) != malformed {
				t.Fatalf("malformed=%v error=%v", malformed, err)
			}
			if !malformed && strings.Count(out.String(), "frame=") != 1 {
				t.Fatal("consumer limit ignored")
			}
		case <-ctx.Done():
			cancel()
			t.Fatal("example did not stop")
		}
	}
}
