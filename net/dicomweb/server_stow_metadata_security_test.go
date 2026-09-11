package dicomweb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

const (
	stowMetaSecuritySOPClassUID       = "1.2.826.0.1.3680043.9.7433.1.1"
	stowMetaSecuritySOPInstanceUID    = "1.2.826.0.1.3680043.9.7433.1.1.1"
	stowMetaSecurityStudyInstanceUID  = "1.2.826.0.1.3680043.9.7433.1.2.1"
	stowMetaSecuritySeriesInstanceUID = "1.2.826.0.1.3680043.9.7433.1.2.1.1"
)

type stowMetaSecurityBackend struct {
	calls atomic.Int32
}

func (b *stowMetaSecurityBackend) Store(_ context.Context, request dicomweb.StoreRequest) dicomweb.StoreOutcome {
	b.calls.Add(1)
	if _, err := io.Copy(io.Discard, request.Reader); err != nil {
		return dicomweb.StoreOutcome{Status: dicomweb.StoreStatusFailed, Err: err}
	}
	return dicomweb.StoreOutcome{Status: dicomweb.StoreStatusStored}
}

type stowMetaSecurityFormat struct {
	name      string
	mediaType string
	metadata  func(string, bool) []byte
}

type stowMetaSecurityPart struct {
	contentType             string
	contentLocation         string
	contentEncoding         string
	contentTransferEncoding string
	body                    []byte
}

func TestSTOWMetadataSecurityRejectsInvalidBulkReferenceGraphs(t *testing.T) {
	for _, format := range stowMetaSecurityFormats() {
		format := format
		t.Run(format.name, func(t *testing.T) {
			target := "/stow-security/bulk/pixel"
			orphan := "/stow-security/bulk/orphan"
			metadata := stowMetaSecurityMetadataPart(format, target, false)
			bulk := stowMetaSecurityBulkPart(target, stowMetaSecurityPixelData())
			orphanBulk := stowMetaSecurityBulkPart(orphan, []byte{1, 2})

			for _, test := range []struct {
				name  string
				parts []stowMetaSecurityPart
			}{
				{name: "missing", parts: []stowMetaSecurityPart{metadata}},
				{name: "duplicate", parts: []stowMetaSecurityPart{metadata, bulk, bulk}},
				{name: "unreferenced", parts: []stowMetaSecurityPart{metadata, bulk, orphanBulk}},
				{name: "bulk-before-metadata", parts: []stowMetaSecurityPart{bulk, metadata}},
			} {
				t.Run(test.name, func(t *testing.T) {
					backend, spool, recorder := stowMetaSecurityServe(t, format.mediaType, test.parts, dicomweb.ServerLimits{})
					stowMetaSecurityAssertRejectedBeforeBackend(t, recorder, backend, spool)
				})
			}
		})
	}
}

func TestSTOWMetadataSecurityReferencesAreRequestScopedAndNeverFetched(t *testing.T) {
	for _, format := range stowMetaSecurityFormats() {
		format := format
		t.Run(format.name, func(t *testing.T) {
			var outbound atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				outbound.Add(1)
			}))
			defer remote.Close()

			location := remote.URL + "/patient-derived-bulk"
			backend := &stowMetaSecurityBackend{}
			spool := t.TempDir()
			server := stowMetaSecurityNewServer(t, backend, spool, dicomweb.ServerLimits{})

			first := stowMetaSecurityRequest(t, format.mediaType, []stowMetaSecurityPart{
				stowMetaSecurityMetadataPart(format, location, false),
				stowMetaSecurityBulkPart(location, stowMetaSecurityPixelData()),
			})
			firstRecorder := httptest.NewRecorder()
			server.ServeHTTP(firstRecorder, first)
			if firstRecorder.Code < http.StatusOK || firstRecorder.Code >= http.StatusMultipleChoices {
				t.Fatalf("first request status=%d body=%q", firstRecorder.Code, firstRecorder.Body.String())
			}
			if got := backend.calls.Load(); got != 1 {
				t.Fatalf("first request backend calls=%d, want 1", got)
			}
			if got := outbound.Load(); got != 0 {
				t.Fatalf("absolute BulkDataURI caused %d outbound requests", got)
			}
			stowMetaSecurityAssertSpoolEmpty(t, spool)

			second := stowMetaSecurityRequest(t, format.mediaType, []stowMetaSecurityPart{
				stowMetaSecurityMetadataPart(format, location, false),
			})
			secondRecorder := httptest.NewRecorder()
			server.ServeHTTP(secondRecorder, second)
			if secondRecorder.Code < http.StatusBadRequest || secondRecorder.Code >= http.StatusInternalServerError {
				t.Fatalf("cross-request reference status=%d body=%q", secondRecorder.Code, secondRecorder.Body.String())
			}
			if got := backend.calls.Load(); got != 1 {
				t.Fatalf("cross-request reference backend calls=%d, want prior call only", got)
			}
			if got := outbound.Load(); got != 0 {
				t.Fatalf("cross-request reference caused %d outbound requests", got)
			}
			stowMetaSecurityAssertSpoolEmpty(t, spool)
		})
	}
}

