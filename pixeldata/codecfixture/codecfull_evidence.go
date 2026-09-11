package codecfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

type codecFullCorpusManifest struct {
	SchemaVersion int `json:"schemaVersion"`
	Profile       string
	FixturePolicy struct {
		NoPHI              bool `json:"noPHI"`
		RedistributionSafe bool `json:"redistributionSafe"`
		Notes              string
	} `json:"fixturePolicy"`
	Sources []struct {
		Name        string
		URL         string
		Commit      string
		License     string
		LicensePath string
	} `json:"sources"`
	Fixtures []codecFullCorpusFixture `json:"fixtures"`
}

// CodecFullCorpusJSON projects the existing, validated fixture inventory into
// capability reports. It contains provenance and metadata, never patient values
// or decoded sample buffers. Presence is structural evidence, not execution.
func CodecFullCorpusJSON(moduleRoot string) (json.RawMessage, error) {
	root := filepath.Join(moduleRoot, "pixeldata", "codecfixture", "testdata", "codecfull")
	if err := validateCodecFullCorpus(root); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(root, "manifest.json"))
}

type codecFullCorpusFixture struct {
	ID                 string
	Family             string
	Path               string
	SHA256             string
	ReferencePath      string
	ReferenceSHA256    string `json:"referenceSha256"`
	Test               string
	Comparison         string
	MaxAbsoluteError   int
	Modality           string
	BitsAllocated      int
	Signed             bool
	Color              bool
	Multiframe         bool
	Lossy              bool
	Reconstruction     *ReconstructionEvidence `json:"reconstruction,omitempty"`
	Input              CorpusInput             `json:"input"`
	Generator          string                  `json:"generator"`
	Expectation        string                  `json:"expectation"`
	BaselineSkip       string                  `json:"baselineSkip"`
	QualificationLimit string                  `json:"qualificationLimit"`
	CaseName           string                  `json:"caseName,omitempty"`
	SamplePolicy       *SamplePolicy           `json:"samplePolicy,omitempty"`
	Provenance         Provenance              `json:"provenance"`
	SourceSamples      *SourceSampleEvidence   `json:"sourceSamples,omitempty"`
}

// SourceSampleEvidence is the original image before JPEG-LS encoding. NEAR
// bounds source error; Reconstruction remains an exact decoder oracle.
type SourceSampleEvidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Near   int    `json:"near"`
}

func validateSourceSamples(root string, f codecFullCorpusFixture) error {
	s := f.SourceSamples
	if s == nil {
		return nil
	}
	if f.Reconstruction == nil || f.Family != "jpeg-ls" || f.Input.Signed || s.Near < 0 || s.Near > 255 || s.Near > ((1<<f.Input.BitsStored)-1)/2 || f.Lossy != (s.Near > 0) {
		return fmt.Errorf("codecfixture: invalid JPEG-LS source-bound evidence for %s", f.ID)
	}
	path, err := resolveCodecFullCorpusPath(root, s.Path)
	if err != nil {
		return err
	}
	if err := validateSHA256(path, s.SHA256); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != int64(f.Reconstruction.Layout.frameBytes())*int64(f.Input.Frames) {
		return fmt.Errorf("codecfixture: source layout mismatch for %s", f.ID)
	}
	return nil
}

// CorpusSamplePolicy returns the approved per-codec full-sample policy. Legacy
// point or absolute-only checks are explicitly unqualified and cannot be used
// through this API as reconstruction evidence.
func CorpusSamplePolicy(moduleRoot, id string) (SamplePolicy, error) {
	root := filepath.Join(moduleRoot, "pixeldata", "codecfixture", "testdata", "codecfull")
	m, err := readCodecFullCorpusManifest(root)
	if err != nil {
		return SamplePolicy{}, err
	}
	for _, f := range m.Fixtures {
		if f.ID != id {
			continue
		}
		if f.SamplePolicy != nil {
			return *f.SamplePolicy, nil
		}
		if f.Comparison == "exact" || f.Comparison == "exact-reconstruction" {
			return SamplePolicy{}, nil
		}
		return SamplePolicy{}, fmt.Errorf("codecfixture: %s has no full-sample policy", id)
	}
	return SamplePolicy{}, fmt.Errorf("codecfixture: unknown corpus fixture %s", id)
}

