package codecfixture

import "testing"

func TestCodecFullPerformanceEvidence(t *testing.T) {
	if err := validateCodecFullPerformance(
		"testdata/codecfull/performance/windows-amd64.json",
		"testdata/codecfull",
	); err != nil {
		t.Fatal(err)
	}
}

func TestPerformanceStudyFixtureRequiresCorpusIndexMatch(t *testing.T) {
	const fixture = "pydicom/example.dcm"
	fixtureIndex := map[string]string{fixture: "manifest-sha256"}

	if err := validatePerformanceStudyFixture(t.TempDir(), fixtureIndex, "pydicom/missing.dcm", "manifest-sha256"); err == nil {
		t.Fatal("missing corpus fixture was accepted")
	}
	if err := validatePerformanceStudyFixture(t.TempDir(), fixtureIndex, fixture, "report-sha256"); err == nil {
		t.Fatal("performance fixture hash mismatch was accepted")
	}
	if err := validatePerformanceStudyFixture(t.TempDir(), map[string]string{"../outside.dcm": "sha256"}, "../outside.dcm", "sha256"); err == nil {
		t.Fatal("performance fixture outside the corpus was accepted")
	}
}