func TestSTOWMetadataSecurityRejectsFileMetaAndContentCodings(t *testing.T) {
	for _, format := range stowMetaSecurityFormats() {
		format := format
		t.Run(format.name, func(t *testing.T) {
			location := "/stow-security/bulk/pixel"
			plainMetadata := stowMetaSecurityMetadataPart(format, location, false)
			plainBulk := stowMetaSecurityBulkPart(location, stowMetaSecurityPixelData())

			metadataCTE := plainMetadata
			metadataCTE.contentTransferEncoding = "quoted-printable"
			bulkCTE := plainBulk
			bulkCTE.contentTransferEncoding = "base64"
			metadataEncoding := plainMetadata
			metadataEncoding.contentEncoding = "gzip"
			bulkEncoding := plainBulk
			bulkEncoding.contentEncoding = "gzip"

			for _, test := range []struct {
				name  string
				parts []stowMetaSecurityPart
			}{
				{
					name: "file-meta-group",
					parts: []stowMetaSecurityPart{
						stowMetaSecurityMetadataPart(format, location, true), plainBulk,
					},
				},
				{name: "metadata-content-transfer-encoding", parts: []stowMetaSecurityPart{metadataCTE, plainBulk}},
				{name: "bulk-content-transfer-encoding", parts: []stowMetaSecurityPart{plainMetadata, bulkCTE}},
				{name: "metadata-content-encoding", parts: []stowMetaSecurityPart{metadataEncoding, plainBulk}},
				{name: "bulk-content-encoding", parts: []stowMetaSecurityPart{plainMetadata, bulkEncoding}},
			} {
				t.Run(test.name, func(t *testing.T) {
					backend, spool, recorder := stowMetaSecurityServe(t, format.mediaType, test.parts, dicomweb.ServerLimits{})
					stowMetaSecurityAssertRejectedBeforeBackend(t, recorder, backend, spool)
				})
			}
		})
	}
}

func TestSTOWMetadataSecurityPartLimitPlusOneFailsBeforeBackend(t *testing.T) {
	format := stowMetaSecurityFormats()[0]
	location := "/stow-security/bulk/pixel"
	metadata := stowMetaSecurityMetadataPart(format, location, false)
	limit := int64(len(metadata.body))
	oversized := bytes.Repeat([]byte{0x5a}, int(limit)+1)
	backend, spool, recorder := stowMetaSecurityServe(t, format.mediaType, []stowMetaSecurityPart{
		metadata,
		stowMetaSecurityBulkPart(location, oversized),
	}, dicomweb.ServerLimits{
		MaxRequestBytes: 1 << 20,
		MaxPartBytes:    limit,
	})
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%q, want 413", recorder.Code, recorder.Body.String())
	}
	if got := backend.calls.Load(); got != 0 {
		t.Fatalf("backend calls=%d, want 0", got)
	}
	stowMetaSecurityAssertSpoolEmpty(t, spool)
}

func TestSTOWMetadataSecurityPreflightsCompleteRequestBeforeBackend(t *testing.T) {
	format := stowMetaSecurityFormats()[0]
	firstLocation := "/stow-security/bulk/first"
	secondLocation := "/stow-security/bulk/second"
	metadata := stowMetaSecurityJSONMetadata([]string{firstLocation, secondLocation}, false)
	backend, spool, recorder := stowMetaSecurityServe(t, format.mediaType, []stowMetaSecurityPart{
		{contentType: format.mediaType, body: metadata},
		stowMetaSecurityBulkPart(firstLocation, stowMetaSecurityPixelData()),
	}, dicomweb.ServerLimits{})
	stowMetaSecurityAssertRejectedBeforeBackend(t, recorder, backend, spool)
}

