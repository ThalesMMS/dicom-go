package dicomweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dicomxml"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
)

func metadataXMLTestDataset() Dataset {
	return Dataset{
		"00080018": {VR: "UI", Value: []any{"1.2.3.4.5"}},
		"00100010": {VR: "PN", Value: []any{map[string]any{"Alphabetic": "DOE^JANE"}}},
		"0020000D": {VR: "UI", Value: []any{"1.2.3"}},
		"0020000E": {VR: "UI", Value: []any{"1.2.3.4"}},
		"7FE00010": {VR: "OB", BulkDataURI: "/bulkdata/pixel-token"},
	}
}

func metadataXMLTestBody(t *testing.T, dataset Dataset) string {
	t.Helper()
	encoded, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := dicomjson.Unmarshal(encoded, std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}
	xmlData, err := dicomxml.Marshal(obj, dicomxml.Options{OmitGroupLength: true})
	if err != nil {
		t.Fatal(err)
	}
	return string(xmlData)
}

func newMetadataXMLTestRequest(t testing.TB, method, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestClientMetadataAcceptHeaderPreservesJSONDefaultAndOrdersPreferences(t *testing.T) {
	if got := (Client{}).metadataAcceptHeader(); got != acceptDICOMJSON {
		t.Fatalf("default Accept=%q", got)
	}
	client := Client{Options: Options{MetadataMediaTypes: []MetadataMediaType{
		MetadataMediaTypeDICOMXML,
		MetadataMediaTypeDICOMJSON,
		MetadataMediaTypeDICOMXML,
	}}}
	want := `multipart/related; type="application/dicom+xml", application/dicom+json;q=0.9`
	if got := client.metadataAcceptHeader(); got != want {
		t.Fatalf("ordered Accept=%q want=%q", got, want)
	}
}

func TestServerClientNegotiatesDICOMXMLForQIDOAndWADOMetadata(t *testing.T) {
	backend := completeServerTestBackend()
	dataset := metadataXMLTestDataset()
	backend.search = func(_ context.Context, _ SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		return SearchResult{}, yield(dataset)
	}
	backend.metadata = func(_ context.Context, _ MetadataRequest, yield func(Dataset) error) error {
		return yield(dataset)
	}
	_, httpServer := newOpenTestServer(t, backend, nil)
	client := Client{
		Endpoint: Endpoint{BaseURL: httpServer.URL},
		Options:  Options{MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML}},
	}

	search, err := client.SearchStudies(context.Background(), nil)
	if err != nil {
		t.Fatalf("SearchStudies XML: %v", err)
	}
	if len(search) != 1 || search[0]["00100010"].VR != "PN" || search[0]["7FE00010"].BulkDataURI != "/bulkdata/pixel-token" {
		t.Fatalf("QIDO XML datasets = %#v", search)
	}
	metadata, err := client.StudyMetadataDatasets(context.Background(), "1.2.3")
	if err != nil {
		t.Fatalf("StudyMetadataDatasets XML: %v", err)
	}
	if len(metadata) != 1 || metadata[0]["00080018"].Value[0] != "1.2.3.4.5" {
		t.Fatalf("WADO XML datasets = %#v", metadata)
	}
}