// CorpusInput records encoded DICOM metadata separately from output layout.
type CorpusInput struct {
	TransferSyntax      string `json:"transferSyntax"`
	Rows                int    `json:"rows"`
	Columns             int    `json:"columns"`
	Components          int    `json:"components"`
	BitsAllocated       int    `json:"bitsAllocated"`
	BitsStored          int    `json:"bitsStored"`
	HighBit             int    `json:"highBit"`
	Signed              bool   `json:"signed"`
	Photometric         string `json:"photometric"`
	PlanarConfiguration int    `json:"planarConfiguration"`
	Frames              int    `json:"frames"`
}

func corpusInput(uid string, m pixeldata.Metadata) CorpusInput {
	return CorpusInput{uid, int(m.Rows), int(m.Columns), int(m.SamplesPerPixel), int(m.BitsAllocated), int(m.BitsStored), int(m.HighBit), m.PixelRepresentation == 1, m.PhotometricInterpretation, int(m.PlanarConfiguration), m.NumberOfFrames}
}

func validateCorpusInput(root string, f codecFullCorpusFixture) error {
	i := f.Input
	l := SampleLayout{Rows: i.Rows, Columns: i.Columns, Components: i.Components, BitsAllocated: i.BitsAllocated, BitsStored: i.BitsStored, HighBit: i.HighBit, Signed: i.Signed}
	if err := l.validate(); err != nil {
		return fmt.Errorf("codecfixture: %s input: %w", f.ID, err)
	}
	if i.TransferSyntax == "" || i.Photometric == "" || i.PlanarConfiguration < 0 || i.PlanarConfiguration > 1 || i.Frames < 1 || f.Generator == "" || f.Expectation == "" {
		return fmt.Errorf("codecfixture: incomplete input/provenance for %s", f.ID)
	}
	if !f.Provenance.NoPHI || f.Provenance.Source == "" || f.Provenance.License == "" || f.Provenance.Permission == "" {
		return fmt.Errorf("codecfixture: incomplete per-fixture provenance for %s", f.ID)
	}
	if i.BitsAllocated != f.BitsAllocated || i.Signed != f.Signed || (i.Components > 1) != f.Color || (i.Frames > 1) != f.Multiframe {
		return fmt.Errorf("codecfixture: contradictory coverage for %s", f.ID)
	}
	if strings.EqualFold(filepath.Ext(f.Path), ".dcm") {
		path, err := resolveCodecFullCorpusPath(root, f.Path)
		if err != nil {
			return err
		}
		file, err := object.OpenFile(path)
		if err != nil {
			return fmt.Errorf("codecfixture: %s DICOM parse: %w", f.ID, err)
		}
		defer file.Close()
		meta, err := pixeldata.ExtractMetadata(file.Dataset)
		if err != nil {
			return err
		}
		if corpusInput(file.TransferSyntax.UID, meta) != i {
			return fmt.Errorf("codecfixture: encoded metadata drift for %s", f.ID)
		}
	}
	return nil
}

// ReconstructionEvidence binds approved raw samples to their actual decoder.
// Backend identifies the implementation, not merely a Python/Go wrapper.
type ReconstructionEvidence struct {
	Layout    SampleLayout `json:"layout"`
	Frames    int          `json:"frames"`
	Backend   string       `json:"backend"`
	Version   string       `json:"version"`
	Source    string       `json:"source"`
	Generator string       `json:"generator"`
}

// ReadFullReconstruction loads hash-checked, approved raw full-frame expectations
// from the existing codecfull corpus. Missing evidence is an error, never a skip.
func ReadFullReconstruction(moduleRoot, id string) (ReconstructionEvidence, [][]byte, error) {
	root := filepath.Join(moduleRoot, "pixeldata", "codecfixture", "testdata", "codecfull")
	manifest, err := readCodecFullCorpusManifest(root)
	if err != nil {
		return ReconstructionEvidence{}, nil, err
	}
	for _, fixture := range manifest.Fixtures {
		if fixture.ID != id {
			continue
		}
		if fixture.Reconstruction == nil {
			break
		}
		e := *fixture.Reconstruction
		if err := validateReconstruction(root, fixture); err != nil {
			return e, nil, err
		}
		path, err := resolveCodecFullCorpusPath(root, fixture.ReferencePath)
		if err != nil {
			return e, nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return e, nil, err
		}
		size := int(e.Layout.frameBytes())
		frames := make([][]byte, e.Frames)
		for i := range frames {
			frames[i] = data[i*size : (i+1)*size]
		}
		return e, frames, nil
	}
	return ReconstructionEvidence{}, nil, fmt.Errorf("codecfixture: full reconstruction unavailable for %s", id)
}

