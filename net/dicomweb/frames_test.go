package dicomweb

import (
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

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
		w.Header().Set("Content-Type", `multipart/related; type="application/octet-stream"; boundary=`+writer.Boundary())
		for _, payload := range []string{"frame-one", "frame-three"} {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Type", "application/octet-stream; transfer-syntax="+transferSyntax)
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
