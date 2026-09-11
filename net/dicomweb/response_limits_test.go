package dicomweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

var responseLimitTestRef = InstanceRef{
	StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3",
}

func TestRetrieveInstanceRejectsMalformedResponseStructure(t *testing.T) {
	const boundary = "dicom-boundary"
	tests := []struct {
		name        string
		contentType string
		body        string
		want        ResponseDecodeErrorKind
	}{
		{name: "missing Content-Type", body: "dicom", want: ResponseDecodeMalformedMediaType},
		{name: "malformed Content-Type", contentType: "not a media type; marker=secret-response-value", body: "dicom", want: ResponseDecodeMalformedMediaType},
		{name: "wrong single media", contentType: "text/plain", body: "dicom", want: ResponseDecodeUnexpectedMedia},
		{name: "missing boundary", contentType: `multipart/related; type="application/dicom"`, want: ResponseDecodeMissingBoundary},
		{name: "invalid boundary", contentType: `multipart/related; type="application/dicom"; boundary="trailing "`, want: ResponseDecodeMalformedMultipart},
		{name: "missing part media", contentType: multipartObjectContentType(boundary), body: "--" + boundary + "\r\n\r\ndata\r\n--" + boundary + "--\r\n", want: ResponseDecodeMalformedMediaType},
		{name: "wrong part media", contentType: multipartObjectContentType(boundary), body: multipartWire(boundary, []string{"Content-Type: text/plain\r\n\r\ndata"}, true), want: ResponseDecodeUnexpectedMedia},
		{name: "nested multipart", contentType: multipartObjectContentType(boundary), body: multipartWire(boundary, []string{`Content-Type: multipart/mixed; boundary="nested"` + "\r\n\r\n--nested--\r\n"}, true), want: ResponseDecodeMultipartDepth},
		{name: "unsupported transfer encoding", contentType: multipartObjectContentType(boundary), body: multipartWire(boundary, []string{"Content-Type: application/dicom\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ndata"}, true), want: ResponseDecodeTransferEncoding},
		{name: "truncated final boundary", contentType: multipartObjectContentType(boundary), body: multipartWire(boundary, []string{"Content-Type: application/dicom\r\n\r\ndata"}, false), want: ResponseDecodeMalformedMultipart},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := responseLimitClient(t, test.contentType, test.body, Options{})
			_, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
			assertClientResponseDecodeKind(t, err, test.want)
			if strings.Contains(err.Error(), "secret-response-value") {
				t.Fatalf("error leaked attacker-controlled Content-Type: %v", err)
			}
		})
	}
}

func TestRetrieveInstanceEnforcesMultipartCountAndByteBoundaries(t *testing.T) {
	const boundary = "limits"
	contentType := multipartObjectContentType(boundary)

	t.Run("part count exact", func(t *testing.T) {
		body := multipartWire(boundary, []string{
			"Content-Type: application/dicom\r\n\r\n",
			"Content-Type: application/dicom\r\n\r\n",
		}, true)
		client := responseLimitClient(t, contentType, body, Options{ResponseLimits: ResponseLimits{MaxParts: 2}})
		parts, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
		if err != nil || len(parts) != 2 {
			t.Fatalf("parts=%d error=%v, want two parts", len(parts), err)
		}
	})

	t.Run("part count plus one", func(t *testing.T) {
		body := multipartWire(boundary, []string{
			"Content-Type: application/dicom\r\n\r\n",
			"Content-Type: application/dicom\r\n\r\n",
		}, true)
		client := responseLimitClient(t, contentType, body, Options{ResponseLimits: ResponseLimits{MaxParts: 1}})
		_, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
		assertClientResponseDecodeKind(t, err, ResponseDecodePartLimit)
	})

	t.Run("part bytes exact and plus one", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			payload string
			wantErr bool
		}{
			{name: "exact", payload: "1234"},
			{name: "plus one", payload: "12345", wantErr: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				body := multipartWire(boundary, []string{"Content-Type: application/dicom\r\n\r\n" + test.payload}, true)
				client := responseLimitClient(t, contentType, body, Options{ResponseLimits: ResponseLimits{MaxPartBytes: 4}})
				parts, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
				if test.wantErr {
					assertClientResponseDecodeKind(t, err, ResponseDecodePartBytes)
					return
				}
				if err != nil || len(parts) != 1 || string(parts[0].Data) != test.payload {
					t.Fatalf("parts=%#v error=%v", parts, err)
				}
			})
		}
	})

	t.Run("single part bytes plus one", func(t *testing.T) {
		client := responseLimitClient(t, "application/dicom", "12345", Options{ResponseLimits: ResponseLimits{MaxPartBytes: 4}})
		_, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
		assertClientResponseDecodeKind(t, err, ResponseDecodePartBytes)
	})
}

func TestRetrieveInstanceEnforcesRawMultipartHeaderLimit(t *testing.T) {
	const boundary = "header-limit"
	header := "Content-Type: application/dicom\r\nX-Padding: abcdef\r\n\r\n"
	body := multipartWire(boundary, []string{header + "data"}, true)
	for _, test := range []struct {
		name    string
		limit   int
		wantErr bool
	}{
		{name: "exact", limit: len(header)},
		{name: "plus one", limit: len(header) - 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := responseLimitClient(t, multipartObjectContentType(boundary), body, Options{
				ResponseLimits: ResponseLimits{MaxPartHeaderBytes: test.limit},
			})
			_, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
			if test.wantErr {
				assertClientResponseDecodeKind(t, err, ResponseDecodeHeaderBytes)
				return
			}
			if err != nil {
				t.Fatalf("exact header limit rejected: %v", err)
			}
		})
	}
}