func validateReconstruction(root string, f codecFullCorpusFixture) error {
	e := f.Reconstruction
	if e == nil {
		return nil
	}
	if err := e.Layout.validate(); err != nil {
		return err
	}
	if e.Layout.Rows != f.Input.Rows || e.Layout.Columns != f.Input.Columns || e.Layout.Components != f.Input.Components || e.Layout.BitsStored != f.Input.BitsStored || e.Layout.Signed != f.Input.Signed || e.Frames != f.Input.Frames {
		return fmt.Errorf("codecfixture: reconstructed layout contradicts input for %s", f.ID)
	}
	if e.Frames < 1 || e.Frames > 10000 || e.Backend == "" || e.Version == "" || e.Source == "" || e.Generator == "" || f.Comparison != "exact-reconstruction" {
		return fmt.Errorf("codecfixture: incomplete reconstruction provenance for %s", f.ID)
	}
	for _, file := range []struct{ path, hash string }{{f.Path, f.SHA256}, {f.ReferencePath, f.ReferenceSHA256}} {
		path, err := resolveCodecFullCorpusPath(root, file.path)
		if err != nil {
			return err
		}
		if err := validateSHA256(path, file.hash); err != nil {
			return err
		}
	}
	path, err := resolveCodecFullCorpusPath(root, f.ReferencePath)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != e.Layout.frameBytes()*int64(e.Frames) || info.Size() > 64<<20 {
		return fmt.Errorf("codecfixture: incomplete or excessive reconstruction for %s", f.ID)
	}
	return nil
}

type codecFullPerformanceReport struct {
	SchemaVersion int
	Profile       string
	RecordedAt    string
	Environment   struct {
		GOOS      string
		GOARCH    string
		GoVersion string
	}
	Dependencies map[string]string
	Memory       struct {
		Metric                       string
		Scope                        string
		SamplingIntervalMicroseconds int64
	} `json:"memoryMeasurement"`
	Studies []struct {
		ID                  string
		Fixture             string
		SHA256              string
		Frames              int
		Iterations          int
		DecodesPerIteration int
		P50Microseconds     float64
		P95Microseconds     float64
		P99Microseconds     float64
		PeakMemoryBytes     uint64
		PeakTotalAllocBytes uint64
	}
}

// ValidateCodecFullReleaseEvidence verifies the checked-in codecfull corpus
// provenance, fixture hashes, and reproducible performance report. moduleRoot
// must be the root of the github.com/ThalesMMS/dicom-go module.
func ValidateCodecFullReleaseEvidence(moduleRoot string) error {
	if strings.TrimSpace(moduleRoot) == "" {
		return fmt.Errorf("codecfixture: codecfull evidence root is empty")
	}
	corpusRoot := filepath.Join(moduleRoot, "pixeldata", "codecfixture", "testdata", "codecfull")
	if err := validateCodecFullCorpus(corpusRoot); err != nil {
		return err
	}
	return validateCodecFullPerformance(filepath.Join(corpusRoot, "performance", "windows-amd64.json"), corpusRoot)
}

func validateCodecFullCorpus(root string) error {
	manifest, err := readCodecFullCorpusManifest(root)
	if err != nil {
		return err
	}
	return validateCodecFullCorpusManifest(root, manifest)
}

func readCodecFullCorpusManifest(root string) (codecFullCorpusManifest, error) {
	var manifest codecFullCorpusManifest
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return manifest, fmt.Errorf("codecfixture: read codecfull corpus manifest: %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("codecfixture: parse codecfull corpus manifest: %w", err)
	}
	return manifest, nil
}

