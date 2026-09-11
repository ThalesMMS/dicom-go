package dicomweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestWADOResponseLimitsRejectTruncatedMultipartAsTypedDecodeError(t *testing.T) {
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=truncated`,
		"--truncated\r\nContent-Type: application/dicom\r\n\r\npayload-without-closing-boundary",
	)

	t.Run("buffered", func(t *testing.T) {
		_, err := objectPartsFromResponseWithPolicy(response, Options{}, dicomObjectResponsePolicy)
		assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMultipart)
	})

	t.Run("streaming handler reads", func(t *testing.T) {
		err := (Client{}).streamObjectParts(
			response,
			strings.NewReader(string(response.Body)),
			dicomObjectResponsePolicy,
			func(part LocatedObjectPartStream) error {
				_, err := io.Copy(io.Discard, part.Part.Reader)
				return err
			},
		)
		var clientErr *Error
		if !errors.As(err, &clientErr) || clientErr.Kind != ErrorKindDecodeResponse {
			t.Fatalf("streaming error = %#v, want ErrorKindDecodeResponse", err)
		}
		assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMultipart)
	})

	t.Run("streaming handler does not read", func(t *testing.T) {
		err := (Client{}).streamObjectParts(
			response,
			strings.NewReader(string(response.Body)),
			dicomObjectResponsePolicy,
			func(LocatedObjectPartStream) error { return nil },
		)
		var clientErr *Error
		if !errors.As(err, &clientErr) || clientErr.Kind != ErrorKindDecodeResponse {
			t.Fatalf("streaming drain error = %#v, want ErrorKindDecodeResponse", err)
		}
		assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMultipart)
	})
}

func TestWADOResponseHeaderLimitCountsRawHeaderBytes(t *testing.T) {
	const limit = 64
	body := "--header-limit\r\n" +
		"Content-Type: application/dicom\r\n" +
		"X-Padding:" + strings.Repeat(" ", limit*4) + "\r\n" +
		"\r\npayload\r\n--header-limit--\r\n"
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=header-limit`,
		body,
	)

	_, err := objectPartsFromResponseWithPolicy(response, Options{
		ResponseLimits: ResponseLimits{MaxPartHeaderBytes: limit},
	}, dicomObjectResponsePolicy)
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeHeaderBytes)
}

func TestMultipartHeaderLimitStopsNextRawPartBeforeLargeHeaderIsParsed(t *testing.T) {
	const (
		boundary = "bounded-header"
		limit    = 64
	)
	body := "--" + boundary + "\r\n" +
		"Content-Type: application/dicom\r\n" +
		"X-Adversarial: " + strings.Repeat("x", 1<<20) + "\r\n" +
		"\r\npayload\r\n--" + boundary + "--\r\n"

	wire := newMultipartHeaderLimitReader(strings.NewReader(body), boundary, limit)
	part, err := multipart.NewReader(wire, boundary).NextRawPart()
	if part != nil {
		_ = part.Close()
		t.Fatal("NextRawPart returned a parsed MIME part after the raw header limit")
	}
	decodeErr := assertAdversarialResponseDecodeKind(t, err, ResponseDecodeHeaderBytes)
	if decodeErr.Limit != limit || decodeErr.Value != limit+1 {
		t.Fatalf("header limit error = %#v, want limit=%d value=%d", decodeErr, limit, limit+1)
	}
}

func TestWADOResponseHeaderLimitAppliesToStreamingBeforeHandler(t *testing.T) {
	const limit = 64
	body := "--stream-header\r\n" +
		"Content-Type: application/dicom\r\n" +
		"X-Adversarial: " + strings.Repeat("x", 1<<20) + "\r\n" +
		"\r\npayload\r\n--stream-header--\r\n"
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=stream-header`,
		body,
	)
	called := false
	err := (Client{Options: Options{ResponseLimits: ResponseLimits{MaxPartHeaderBytes: limit}}}).streamObjectParts(
		response,
		strings.NewReader(body),
		dicomObjectResponsePolicy,
		func(LocatedObjectPartStream) error {
			called = true
			return nil
		},
	)
	if called {
		t.Fatal("stream handler was called for a part with oversized raw headers")
	}
	var clientErr *Error
	if !errors.As(err, &clientErr) || clientErr.Kind != ErrorKindDecodeResponse {
		t.Fatalf("streaming error = %#v, want ErrorKindDecodeResponse", err)
	}
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeHeaderBytes)
}

func TestDICOMXMLResponseLimitsApplyToRawHeadersAndTransferEncoding(t *testing.T) {
	const (
		limit = 96
		xml   = `<?xml version="1.0" encoding="UTF-8"?><NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve"></NativeDicomModel>`
	)
	outerContentType := `multipart/related; type="application/dicom+xml"; boundary=metadata-limits`
	client := Client{Options: Options{
		MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML},
		ResponseLimits:     ResponseLimits{MaxPartHeaderBytes: limit},
	}}

	t.Run("raw header bytes", func(t *testing.T) {
		body := "--metadata-limits\r\n" +
			"Content-Type: application/dicom+xml\r\n" +
			"X-Padding:" + strings.Repeat(" ", limit*4) + "\r\n" +
			"\r\n" + xml + "\r\n--metadata-limits--\r\n"
		_, err := client.datasetsFromMetadataResponse(context.Background(), adversarialWADOResponse(outerContentType, body), false)
		assertAdversarialResponseDecodeKind(t, err, ResponseDecodeHeaderBytes)
	})

	t.Run("quoted printable", func(t *testing.T) {
		body := "--metadata-limits\r\n" +
			"Content-Type: application/dicom+xml\r\n" +
			"Content-Transfer-Encoding: quoted-printable\r\n" +
			"\r\n" + xml + "\r\n--metadata-limits--\r\n"
		_, err := client.datasetsFromMetadataResponse(context.Background(), adversarialWADOResponse(outerContentType, body), false)
		assertAdversarialResponseDecodeKind(t, err, ResponseDecodeTransferEncoding)
	})
}

