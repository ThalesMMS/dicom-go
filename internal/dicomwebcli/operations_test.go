package dicomwebcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	dicomwebCLITestStudyUID    = "1.2.826.0.1.3680043.9.7433.833.1"
	dicomwebCLITestSeriesUID   = "1.2.826.0.1.3680043.9.7433.833.1.1"
	dicomwebCLITestInstanceUID = "1.2.826.0.1.3680043.9.7433.833.1.1.1"
)

func TestVerifyUsesConfiguredQIDOPathAndWritesJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/root/qido/studies" || r.URL.Query().Get("limit") != "1" {
			t.Fatalf("request=%s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := RunWithContext(context.Background(), []string{"verify", "-base-url", server.URL + "/root", "-qido-path", "qido"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result verifyOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.StatusCode != http.StatusOK || result.DurationMS < 0 {
		t.Fatalf("verify output=%q result=%+v err=%v", stdout.String(), result, err)
	}
}

func TestVerifyDistinguishesAuthHTTPAndDecodeFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{name: "auth", status: http.StatusUnauthorized, body: `{}`, want: ExitAuth},
		{name: "HTTP", status: http.StatusNotFound, body: `{}`, want: ExitHTTP},
		{name: "decode", status: http.StatusOK, body: `{not-json`, want: ExitDecode},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/dicom+json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			code := RunWithContext(context.Background(), []string{"verify", "-base-url", server.URL}, &stdout, &stderr)
			if code != test.want {
				t.Fatalf("code=%d, want %d; stdout=%q stderr=%q", code, test.want, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "endpoint="+server.URL) || strings.Contains(stderr.String(), "/studies") {
				t.Fatalf("diagnostic did not retain only the safe endpoint origin: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRetrieveMalformedMultipartMapsToDecodeAndCleansOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", `multipart/related; type="application/dicom"`)
		_, _ = w.Write([]byte("malformed multipart"))
	}))
	defer server.Close()

	output := t.TempDir()
	args := []string{"retrieve", "-base-url", server.URL, "-output", output,
		"-study-uid", dicomwebCLITestStudyUID, "study"}
	var stdout, stderr bytes.Buffer
	if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitDecode {
		t.Fatalf("code=%d, want %d; stdout=%q stderr=%q", code, ExitDecode, stdout.String(), stderr.String())
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 {
		t.Fatalf("output entries=%v err=%v", entries, err)
	}
}

func TestQIDOStudiesSeriesInstancesAndRepeatedQuery(t *testing.T) {
	requests := make(chan *http.Request, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	for _, test := range []struct {
		name     string
		resource string
		extra    []string
		wantPath string
	}{
		{name: "studies", resource: "studies", wantPath: "/qido/studies"},
		{name: "series", resource: "series", extra: []string{"-study-uid", dicomwebCLITestStudyUID}, wantPath: "/qido/studies/" + dicomwebCLITestStudyUID + "/series"},
		{name: "instances", resource: "instances", extra: []string{"-study-uid", dicomwebCLITestStudyUID, "-series-uid", dicomwebCLITestSeriesUID}, wantPath: "/qido/studies/" + dicomwebCLITestStudyUID + "/series/" + dicomwebCLITestSeriesUID + "/instances"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"query", "-base-url", server.URL, "-qido-path", "qido", "-query", "PatientID=SYNTHETIC-A", "-query", "PatientID=SYNTHETIC-B"}
			args = append(args, test.extra...)
			args = append(args, test.resource)
			var stdout, stderr bytes.Buffer
			if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitOK {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			request := <-requests
			if request.URL.Path != test.wantPath {
				t.Fatalf("path=%q, want %q", request.URL.Path, test.wantPath)
			}
			if got := request.URL.Query()["PatientID"]; len(got) != 2 || got[0] != "SYNTHETIC-A" || got[1] != "SYNTHETIC-B" {
				t.Fatalf("repeated query=%v", got)
			}
			if strings.TrimSpace(stdout.String()) != "[]" {
				t.Fatalf("stdout=%q", stdout.String())
			}
		})
	}
}