func TestRetrieveInstanceStreamDrainsAndEnforcesUnreadPartLimit(t *testing.T) {
	const boundary = "stream-limit"
	body := multipartWire(boundary, []string{"Content-Type: application/dicom\r\n\r\n12345"}, true)
	client := responseLimitClient(t, multipartObjectContentType(boundary), body, Options{
		ResponseLimits: ResponseLimits{MaxPartBytes: 4},
	})
	called := 0
	err := client.RetrieveInstanceStreamWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{}, func(part ObjectPartStream) error {
		called++
		return nil // Deliberately leave Reader unread; the client must drain and validate it.
	})
	if called != 1 {
		t.Fatalf("callback count=%d, want 1", called)
	}
	assertClientResponseDecodeKind(t, err, ResponseDecodePartBytes)
}

func TestRetrieveStudyStreamRequiresMultipartResponse(t *testing.T) {
	client := responseLimitClient(t, "application/dicom", "data", Options{})
	err := client.RetrieveStudyStreamWithOptions(context.Background(), "1", RetrieveOptions{}, func(LocatedObjectPartStream) error {
		return nil
	})
	assertClientResponseDecodeKind(t, err, ResponseDecodeUnexpectedMedia)
}

func TestResponseLimitConfigurationFailsBeforeRequest(t *testing.T) {
	for _, limits := range []ResponseLimits{
		{MaxParts: -1},
		{MaxPartBytes: -1},
		{MaxPartHeaderBytes: -1},
		{MaxMultipartDepth: -1},
		{MaxMultipartDepth: 2},
		{AllowedMediaTypes: []string{"not a media type"}},
	} {
		var requests atomic.Int32
		client := verifyClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			requests.Add(1)
		}), Endpoint{WADOPath: "wado"}, Options{ResponseLimits: limits})
		_, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
		assertErrorKind(t, err, ErrorKindRequestFailure)
		if requests.Load() != 0 {
			t.Fatalf("invalid limits issued %d request(s)", requests.Load())
		}
	}
}

func TestResponseAllowedMediaTypesOnlyNarrowOperationPolicy(t *testing.T) {
	t.Run("allows configured DICOM", func(t *testing.T) {
		client := responseLimitClient(t, "application/dicom", "data", Options{
			ResponseLimits: ResponseLimits{AllowedMediaTypes: []string{"application/dicom"}},
		})
		if _, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cannot enable text", func(t *testing.T) {
		client := responseLimitClient(t, "text/plain", "data", Options{
			ResponseLimits: ResponseLimits{AllowedMediaTypes: []string{"text/plain"}},
		})
		_, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
		assertClientResponseDecodeKind(t, err, ResponseDecodeUnexpectedMedia)
	})
}

func TestRetrieveFramesRejectsMediaTransferSyntaxMismatch(t *testing.T) {
	const boundary = "frame-media"
	body := multipartWire(boundary, []string{
		"Content-Type: application/octet-stream; transfer-syntax=1.2.840.10008.1.2.4.50\r\n\r\ndata",
	}, true)
	client := responseLimitClient(t,
		`multipart/related; type="application/octet-stream"; boundary="`+boundary+`"`, body, Options{})
	_, err := client.RetrieveFrames(context.Background(), responseLimitTestRef, []int{1}, RetrieveOptions{})
	assertClientResponseDecodeKind(t, err, ResponseDecodeUnexpectedMedia)
}

func TestZeroPartLimitInheritsRaisedBodyLimit(t *testing.T) {
	payload := strings.Repeat("d", 9)
	client := responseLimitClient(t, "application/dicom", payload, Options{MaxBodyBytes: 10})
	parts, err := client.RetrieveInstanceWithOptions(context.Background(), responseLimitTestRef, RetrieveOptions{})
	if err != nil || len(parts) != 1 || string(parts[0].Data) != payload {
		t.Fatalf("parts=%#v error=%v", parts, err)
	}
}

func responseLimitClient(t *testing.T, contentType, body string, options Options) Client {
	t.Helper()
	return verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		} else {
			// Keep net/http from sniffing a value so the fixture represents a
			// genuinely absent response media type.
			w.Header()["Content-Type"] = []string{}
		}
		_, _ = io.WriteString(w, body)
	}), Endpoint{WADOPath: "wado"}, options)
}

func multipartObjectContentType(boundary string) string {
	return fmt.Sprintf(`multipart/related; type="application/dicom"; boundary="%s"`, boundary)
}

func multipartWire(boundary string, parts []string, closeBoundary bool) string {
	var body strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&body, "--%s\r\n%s\r\n", boundary, part)
	}
	if closeBoundary {
		fmt.Fprintf(&body, "--%s--\r\n", boundary)
	}
	return body.String()
}

func assertClientResponseDecodeKind(t *testing.T, err error, want ResponseDecodeErrorKind) {
	t.Helper()
	assertErrorKind(t, err, ErrorKindDecodeResponse)
	var decodeErr *ResponseDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("error chain has no *ResponseDecodeError: %v", err)
	}
	if decodeErr.Kind != want {
		t.Fatalf("decode error kind=%q, want %q (error: %v)", decodeErr.Kind, want, err)
	}
}
