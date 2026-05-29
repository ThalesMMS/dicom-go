package dicomweb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEndpointStudySearchURLJoinsBaseAndQIDOWithoutDuplicateStudies(t *testing.T) {
	endpoint := Endpoint{
		BaseURL:  "https://pacs.example.test/dicom-web/",
		QIDOPath: "/qido-rs/studies/",
	}
	params := url.Values{"PatientID": {"P123"}}

	u, err := endpoint.StudySearchURL(params)
	if err != nil {
		t.Fatalf("StudySearchURL: %v", err)
	}
	if got, want := u.String(), "https://pacs.example.test/dicom-web/qido-rs/studies?PatientID=P123"; got != want {
		t.Fatalf("StudySearchURL = %q, want %q", got, want)
	}
}

func TestEndpointSeriesSearchURLBuildsPathEscapesUIDAndRequiresStudy(t *testing.T) {
	endpoint := Endpoint{
		BaseURL:  "https://pacs.example.test/dicom-web/",
		QIDOPath: "/qido-rs/studies/",
	}
	params := url.Values{"Modality": {"CT"}}

	u, err := endpoint.SeriesSearchURL(" 1.2.3/4 ", params)
	if err != nil {
		t.Fatalf("SeriesSearchURL: %v", err)
	}
	if got, want := u.Path, "/dicom-web/qido-rs/studies/1.2.3%2F4/series"; got != want {
		t.Fatalf("SeriesSearchURL path = %q, want %q", got, want)
	}
	if got := u.Query().Get("Modality"); got != "CT" {
		t.Fatalf("Modality query = %q", got)
	}

	_, err = endpoint.SeriesSearchURL(" \t ", nil)
	assertErrorKind(t, err, ErrorKindInvalidEndpoint)
}

func TestEndpointInstanceSearchURLBuildsPathEscapesUIDsAndRequiresContext(t *testing.T) {
	endpoint := Endpoint{
		BaseURL:  "https://pacs.example.test/dicom-web/",
		QIDOPath: "/qido-rs/studies/",
	}
	params := url.Values{"SOPInstanceUID": {"9.10.11"}}

	u, err := endpoint.InstanceSearchURL(" 1.2.3/4 ", " 5.6.7/8 ", params)
	if err != nil {
		t.Fatalf("InstanceSearchURL: %v", err)
	}
	if got, want := u.Path, "/dicom-web/qido-rs/studies/1.2.3%2F4/series/5.6.7%2F8/instances"; got != want {
		t.Fatalf("InstanceSearchURL path = %q, want %q", got, want)
	}
	if got := u.Query().Get("SOPInstanceUID"); got != "9.10.11" {
		t.Fatalf("SOPInstanceUID query = %q", got)
	}

	_, err = endpoint.InstanceSearchURL(" \t ", "5.6.7", nil)
	assertErrorKind(t, err, ErrorKindInvalidEndpoint)
	_, err = endpoint.InstanceSearchURL("1.2.3", " \t ", nil)
	assertErrorKind(t, err, ErrorKindInvalidEndpoint)
}

func TestEndpointWADOURLsDoNotDuplicateStudiesWhenBaseEndsWithStudies(t *testing.T) {
	endpoint := Endpoint{BaseURL: "https://pacs.example.test/wado-rs/studies"}

	study, err := endpoint.StudyMetadataURL("1.2.3")
	if err != nil {
		t.Fatalf("StudyMetadataURL: %v", err)
	}
	if got, want := study.Path, "/wado-rs/studies/1.2.3/metadata"; got != want {
		t.Fatalf("StudyMetadataURL path = %q, want %q", got, want)
	}

	series, err := endpoint.SeriesMetadataURL("1.2.3", "4.5.6")
	if err != nil {
		t.Fatalf("SeriesMetadataURL: %v", err)
	}
	if got, want := series.Path, "/wado-rs/studies/1.2.3/series/4.5.6/metadata"; got != want {
		t.Fatalf("SeriesMetadataURL path = %q, want %q", got, want)
	}

	instance, err := endpoint.InstanceURL(InstanceRef{
		StudyInstanceUID:  "1.2.3",
		SeriesInstanceUID: "4.5.6",
		SOPInstanceUID:    "7.8.9",
	})
	if err != nil {
		t.Fatalf("InstanceURL: %v", err)
	}
	if got, want := instance.Path, "/wado-rs/studies/1.2.3/series/4.5.6/instances/7.8.9"; got != want {
		t.Fatalf("InstanceURL path = %q, want %q", got, want)
	}
}

func TestEndpointWADOURLsReturnTypedInvalidEndpointErrors(t *testing.T) {
	endpoint := Endpoint{BaseURL: "https://pacs.example.test/wado-rs"}

	tests := []struct {
		name string
		call func() error
	}{
		{"study metadata study", func() error {
			_, err := endpoint.StudyMetadataURL(" \t ")
			return err
		}},
		{"series metadata study", func() error {
			_, err := endpoint.SeriesMetadataURL(" \t ", "4.5.6")
			return err
		}},
		{"series metadata series", func() error {
			_, err := endpoint.SeriesMetadataURL("1.2.3", " \t ")
			return err
		}},
		{"instance study", func() error {
			_, err := endpoint.InstanceURL(InstanceRef{StudyInstanceUID: " \t ", SeriesInstanceUID: "4.5.6", SOPInstanceUID: "7.8.9"})
			return err
		}},
		{"instance series", func() error {
			_, err := endpoint.InstanceURL(InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: " \t ", SOPInstanceUID: "7.8.9"})
			return err
		}},
		{"instance sop", func() error {
			_, err := endpoint.InstanceURL(InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "4.5.6", SOPInstanceUID: " \t "})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorKind(t, tt.call(), ErrorKindInvalidEndpoint)
		})
	}
}