func validateCodecFullCorpusManifest(root string, manifest codecFullCorpusManifest) error {
	if manifest.SchemaVersion != 1 || manifest.Profile != "codecfull" {
		return fmt.Errorf("codecfixture: unexpected corpus identity schema=%d profile=%q", manifest.SchemaVersion, manifest.Profile)
	}
	if !manifest.FixturePolicy.NoPHI || !manifest.FixturePolicy.RedistributionSafe ||
		strings.TrimSpace(manifest.FixturePolicy.Notes) == "" {
		return fmt.Errorf("codecfixture: codecfull fixture policy is incomplete")
	}

	requiredFamilies := map[string]bool{
		"native":                    false,
		"encapsulated-uncompressed": false,
		"jpeg-baseline":             false,
		"jpeg-extended":             false,
		"jpeg-lossless":             false,
		"rle":                       false,
		"jpeg-ls":                   false,
		"jpeg-2000-part-1":          false,
		"jpeg-2000-part-2":          false,
		"htj2k":                     false,
		"jpeg-xl":                   false,
	}
	var hasSigned, hasColor, hasMultiframe, hasLossy, hasLossless, has8Bit, has16Bit bool
	ids := make(map[string]bool, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		if strings.TrimSpace(fixture.ID) == "" || ids[fixture.ID] {
			return fmt.Errorf("codecfixture: blank or duplicate fixture id %q", fixture.ID)
		}
		ids[fixture.ID] = true
		if err := validateCorpusInput(root, fixture); err != nil {
			return err
		}
		if err := validateReconstruction(root, fixture); err != nil {
			return err
		}
		if err := validateSourceSamples(root, fixture); err != nil {
			return err
		}
		if _, ok := requiredFamilies[fixture.Family]; !ok {
			return fmt.Errorf("codecfixture: fixture %s has unknown family %q", fixture.ID, fixture.Family)
		}
		requiredFamilies[fixture.Family] = true
		if fixture.Path == "" && fixture.Test == "" {
			return fmt.Errorf("codecfixture: fixture %s has neither file nor test evidence", fixture.ID)
		}
		if fixture.Path != "" {
			path, err := resolveCodecFullCorpusPath(root, fixture.Path)
			if err != nil {
				return fmt.Errorf("codecfixture: fixture %s: %w", fixture.ID, err)
			}
			if err := validateSHA256(path, fixture.SHA256); err != nil {
				return fmt.Errorf("codecfixture: fixture %s: %w", fixture.ID, err)
			}
		}
		if fixture.ReferencePath != "" {
			path, err := resolveCodecFullCorpusPath(root, fixture.ReferencePath)
			if err != nil {
				return fmt.Errorf("codecfixture: fixture %s reference: %w", fixture.ID, err)
			}
			if err := validateSHA256(path, fixture.ReferenceSHA256); err != nil {
				return fmt.Errorf("codecfixture: fixture %s reference: %w", fixture.ID, err)
			}
		}
		if fixture.Comparison == "" || fixture.Modality == "" || fixture.BitsAllocated == 0 {
			return fmt.Errorf("codecfixture: fixture %s has incomplete comparison metadata", fixture.ID)
		}
		if fixture.Lossy && fixture.Comparison == "absolute-tolerance" && fixture.MaxAbsoluteError <= 0 {
			return fmt.Errorf("codecfixture: lossy fixture %s has no positive tolerance", fixture.ID)
		}
		hasSigned = hasSigned || fixture.Signed
		hasColor = hasColor || fixture.Color
		hasMultiframe = hasMultiframe || fixture.Multiframe
		hasLossy = hasLossy || fixture.Lossy
		hasLossless = hasLossless || !fixture.Lossy
		has8Bit = has8Bit || fixture.BitsAllocated == 8
		has16Bit = has16Bit || fixture.BitsAllocated == 16
	}
	for family, covered := range requiredFamilies {
		if !covered {
			return fmt.Errorf("codecfixture: codecfull corpus does not cover %s", family)
		}
	}
	if !hasSigned || !hasColor || !hasMultiframe || !hasLossy || !hasLossless || !has8Bit || !has16Bit {
		return fmt.Errorf(
			"codecfixture: incomplete coverage signed=%t color=%t multiframe=%t lossy=%t lossless=%t 8bit=%t 16bit=%t",
			hasSigned, hasColor, hasMultiframe, hasLossy, hasLossless, has8Bit, has16Bit,
		)
	}
	if len(manifest.Sources) < 3 {
		return fmt.Errorf("codecfixture: codecfull provenance sources are incomplete")
	}
	for _, source := range manifest.Sources {
		if source.Name == "" || source.URL == "" || source.Commit == "" || source.License == "" {
			return fmt.Errorf("codecfixture: incomplete source provenance for %q", source.Name)
		}
		if source.LicensePath != "" {
			path, err := resolveCodecFullCorpusPath(root, source.LicensePath)
			if err != nil {
				return fmt.Errorf("codecfixture: %s license: %w", source.Name, err)
			}
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("codecfixture: %s license: %w", source.Name, err)
			}
		}
	}
	return nil
}