func TestServerMetadataNegotiationQualityWildcardsAndParameters(t *testing.T) {
	backend := completeServerTestBackend()
	backend.search = func(_ context.Context, _ SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		return SearchResult{}, yield(metadataXMLTestDataset())
	}
	server, _ := newOpenTestServer(t, backend, nil)
	tests := []struct {
		name        string
		accept      string
		status      int
		contentType string
	}{
		{"prefer XML", `application/dicom+json;q=0.4, multipart/related; type="application/dicom+xml";q=0.9`, http.StatusOK, "multipart/related"},
		{"prefer JSON", `multipart/related; type="application/dicom+xml";q=0.4, application/dicom+json;q=0.9`, http.StatusOK, "application/dicom+json"},
		{"equal quality prefers specific XML", `*/*;q=0.5, multipart/related; type="application/dicom+xml";q=0.5`, http.StatusOK, "multipart/related"},
		{"equal quality prefers specific JSON", `*/*;q=0.5, application/dicom+json;q=0.5`, http.StatusOK, "application/dicom+json"},
		{"wildcard defaults JSON", `*/*`, http.StatusOK, "application/dicom+json"},
		{"JSON explicitly excluded", `*/*;q=1, application/dicom+json;q=0`, http.StatusOK, "multipart/related"},
		{"explicit XML transfer syntax", `multipart/related; type="application/dicom+xml"; transfer-syntax=1.2.840.10008.1.2.1`, http.StatusOK, "multipart/related"},
		{"direct XML is not a search media type", `application/dicom+xml`, http.StatusNotAcceptable, "text/plain"},
		{"unsupported transfer syntax", `multipart/related; type="application/dicom+xml"; transfer-syntax=1.2.840.10008.1.2`, http.StatusNotAcceptable, "text/plain"},
		{"unsupported charset", `multipart/related; type="application/dicom+xml"; charset=iso-8859-1`, http.StatusNotAcceptable, "text/plain"},
		{"unknown parameter", `multipart/related; type="application/dicom+xml"; profile=unsafe`, http.StatusNotAcceptable, "text/plain"},
		{"malformed quality", `multipart/related; type="application/dicom+xml";q=NaN`, http.StatusNotAcceptable, "text/plain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := newMetadataXMLTestRequest(t, http.MethodGet, "/studies")
			request.Header.Set("Accept", test.accept)
			server.ServeHTTP(recorder, request)
			mediaType, _, _ := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
			if recorder.Code != test.status || mediaType != test.contentType {
				t.Fatalf("status=%d Content-Type=%q body=%q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
			}
			if test.status == http.StatusOK && (recorder.Header().Get("Vary") != "Accept" || recorder.Header().Get("X-Content-Type-Options") != "nosniff") {
				t.Fatalf("negotiated response headers=%v", recorder.Header())
			}
		})
	}
}

func TestServerQIDOEmptyDICOMXMLUsesEmptyNativeModel(t *testing.T) {
	backend := completeServerTestBackend()
	server, _ := newOpenTestServer(t, backend, nil)
	recorder := httptest.NewRecorder()
	request := newMetadataXMLTestRequest(t, http.MethodGet, "/studies")
	request.Header.Set("Accept", `multipart/related; type="application/dicom+xml"`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	response := Response{URL: "http://example.test/studies", Header: recorder.Header(), Body: recorder.Body.Bytes()}
	datasets, err := (Client{Options: Options{MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML}}}).datasetsFromMetadataResponse(context.Background(), response, false)
	if err != nil || len(datasets) != 0 {
		t.Fatalf("datasets=%#v err=%v", datasets, err)
	}
}