func TestDICOMwebErrorStringHandlesNilReceiver(t *testing.T) {
	var err *Error
	if got := err.Error(); got != "DICOMweb error" {
		t.Fatalf("(*Error)(nil).Error() = %q, want DICOMweb error", got)
	}
}

func TestEndpointAllowsBlankServicePaths(t *testing.T) {
	endpoint := Endpoint{BaseURL: "https://pacs.example.test/dicom-web"}

	qido, err := endpoint.StudySearchURL(nil)
	if err != nil {
		t.Fatalf("StudySearchURL: %v", err)
	}
	if got, want := qido.Path, "/dicom-web/studies"; got != want {
		t.Fatalf("QIDO path = %q, want %q", got, want)
	}

	stow, err := endpoint.StoreStudiesURL()
	if err != nil {
		t.Fatalf("StoreStudiesURL: %v", err)
	}
	if got, want := stow.Path, "/dicom-web/studies"; got != want {
		t.Fatalf("STOW path = %q, want %q", got, want)
	}
}

func TestSearchStudiesSendsDICOMJSONAcceptBasicAuthAndReturnsRawDatasets(t *testing.T) {
	var sawAccept, sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/dicom-web/qido/studies"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("PatientName"); got != "DOE^JANE" {
			t.Fatalf("PatientName query = %q", got)
		}
		sawAccept = strings.Contains(r.Header.Get("Accept"), "application/dicom+json")
		user, pass, ok := r.BasicAuth()
		sawAuth = ok && user == "alice" && pass == "secret"
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`[
			{
				"00100010": {"vr": "PN", "Value": [{"Alphabetic": "DOE^JANE"}]},
				"00100020": {"vr": "LO", "Value": ["P123"]},
				"0020000D": {"vr": "UI", "Value": ["1.2.3"]}
			}
		]`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", QIDOPath: "qido"},
		Options: Options{
			HTTPClient:    server.Client(),
			BasicUsername: "alice",
			BasicPassword: "secret",
		},
	}
	datasets, err := client.SearchStudies(context.Background(), url.Values{"PatientName": {"DOE^JANE"}})
	if err != nil {
		t.Fatalf("SearchStudies: %v", err)
	}
	if !sawAccept {
		t.Fatal("SearchStudies did not send DICOM JSON Accept header")
	}
	if !sawAuth {
		t.Fatal("SearchStudies did not send Basic auth")
	}
	if len(datasets) != 1 {
		t.Fatalf("datasets = %d, want 1", len(datasets))
	}
	if got := datasets[0]["00100020"].Value[0]; got != "P123" {
		t.Fatalf("PatientID element = %#v, want P123", got)
	}
	if got := datasets[0]["00100010"].Value[0].(map[string]any)["Alphabetic"]; got != "DOE^JANE" {
		t.Fatalf("PatientName element = %#v, want DOE^JANE", got)
	}
}

func TestSearchSeriesSendsDICOMJSONAcceptBasicAuthAndReturnsRawDatasets(t *testing.T) {
	var sawAccept, sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/dicom-web/qido/studies/1.2.3/series"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("Modality"); got != "CT" {
			t.Fatalf("Modality query = %q", got)
		}
		sawAccept = strings.Contains(r.Header.Get("Accept"), "application/dicom+json")
		user, pass, ok := r.BasicAuth()
		sawAuth = ok && user == "alice" && pass == "secret"
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`[
			{
				"00080060": {"vr": "CS", "Value": ["CT"]},
				"0020000E": {"vr": "UI", "Value": ["1.2.3.4"]}
			}
		]`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", QIDOPath: "qido"},
		Options: Options{
			HTTPClient:    server.Client(),
			BasicUsername: "alice",
			BasicPassword: "secret",
		},
	}
	datasets, err := client.SearchSeries(context.Background(), "1.2.3", url.Values{"Modality": {"CT"}})
	if err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	if !sawAccept {
		t.Fatal("SearchSeries did not send DICOM JSON Accept header")
	}
	if !sawAuth {
		t.Fatal("SearchSeries did not send Basic auth")
	}
	if len(datasets) != 1 {
		t.Fatalf("datasets = %d, want 1", len(datasets))
	}
	if got := datasets[0]["0020000E"].Value[0]; got != "1.2.3.4" {
		t.Fatalf("SeriesInstanceUID element = %#v, want 1.2.3.4", got)
	}
}