func validateCodecFullPerformance(path, corpusRoot string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("codecfixture: read codecfull performance report: %w", err)
	}
	var report codecFullPerformanceReport
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("codecfixture: parse codecfull performance report: %w", err)
	}
	if report.SchemaVersion != 1 || report.Profile != "codecfull" || report.RecordedAt == "" {
		return fmt.Errorf("codecfixture: invalid performance report identity")
	}
	if report.Environment.GOOS == "" || report.Environment.GOARCH == "" || report.Environment.GoVersion == "" {
		return fmt.Errorf("codecfixture: performance environment is incomplete")
	}
	for dependency, version := range map[string]string{
		"CharLS": "2.4.2", "libjxl": "0.11.2", "OpenJPEG": "2.5.4", "OpenJPH": "0.31.0",
	} {
		if report.Dependencies[dependency] != version {
			return fmt.Errorf("codecfixture: performance dependency %s=%q, want %q", dependency, report.Dependencies[dependency], version)
		}
	}
	if report.Memory.Metric != "peak-process-tree-working-set-bytes" ||
		report.Memory.Scope != "measurement-process-and-descendants" ||
		report.Memory.SamplingIntervalMicroseconds <= 0 {
		return fmt.Errorf("codecfixture: performance memory measurement is not process-tree peak working set")
	}
	required := map[string]bool{
		"jpeg2000-mr-10-frame": false,
		"jpegls-mr-10-frame":   false,
		"rle-mr-10-frame":      false,
		"htj2k-rgb-ultrasound": false,
	}
	manifest, err := readCodecFullCorpusManifest(corpusRoot)
	if err != nil {
		return err
	}
	fixtureIndex := make(map[string]string, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		if fixture.Path != "" {
			fixtureIndex[normalizeCodecFullFixturePath(fixture.Path)] = fixture.SHA256
		}
	}
	for _, study := range report.Studies {
		if _, ok := required[study.ID]; !ok {
			return fmt.Errorf("codecfixture: unexpected performance study %q", study.ID)
		}
		required[study.ID] = true
		if study.Fixture == "" || study.SHA256 == "" || study.Frames <= 0 ||
			study.Iterations < 5 || study.DecodesPerIteration <= 0 {
			return fmt.Errorf("codecfixture: performance study %s has incomplete provenance", study.ID)
		}
		if err := validatePerformanceStudyFixture(corpusRoot, fixtureIndex, study.Fixture, study.SHA256); err != nil {
			return fmt.Errorf("codecfixture: performance study %s: %w", study.ID, err)
		}
		if study.P50Microseconds <= 0 || study.P95Microseconds < study.P50Microseconds ||
			study.P99Microseconds < study.P95Microseconds {
			return fmt.Errorf("codecfixture: performance study %s has invalid percentiles", study.ID)
		}
		if study.PeakMemoryBytes == 0 || study.PeakTotalAllocBytes == 0 {
			return fmt.Errorf("codecfixture: performance study %s has no peak-memory evidence", study.ID)
		}
	}
	for id, found := range required {
		if !found {
			return fmt.Errorf("codecfixture: performance report is missing %s", id)
		}
	}
	return nil
}

func validatePerformanceStudyFixture(corpusRoot string, fixtureIndex map[string]string, fixture, wantSHA256 string) error {
	normalized := normalizeCodecFullFixturePath(fixture)
	path, err := resolveCodecFullCorpusPath(corpusRoot, normalized)
	if err != nil {
		return fmt.Errorf("fixture %q: %w", fixture, err)
	}
	manifestSHA256, ok := fixtureIndex[normalized]
	if !ok {
		return fmt.Errorf("fixture %q is absent from the codecfull corpus manifest", fixture)
	}
	if manifestSHA256 != wantSHA256 {
		return fmt.Errorf("fixture %q sha256=%s does not match corpus manifest sha256=%s", fixture, wantSHA256, manifestSHA256)
	}
	if err := validateSHA256(path, wantSHA256); err != nil {
		return fmt.Errorf("fixture %q: %w", fixture, err)
	}
	return nil
}

func normalizeCodecFullFixturePath(path string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
}

func resolveCodecFullCorpusPath(corpusRoot, relativePath string) (string, error) {
	normalized := normalizeCodecFullFixturePath(relativePath)
	if normalized == "" ||
		filepath.IsAbs(relativePath) ||
		filepath.VolumeName(relativePath) != "" ||
		normalized == ".." ||
		strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("path escapes codecfull corpus")
	}
	rootAbs, err := filepath.Abs(corpusRoot)
	if err != nil {
		return "", fmt.Errorf("resolve corpus root: %w", err)
	}
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(normalized)))
	if err != nil {
		return "", fmt.Errorf("resolve corpus fixture: %w", err)
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes codecfull corpus")
	}
	return pathAbs, nil
}

func validateSHA256(path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("sha256=%s want=%s", got, want)
	}
	return nil
}
