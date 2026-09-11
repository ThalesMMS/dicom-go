package dicom

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRoadmapUsesLivePlanningSourcesAndStableDependencies(t *testing.T) {
	data, err := os.ReadFile("docs/ROADMAP.md")
	if err != nil {
		t.Fatal(err)
	}
	document := string(data)
	for _, required := range []string{
		"CAPABILITIES.md", "state%3Aopen", "Epic #899",
		"## Outcome dependency model", "## Sequencing live work",
		"## Maintenance rule", "Correct, bounded DICOM primitives",
		"Reusable protocol, pixel, render, ROI, and clinical services",
		"Product archive, network, export, settings, and viewer workflows",
		"Independent Mac-style and Windows-style experiences",
		"Release qualification", "No versioned Markdown document may contain a generated",
	} {
		if !strings.Contains(document, required) {
			t.Errorf("roadmap is missing %q", required)
		}
	}
	for _, obsolete := range []string{
		"## Current capabilities", "## Future outcomes by dependency",
		"gh issue list", "Last reconciled", "No open issue remains",
		"#716", "#717", "#784",
	} {
		if strings.Contains(document, obsolete) {
			t.Errorf("roadmap retains obsolete snapshot text %q", obsolete)
		}
	}
	if regexp.MustCompile(`(?m)^\s*[-*+]\s+\[[ xX]\]\s+#\d+`).MatchString(document) {
		t.Error("roadmap contains a copied issue checklist")
	}
}

func TestImplementationPlanningDoesNotCopyIssueInventory(t *testing.T) {
	data, err := os.ReadFile("docs/IMPLEMENTATION_ISSUES.md")
	if err != nil {
		t.Fatal(err)
	}
	document := string(data)
	for _, required := range []string{"state%3Aopen", "Epic #899", "ROADMAP.md", "CAPABILITIES.md"} {
		if !strings.Contains(document, required) {
			t.Errorf("implementation planning page is missing %q", required)
		}
	}
	if regexp.MustCompile(`(?m)^\s*[-*+]\s+\[[ xX]\]\s+#?\d+`).MatchString(document) {
		t.Error("implementation planning page contains a copied issue checklist")
	}
}