func TestSearchInstancesSendsDICOMJSONAcceptBasicAuthAndReturnsRawDatasets(t *testing.T) {
	var sawAccept, sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/dicom-web/qido/studies/1.2.3/series/1.2.3.4/instances"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("SOPInstanceUID"); got != "1.2.3.4.5" {
			t.Fatalf("SOPInstanceUID query = %q", got)
		}
		sawAccept = strings.Contains(r.Header.Get("Accept"), "application/dicom+json")
		user, pass, ok := r.BasicAuth()
		sawAuth = ok && user == "alice" && pass == "secret"
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`[
			{
				"00080018": {"vr": "UI", "Value": ["1.2.3.4.5"]},
				"00200013": {"vr": "IS", "Value": [7]}
			}
		]`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", QIDOPath: "qido"},
		Options: Options{
			HTTPClient:    server.Client(),
			BasicUsername: "alice",
			BasicPassword: "secret",
		},
	}
	datasets, err := client.SearchInstances(context.Background(), "1.2.3", "1.2.3.4", url.Values{"SOPInstanceUID": {"1.2.3.4.5"}})
	if err != nil {
		t.Fatalf("SearchInstances: %v", err)
	}
	if !sawAccept {
		t.Fatal("SearchInstances did not send DICOM JSON Accept header")
	}
	if !sawAuth {
		t.Fatal("SearchInstances did not send Basic auth")
	}
	if len(datasets) != 1 {
		t.Fatalf("datasets = %d, want 1", len(datasets))
	}
	if got := datasets[0]["00080018"].Value[0]; got != "1.2.3.4.5" {
		t.Fatalf("SOPInstanceUID element = %#v, want 1.2.3.4.5", got)
	}
}

func TestClientUsesExistingContextDeadlineWhenTimeoutUnset(t *testing.T) {
	var seenDeadline time.Duration
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("request context has no deadline")
		}
		seenDeadline = time.Until(deadline)
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	client := Client{
		Endpoint: Endpoint{BaseURL: "https://pacs.example.test/dicom-web"},
		Options:  Options{HTTPClient: httpClient},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	if _, err := client.SearchStudies(ctx, nil); err != nil {
		t.Fatalf("SearchStudies: %v", err)
	}
	if seenDeadline < 50*time.Minute {
		t.Fatalf("deadline shortened to %s, want caller deadline near one hour", seenDeadline)
	}
}

func TestSearchStudiesSendsBearerAuth(t *testing.T) {
	var sawBearer bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawBearer = r.Header.Get("Authorization") == "Bearer token-1"
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL},
		Options:  Options{HTTPClient: server.Client(), BearerToken: "token-1"},
	}
	if _, err := client.SearchStudies(context.Background(), nil); err != nil {
		t.Fatalf("SearchStudies: %v", err)
	}
	if !sawBearer {
		t.Fatal("SearchStudies did not send Bearer auth")
	}
}

func TestSearchSeriesSendsBearerAuth(t *testing.T) {
	var sawBearer bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawBearer = r.Header.Get("Authorization") == "Bearer token-1"
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL},
		Options:  Options{HTTPClient: server.Client(), BearerToken: "token-1"},
	}
	if _, err := client.SearchSeries(context.Background(), "1.2.3", nil); err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	if !sawBearer {
		t.Fatal("SearchSeries did not send Bearer auth")
	}
}

func TestSearchInstancesSendsBearerAuth(t *testing.T) {
	var sawBearer bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawBearer = r.Header.Get("Authorization") == "Bearer token-1"
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL},
		Options:  Options{HTTPClient: server.Client(), BearerToken: "token-1"},
	}
	if _, err := client.SearchInstances(context.Background(), "1.2.3", "1.2.3.4", nil); err != nil {
		t.Fatalf("SearchInstances: %v", err)
	}
	if !sawBearer {
		t.Fatal("SearchInstances did not send Bearer auth")
	}
}

func TestSearchStudiesTreatsNoContentAndEmptyBodyAsEmptySuccess(t *testing.T) {
	statuses := []int{http.StatusNoContent, http.StatusOK}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.URL.Path, "/qido/studies"; got != want {
					t.Fatalf("path = %q, want %q", got, want)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := Client{
				Endpoint: Endpoint{BaseURL: server.URL, QIDOPath: "qido"},
				Options:  Options{HTTPClient: server.Client()},
			}
			datasets, err := client.SearchStudies(context.Background(), nil)
			if err != nil {
				t.Fatalf("SearchStudies: %v", err)
			}
			if len(datasets) != 0 {
				t.Fatalf("datasets = %d, want empty success", len(datasets))
			}
		})
	}
}

func TestSearchSeriesTreatsNoContentAndEmptyBodyAsEmptySuccess(t *testing.T) {
	statuses := []int{http.StatusNoContent, http.StatusOK}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.URL.Path, "/qido/studies/1.2.3/series"; got != want {
					t.Fatalf("path = %q, want %q", got, want)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := Client{
				Endpoint: Endpoint{BaseURL: server.URL, QIDOPath: "qido"},
				Options:  Options{HTTPClient: server.Client()},
			}
			datasets, err := client.SearchSeries(context.Background(), "1.2.3", nil)
			if err != nil {
				t.Fatalf("SearchSeries: %v", err)
			}
			if len(datasets) != 0 {
				t.Fatalf("datasets = %d, want empty success", len(datasets))
			}
		})
	}
}

