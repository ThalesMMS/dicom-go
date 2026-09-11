package dicomweb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"runtime"
	"testing"

	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	benchmarkFrameCount = 32
	benchmarkFrameBytes = 1 << 20
)

type benchmarkRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip benchmarkRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

// BenchmarkRetrieveFramesBufferedVsStreaming quantifies allocation
// amplification through the public APIs. A deterministic RoundTripper returns
// the same immutable 32 MiB multipart wire fixture through a fresh reader on
// every request, without sockets or filesystem access. The fixture is retained
// outside each sub-benchmark timer. The buffered consumer keeps every frame in
// []FramePart until the call returns; the streaming consumer does not retain
// frame payloads.
//
// Use a fixed iteration count for comparable memory results:
//
//	go test ./net/dicomweb -run '^$' \
//	  -bench '^BenchmarkRetrieveFramesBufferedVsStreaming$' \
//	  -benchmem -benchtime=1x -count=5
//
// B/op is total allocation, not peak resident memory. For peak RSS, first build
// one test binary, then run fixture_baseline, buffered, and streaming in fresh
// processes with identical GOGC and -test.benchtime=1x. On Linux:
//
//	go test -c -o /tmp/dicomweb-bench ./net/dicomweb
//	/usr/bin/time -v env GOGC=off /tmp/dicomweb-bench \
//	  -test.run='^$' \
//	  -test.bench='^BenchmarkRetrieveFramesBufferedVsStreaming/buffered$' \
//	  -test.benchtime=1x -test.count=1
//
// Repeat the command with fixture_baseline and streaming in at least five fresh
// processes and compare medians. Maximum resident set size minus the baseline
// estimates incremental operation overhead. Do not compare cases in one
// process: Go retains heap arenas and would bias the case run second.
func BenchmarkRetrieveFramesBufferedVsStreaming(b *testing.B) {
	wire, contentType, frames := benchmarkFrameMultipart(b, benchmarkFrameCount, benchmarkFrameBytes)
	payloadBytes := int64(len(frames) * benchmarkFrameBytes)
	options := Options{
		MaxBodyBytes: int64(len(wire) + 1),
		ResponseLimits: ResponseLimits{
			MaxParts:     len(frames),
			MaxPartBytes: benchmarkFrameBytes,
		},
	}
	options.HTTPClient = &http.Client{Transport: benchmarkRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Type": {contentType}},
			Body:          io.NopCloser(bytes.NewReader(wire)),
			ContentLength: int64(len(wire)),
			Request:       request,
		}, nil
	})}
	client := Client{
		Endpoint: Endpoint{BaseURL: "https://pacs.test/dicomweb", WADOPath: "wado"},
		Options:  options,
	}
	ref := InstanceRef{StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3"}

	b.Run("fixture_baseline", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			runtime.KeepAlive(wire)
		}
	})

	b.Run("buffered", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(payloadBytes)
		b.ResetTimer()
		for range b.N {
			result, err := client.RetrieveFrames(context.Background(), ref, frames, RetrieveOptions{})
			if err != nil {
				b.Fatal(err)
			}
			var consumed int64
			for _, frame := range result {
				consumed += int64(len(frame.Data))
			}
			if consumed != payloadBytes {
				b.Fatalf("consumed %d bytes, want %d", consumed, payloadBytes)
			}
			runtime.KeepAlive(result)
		}
	})

	b.Run("streaming", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(payloadBytes)
		b.ResetTimer()
		for range b.N {
			index := 0
			var consumed int64
			err := client.RetrieveFramesStreamWithOptions(
				context.Background(), ref, frames, RetrieveOptions{},
				func(frame FramePartStream) error {
					n, err := io.Copy(io.Discard, frame.Reader)
					consumed += n
					index++
					return err
				},
			)
			if err != nil {
				b.Fatal(err)
			}
			if index != len(frames) || consumed != payloadBytes {
				b.Fatalf("received %d frames and %d bytes, want %d frames and %d bytes", index, consumed, len(frames), payloadBytes)
			}
		}
	})
}

func benchmarkFrameMultipart(tb testing.TB, frameCount, frameBytes int) ([]byte, string, []int) {
	tb.Helper()
	if frameCount <= 0 || frameBytes <= 0 {
		tb.Fatal("benchmark frame dimensions must be positive")
	}
	payload := deterministicFramePayload(frameBytes)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary("dicomweb-frame-memory-benchmark"); err != nil {
		tb.Fatal(err)
	}
	frames := make([]int, frameCount)
	for index := range frameCount {
		frames[index] = index + 1
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", "application/octet-stream; transfer-syntax="+transfer.ExplicitVRLittleEndian.UID)
		part, err := writer.CreatePart(header)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := part.Write(payload); err != nil {
			tb.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	contentType := fmt.Sprintf(
		`multipart/related; type="application/octet-stream"; boundary="%s"`,
		writer.Boundary(),
	)
	return body.Bytes(), contentType, frames
}

func deterministicFramePayload(size int) []byte {
	payload := make([]byte, size)
	state := uint32(0x865c0de)
	for index := range payload {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		payload[index] = byte(state)
	}
	return payload
}