func stowMetaSecurityFormats() []stowMetaSecurityFormat {
	return []stowMetaSecurityFormat{
		{name: "json", mediaType: "application/dicom+json", metadata: stowMetaSecuritySingleJSONMetadata},
		{name: "xml", mediaType: "application/dicom+xml", metadata: stowMetaSecurityXMLMetadata},
	}
}

func stowMetaSecurityMetadataPart(format stowMetaSecurityFormat, location string, includeGroup2 bool) stowMetaSecurityPart {
	return stowMetaSecurityPart{contentType: format.mediaType, body: format.metadata(location, includeGroup2)}
}

func stowMetaSecurityBulkPart(location string, body []byte) stowMetaSecurityPart {
	return stowMetaSecurityPart{contentType: "application/octet-stream", contentLocation: location, body: body}
}

func stowMetaSecurityServe(t *testing.T, mediaType string, parts []stowMetaSecurityPart, limits dicomweb.ServerLimits) (*stowMetaSecurityBackend, string, *httptest.ResponseRecorder) {
	t.Helper()
	backend := &stowMetaSecurityBackend{}
	spool := t.TempDir()
	server := stowMetaSecurityNewServer(t, backend, spool, limits)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, stowMetaSecurityRequest(t, mediaType, parts))
	return backend, spool, recorder
}