func TestSearchInstancesTreatsNoContentAndEmptyBodyAsEmptySuccess(t *testing.T) {
	statuses := []int{http.StatusNoContent, http.StatusOK}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.URL.Path, "/qido/studies/1.2.3/series/1.2.3.4/instances"; got != want {
					t.Fatalf("path = %q, want %q", got, want)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := Client{
				Endpoint: Endpoint{BaseURL: server.URL, QIDOPath: "qido"},
				Options:  Options{HTTPClient: server.Client()},
			}
			datasets, err := client.SearchInstances(context.Background(), "1.2.3", "1.2.3.4", nil)
			if err != nil {
				t.Fatalf("SearchInstances: %v", err)
			}
			if len(datasets) != 0 {
				t.Fatalf("datasets = %d, want empty success", len(datasets))
			}
		})
	}
}

func TestSearchStudiesReturnsTypedHTTPStatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantKind   ErrorKind
	}{
		{name: "not found", statusCode: http.StatusNotFound, wantKind: ErrorKindHTTPStatus},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantKind: ErrorKindAuthStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, http.StatusText(tt.statusCode), tt.statusCode)
			}), Endpoint{QIDOPath: "qido"}, Options{})

			_, err := client.SearchStudies(context.Background(), nil)
			var derr *Error
			if !errors.As(err, &derr) {
				t.Fatalf("error type = %T, want *Error", err)
			}
			if derr.Kind != tt.wantKind || derr.StatusCode != tt.statusCode {
				t.Fatalf("typed error = %+v, want kind %q status %d", derr, tt.wantKind, tt.statusCode)
			}
		})
	}
}

func TestSearchSeriesReturnsTypedHTTPStatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantKind   ErrorKind
	}{
		{name: "not found", statusCode: http.StatusNotFound, wantKind: ErrorKindHTTPStatus},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantKind: ErrorKindAuthStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, http.StatusText(tt.statusCode), tt.statusCode)
			}), Endpoint{QIDOPath: "qido"}, Options{})

			_, err := client.SearchSeries(context.Background(), "1.2.3", nil)
			var derr *Error
			if !errors.As(err, &derr) {
				t.Fatalf("error type = %T, want *Error", err)
			}
			if derr.Kind != tt.wantKind || derr.StatusCode != tt.statusCode {
				t.Fatalf("typed error = %+v, want kind %q status %d", derr, tt.wantKind, tt.statusCode)
			}
		})
	}
}

func TestSearchInstancesReturnsTypedHTTPStatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantKind   ErrorKind
	}{
		{name: "not found", statusCode: http.StatusNotFound, wantKind: ErrorKindHTTPStatus},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantKind: ErrorKindAuthStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, http.StatusText(tt.statusCode), tt.statusCode)
			}), Endpoint{QIDOPath: "qido"}, Options{})

			_, err := client.SearchInstances(context.Background(), "1.2.3", "1.2.3.4", nil)
			var derr *Error
			if !errors.As(err, &derr) {
				t.Fatalf("error type = %T, want *Error", err)
			}
			if derr.Kind != tt.wantKind || derr.StatusCode != tt.statusCode {
				t.Fatalf("typed error = %+v, want kind %q status %d", derr, tt.wantKind, tt.statusCode)
			}
		})
	}
}

func TestSearchSeriesRejectsMalformedDICOMJSON(t *testing.T) {
	client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`not-json`))
	}), Endpoint{QIDOPath: "qido"}, Options{})

	_, err := client.SearchSeries(context.Background(), "1.2.3", nil)
	assertErrorKind(t, err, ErrorKindDecodeResponse)
}

func TestSearchInstancesRejectsMalformedDICOMJSON(t *testing.T) {
	client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`not-json`))
	}), Endpoint{QIDOPath: "qido"}, Options{})

	_, err := client.SearchInstances(context.Background(), "1.2.3", "1.2.3.4", nil)
	assertErrorKind(t, err, ErrorKindDecodeResponse)
}

func TestVerifyTreatsNoContentAndEmptyBodyAsSuccess(t *testing.T) {
	statuses := []int{http.StatusNoContent, http.StatusOK}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.URL.Path, "/qido/studies"; got != want {
					t.Fatalf("path = %q, want %q", got, want)
				}
				if got := r.URL.Query().Get("limit"); got != "1" {
					t.Fatalf("limit = %q, want 1", got)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := Client{
				Endpoint: Endpoint{BaseURL: server.URL, QIDOPath: "qido"},
				Options:  Options{HTTPClient: server.Client()},
			}
			result, err := client.Verify(context.Background())
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if result.StatusCode != status {
				t.Fatalf("StatusCode = %d, want %d", result.StatusCode, status)
			}
		})
	}
}

