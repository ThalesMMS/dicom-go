package codecprofile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestCodecFullManifestIsValidAndComplete(t *testing.T) {
	manifest := CodecFullManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if !manifest.ClinicalReleaseReady {
		t.Fatal("codecfull does not claim release readiness after all evidence gates passed")
	}

	got := make(map[string]bool)
	for _, capability := range manifest.Capabilities {
		for _, syntax := range capability.TransferSyntaxes {
			got[syntax.UID] = true
		}
	}
	for _, syntax := range RequiredCodecFullTransferSyntaxes() {
		if !got[syntax.UID] {
			t.Errorf("required transfer syntax %s (%s) is missing", syntax.UID, syntax.Name)
		}
	}
}

func TestCodecFullManifestJSONIsDeterministicAndNonPHI(t *testing.T) {
	first, err := json.MarshalIndent(CodecFullManifest(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.MarshalIndent(CodecFullManifest(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("manifest JSON is not deterministic")
	}
	if strings.Contains(strings.ToLower(string(first)), "patientname") ||
		strings.Contains(strings.ToLower(string(first)), "patientid") {
		t.Fatal("manifest unexpectedly contains patient identity fields")
	}

	var decoded ProfileManifest
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped manifest is invalid: %v", err)
	}
}

func TestCodecFullManifestDeclaresDirectionalCapabilities(t *testing.T) {
	manifest := CodecFullManifest()
	for _, capability := range manifest.Capabilities {
		wantDirections := []CodecDirection{DirectionDecode}
		if capability.ID == "rle-lossless" || capability.ID == "encapsulated-uncompressed" {
			wantDirections = []CodecDirection{DirectionDecode, DirectionEncode}
		}
		if !reflect.DeepEqual(capability.Directions, wantDirections) {
			t.Errorf("%s directions = %#v, want %#v", capability.ID, capability.Directions, wantDirections)
		}
		for _, evidence := range capability.Evidence {
			if evidence.Direction == "" {
				t.Errorf("%s evidence %s has no explicit direction", capability.ID, evidence.Path)
			}
		}
	}

	rle := capabilityByID(t, manifest, "rle-lossless")
	var encodeEvidence []Evidence
	for _, evidence := range rle.Evidence {
		if evidence.Direction == DirectionEncode {
			encodeEvidence = append(encodeEvidence, evidence)
		}
	}
	if len(encodeEvidence) != 2 {
		t.Fatalf("RLE encode evidence = %#v, want encoder and pipeline entries", encodeEvidence)
	}
	byPath := make(map[string]Evidence, len(encodeEvidence))
	for _, evidence := range encodeEvidence {
		byPath[evidence.Path] = evidence
	}
	encoderEvidence, ok := byPath["pixeldata/rle/encoder_test.go"]
	if !ok {
		t.Fatalf("RLE encode evidence lacks pure-Go encoder tests: %#v", encodeEvidence)
	}
	wantTests := []string{
		"TestEncoderMono8GoldenPreservesRowBoundaries",
		"TestEncoderMono16GoldenUsesMostSignificantBytePlaneFirst",
		"TestEncoderRGB8GoldenUsesSampleOrderAndEvenSegmentPadding",
		"TestEncoderRoundTripSupportedNativeFrames",
		"TestEncoderAcceptsTwelveStoredBitsAndSignedOrUnsignedPixels",
	}
	if !reflect.DeepEqual(encoderEvidence.Tests, wantTests) {
		t.Fatalf("RLE encode evidence tests = %#v, want %#v", encoderEvidence.Tests, wantTests)
	}
	pipelineEvidence, ok := byPath["pixeldata/transcode_rle_test.go"]
	if !ok || !reflect.DeepEqual(pipelineEvidence.Tests, []string{"TestTranscodeNativeRLEPipelineIsBitExactAcrossProfiles"}) {
		t.Fatalf("RLE pipeline evidence = %#v", pipelineEvidence)
	}
}

func TestManifestLegacyJSONWithoutDirectionsDefaultsToDecode(t *testing.T) {
	manifest := CodecFullManifest()
	for capabilityIndex := range manifest.Capabilities {
		capability := &manifest.Capabilities[capabilityIndex]
		capability.Directions = nil
		for evidenceIndex := range capability.Evidence {
			capability.Evidence[evidenceIndex].Direction = ""
		}
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("legacy direction-less manifest is invalid: %v", err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"directions"`) || strings.Contains(string(encoded), `"direction"`) {
		t.Fatalf("legacy manifest emitted additive direction fields: %s", encoded)
	}
	var decoded ProfileManifest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("legacy JSON round-trip is invalid: %v", err)
	}
}

func TestManifestValidationAllowsTransferSyntaxAcrossDisjointDirections(t *testing.T) {
	manifest := CodecFullManifest()
	jpegBaseline := capabilityByID(t, manifest, "jpeg-baseline")
	manifest.Capabilities = append(manifest.Capabilities, Capability{
		ID:               "jpeg-baseline-encode-test",
		Family:           jpegBaseline.Family,
		Status:           StatusValidated,
		Directions:       []CodecDirection{DirectionEncode},
		TransferSyntaxes: append([]TransferSyntax(nil), jpegBaseline.TransferSyntaxes...),
		Implementations:  []string{"test-only encoder capability"},
		Coverage:         []string{"direction-aware manifest validation"},
		Evidence: []Evidence{{
			Path:               "pixeldata/rle/encoder_test.go",
			Tests:              []string{"TestEncoderMono8GoldenPreservesRowBoundaries"},
			Direction:          DirectionEncode,
			Comparison:         ComparisonExact,
			ReferenceDecoder:   "literal test vector",
			Deterministic:      true,
			NoPHI:              true,
			RedistributionSafe: true,
		}},
	})
	if err := manifest.Validate(); err != nil {
		t.Fatalf("same transfer syntax in disjoint directions is invalid: %v", err)
	}
}

func TestManifestValidationRejectsTransferSyntaxOverlapWithinDirection(t *testing.T) {
	manifest := CodecFullManifest()
	jpegBaseline := capabilityByID(t, manifest, "jpeg-baseline")
	duplicate := *jpegBaseline
	duplicate.ID = "jpeg-baseline-decode-duplicate"
	manifest.Capabilities = append(manifest.Capabilities, duplicate)
	err := manifest.Validate()
	if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("Validate() error = %v, want direction-specific duplicate rejection", err)
	}
}

func TestManifestValidationRejectsInvalidDirectionContracts(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProfileManifest)
		wantErr string
	}{
		{
			name: "unknown capability direction",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].Directions = []CodecDirection{"transform"}
			},
			wantErr: "unknown direction",
		},
		{
			name: "duplicate capability direction",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].Directions = []CodecDirection{DirectionDecode, DirectionDecode}
			},
			wantErr: "duplicate direction",
		},
		{
			name: "evidence direction not declared",
			mutate: func(manifest *ProfileManifest) {
				capabilityByID(t, *manifest, "jpeg-baseline").Evidence[0].Direction = DirectionEncode
			},
			wantErr: "evidence direction",
		},
		{
			name: "declared direction lacks evidence",
			mutate: func(manifest *ProfileManifest) {
				rle := capabilityByID(t, *manifest, "rle-lossless")
				filtered := rle.Evidence[:0]
				for _, evidence := range rle.Evidence {
					if evidence.Direction != DirectionEncode {
						filtered = append(filtered, evidence)
					}
				}
				rle.Evidence = filtered
			},
			wantErr: "has no encode evidence",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := CodecFullManifest()
			test.mutate(&manifest)
			err := manifest.Validate()
			if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want ErrInvalidManifest containing %q", err, test.wantErr)
			}
		})
	}
}

func capabilityByID(t *testing.T, manifest ProfileManifest, id string) *Capability {
	t.Helper()
	for index := range manifest.Capabilities {
		if manifest.Capabilities[index].ID == id {
			return &manifest.Capabilities[index]
		}
	}
	t.Fatalf("manifest capability %q not found", id)
	return nil
}

func TestCodecFullManifestEvidenceReferencesExistingTests(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	for _, capability := range CodecFullManifest().Capabilities {
		for _, evidence := range capability.Evidence {
			path := filepath.Join(moduleRoot, filepath.FromSlash(evidence.Path))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s evidence path %s: %v", capability.ID, evidence.Path, err)
				continue
			}
			for _, testName := range evidence.Tests {
				if !strings.Contains(string(data), "func "+testName+"(") {
					t.Errorf("%s evidence %s does not define %s", capability.ID, evidence.Path, testName)
				}
			}
		}
	}
}

func TestCodecFullManifestBuildDependenciesMatchModuleManifests(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	moduleFiles := map[string]string{
		"github.com/ebitengine/purego": "examples/codec-adapters/jpegls/go.mod",
	}
	checked := map[string]bool{}
	for _, capability := range CodecFullManifest().Capabilities {
		for _, dependency := range capability.Dependencies {
			if dependency.Scope != "build" || dependency.Kind != "go-module" || checked[dependency.Name] {
				continue
			}
			moduleFile, ok := moduleFiles[dependency.Name]
			if !ok {
				t.Errorf("build dependency %s has no module-manifest mapping", dependency.Name)
				continue
			}
			data, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(moduleFile)))
			if err != nil {
				t.Errorf("read %s: %v", moduleFile, err)
				continue
			}
			pattern := `(?m)^\s*` + regexp.QuoteMeta(dependency.Name) +
				`\s+` + regexp.QuoteMeta(dependency.Version) + `(?:[ \t]+//.*)?\r?$`
			if !regexp.MustCompile(pattern).Match(data) {
				t.Errorf("%s does not pin %s %s", moduleFile, dependency.Name, dependency.Version)
			}
			checked[dependency.Name] = true
		}
	}
	for dependency := range moduleFiles {
		if !checked[dependency] {
			t.Errorf("module-manifest mapping %s is not used by the codecfull manifest", dependency)
		}
	}
}

func TestCodecFullReleaseGatePasses(t *testing.T) {
	if err := CodecFullManifest().ValidateForRelease(); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyByNameDoesNotDependOnSliceOrder(t *testing.T) {
	dependencies := []Dependency{
		{Name: "CharLS", Version: "old"},
		{Name: "github.com/ebitengine/purego", Version: "unchanged"},
	}
	dependency := dependencyByName(dependencies, "CharLS")
	if dependency == nil {
		t.Fatal("CharLS dependency was not found")
	}
	dependency.Version = "2.4.2"
	if dependencies[0].Version != "2.4.2" || dependencies[1].Version != "unchanged" {
		t.Fatalf("dependency update selected the wrong entry: %#v", dependencies)
	}
	if got := dependencyByName(dependencies, "missing"); got != nil {
		t.Fatalf("missing dependency = %#v, want nil", got)
	}
}

func TestCodecFullReleaseGateFailsClosedAfterRegression(t *testing.T) {
	manifest := CodecFullManifest()
	manifest.Capabilities[5].Status = StatusProvisional
	manifest.Capabilities[5].Blockers = []string{"runtime regression"}
	manifest.ClinicalReleaseReady = false
	err := manifest.ValidateForRelease()
	if !errors.Is(err, ErrCodecFullNotReady) {
		t.Fatalf("ValidateForRelease() error = %v, want ErrCodecFullNotReady", err)
	}
	for _, want := range []string{
		"jpeg-ls:",
		"runtime regression",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("release error is missing %q: %v", want, err)
		}
	}
}

func TestCodecFullManifestReleaseEvidencePathsExist(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	manifest := CodecFullManifest()
	for _, evidence := range manifest.PerformanceEvidence {
		if _, err := os.Stat(filepath.Join(moduleRoot, filepath.FromSlash(evidence.Path))); err != nil {
			t.Errorf("performance evidence %s: %v", evidence.Path, err)
		}
	}
	for _, artifact := range manifest.ReleaseArtifacts {
		if strings.ContainsAny(artifact, `/\`) {
			t.Errorf("release artifact %q must be a package-root basename", artifact)
		}
	}
}

func TestManifestValidationRejectsUnsafeEvidence(t *testing.T) {
	manifest := CodecFullManifest()
	manifest.Capabilities[0].Evidence[0].NoPHI = false
	err := manifest.Validate()
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidManifest", err)
	}
	if !strings.Contains(err.Error(), "violates fixture policy") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestManifestValidationRejectsDuplicateTransferSyntax(t *testing.T) {
	manifest := CodecFullManifest()
	manifest.Capabilities[1].TransferSyntaxes = append(
		manifest.Capabilities[1].TransferSyntaxes,
		manifest.Capabilities[0].TransferSyntaxes[0],
	)
	err := manifest.Validate()
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidManifest", err)
	}
	if !strings.Contains(err.Error(), "appears in") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestManifestValidationRejectsWhitespaceOnlyRequiredScalars(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProfileManifest)
		wantErr string
	}{
		{
			name: "capability id",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].ID = " \t"
			},
			wantErr: "capability id and family are required",
		},
		{
			name: "capability family",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].Family = " \t"
			},
			wantErr: "capability id and family are required",
		},
		{
			name: "transfer syntax uid",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].TransferSyntaxes[0].UID = " \t"
			},
			wantErr: "has an incomplete transfer syntax",
		},
		{
			name: "transfer syntax name",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].TransferSyntaxes[0].Name = " \t"
			},
			wantErr: "has an incomplete transfer syntax",
		},
		{
			name: "dependency name",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[5].Dependencies[0].Name = " \t"
			},
			wantErr: "has an incomplete dependency",
		},
		{
			name: "dependency scope",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[5].Dependencies[0].Scope = " \t"
			},
			wantErr: "has an incomplete dependency",
		},
		{
			name: "dependency kind",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[5].Dependencies[0].Kind = " \t"
			},
			wantErr: "has an incomplete dependency",
		},
		{
			name: "dependency version",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[5].Dependencies[0].Version = " \t"
			},
			wantErr: "has an incomplete dependency",
		},
		{
			name: "dependency license",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[5].Dependencies[0].License = " \t"
			},
			wantErr: "has an incomplete dependency",
		},
		{
			name: "dependency acquisition",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[5].Dependencies[0].Acquisition = " \t"
			},
			wantErr: "has an incomplete dependency",
		},
		{
			name: "evidence path",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].Evidence[0].Path = " \t"
			},
			wantErr: "has incomplete evidence",
		},
		{
			name: "evidence reference decoder",
			mutate: func(manifest *ProfileManifest) {
				manifest.Capabilities[0].Evidence[0].ReferenceDecoder = " \t"
			},
			wantErr: "has incomplete evidence",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := CodecFullManifest()
			test.mutate(&manifest)
			err := manifest.Validate()
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Validate() error = %v, want ErrInvalidManifest", err)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}