func TestWADORetrieveInstanceStudyAndSeriesStreamsToOrdinalFiles(t *testing.T) {
	for _, test := range []struct {
		resource string
		extra    []string
		wantPath string
		parts    [][]byte
	}{
		{resource: "instance", extra: []string{"-study-uid", dicomwebCLITestStudyUID, "-series-uid", dicomwebCLITestSeriesUID, "-instance-uid", dicomwebCLITestInstanceUID}, wantPath: "/wado/studies/" + dicomwebCLITestStudyUID + "/series/" + dicomwebCLITestSeriesUID + "/instances/" + dicomwebCLITestInstanceUID, parts: [][]byte{[]byte("instance-part")}},
		{resource: "study", extra: []string{"-study-uid", dicomwebCLITestStudyUID}, wantPath: "/wado/studies/" + dicomwebCLITestStudyUID, parts: [][]byte{[]byte("study-one"), []byte("study-two")}},
		{resource: "series", extra: []string{"-study-uid", dicomwebCLITestStudyUID, "-series-uid", dicomwebCLITestSeriesUID}, wantPath: "/wado/studies/" + dicomwebCLITestStudyUID + "/series/" + dicomwebCLITestSeriesUID, parts: [][]byte{[]byte("series-one"), []byte("series-two")}},
	} {
		t.Run(test.resource, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.wantPath {
					t.Fatalf("path=%q want=%q", r.URL.Path, test.wantPath)
				}
				if !strings.Contains(r.Header.Get("Accept"), "1.2.840.10008.1.2.1") || !strings.Contains(r.Header.Get("Accept"), "1.2.840.10008.1.2") {
					t.Fatalf("Accept=%q", r.Header.Get("Accept"))
				}
				if len(test.parts) == 1 {
					w.Header().Set("Content-Type", "application/dicom")
					_, _ = w.Write(test.parts[0])
					return
				}
				dicomwebCLITestWriteMultipart(t, w, "application/dicom", test.parts)
			}))
			defer server.Close()

			output := t.TempDir()
			args := []string{"retrieve", "-base-url", server.URL, "-wado-path", "wado", "-output", output, "-transfer-syntax", "1.2.840.10008.1.2.1", "-transfer-syntax", "1.2.840.10008.1.2"}
			args = append(args, test.extra...)
			args = append(args, test.resource)
			var stdout, stderr bytes.Buffer
			if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitOK {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			for index, want := range test.parts {
				path := filepath.Join(output, fmt.Sprintf("part-%06d.dcm", index+1))
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("read %s=%q err=%v, want %q", path, got, err, want)
				}
			}
		})
	}
}

func TestWADOFramesWritesRequestedParts(t *testing.T) {
	parts := [][]byte{[]byte{0, 1, 2, 3}, []byte{255, 254, 253}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/wado/studies/" + dicomwebCLITestStudyUID + "/series/" + dicomwebCLITestSeriesUID + "/instances/" + dicomwebCLITestInstanceUID + "/frames/1,3"
		if r.URL.Path != want {
			t.Fatalf("path=%q want=%q", r.URL.Path, want)
		}
		dicomwebCLITestWriteMultipart(t, w, "application/octet-stream", parts)
	}))
	defer server.Close()

	output := t.TempDir()
	args := []string{"frames", "-base-url", server.URL, "-wado-path", "wado", "-output", output,
		"-study-uid", dicomwebCLITestStudyUID, "-series-uid", dicomwebCLITestSeriesUID, "-instance-uid", dicomwebCLITestInstanceUID, "-frames", "1,3"}
	var stdout, stderr bytes.Buffer
	if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for index, want := range parts {
		got, err := os.ReadFile(filepath.Join(output, fmt.Sprintf("part-%06d.bin", index+1)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("part %d=%v err=%v, want %v", index+1, got, err, want)
		}
	}
}

func TestWADOFramesLateCountFailureCleansEveryCreatedOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The first requested frame is valid and is published to the callback,
		// but the terminal count check must reject the missing second frame.
		dicomwebCLITestWriteMultipart(t, w, "application/octet-stream", [][]byte{[]byte("frame-one")})
	}))
	defer server.Close()

	output := t.TempDir()
	args := []string{"frames", "-base-url", server.URL, "-wado-path", "wado", "-output", output,
		"-study-uid", dicomwebCLITestStudyUID, "-series-uid", dicomwebCLITestSeriesUID,
		"-instance-uid", dicomwebCLITestInstanceUID, "-frames", "1,3"}
	var stdout, stderr bytes.Buffer
	if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitDecode {
		t.Fatalf("code=%d stderr=%q, want decode failure", code, stderr.String())
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 {
		t.Fatalf("output entries=%v err=%v, want rollback after late failure", entries, err)
	}
}

func TestRetrieveLimitFailureCleansEveryCreatedOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dicomwebCLITestWriteMultipart(t, w, "application/dicom", [][]byte{[]byte("one"), []byte("two")})
	}))
	defer server.Close()
	output := t.TempDir()
	args := []string{"retrieve", "-base-url", server.URL, "-output", output, "-max-parts", "1", "-study-uid", dicomwebCLITestStudyUID, "study"}
	var stdout, stderr bytes.Buffer
	if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitInput {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 {
		t.Fatalf("output entries=%v err=%v", entries, err)
	}
}

func TestResponseBodyLimitMapsToFailureExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := RunWithContext(context.Background(), []string{"verify", "-base-url", server.URL, "-max-body-bytes", "1"}, &stdout, &stderr)
	if code != ExitFailure {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSTOWSuccessAndPartialResult(t *testing.T) {
	part10, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "synthetic.dcm")
	if err := os.WriteFile(source, part10, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		status   int
		response string
		wantExit int
	}{
		{name: "success", status: http.StatusOK, response: dicomwebCLITestSTOWResponse(false), wantExit: ExitOK},
		{name: "partial", status: http.StatusAccepted, response: dicomwebCLITestSTOWResponse(true), wantExit: ExitPartialStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/stow/studies/"+dicomwebCLITestStudyUID {
					t.Fatalf("request=%s %s", r.Method, r.URL.Path)
				}
				reader, err := multipart.NewReader(r.Body, dicomwebCLITestBoundary(r.Header.Get("Content-Type"))).NextPart()
				if err != nil {
					t.Fatalf("read STOW part: %v", err)
				}
				data, _ := io.ReadAll(reader)
				if !bytes.Equal(data, part10) {
					t.Fatalf("STOW payload differs: got=%d want=%d", len(data), len(part10))
				}
				w.Header().Set("Content-Type", "application/dicom+json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			args := []string{"store", "-base-url", server.URL, "-stow-path", "stow", "-study-uid", dicomwebCLITestStudyUID, source}
			if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != test.wantExit {
				t.Fatalf("code=%d want=%d stdout=%q stderr=%q", code, test.wantExit, stdout.String(), stderr.String())
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d", requests.Load())
			}
			var summary storeSummary
			if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil || summary.StatusCode != test.status {
				t.Fatalf("summary=%+v err=%v output=%q", summary, err, stdout.String())
			}
		})
	}
}

func TestSTOWPreflightRejectsInvalidPart10AndLimitsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	directory := t.TempDir()
	invalid := filepath.Join(directory, "invalid.dcm")
	if err := os.WriteFile(invalid, []byte("not Part 10"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, extra := range [][]string{
		{invalid},
		{"-max-upload-bytes", "1", invalid},
		{"-max-files", "1", invalid, invalid},
	} {
		args := []string{"store", "-base-url", server.URL}
		args = append(args, extra...)
		var stdout, stderr bytes.Buffer
		if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitInput {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", extra, code, stdout.String(), stderr.String())
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("preflight issued %d HTTP requests", requests.Load())
	}
}

func TestSTOWPreflightRejectsExistingSummaryBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(dicomwebCLITestSTOWResponse(false)))
	}))
	defer server.Close()
	part10, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "synthetic.dcm")
	if err := os.WriteFile(source, part10, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "summary.json")
	if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunWithContext(context.Background(), []string{"store", "-base-url", server.URL, "-output", output, source}, &stdout, &stderr)
	if code != ExitInput || requests.Load() != 0 {
		t.Fatalf("code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing output changed: err=%v contents=%q", err, got)
	}
}

func dicomwebCLITestWriteMultipart(t *testing.T, w http.ResponseWriter, rootType string, bodies [][]byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for index, data := range bodies {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Type", rootType)
		header.Set("Content-Location", fmt.Sprintf("/synthetic/%d", index+1))
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(data)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", fmt.Sprintf(`multipart/related; type=%q; boundary=%q`, rootType, writer.Boundary()))
	_, _ = w.Write(body.Bytes())
}

func dicomwebCLITestBoundary(contentType string) string {
	_, after, _ := strings.Cut(contentType, "boundary=")
	return strings.Trim(after, `"`)
}

func dicomwebCLITestSTOWResponse(partial bool) string {
	stored := `"00081199":{"vr":"SQ","Value":[{"00081150":{"vr":"UI","Value":["` + dicomtest.TestSOPClassUID + `"]},"00081155":{"vr":"UI","Value":["` + dicomtest.TestSOPInstanceUID + `"]}}]}`
	if !partial {
		return "{" + stored + "}"
	}
	failedUID := dicomtest.TestSOPInstanceUID + ".2"
	failed := `"00081198":{"vr":"SQ","Value":[{"00081150":{"vr":"UI","Value":["` + dicomtest.TestSOPClassUID + `"]},"00081155":{"vr":"UI","Value":["` + failedUID + `"]},"00081197":{"vr":"US","Value":[272]}}]}`
	return "{" + stored + "," + failed + "}"
}
