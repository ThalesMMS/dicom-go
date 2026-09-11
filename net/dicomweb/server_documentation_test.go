package dicomweb

import (
	"os"
	"strings"
	"testing"
)

func TestServerDocumentationCoversHTTPContract(t *testing.T) {
	data, err := os.ReadFile("../../docs/DICOMWEB_SERVER.md")
	if err != nil {
		t.Fatal(err)
	}
	document := string(data)
	for _, token := range []string{
		"GET /studies", "GET /series", "GET /instances",
		"GET /studies/{study}/series", "GET /studies/{study}/instances",
		"GET /studies/{study}/series/{series}/instances",
		"/metadata", "/frames/{frames}", "GET /bulkdata/{token...}",
		"POST /studies", "POST /studies/{study}",
		"application/dicom+json", "multipart/related", "application/dicom",
		"`200`", "`202`", "`400`", "`401`", "`403`", "`404`", "`405`",
		"`406`", "`409`", "`413`", "`415`", "`499`", "`500`", "`504`",
		"AllowUnauthenticated", "Authorize", "CORS", "reverse proxy", "TLS",
		"MaxRequestBytes", "MaxPartBytes", "MaxDuration", "SpoolDirectory",
		"PHI", "server_hardening_test.go", "server_characterization_test.go",
	} {
		if !strings.Contains(document, token) {
			t.Errorf("DICOMweb server documentation is missing %q", token)
		}
	}
}
