package dicomweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestRetrieveFramesUsesFrameScopedWADORSAndPreservesPartOrder(t *testing.T) {
	const transferSyntax = "1.2.840.10008.1.2.4.50"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/dicom-web/studies/1/series/2/instances/3/frames/1,3"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, `type="image/jpeg"`) ||
			!strings.Contains(accept, "transfer-syntax="+transferSyntax) {
			t.Fatalf("Accept = %q", accept)
		}

		writer := multipart.NewWriter(w)
		w.Header().Set("Content-Type", `multipart/related; type="image/jpeg"; boundary=`+writer.Boundary())
		for _, payload := range []string{"frame-one", "frame-three"} {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Type", "image/jpeg; transfer-syntax="+transferSyntax)
			part, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte(payload)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web"},
		Options:  Options{HTTPClient: server.Client()},
	}
	parts, err := client.RetrieveFrames(context.Background(), InstanceRef{
		StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
	}, []int{1, 3}, RetrieveOptions{TransferSyntaxUIDs: []string{transferSyntax}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 ||
		parts[0].FrameNumber != 1 || string(parts[0].Data) != "frame-one" ||
		parts[1].FrameNumber != 3 || string(parts[1].Data) != "frame-three" {
		t.Fatalf("parts = %#v", parts)
	}
	for _, part := range parts {
		if part.TransferSyntaxUID != transferSyntax {
			t.Fatalf("transfer syntax = %q, want %q", part.TransferSyntaxUID, transferSyntax)
		}
	}
}

func TestRetrieveFramesStreamWithOptionsPreservesOrderAndDrainsParts(t *testing.T) {
	const boundary = "stream-frames"
	body := multipartWire(boundary, []string{
		"Content-Type: application/octet-stream\r\n\r\nframe-one",
		"Content-Type: application/octet-stream\r\n\r\nframe-three",
	}, true)
	client := frameStreamTestClient(t, multipartObjectFrameContentType(boundary), body, nil, Options{})

	var got []string
	err := client.RetrieveFramesStreamWithOptions(context.Background(), InstanceRef{
		StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
	}, []int{1, 3}, RetrieveOptions{}, func(part FramePartStream) error {
		if part.Size != -1 {
			t.Fatalf("Size=%d, want unknown -1", part.Size)
		}
		if len(got) == 0 {
			got = append(got, fmt.Sprintf("%d:unread", part.FrameNumber))
			return nil // The client drains the first part before advancing.
		}
		data, err := io.ReadAll(part.Reader)
		if err != nil {
			return err
		}
		got = append(got, fmt.Sprintf("%d:%s", part.FrameNumber, data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1:unread", "3:frame-three"}; !slices.Equal(got, want) {
		t.Fatalf("frames=%v, want %v", got, want)
	}
}

func TestRetrieveFramesStreamStopsEarlyAndClosesResponse(t *testing.T) {
	const boundary = "early-stop"
	body := multipartWire(boundary, []string{
		"Content-Type: application/octet-stream\r\n\r\nframe-one",
		"Content-Type: application/octet-stream\r\n\r\n" + strings.Repeat("x", 1<<20),
	}, true)
	responseBody := &countingFrameResponseBody{reader: strings.NewReader(body)}
	stop := errors.New("consumer stopped")
	client := frameStreamTestClientWithBody(multipartObjectFrameContentType(boundary), responseBody, Options{})
	callbacks := 0
	err := client.RetrieveFramesStreamWithOptions(context.Background(), InstanceRef{
		StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
	}, []int{1, 2}, RetrieveOptions{}, func(FramePartStream) error {
		callbacks++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("error=%v, want consumer error", err)
	}
	if callbacks != 1 {
		t.Fatalf("callbacks=%d, want 1", callbacks)
	}
	if responseBody.closed.Load() != 1 {
		t.Fatalf("response close count=%d, want 1", responseBody.closed.Load())
	}
	if read := responseBody.read.Load(); read >= int64(len(body))/2 {
		t.Fatalf("early stop read %d of %d response bytes; body was drained", read, len(body))
	}
}

func TestRetrieveFramesStreamInvokesCallbackBeforeResponseCompletes(t *testing.T) {
	const boundary = "live-stream"
	reader, writer := io.Pipe()
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, "--"+boundary+"\r\nContent-Type: application/octet-stream\r\n\r\n")
		if err == nil {
			<-releaseWriter
			_, err = io.WriteString(writer, "frame\r\n--"+boundary+"--\r\n")
		}
		_ = writer.CloseWithError(err)
		writerDone <- err
	}()

	client := frameStreamTestClientWithBody(multipartObjectFrameContentType(boundary), reader, Options{})
	callbackStarted := make(chan struct{})
	stop := errors.New("stop after headers")
	methodDone := make(chan error, 1)
	go func() {
		methodDone <- client.RetrieveFramesStreamWithOptions(context.Background(), InstanceRef{
			StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
		}, []int{1}, RetrieveOptions{}, func(FramePartStream) error {
			close(callbackStarted)
			return stop
		})
	}()

	select {
	case <-callbackStarted:
	case <-time.After(2 * time.Second):
		close(releaseWriter)
		t.Fatal("callback was not invoked while the response remained open")
	}
	select {
	case err := <-methodDone:
		if !errors.Is(err, stop) {
			t.Fatalf("error=%v, want callback sentinel", err)
		}
	case <-time.After(2 * time.Second):
		close(releaseWriter)
		t.Fatal("early stop waited for the unfinished response")
	}
	close(releaseWriter)
	select {
	case <-writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("response writer did not exit after client close")
	}
}

func TestRetrieveFramesStreamCancellationPreservesContextErrorAndClosesResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bodyReady := make(chan *blockingFrameResponseBody, 1)
	client := Client{
		Endpoint: Endpoint{BaseURL: "http://pacs.test", WADOPath: "wado"},
		Options: Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := &blockingFrameResponseBody{ctx: request.Context(), started: make(chan struct{})}
			bodyReady <- body
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": {multipartObjectFrameContentType("cancel")}},
				Body:       body,
			}, nil
		})}},
	}
	methodDone := make(chan error, 1)
	go func() {
		methodDone <- client.RetrieveFramesStreamWithOptions(ctx, InstanceRef{
			StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
		}, []int{1}, RetrieveOptions{}, func(FramePartStream) error {
			t.Error("callback invoked after cancellation before the first part")
			return nil
		})
	}()
	var body *blockingFrameResponseBody
	select {
	case body = <-bodyReady:
	case err := <-methodDone:
		t.Fatalf("frame stream returned before response read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("frame stream did not issue request")
	}
	select {
	case <-body.started:
	case <-time.After(2 * time.Second):
		t.Fatal("response parser did not begin reading")
	}
	cancel()
	select {
	case err := <-methodDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled frame stream did not return")
	}
	if body.closed.Load() != 1 {
		t.Fatalf("response close count=%d, want 1", body.closed.Load())
	}
}