func TestVerifyReturnsTypedHTTPAuthDecodeTimeoutAndEndpointErrors(t *testing.T) {
	t.Run("http status", func(t *testing.T) {
		client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "missing", http.StatusNotFound)
		}), Endpoint{QIDOPath: "qido"}, Options{})

		_, err := client.Verify(context.Background())
		assertErrorKind(t, err, ErrorKindHTTPStatus)
	})

	t.Run("auth status", func(t *testing.T) {
		client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}), Endpoint{QIDOPath: "qido"}, Options{})

		_, err := client.Verify(context.Background())
		assertErrorKind(t, err, ErrorKindAuthStatus)
	})

	t.Run("decode response", func(t *testing.T) {
		client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/dicom+json")
			_, _ = w.Write([]byte(`not-json`))
		}), Endpoint{QIDOPath: "qido"}, Options{})

		_, err := client.Verify(context.Background())
		assertErrorKind(t, err, ErrorKindDecodeResponse)
	})

	t.Run("timeout", func(t *testing.T) {
		client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(`[]`))
		}), Endpoint{QIDOPath: "qido"}, Options{Timeout: 5 * time.Millisecond})

		_, err := client.Verify(context.Background())
		assertErrorKind(t, err, ErrorKindTimeout)
	})

	t.Run("invalid endpoint", func(t *testing.T) {
		client := Client{Endpoint: Endpoint{BaseURL: "ftp://pacs.example.test"}}

		_, err := client.Verify(context.Background())
		assertErrorKind(t, err, ErrorKindInvalidEndpoint)
	})

	t.Run("request failure", func(t *testing.T) {
		httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network down")
		})}
		client := Client{
			Endpoint: Endpoint{BaseURL: "https://pacs.example.test"},
			Options:  Options{HTTPClient: httpClient},
		}

		_, err := client.Verify(context.Background())
		assertErrorKind(t, err, ErrorKindRequestFailure)
	})
}

func TestDICOMJSONParsingRejectsTrailingGarbage(t *testing.T) {
	t.Run("QIDO search", func(t *testing.T) {
		client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/dicom+json")
			_, _ = w.Write([]byte(`[] trailing`))
		}), Endpoint{QIDOPath: "qido"}, Options{})

		_, err := client.SearchStudies(context.Background(), nil)
		assertErrorKind(t, err, ErrorKindDecodeResponse)
	})

	t.Run("verify", func(t *testing.T) {
		client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/dicom+json")
			_, _ = w.Write([]byte(`[] trailing`))
		}), Endpoint{QIDOPath: "qido"}, Options{})

		_, err := client.Verify(context.Background())
		assertErrorKind(t, err, ErrorKindDecodeResponse)
	})

	t.Run("STOW result", func(t *testing.T) {
		_, err := StoreResultFromDICOMJSON([]byte(`{"00081199":{"vr":"SQ","Value":[]}} trailing`))
		if err == nil {
			t.Fatal("StoreResultFromDICOMJSON error = nil, want trailing JSON error")
		}
	})
}

func TestWADOMetadataParsesStudyAndSeriesInstanceReferences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "application/dicom+json") {
			t.Fatalf("Accept = %q, want DICOM JSON", r.Header.Get("Accept"))
		}
		switch r.URL.Path {
		case "/dicom-web/wado/studies/1.2.3/metadata":
			_, _ = w.Write([]byte(`[
				{
					"0020000D": {"vr": "UI", "Value": ["1.2.3"]},
					"0020000E": {"vr": "UI", "Value": ["1.2.3.4"]},
					"00080018": {"vr": "UI", "Value": ["1.2.3.4.5"]}
				}
			]`))
		case "/dicom-web/wado/studies/1.2.3/series/1.2.3.4/metadata":
			_, _ = w.Write([]byte(`[
				{
					"00080018": {"vr": "UI", "Value": ["1.2.3.4.6"]}
				}
			]`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", WADOPath: "wado"},
		Options:  Options{HTTPClient: server.Client()},
	}
	studyRefs, err := client.StudyMetadata(context.Background(), "1.2.3")
	if err != nil {
		t.Fatalf("StudyMetadata: %v", err)
	}
	if len(studyRefs) != 1 || studyRefs[0] != (InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.4", SOPInstanceUID: "1.2.3.4.5"}) {
		t.Fatalf("study refs = %+v", studyRefs)
	}

	seriesRefs, err := client.SeriesMetadata(context.Background(), "1.2.3", "1.2.3.4")
	if err != nil {
		t.Fatalf("SeriesMetadata: %v", err)
	}
	if len(seriesRefs) != 1 || seriesRefs[0] != (InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.4", SOPInstanceUID: "1.2.3.4.6"}) {
		t.Fatalf("series refs = %+v", seriesRefs)
	}
}

func TestRetrieveInstanceParsesSingleAndMultipartDICOMPayloads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "application/dicom") {
			t.Fatalf("Accept = %q, want DICOM object", r.Header.Get("Accept"))
		}
		switch r.URL.Path {
		case "/dicom-web/wado/studies/1/series/2/instances/3":
			w.Header().Set("Content-Type", "application/dicom")
			_, _ = w.Write([]byte("single-object"))
		case "/dicom-web/wado/studies/1/series/2/instances/4":
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreatePart(map[string][]string{"Content-Type": {"application/dicom"}})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write([]byte("part-one"))
			part, err = writer.CreatePart(map[string][]string{"Content-Type": {"application/dicom"}})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write([]byte("part-two"))
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", `multipart/related; type="application/dicom"; boundary=`+writer.Boundary())
			_, _ = w.Write(body.Bytes())
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", WADOPath: "wado"},
		Options:  Options{HTTPClient: server.Client()},
	}
	single, err := client.RetrieveInstance(context.Background(), InstanceRef{StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "3"})
	if err != nil {
		t.Fatalf("RetrieveInstance single: %v", err)
	}
	if len(single) != 1 || string(single[0].Data) != "single-object" || single[0].ContentType != "application/dicom" {
		t.Fatalf("single parts = %+v", single)
	}

	multi, err := client.RetrieveInstance(context.Background(), InstanceRef{StudyInstanceUID: "1", SeriesInstanceUID: "2", SOPInstanceUID: "4"})
	if err != nil {
		t.Fatalf("RetrieveInstance multipart: %v", err)
	}
	if got, want := len(multi), 2; got != want {
		t.Fatalf("multipart parts = %d, want %d", got, want)
	}
	if string(multi[0].Data) != "part-one" || string(multi[1].Data) != "part-two" {
		t.Fatalf("multipart data = %+v", multi)
	}
}