func TestServerWADODICOMXMLSetsValidatedContentLocationAndRejectsRouteMismatch(t *testing.T) {
	backend := completeServerTestBackend()
	dataset := metadataXMLTestDataset()
	backend.metadata = func(_ context.Context, _ MetadataRequest, yield func(Dataset) error) error {
		return yield(dataset)
	}
	server, _ := newOpenTestServer(t, backend, nil)
	recorder := httptest.NewRecorder()
	request := newMetadataXMLTestRequest(t, http.MethodGet, "/studies/1.2.3/metadata")
	request.Header.Set("Accept", `multipart/related; type="application/dicom+xml"`)
	server.ServeHTTP(recorder, request)
	mediaType, params, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
	if recorder.Code != http.StatusOK || err != nil || mediaType != "multipart/related" || params["type"] != "application/dicom+xml" {
		t.Fatalf("status=%d Content-Type=%q err=%v body=%q", recorder.Code, recorder.Header().Get("Content-Type"), err, recorder.Body.String())
	}
	part, err := multipart.NewReader(bytes.NewReader(recorder.Body.Bytes()), params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	wantLocation := "/studies/1.2.3/series/1.2.3.4/instances/1.2.3.4.5/metadata"
	if part.Header.Get("Content-Type") != "application/dicom+xml" || part.Header.Get("Content-Location") != wantLocation {
		t.Fatalf("part headers=%v", part.Header)
	}

	recorder = httptest.NewRecorder()
	request = newMetadataXMLTestRequest(t, http.MethodGet, "/studies/1.9/metadata")
	request.Header.Set("Accept", `multipart/related; type="application/dicom+xml"`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "NativeDicomModel") {
		t.Fatalf("mismatch status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServerDICOMXMLStagesLimitsAndConversionErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		dataset Dataset
		limit   int64
		status  int
	}{
		{"oversize", metadataXMLTestDataset(), 256, http.StatusRequestEntityTooLarge},
		{"invalid inline binary", Dataset{"7FE00010": {VR: "OB", InlineBinary: "%%%%"}}, 1 << 20, http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := completeServerTestBackend()
			backend.search = func(_ context.Context, _ SearchRequest, yield func(Dataset) error) (SearchResult, error) {
				return SearchResult{}, yield(test.dataset)
			}
			server, _ := newOpenTestServer(t, backend, func(options *ServerOptions) {
				options.Limits.MaxResponseBytes = test.limit
			})
			recorder := httptest.NewRecorder()
			request := newMetadataXMLTestRequest(t, http.MethodGet, "/studies")
			request.Header.Set("Accept", `multipart/related; type="application/dicom+xml"`)
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.status || strings.Contains(recorder.Body.String(), "NativeDicomModel") {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestClientRejectsUnsafeOrMalformedDICOMXMLMultipart(t *testing.T) {
	emptyXML := `<?xml version="1.0" encoding="UTF-8"?><NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve"></NativeDicomModel>`
	instanceXML := metadataXMLTestBody(t, metadataXMLTestDataset())
	encapsulatedValue := []byte{0xfe, 0xff, 0x00, 0xe0, 0, 0, 0, 0, 0xfe, 0xff, 0xdd, 0xe0, 0, 0, 0, 0}
	pixelXML := `<?xml version="1.0" encoding="UTF-8"?><NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve"><DicomAttribute tag="7FE00010" vr="OB" keyword="PixelData"><InlineBinary>` + base64.StdEncoding.EncodeToString(encapsulatedValue) + `</InlineBinary></DicomAttribute></NativeDicomModel>`
	tests := []struct {
		name       string
		parts      []xmlMetadataTestPart
		outerExtra map[string]string
		requireLoc bool
		maxParts   int
		maxBytes   int64
	}{
		{"malformed XML", []xmlMetadataTestPart{{contentType: "application/dicom+xml", body: "<NativeDicomModel>"}}, nil, false, 0, 0},
		{"heterogeneous part", []xmlMetadataTestPart{{contentType: "application/dicom+json", body: "{}"}}, nil, false, 0, 0},
		{"missing WADO location", []xmlMetadataTestPart{{contentType: "application/dicom+xml", body: emptyXML}}, nil, true, 0, 0},
		{"cross-origin WADO location", []xmlMetadataTestPart{{contentType: "application/dicom+xml", location: "https://evil.test/metadata", body: emptyXML}}, nil, true, 0, 0},
		{"invalid WADO location path", []xmlMetadataTestPart{{contentType: "application/dicom+xml", location: "https://pacs.test/not-metadata", body: emptyXML}}, nil, true, 0, 0},
		{"WADO location UID mismatch", []xmlMetadataTestPart{{contentType: "application/dicom+xml", location: "/dicomweb/studies/1.2.3/series/1.2.3.4/instances/1.2.3.4.6/metadata", body: instanceXML}}, nil, true, 0, 0},
		{"part limit", []xmlMetadataTestPart{{contentType: "application/dicom+xml", body: emptyXML}, {contentType: "application/dicom+xml", body: emptyXML}}, nil, false, 1, 0},
		{"part bytes", []xmlMetadataTestPart{{contentType: "application/dicom+xml", body: emptyXML}}, nil, false, 0, 32},
		{"unknown outer parameter", []xmlMetadataTestPart{{contentType: "application/dicom+xml", body: emptyXML}}, map[string]string{"profile": "unsafe"}, false, 0, 0},
		{"conflicting transfer syntax", []xmlMetadataTestPart{{contentType: "application/dicom+xml; transfer-syntax=1.2.840.10008.1.2.4.50", body: emptyXML}}, map[string]string{"transfer-syntax": "1.2.840.10008.1.2.1"}, false, 0, 0},
		{"encapsulated pixel data without transfer syntax", []xmlMetadataTestPart{{contentType: "application/dicom+xml", body: pixelXML}}, nil, false, 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, contentType := xmlMetadataMultipart(t, test.parts, test.outerExtra)
			client := Client{Options: Options{MaxMetadataParts: test.maxParts, MaxBodyBytes: test.maxBytes, MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML}}}
			_, err := client.datasetsFromMetadataResponse(context.Background(), Response{
				URL:    "https://pacs.test/dicomweb/studies/1.2.3/metadata",
				Header: http.Header{"Content-Type": []string{contentType}},
				Body:   body,
			}, test.requireLoc)
			if err == nil {
				t.Fatal("expected malformed or unsafe multipart response to fail")
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Client{}).datasetsFromMetadataResponse(ctx, Response{}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled decode error=%v", err)
	}

	_, err = (Client{Options: Options{MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML}}}).datasetsFromMetadataResponse(context.Background(), Response{
		Header: http.Header{"Content-Type": []string{"application/dicom+xml"}},
		Body:   []byte(emptyXML),
	}, false)
	if err == nil {
		t.Fatal("direct application/dicom+xml response was accepted without multipart/related")
	}

	for _, placement := range []struct {
		name       string
		partType   string
		outerExtra map[string]string
	}{
		{"part parameter", "application/dicom+xml; transfer-syntax=1.2.840.10008.1.2.4.50", nil},
		{"outer parameter", "application/dicom+xml", map[string]string{"transfer-syntax": "1.2.840.10008.1.2.4.50"}},
	} {
		t.Run(placement.name, func(t *testing.T) {
			body, contentType := xmlMetadataMultipart(t, []xmlMetadataTestPart{{contentType: placement.partType, body: pixelXML}}, placement.outerExtra)
			datasets, err := (Client{Options: Options{MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML}}}).datasetsFromMetadataResponse(context.Background(), Response{
				URL:    "https://pacs.test/studies",
				Header: http.Header{"Content-Type": []string{contentType}},
				Body:   body,
			}, false)
			if err != nil || len(datasets) != 1 || datasets[0]["7FE00010"].InlineBinary == "" {
				t.Fatalf("encapsulated transfer syntax propagation datasets=%#v err=%v", datasets, err)
			}
		})
	}
}

func TestDICOMXMLMetadataUsesSharedResponseLimits(t *testing.T) {
	emptyXML := `<?xml version="1.0" encoding="UTF-8"?><NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve"></NativeDicomModel>`
	tests := []struct {
		name     string
		parts    []xmlMetadataTestPart
		limits   ResponseLimits
		wantKind ResponseDecodeErrorKind
	}{
		{
			name: "shared part count",
			parts: []xmlMetadataTestPart{
				{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML},
				{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML},
			},
			limits:   ResponseLimits{MaxParts: 1},
			wantKind: ResponseDecodePartLimit,
		},
		{
			name: "shared part bytes",
			parts: []xmlMetadataTestPart{
				{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML},
			},
			limits:   ResponseLimits{MaxPartBytes: 32},
			wantKind: ResponseDecodePartBytes,
		},
		{
			name: "shared header bytes",
			parts: []xmlMetadataTestPart{
				{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML, extraHeaders: textproto.MIMEHeader{"X-Test": {strings.Repeat("x", 128)}}},
			},
			limits:   ResponseLimits{MaxPartHeaderBytes: 64},
			wantKind: ResponseDecodeHeaderBytes,
		},
		{
			name: "nested multipart",
			parts: []xmlMetadataTestPart{
				{contentType: `multipart/related; type="application/dicom+xml"; boundary=nested`, body: emptyXML},
			},
			limits:   ResponseLimits{MaxMultipartDepth: 1},
			wantKind: ResponseDecodeMultipartDepth,
		},
		{
			name: "configured media allowlist",
			parts: []xmlMetadataTestPart{
				{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML},
			},
			limits:   ResponseLimits{AllowedMediaTypes: []string{"application/dicom"}},
			wantKind: ResponseDecodeUnexpectedMedia,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, contentType := xmlMetadataMultipart(t, test.parts, nil)
			client := Client{Options: Options{
				MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML},
				ResponseLimits:     test.limits,
			}}
			_, err := client.datasetsFromMetadataResponse(context.Background(), Response{
				URL:    "https://pacs.test/dicomweb/studies/1.2.3/metadata",
				Header: http.Header{"Content-Type": []string{contentType}},
				Body:   body,
			}, false)
			var decodeErr *ResponseDecodeError
			if !errors.As(err, &decodeErr) || decodeErr.Kind != test.wantKind {
				t.Fatalf("error = %v, want ResponseDecodeError kind %q", err, test.wantKind)
			}
		})
	}
}

func TestDICOMXMLMetadataPreservesLegacyPartLimitAndBodyInheritance(t *testing.T) {
	emptyXML := `<?xml version="1.0" encoding="UTF-8"?><NativeDicomModel xmlns="http://dicom.nema.org/PS3.19/models/NativeDICOM" xml:space="preserve"></NativeDicomModel>`
	body, contentType := xmlMetadataMultipart(t, []xmlMetadataTestPart{
		{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML},
		{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML},
	}, nil)
	response := Response{Header: http.Header{"Content-Type": []string{contentType}}, Body: body}

	client := Client{Options: Options{
		MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML},
		MaxMetadataParts:   1,
		ResponseLimits:     ResponseLimits{MaxParts: 2},
	}}
	_, err := client.datasetsFromMetadataResponse(context.Background(), response, false)
	var decodeErr *ResponseDecodeError
	if !errors.As(err, &decodeErr) || decodeErr.Kind != ResponseDecodePartLimit || decodeErr.Limit != 1 {
		t.Fatalf("legacy MaxMetadataParts error = %v", err)
	}

	singleBody, singleContentType := xmlMetadataMultipart(t, []xmlMetadataTestPart{
		{contentType: string(MetadataMediaTypeDICOMXML), body: emptyXML},
	}, nil)
	client = Client{Options: Options{
		MetadataMediaTypes: []MetadataMediaType{MetadataMediaTypeDICOMXML},
		MaxBodyBytes:       int64(len(emptyXML) + 16),
	}}
	_, err = client.datasetsFromMetadataResponse(context.Background(), Response{
		Header: http.Header{"Content-Type": []string{singleContentType}}, Body: singleBody,
	}, false)
	if err != nil {
		t.Fatalf("zero MaxPartBytes did not inherit MaxBodyBytes: %v", err)
	}
}

func TestMetadataResponseOptionsFailBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	for _, options := range []Options{
		{ResponseLimits: ResponseLimits{MaxParts: -1}},
		{MaxMetadataParts: -1},
		{ResponseLimits: ResponseLimits{AllowedMediaTypes: []string{"not a media type"}}},
	} {
		client := Client{Endpoint: Endpoint{BaseURL: server.URL}, Options: options}
		_, err := client.SearchStudies(context.Background(), nil)
		assertErrorKind(t, err, ErrorKindRequestFailure)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("invalid response options sent %d HTTP requests", got)
	}
}

type xmlMetadataTestPart struct {
	contentType  string
	location     string
	body         string
	extraHeaders textproto.MIMEHeader
}

func xmlMetadataMultipart(t *testing.T, parts []xmlMetadataTestPart, outerExtra map[string]string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, item := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", item.contentType)
		if item.location != "" {
			header.Set("Content-Location", item.location)
		}
		for name, values := range item.extraHeaders {
			for _, value := range values {
				header.Add(name, value)
			}
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(item.body))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"type": string(MetadataMediaTypeDICOMXML), "boundary": writer.Boundary()}
	for key, value := range outerExtra {
		params[key] = value
	}
	return body.Bytes(), mime.FormatMediaType("multipart/related", params)
}