func TestRetrieveFramesStreamRejectsNilHandlerWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	client := Client{
		Endpoint: Endpoint{BaseURL: "http://pacs.test", WADOPath: "wado"},
		Options: Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, errors.New("unexpected request")
		})}},
	}
	err := client.RetrieveFramesStreamWithOptions(context.Background(), InstanceRef{
		StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
	}, []int{1}, RetrieveOptions{}, nil)
	if err == nil || requests.Load() != 0 {
		t.Fatalf("error=%v requests=%d, want local validation", err, requests.Load())
	}
}

func TestRetrieveFramesStreamEnforcesPartAndTotalLimits(t *testing.T) {
	const boundary = "frame-limits"
	body := multipartWire(boundary, []string{
		"Content-Type: application/octet-stream\r\n\r\n12345",
	}, true)
	t.Run("per part", func(t *testing.T) {
		client := frameStreamTestClient(t, multipartObjectFrameContentType(boundary), body, nil, Options{
			ResponseLimits: ResponseLimits{MaxPartBytes: 4},
		})
		err := client.RetrieveFramesStreamWithOptions(context.Background(), InstanceRef{
			StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
		}, []int{1}, RetrieveOptions{}, func(FramePartStream) error { return nil })
		assertClientResponseDecodeKind(t, err, ResponseDecodePartBytes)
	})

	t.Run("total body", func(t *testing.T) {
		client := frameStreamTestClient(t, multipartObjectFrameContentType(boundary), body, nil, Options{MaxBodyBytes: 16})
		err := client.RetrieveFramesStreamWithOptions(context.Background(), InstanceRef{
			StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
		}, []int{1}, RetrieveOptions{}, func(part FramePartStream) error {
			_, err := io.Copy(io.Discard, part.Reader)
			return err
		})
		assertErrorKind(t, err, ErrorKindRequestFailure)
	})
}