func TestRetrieveInstanceWithOptionsRequestsPreferredTransferSyntaxAndReportsPartSyntax(t *testing.T) {
	var sawAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAccept = r.Header.Get("Accept")
		if !strings.Contains(sawAccept, `transfer-syntax=1.2.840.10008.1.2.1`) {
			t.Fatalf("Accept = %q, want preferred transfer syntax parameter", sawAccept)
		}
		if !strings.Contains(sawAccept, `transfer-syntax=*`) {
			t.Fatalf("Accept = %q, want wildcard preservation fallback", sawAccept)
		}
		w.Header().Set("Content-Type", `application/dicom; transfer-syntax=1.2.840.10008.1.2.4.70`)
		_, _ = w.Write([]byte("dicom-object"))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", WADOPath: "wado"},
		Options:  Options{HTTPClient: server.Client()},
	}
	parts, err := client.RetrieveInstanceWithOptions(context.Background(), InstanceRef{
		StudyInstanceUID:  "1",
		SeriesInstanceUID: "2",
		SOPInstanceUID:    "3",
	}, RetrieveOptions{
		TransferSyntaxUIDs: []string{"1.2.840.10008.1.2.1", "*"},
	})
	if err != nil {
		t.Fatalf("RetrieveInstanceWithOptions: %v", err)
	}
	if sawAccept == "" {
		t.Fatal("server did not observe WADO Accept header")
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	if parts[0].TransferSyntaxUID != "1.2.840.10008.1.2.4.70" {
		t.Fatalf("TransferSyntaxUID = %q, want response content-type transfer syntax", parts[0].TransferSyntaxUID)
	}
}

func TestRetrieveInstanceStreamWithOptionsPassesResponseReaderToHandler(t *testing.T) {
	var sawAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", `application/dicom; transfer-syntax=1.2.840.10008.1.2.1`)
		_, _ = w.Write([]byte("dicom-object"))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", WADOPath: "wado"},
		Options:  Options{HTTPClient: server.Client()},
	}
	var got ObjectPartStream
	err := client.RetrieveInstanceStreamWithOptions(context.Background(), InstanceRef{
		StudyInstanceUID:  "1",
		SeriesInstanceUID: "2",
		SOPInstanceUID:    "3",
	}, RetrieveOptions{TransferSyntaxUIDs: []string{"1.2.840.10008.1.2.1"}}, func(part ObjectPartStream) error {
		data, err := io.ReadAll(part.Reader)
		if err != nil {
			return err
		}
		part.Reader = bytes.NewReader(data)
		got = part
		return nil
	})
	if err != nil {
		t.Fatalf("RetrieveInstanceStreamWithOptions: %v", err)
	}
	if !strings.Contains(sawAccept, `transfer-syntax=1.2.840.10008.1.2.1`) {
		t.Fatalf("Accept = %q, want preferred transfer syntax", sawAccept)
	}
	if got.TransferSyntaxUID != "1.2.840.10008.1.2.1" {
		t.Fatalf("TransferSyntaxUID = %q, want response content-type transfer syntax", got.TransferSyntaxUID)
	}
	data, err := io.ReadAll(got.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "dicom-object" {
		t.Fatalf("streamed payload = %q, want dicom-object", data)
	}
}

func TestWADOReturnsTypedHTTPStatusErrors(t *testing.T) {
	tests := []struct {
		name string
		call func(Client) error
	}{
		{name: "study metadata", call: func(c Client) error {
			_, err := c.StudyMetadata(context.Background(), "1.2.3")
			return err
		}},
		{name: "series metadata", call: func(c Client) error {
			_, err := c.SeriesMetadata(context.Background(), "1.2.3", "1.2.3.4")
			return err
		}},
		{name: "retrieve instance", call: func(c Client) error {
			_, err := c.RetrieveInstance(context.Background(), InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.4", SOPInstanceUID: "1.2.3.4.5"})
			return err
		}},
		{name: "retrieve stream", call: func(c Client) error {
			return c.RetrieveInstanceStreamWithOptions(context.Background(), InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.4", SOPInstanceUID: "1.2.3.4.5"}, RetrieveOptions{}, func(part ObjectPartStream) error {
				_, err := io.Copy(io.Discard, part.Reader)
				return err
			})
		}},
	}
	statuses := []struct {
		code int
		kind ErrorKind
	}{
		{code: http.StatusNotFound, kind: ErrorKindHTTPStatus},
		{code: http.StatusUnauthorized, kind: ErrorKindAuthStatus},
	}
	for _, tt := range tests {
		for _, status := range statuses {
			t.Run(tt.name+"/"+http.StatusText(status.code), func(t *testing.T) {
				client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, http.StatusText(status.code), status.code)
				}), Endpoint{WADOPath: "wado"}, Options{})

				err := tt.call(client)
				var derr *Error
				if !errors.As(err, &derr) {
					t.Fatalf("error type = %T, want *Error", err)
				}
				if derr.Kind != status.kind || derr.StatusCode != status.code {
					t.Fatalf("typed error = %+v, want kind %q status %d", derr, status.kind, status.code)
				}
			})
		}
	}
}