func TestDICOMXMLResponseRejectsTruncatedMultipartAsTypedError(t *testing.T) {
	const xml = `<?xml version="1.0" encoding="UTF-8"?><NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve"></NativeDicomModel>`
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom+xml"; boundary=metadata-truncated`,
		"--metadata-truncated\r\nContent-Type: application/dicom+xml\r\n\r\n"+xml,
	)
	client := Client{Options: Options{MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML}}}

	_, err := client.datasetsFromMetadataResponse(context.Background(), response, false)
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMultipart)
}

func TestDICOMXMLResponseValidatesMultipartTypeBeforeEmptyBody(t *testing.T) {
	client := Client{Options: Options{MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML}}}
	response := adversarialWADOResponse(`multipart/related; type="application/dicom+xml"`, "")

	_, err := client.datasetsFromMetadataResponse(context.Background(), response, false)
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMissingBoundary)
}

func TestWADOResponseRejectsAmbiguousDuplicateContentType(t *testing.T) {
	body := "--duplicate-type\r\n" +
		"Content-Type: application/dicom\r\n" +
		"Content-Type: multipart/related; boundary=inner\r\n" +
		"\r\npayload\r\n--duplicate-type--\r\n"
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=duplicate-type`,
		body,
	)

	_, err := objectPartsFromResponseWithPolicy(response, Options{}, dicomObjectResponsePolicy)
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMediaType)
}

func TestWADOResponseRejectsAmbiguousDuplicateOuterContentType(t *testing.T) {
	response := adversarialWADOResponse("application/dicom", "payload")
	response.Header["Content-Type"] = []string{"application/dicom", "text/plain"}

	_, err := objectPartsFromResponseWithPolicy(response, Options{}, dicomObjectResponsePolicy)
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMediaType)
}

func TestWADOResponsePartLimitCountsEmptyParts(t *testing.T) {
	var body strings.Builder
	for range 3 {
		body.WriteString("--empty-parts\r\nContent-Type: application/dicom\r\n\r\n\r\n")
	}
	body.WriteString("--empty-parts--\r\n")
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=empty-parts`,
		body.String(),
	)

	_, err := objectPartsFromResponseWithPolicy(response, Options{
		ResponseLimits: ResponseLimits{MaxParts: 2},
	}, dicomObjectResponsePolicy)
	decodeErr := assertAdversarialResponseDecodeKind(t, err, ResponseDecodePartLimit)
	if decodeErr.Limit != 2 || decodeErr.Value != 3 {
		t.Fatalf("part limit error = %#v, want limit=2 value=3", decodeErr)
	}
}

func TestWADOResponseRejectsMultipartWithNoParts(t *testing.T) {
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=no-parts`,
		"--no-parts--\r\n",
	)

	_, err := objectPartsFromResponseWithPolicy(response, Options{}, dicomObjectResponsePolicy)
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMultipart)
}

func TestWADOResponseRejectsNestedMultipartBeforePayload(t *testing.T) {
	body := "--outer\r\n" +
		"Content-Type: multipart/related; boundary=inner\r\n" +
		"\r\n--inner--\r\n\r\n--outer--\r\n"
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=outer`,
		body,
	)

	_, err := objectPartsFromResponseWithPolicy(response, Options{}, dicomObjectResponsePolicy)
	decodeErr := assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMultipartDepth)
	if decodeErr.Value != 2 {
		t.Fatalf("multipart depth error = %#v, want value=2", decodeErr)
	}
}

func TestWADOResponseMalformedStructureDoesNotExposeInput(t *testing.T) {
	const secret = "PATIENT_SECRET_MARKER"
	response := adversarialWADOResponse(
		`multipart/related; type="application/dicom"; boundary=malformed`,
		"--malformed\r\nContent-Type: application/dicom\r\n"+secret+"\r\n\r\npayload\r\n--malformed--\r\n",
	)

	_, err := objectPartsFromResponseWithPolicy(response, Options{}, dicomObjectResponsePolicy)
	assertAdversarialResponseDecodeKind(t, err, ResponseDecodeMalformedMultipart)
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("decode error exposed raw multipart input: %q", rendered)
		}
	}
}

func adversarialWADOResponse(contentType, body string) Response {
	return Response{
		URL:        "https://pacs.test/dicomweb/studies/1/series/2/instances/3",
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {contentType}},
		Body:       []byte(body),
	}
}

func assertAdversarialResponseDecodeKind(t *testing.T, err error, want ResponseDecodeErrorKind) *ResponseDecodeError {
	t.Helper()
	if err == nil {
		t.Fatalf("response decode error = nil, want kind %q", want)
	}
	var decodeErr *ResponseDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("response decode error = %#v, want *ResponseDecodeError", err)
	}
	if decodeErr.Kind != want {
		t.Fatalf("response decode kind = %q, want %q", decodeErr.Kind, want)
	}
	return decodeErr
}