func TestRetrieveFramesStreamRejectsResponsePartCountMismatch(t *testing.T) {
	const boundary = "frame-count"
	for _, test := range []struct {
		name  string
		parts []string
	}{
		{name: "too few", parts: []string{"Content-Type: application/octet-stream\r\n\r\none"}},
		{name: "too many", parts: []string{
			"Content-Type: application/octet-stream\r\n\r\none",
			"Content-Type: application/octet-stream\r\n\r\ntwo",
			"Content-Type: application/octet-stream\r\n\r\nthree",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := frameStreamTestClient(t, multipartObjectFrameContentType(boundary), multipartWire(boundary, test.parts, true), nil, Options{})
			err := client.RetrieveFramesStreamWithOptions(context.Background(), InstanceRef{
				StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
			}, []int{1, 2}, RetrieveOptions{}, func(part FramePartStream) error {
				_, err := io.Copy(io.Discard, part.Reader)
				return err
			})
			assertErrorKind(t, err, ErrorKindDecodeResponse)
		})
	}
}

func frameStreamTestClient(t *testing.T, contentType, body string, closed *atomic.Int32, options Options) Client {
	t.Helper()
	responseBody := io.NopCloser(strings.NewReader(body))
	if closed != nil {
		responseBody = &trackingReadCloser{Reader: strings.NewReader(body), closed: closed}
	}
	return frameStreamTestClientWithBody(contentType, responseBody, options)
}

func frameStreamTestClientWithBody(contentType string, responseBody io.ReadCloser, options Options) Client {
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {contentType}},
			Body:       responseBody,
		}, nil
	})}
	return Client{Endpoint: Endpoint{BaseURL: "http://pacs.test", WADOPath: "wado"}, Options: options}
}

type countingFrameResponseBody struct {
	reader io.Reader
	read   atomic.Int64
	closed atomic.Int32
}

func (body *countingFrameResponseBody) Read(buffer []byte) (int, error) {
	n, err := body.reader.Read(buffer)
	body.read.Add(int64(n))
	return n, err
}

func (body *countingFrameResponseBody) Close() error {
	body.closed.Add(1)
	return nil
}

type blockingFrameResponseBody struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
	closed  atomic.Int32
}

func (body *blockingFrameResponseBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.started) })
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *blockingFrameResponseBody) Close() error {
	body.closed.Add(1)
	return nil
}

func multipartObjectFrameContentType(boundary string) string {
	return `multipart/related; type="application/octet-stream"; boundary="` + boundary + `"`
}

func TestRetrieveFramesAcceptHeaderUsesTransferSyntaxMediaTypes(t *testing.T) {
	tests := []struct {
		name      string
		uid       string
		mediaType string
	}{
		{name: "JPEG Baseline", uid: transfer.JPEGBaseline.UID, mediaType: "image/jpeg"},
		{name: "JPEG-LS", uid: transfer.JPEGLSLossless.UID, mediaType: "image/jls"},
		{name: "JPEG 2000", uid: transfer.JPEG2000LosslessOnly.UID, mediaType: "image/jp2"},
		{name: "JPEG 2000 Part 2", uid: transfer.JPEG2000Part2.UID, mediaType: "image/jpx"},
		{name: "RLE", uid: transfer.RLELossless.UID, mediaType: "image/dicom-rle"},
		{name: "uncompressed", uid: transfer.ExplicitVRLittleEndian.UID, mediaType: "application/octet-stream"},
		{name: "unknown", uid: "1.2.3.4", mediaType: "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retrieveFramesAcceptHeader(RetrieveOptions{TransferSyntaxUIDs: []string{tt.uid}})
			want := `multipart/related; type="` + tt.mediaType + `"; transfer-syntax=` + tt.uid
			if got != want {
				t.Fatalf("retrieveFramesAcceptHeader() = %q, want %q", got, want)
			}
		})
	}
	if got := retrieveFramesAcceptHeader(RetrieveOptions{}); got != `multipart/related; type="application/octet-stream"` {
		t.Fatalf("default retrieveFramesAcceptHeader() = %q", got)
	}
}

func TestFramesURLRejectsMissingInvalidAndDuplicateFrameNumbers(t *testing.T) {
	endpoint := Endpoint{BaseURL: "https://pacs.example.test/dicom-web"}
	ref := InstanceRef{StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3"}
	for _, frames := range [][]int{nil, {0}, {1, 1}} {
		_, err := endpoint.FramesURL(ref, frames)
		assertErrorKind(t, err, ErrorKindInvalidEndpoint)
	}
}