func TestWADOHonorsMaxBodyBytes(t *testing.T) {
	tests := []struct {
		name string
		call func(Client) error
	}{
		{name: "study metadata", call: func(c Client) error {
			_, err := c.StudyMetadata(context.Background(), "1.2.3")
			return err
		}},
		{name: "retrieve instance", call: func(c Client) error {
			_, err := c.RetrieveInstance(context.Background(), InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.4", SOPInstanceUID: "1.2.3.4.5"})
			return err
		}},
		{name: "retrieve stream", call: func(c Client) error {
			return c.RetrieveInstanceStreamWithOptions(context.Background(), InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.4", SOPInstanceUID: "1.2.3.4.5"}, RetrieveOptions{}, func(part ObjectPartStream) error {
				_, err := io.Copy(io.Discard, part.Reader)
				return err
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("response-body-too-large"))
			}), Endpoint{WADOPath: "wado"}, Options{MaxBodyBytes: 4})

			err := tt.call(client)
			assertErrorKind(t, err, ErrorKindRequestFailure)
		})
	}
}

func TestStoreInstancesBuildsMultipartRequestAndParsesStoredAndFailedItems(t *testing.T) {
	var requestParts [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/dicom-web/stow/studies"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if !strings.Contains(r.Header.Get("Accept"), "application/dicom+json") {
			t.Fatalf("Accept = %q, want DICOM JSON", r.Header.Get("Accept"))
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("Content-Type parse: %v", err)
		}
		if mediaType != "multipart/related" || params["type"] != "application/dicom" || params["boundary"] == "" {
			t.Fatalf("Content-Type = %q, params=%v", mediaType, params)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			if got := part.Header.Get("Content-Type"); got != "application/dicom" {
				t.Fatalf("part Content-Type = %q, want application/dicom", got)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("ReadAll part: %v", err)
			}
			requestParts = append(requestParts, data)
		}
		w.Header().Set("Content-Type", "application/dicom+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"00081199": {"vr": "SQ", "Value": [
				{
					"00081150": {"vr": "UI", "Value": ["1.2.840.10008.5.1.4.1.1.2"]},
					"00081155": {"vr": "UI", "Value": ["1.2.3"]},
					"00081190": {"vr": "UR", "Value": ["https://pacs.example.test/wado/1.2.3"]}
				}
			]},
			"00081198": {"vr": "SQ", "Value": [
				{
					"00081150": {"vr": "UI", "Value": ["1.2.840.10008.5.1.4.1.1.4"]},
					"00081155": {"vr": "UI", "Value": ["1.2.4"]},
					"00081197": {"vr": "US", "Value": ["0x0110"]}
				}
			]}
		}`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", STOWPath: "stow/studies"},
		Options:  Options{HTTPClient: server.Client()},
	}
	result, err := client.StoreInstances(context.Background(), []StoreInstance{
		{SOPInstanceUID: "1.2.3", Data: []byte("dicom-one")},
		{SOPInstanceUID: "1.2.4", Data: []byte("dicom-two")},
	})
	if err != nil {
		t.Fatalf("StoreInstances: %v", err)
	}
	if len(requestParts) != 2 || string(requestParts[0]) != "dicom-one" || string(requestParts[1]) != "dicom-two" {
		t.Fatalf("request parts = %q", requestParts)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", result.StatusCode, http.StatusOK)
	}
	if len(result.Stored) != 1 || result.Stored[0].SOPInstanceUID != "1.2.3" || result.Stored[0].RetrieveURL == "" {
		t.Fatalf("stored = %+v", result.Stored)
	}
	if len(result.Failed) != 1 || result.Failed[0].SOPInstanceUID != "1.2.4" || result.Failed[0].FailureReason != 0x0110 {
		t.Fatalf("failed = %+v", result.Failed)
	}
}

func TestStoreInstancesStreamsReaderPayload(t *testing.T) {
	var requestParts [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("Content-Type parse: %v", err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("ReadAll part: %v", err)
			}
			requestParts = append(requestParts, data)
		}
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`{
			"00081199": {"vr": "SQ", "Value": [
				{"00081155": {"vr": "UI", "Value": ["1.2.3"]}}
			]}
		}`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", STOWPath: "stow"},
		Options:  Options{HTTPClient: server.Client()},
	}
	_, err := client.StoreInstances(context.Background(), []StoreInstance{
		{SOPInstanceUID: "1.2.3", Reader: strings.NewReader("dicom-stream")},
	})
	if err != nil {
		t.Fatalf("StoreInstances reader payload: %v", err)
	}
	if len(requestParts) != 1 || string(requestParts[0]) != "dicom-stream" {
		t.Fatalf("request parts = %q, want reader payload", requestParts)
	}
}

func TestStoreInstancesStreamsOpenPayload(t *testing.T) {
	var requestParts [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("Content-Type parse: %v", err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("ReadAll part: %v", err)
			}
			requestParts = append(requestParts, data)
		}
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`{
			"00081199": {"vr": "SQ", "Value": [
				{"00081155": {"vr": "UI", "Value": ["1.2.3"]}}
			]}
		}`))
	}))
	defer server.Close()

	var opened int
	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", STOWPath: "stow"},
		Options:  Options{HTTPClient: server.Client()},
	}
	_, err := client.StoreInstances(context.Background(), []StoreInstance{
		{
			SOPInstanceUID: "1.2.3",
			Open: func() (io.ReadCloser, error) {
				opened++
				return io.NopCloser(strings.NewReader("dicom-open")), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("StoreInstances open payload: %v", err)
	}
	if opened != 1 {
		t.Fatalf("opened = %d, want 1", opened)
	}
	if len(requestParts) != 1 || string(requestParts[0]) != "dicom-open" {
		t.Fatalf("request parts = %q, want open payload", requestParts)
	}
}

func TestSTOWMultipartReaderDoesNotReadPayloadBeforeBodyIsRead(t *testing.T) {
	readCalled := make(chan struct{})
	readDone := false
	body, _, err := stowMultipartReader([]StoreInstance{
		{
			SOPInstanceUID: "1.2.3",
			Reader: readerFunc(func(p []byte) (int, error) {
				if !readDone {
					close(readCalled)
					readDone = true
					return copy(p, "dicom-lazy"), nil
				}
				return 0, io.EOF
			}),
		},
	})
	if err != nil {
		t.Fatalf("stowMultipartReader: %v", err)
	}
	defer body.Close()

	select {
	case <-readCalled:
		t.Fatal("payload reader was consumed before the multipart body was read")
	case <-time.After(20 * time.Millisecond):
	}

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll body: %v", err)
	}
	if !bytes.Contains(data, []byte("dicom-lazy")) {
		t.Fatalf("multipart body does not contain payload: %q", data)
	}
	select {
	case <-readCalled:
	default:
		t.Fatal("payload reader was not consumed after reading the multipart body")
	}
}

type readerFunc func([]byte) (int, error)

func (fn readerFunc) Read(p []byte) (int, error) {
	return fn(p)
}

func TestStoreInstancesEmptyInputIsNoop(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("StoreInstances sent unexpected request to %s", r.URL.Path)
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL + "/dicom-web", STOWPath: "stow"},
		Options:  Options{HTTPClient: server.Client()},
	}
	result, err := client.StoreInstances(context.Background(), nil)
	if err != nil {
		t.Fatalf("StoreInstances empty: %v", err)
	}
	if called {
		t.Fatal("StoreInstances empty should not issue a STOW-RS request")
	}
	if result.StatusCode != 0 || len(result.Stored) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want zero-value no-op result", result)
	}
}

func TestStoreInstancesRejectsEmptyObjectData(t *testing.T) {
	client := Client{Endpoint: Endpoint{BaseURL: "https://pacs.example.test/dicom-web", STOWPath: "stow"}}
	_, err := client.StoreInstances(context.Background(), []StoreInstance{{SOPInstanceUID: "1.2.3"}})
	if err == nil {
		t.Fatal("StoreInstances accepted an empty DICOM object")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want empty object message", err)
	}
}

func TestStoreInstancesHTTPStatusPreservesFailedSequence(t *testing.T) {
	client := verifyClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dicom+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"00081198": {"vr": "SQ", "Value": [
				{
					"00081150": {"vr": "UI", "Value": ["1.2.840.10008.5.1.4.1.1.2"]},
					"00081155": {"vr": "UI", "Value": ["1.2.3"]},
					"00081197": {"vr": "US", "Value": [49442]}
				}
			]}
		}`))
	}), Endpoint{STOWPath: "stow"}, Options{})

	result, err := client.StoreInstances(context.Background(), []StoreInstance{
		{SOPInstanceUID: "1.2.3", Data: []byte("dicom")},
	})
	var derr *Error
	if !errors.As(err, &derr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if derr.Kind != ErrorKindHTTPStatus || derr.StatusCode != http.StatusConflict {
		t.Fatalf("typed error = %+v, want HTTP conflict", derr)
	}
	if result.StatusCode != http.StatusConflict {
		t.Fatalf("StatusCode = %d, want %d", result.StatusCode, http.StatusConflict)
	}
	if len(result.Failed) != 1 || result.Failed[0].SOPInstanceUID != "1.2.3" || result.Failed[0].FailureReason != 0xC122 {
		t.Fatalf("failed sequence = %+v, want SOP 1.2.3 failure 0xC122", result.Failed)
	}
}

func verifyClient(t *testing.T, handler http.Handler, endpoint Endpoint, opts Options) Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	endpoint.BaseURL = server.URL
	opts.HTTPClient = server.Client()
	return Client{Endpoint: endpoint, Options: opts}
}

func assertErrorKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want kind %q", want)
	}
	var derr *Error
	if !errors.As(err, &derr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if derr.Kind != want {
		t.Fatalf("error kind = %q, want %q (error: %v)", derr.Kind, want, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