func stowMetaSecurityNewServer(t *testing.T, backend *stowMetaSecurityBackend, spool string, limits dicomweb.ServerLimits) *dicomweb.Server {
	t.Helper()
	server, err := dicomweb.NewServer(dicomweb.ServerOptions{
		Backend:              backend,
		AllowUnauthenticated: true,
		SpoolDirectory:       spool,
		Limits:               limits,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func stowMetaSecurityRequest(t *testing.T, mediaType string, parts []stowMetaSecurityPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary("stow-metadata-security-boundary"); err != nil {
		t.Fatalf("SetBoundary: %v", err)
	}
	for _, part := range parts {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Type", part.contentType)
		if part.contentLocation != "" {
			header.Set("Content-Location", part.contentLocation)
		}
		if part.contentEncoding != "" {
			header.Set("Content-Encoding", part.contentEncoding)
		}
		if part.contentTransferEncoding != "" {
			header.Set("Content-Transfer-Encoding", part.contentTransferEncoding)
		}
		partWriter, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("CreatePart: %v", err)
		}
		if _, err := partWriter.Write(part.body); err != nil {
			t.Fatalf("write multipart part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/studies", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", fmt.Sprintf(`multipart/related; type=%q; boundary=%q`, mediaType, writer.Boundary()))
	request.Header.Set("Accept", "application/dicom+json")
	return request
}

func stowMetaSecurityAssertRejectedBeforeBackend(t *testing.T, recorder *httptest.ResponseRecorder, backend *stowMetaSecurityBackend, spool string) {
	t.Helper()
	if recorder.Code < http.StatusBadRequest || recorder.Code >= http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q, want a client error", recorder.Code, recorder.Body.String())
	}
	if got := backend.calls.Load(); got != 0 {
		t.Fatalf("backend calls=%d, want 0", got)
	}
	stowMetaSecurityAssertSpoolEmpty(t, spool)
}

func stowMetaSecurityAssertSpoolEmpty(t *testing.T, spool string) {
	t.Helper()
	entries, err := os.ReadDir(spool)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", spool, err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool contains %d entries after request: %v", len(entries), entries)
	}
}

func stowMetaSecuritySingleJSONMetadata(location string, includeGroup2 bool) []byte {
	return stowMetaSecurityJSONMetadata([]string{location}, includeGroup2)
}

func stowMetaSecurityJSONMetadata(locations []string, includeGroup2 bool) []byte {
	datasets := make([]map[string]any, 0, len(locations))
	for index, location := range locations {
		dataset := stowMetaSecurityJSONDataset(location, index)
		if includeGroup2 {
			dataset["00020010"] = map[string]any{"vr": "UI", "Value": []string{"1.2.840.10008.1.2.1"}}
		}
		datasets = append(datasets, dataset)
	}
	encoded, err := json.Marshal(datasets)
	if err != nil {
		panic(err)
	}
	return encoded
}

func stowMetaSecurityJSONDataset(location string, index int) map[string]any {
	instanceUID := stowMetaSecuritySOPInstanceUID
	if index > 0 {
		instanceUID = fmt.Sprintf("%s.%d", instanceUID, index+1)
	}
	return map[string]any{
		"00080016": map[string]any{"vr": "UI", "Value": []string{stowMetaSecuritySOPClassUID}},
		"00080018": map[string]any{"vr": "UI", "Value": []string{instanceUID}},
		"00080060": map[string]any{"vr": "CS", "Value": []string{"OT"}},
		"0020000D": map[string]any{"vr": "UI", "Value": []string{stowMetaSecurityStudyInstanceUID}},
		"0020000E": map[string]any{"vr": "UI", "Value": []string{stowMetaSecuritySeriesInstanceUID}},
		"00280002": map[string]any{"vr": "US", "Value": []int{1}},
		"00280004": map[string]any{"vr": "CS", "Value": []string{"MONOCHROME2"}},
		"00280010": map[string]any{"vr": "US", "Value": []int{8}},
		"00280011": map[string]any{"vr": "US", "Value": []int{8}},
		"00280100": map[string]any{"vr": "US", "Value": []int{8}},
		"00280101": map[string]any{"vr": "US", "Value": []int{8}},
		"00280102": map[string]any{"vr": "US", "Value": []int{7}},
		"00280103": map[string]any{"vr": "US", "Value": []int{0}},
		"7FE00010": map[string]any{"vr": "OB", "BulkDataURI": location},
	}
}

func stowMetaSecurityXMLMetadata(location string, includeGroup2 bool) []byte {
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(location)); err != nil {
		panic(err)
	}
	group2 := ""
	if includeGroup2 {
		group2 = `<DicomAttribute tag="00020010" vr="UI" keyword="TransferSyntaxUID"><Value number="1">1.2.840.10008.1.2.1</Value></DicomAttribute>`
	}
	return []byte(`<NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve">` +
		group2 +
		`<DicomAttribute tag="00080016" vr="UI" keyword="SOPClassUID"><Value number="1">` + stowMetaSecuritySOPClassUID + `</Value></DicomAttribute>` +
		`<DicomAttribute tag="00080018" vr="UI" keyword="SOPInstanceUID"><Value number="1">` + stowMetaSecuritySOPInstanceUID + `</Value></DicomAttribute>` +
		`<DicomAttribute tag="00080060" vr="CS" keyword="Modality"><Value number="1">OT</Value></DicomAttribute>` +
		`<DicomAttribute tag="0020000D" vr="UI" keyword="StudyInstanceUID"><Value number="1">` + stowMetaSecurityStudyInstanceUID + `</Value></DicomAttribute>` +
		`<DicomAttribute tag="0020000E" vr="UI" keyword="SeriesInstanceUID"><Value number="1">` + stowMetaSecuritySeriesInstanceUID + `</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280002" vr="US" keyword="SamplesPerPixel"><Value number="1">1</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280004" vr="CS" keyword="PhotometricInterpretation"><Value number="1">MONOCHROME2</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280010" vr="US" keyword="Rows"><Value number="1">8</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280011" vr="US" keyword="Columns"><Value number="1">8</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280100" vr="US" keyword="BitsAllocated"><Value number="1">8</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280101" vr="US" keyword="BitsStored"><Value number="1">8</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280102" vr="US" keyword="HighBit"><Value number="1">7</Value></DicomAttribute>` +
		`<DicomAttribute tag="00280103" vr="US" keyword="PixelRepresentation"><Value number="1">0</Value></DicomAttribute>` +
		`<DicomAttribute tag="7FE00010" vr="OB" keyword="PixelData"><BulkData uri="` + escaped.String() + `"></BulkData></DicomAttribute>` +
		`</NativeDicomModel>`)
}

func stowMetaSecurityPixelData() []byte {
	data := make([]byte, 64)
	for index := range data {
		data[index] = byte(index)
	}
	return data
}
