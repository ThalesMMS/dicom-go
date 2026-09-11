package dicomweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

func TestOpenStagedStoreMIMEPartRejectsSameSizeTampering(t *testing.T) {
	path := t.TempDir() + "/bulk.bin"
	original := []byte{1, 2, 3, 4}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	part := stagedStoreMIMEPart{path: path, size: int64(len(original)), digest: sha256.Sum256(original), info: info}
	if err := os.WriteFile(path, []byte{4, 3, 2, 1}, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := openStagedStoreMIMEPart(context.Background(), part)
	if reader != nil {
		_ = reader.Close()
		t.Fatal("tampered spool returned a reader")
	}
	if !errors.Is(err, ErrBackend) {
		t.Fatalf("open tampered spool error = %v, want ErrBackend", err)
	}
}

type stowMetadataTestPart struct {
	contentType      string
	contentLocation  string
	contentID        string
	contentEncoding  string
	transferEncoding string
	body             []byte
}

func TestServerSTOWMetadataJSONAndXMLResolveRequestLocalBulk(t *testing.T) {
	tests := []struct {
		name     string
		rootType string
		metadata []byte
	}{
		{"JSON", "application/dicom+json", stowMetadataJSON("bulk-1", false)},
		{"XML", "application/dicom+xml", stowMetadataXML("bulk-1", false)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := completeServerTestBackend()
			var calls atomic.Int32
			backend.store = func(_ context.Context, request StoreRequest) StoreOutcome {
				calls.Add(1)
				data, err := io.ReadAll(request.Reader)
				if err != nil {
					return StoreOutcome{Status: StoreStatusFailed, Err: err}
				}
				if len(data) < 132 || string(data[128:132]) != "DICM" || !bytes.Contains(data, []byte{1, 2, 3, 4}) {
					return StoreOutcome{Status: StoreStatusFailed, Err: fmt.Errorf("invalid reconstructed Part 10")}
				}
				if request.ContentType != "application/dicom" || request.TransferSyntaxUID != explicitVRLittleEndianUID {
					return StoreOutcome{Status: StoreStatusFailed, Err: fmt.Errorf("unexpected representation")}
				}
				return StoreOutcome{Status: StoreStatusStored}
			}
			server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			body, contentType := stowMetadataMultipart(t, test.rootType,
				stowMetadataTestPart{contentType: test.rootType + `; charset=utf-8`, contentID: "<root@test>", body: test.metadata},
				stowMetadataTestPart{contentType: "application/octet-stream; transfer-syntax=" + explicitVRLittleEndianUID, contentLocation: "bulk-1", body: []byte{1, 2, 3, 4}},
			)
			contentType += `; start="<root@test>"; charset=utf-8; transfer-syntax=` + explicitVRLittleEndianUID
			request := httptest.NewRequest(http.MethodPost, "/studies/"+dicomtest.TestStudyInstanceUID, body)
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Accept", "application/dicom+json")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || calls.Load() != 1 {
				t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls.Load(), recorder.Body.String())
			}
		})
	}
}

func TestServerSTOWMetadataRepeatedReferenceAndPartialBackendResult(t *testing.T) {
	first := string(stowMetadataJSON("bulk-1", false))
	first = first[1 : len(first)-1]
	second := strings.Replace(first, dicomtest.TestSOPInstanceUID, dicomtest.TestSOPInstanceUID+".2", 1)
	metadata := []byte("[" + first + "," + second + "]")
	backend := completeServerTestBackend()
	var calls atomic.Int32
	backend.store = func(_ context.Context, request StoreRequest) StoreOutcome {
		if _, err := io.Copy(io.Discard, request.Reader); err != nil {
			return StoreOutcome{Status: StoreStatusFailed, Err: err}
		}
		if calls.Add(1) == 1 {
			return StoreOutcome{Status: StoreStatusStored}
		}
		return StoreOutcome{Status: StoreStatusConflict, FailureReason: 0x0110}
	}
	server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	body, contentType := stowMetadataMultipart(t, "application/dicom+json",
		stowMetadataTestPart{contentType: "application/dicom+json", body: metadata},
		stowMetadataTestPart{contentType: "application/octet-stream", contentLocation: "bulk-1", body: []byte{1, 2, 3, 4}},
	)
	request := httptest.NewRequest(http.MethodPost, "/studies", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/dicom+json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	result, parseErr := StoreResultFromDICOMJSON(recorder.Body.Bytes())
	if recorder.Code != http.StatusAccepted || calls.Load() != 2 || parseErr != nil || len(result.Stored) != 1 || len(result.Failed) != 1 {
		t.Fatalf("status=%d calls=%d stored=%d failed=%d parse=%v body=%q", recorder.Code, calls.Load(), len(result.Stored), len(result.Failed), parseErr, recorder.Body.String())
	}
}

func TestServerSTOWMetadataHasIndependentAggregateLimit(t *testing.T) {
	metadata := stowMetadataJSON("", false)
	backend := completeServerTestBackend()
	var calls atomic.Int32
	backend.store = func(context.Context, StoreRequest) StoreOutcome {
		calls.Add(1)
		return StoreOutcome{Status: StoreStatusStored}
	}
	server, err := NewServer(ServerOptions{
		Backend: backend, AllowUnauthenticated: true,
		Limits: ServerLimits{MaxRequestBytes: 1 << 20, MaxPartBytes: 1 << 20, MaxMetadataBytes: int64(len(metadata) - 1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, contentType := stowMetadataMultipart(t, "application/dicom+json", stowMetadataTestPart{contentType: "application/dicom+json", body: metadata})
	request := httptest.NewRequest(http.MethodPost, "/studies", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/dicom+json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls.Load(), recorder.Body.String())
	}
}

func TestServerSTOWMetadataRejectsInvalidGraphsBeforeBackend(t *testing.T) {
	validMetadata := stowMetadataTestPart{contentType: "application/dicom+json", body: stowMetadataJSON("bulk-1", false)}
	validBulk := stowMetadataTestPart{contentType: "application/octet-stream", contentLocation: "bulk-1", body: []byte{1, 2}}
	tests := []struct {
		name       string
		rootType   string
		parts      []stowMetadataTestPart
		wantStatus int
	}{
		{"missing bulk", "application/dicom+json", []stowMetadataTestPart{validMetadata}, http.StatusBadRequest},
		{"duplicate bulk location", "application/dicom+json", []stowMetadataTestPart{validMetadata, validBulk, validBulk}, http.StatusBadRequest},
		{"unreferenced bulk", "application/dicom+json", []stowMetadataTestPart{{contentType: "application/dicom+json", body: stowMetadataJSON("", false)}, validBulk}, http.StatusBadRequest},
		{"bulk before metadata", "application/dicom+json", []stowMetadataTestPart{validBulk, validMetadata}, http.StatusBadRequest},
		{"second JSON metadata root", "application/dicom+json", []stowMetadataTestPart{validMetadata, validMetadata, validBulk}, http.StatusBadRequest},
		{"file meta group", "application/dicom+json", []stowMetadataTestPart{{contentType: "application/dicom+json", body: stowMetadataJSON("bulk-1", true)}, validBulk}, http.StatusBadRequest},
		{"compressed bulk", "application/dicom+json", []stowMetadataTestPart{validMetadata, {contentType: "image/jpeg", contentLocation: "bulk-1", body: []byte{1, 2}}}, http.StatusUnsupportedMediaType},
		{"ambiguous transfer syntax", "application/dicom+json", []stowMetadataTestPart{validMetadata, {contentType: "application/octet-stream; transfer-syntax=1.2.840.10008.1.2", contentLocation: "bulk-1", body: []byte{1, 2}}}, http.StatusUnsupportedMediaType},
		{"part content encoding", "application/dicom+json", []stowMetadataTestPart{validMetadata, {contentType: "application/octet-stream", contentLocation: "bulk-1", contentEncoding: "gzip", body: []byte{1, 2}}}, http.StatusUnsupportedMediaType},
		{"part transfer encoding", "application/dicom+json", []stowMetadataTestPart{validMetadata, {contentType: "application/octet-stream", contentLocation: "bulk-1", transferEncoding: "base64", body: []byte("AQI=")}}, http.StatusUnsupportedMediaType},
		{"empty JSON array", "application/dicom+json", []stowMetadataTestPart{{contentType: "application/dicom+json", body: []byte("[]")}}, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := completeServerTestBackend()
			var calls atomic.Int32
			backend.store = func(context.Context, StoreRequest) StoreOutcome {
				calls.Add(1)
				return StoreOutcome{Status: StoreStatusStored}
			}
			server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			body, contentType := stowMetadataMultipart(t, test.rootType, test.parts...)
			request := httptest.NewRequest(http.MethodPost, "/studies", body)
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Accept", "application/dicom+json")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus || calls.Load() != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%q", recorder.Code, test.wantStatus, calls.Load(), recorder.Body.String())
			}
		})
	}
}

func TestServerSTOWMetadataBulkDoesNotCrossRequests(t *testing.T) {
	backend := completeServerTestBackend()
	var calls atomic.Int32
	backend.store = func(context.Context, StoreRequest) StoreOutcome {
		calls.Add(1)
		return StoreOutcome{Status: StoreStatusStored}
	}
	server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	send := func(parts ...stowMetadataTestPart) int {
		body, contentType := stowMetadataMultipart(t, "application/dicom+json", parts...)
		request := httptest.NewRequest(http.MethodPost, "/studies", body)
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("Accept", "application/dicom+json")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if status := send(
		stowMetadataTestPart{contentType: "application/dicom+json", body: stowMetadataJSON("bulk-1", false)},
		stowMetadataTestPart{contentType: "application/octet-stream", contentLocation: "bulk-1", body: []byte{1, 2}},
	); status != http.StatusOK {
		t.Fatalf("first status=%d", status)
	}
	if status := send(stowMetadataTestPart{contentType: "application/dicom+json", body: stowMetadataJSON("bulk-1", false)}); status != http.StatusBadRequest {
		t.Fatalf("second status=%d", status)
	}
	if calls.Load() != 1 {
		t.Fatalf("backend calls=%d, want 1", calls.Load())
	}
}

func stowMetadataMultipart(t *testing.T, rootType string, parts ...stowMetadataTestPart) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, item := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", item.contentType)
		if item.contentLocation != "" {
			header.Set("Content-Location", item.contentLocation)
		}
		if item.contentID != "" {
			header.Set("Content-ID", item.contentID)
		}
		if item.contentEncoding != "" {
			header.Set("Content-Encoding", item.contentEncoding)
		}
		if item.transferEncoding != "" {
			header.Set("Content-Transfer-Encoding", item.transferEncoding)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(item.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, `multipart/related; type="` + rootType + `"; boundary=` + writer.Boundary()
}

func stowMetadataJSON(bulkURI string, includeFileMeta bool) []byte {
	bulk := ""
	if bulkURI != "" {
		bulk = fmt.Sprintf(`,"7FE00010":{"vr":"OB","BulkDataURI":%q}`, bulkURI)
	}
	fileMeta := ""
	if includeFileMeta {
		fileMeta = `"00020010":{"vr":"UI","Value":["1.2.840.10008.1.2.1"]},`
	}
	return []byte(fmt.Sprintf(`[{%s"00080016":{"vr":"UI","Value":[%q]},"00080018":{"vr":"UI","Value":[%q]},"0020000D":{"vr":"UI","Value":[%q]},"0020000E":{"vr":"UI","Value":[%q]}%s}]`,
		fileMeta, dicomtest.TestSOPClassUID, dicomtest.TestSOPInstanceUID, dicomtest.TestStudyInstanceUID, dicomtest.TestSeriesInstanceUID, bulk))
}

func stowMetadataXML(bulkURI string, includeFileMeta bool) []byte {
	fileMeta := ""
	if includeFileMeta {
		fileMeta = `<DicomAttribute tag="00020010" vr="UI" keyword="TransferSyntaxUID"><Value number="1">1.2.840.10008.1.2.1</Value></DicomAttribute>`
	}
	bulk := ""
	if bulkURI != "" {
		bulk = `<DicomAttribute tag="7FE00010" vr="OB" keyword="PixelData"><BulkData uri="` + bulkURI + `"/></DicomAttribute>`
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve">` + fileMeta +
		`<DicomAttribute tag="00080016" vr="UI" keyword="SOPClassUID"><Value number="1">` + dicomtest.TestSOPClassUID + `</Value></DicomAttribute>` +
		`<DicomAttribute tag="00080018" vr="UI" keyword="SOPInstanceUID"><Value number="1">` + dicomtest.TestSOPInstanceUID + `</Value></DicomAttribute>` +
		`<DicomAttribute tag="0020000D" vr="UI" keyword="StudyInstanceUID"><Value number="1">` + dicomtest.TestStudyInstanceUID + `</Value></DicomAttribute>` +
		`<DicomAttribute tag="0020000E" vr="UI" keyword="SeriesInstanceUID"><Value number="1">` + dicomtest.TestSeriesInstanceUID + `</Value></DicomAttribute>` + bulk + `</NativeDicomModel>`)
}

func TestSTOWMetadataRequestContentEncodingRejected(t *testing.T) {
	backend := completeServerTestBackend()
	server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	body, contentType := stowMetadataMultipart(t, "application/dicom+json", stowMetadataTestPart{contentType: "application/dicom+json", body: stowMetadataJSON("", false)})
	request := httptest.NewRequest(http.MethodPost, "/studies", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Accept", "application/dicom+json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
