package codeccost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const reportSchemaVersion = 1

// Report is the campaign JSON document.
type Report struct {
	SchemaVersion   int               `json:"schemaVersion"`
	RecordedAt      string            `json:"recordedAt"`
	Environment     Environment       `json:"environment"`
	Criteria        PromotionCriteria `json:"criteria"`
	Instrumentation Instrumentation   `json:"instrumentation"`
	Skips           []Skip            `json:"skips"`
	Fixtures        []FixturePin      `json:"fixtures,omitempty"`
	Frames          []FrameResult     `json:"frames"`
	Cohorts         []CohortSummary   `json:"cohorts"`
	Decision        Decision          `json:"decision"`
	Resources       ResourceCheck     `json:"resourcesAfterClose"`
}

// FixturePin pins a non-PHI cohort file by hash and geometry.
type FixturePin struct {
	Name        string `json:"name"`
	SHA256      string `json:"sha256,omitempty"`
	Rows        uint16 `json:"rows"`
	Columns     uint16 `json:"columns"`
	Lossy       bool   `json:"lossy,omitempty"`
	MaxAbsError int    `json:"maxAbsError,omitempty"`
}

// ResourceCheck records whether helper/temp resources survived Close.
type ResourceCheck struct {
	HelperPID                 int  `json:"helperPid,omitempty"`
	HelperAliveAfterClose     bool `json:"helperAliveAfterClose"`
	RetainedCodecCostTempDirs int  `json:"retainedCodecCostTempDirs"`
}

// Environment pins the machine and toolchain. It contains no study identifiers.
type Environment struct {
	GOOS            string            `json:"goos"`
	GOARCH          string            `json:"goarch"`
	GoVersion       string            `json:"goVersion"`
	LogicalCPU      int               `json:"logicalCPU"`
	GOMAXPROCS      int               `json:"gomaxprocs"`
	CPUBrand        string            `json:"cpuBrand,omitempty"`
	PowerNote       string            `json:"powerPolicyNote"`
	Runtimes        map[string]string `json:"runtimes"`
	GitCommit       string            `json:"gitCommit,omitempty"`
	CachePolicy     string            `json:"cachePolicy"`
	ConcurrencyNote string            `json:"concurrency"`
}

// Instrumentation discloses overlap, warmup, and clock overhead.
type Instrumentation struct {
	ExclusiveStagesNonOverlapping bool   `json:"exclusiveStagesNonOverlapping"`
	FirstFrameInclusive           bool   `json:"firstFrameInclusiveNotSummed"`
	CmdRunNotExecuteOnly          string `json:"cmdRunNote"`
	WarmupIterations              int    `json:"warmupIterations"`
	ClockOverheadNanoseconds      int64  `json:"clockOverheadNanoseconds"`
	Profiles                      string `json:"profiles,omitempty"`
}

// CampaignOptions configures an opt-in measurement run.
type CampaignOptions struct {
	CorpusRoot    string
	Iterations    int
	Warmup        int
	Djxl          string
	Cjxl          string
	HelperPath    string
	CompileHelper bool
	HelperSource  string
	Require       []string
	Profiles      ProfileFiles
}

// RunCampaign measures JPEG XL CLI, pipe, and helper prototypes.
func RunCampaign(ctx context.Context, opts CampaignOptions) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Iterations < 1 {
		opts.Iterations = 8
	}
	if opts.Warmup < 0 {
		opts.Warmup = 1
	}
	if opts.Warmup == 0 {
		opts.Warmup = 1
	}
	if opts.CorpusRoot == "" {
		opts.CorpusRoot = discoverCorpusRoot()
	}
	report := Report{
		SchemaVersion: reportSchemaVersion,
		RecordedAt:    time.Now().UTC().Format(time.RFC3339),
		Environment:   captureEnvironment(ctx, opts),
		Criteria:      DefaultPromotionCriteria(),
		Instrumentation: Instrumentation{
			ExclusiveStagesNonOverlapping: true,
			FirstFrameInclusive:           true,
			CmdRunNotExecuteOnly:          CombinedLaunchExecuteNote(),
			WarmupIterations:              opts.Warmup,
			ClockOverheadNanoseconds:      measureClockOverhead(),
		},
	}
	if opts.Profiles.CPU != "" || opts.Profiles.Heap != "" || opts.Profiles.Trace != "" {
		report.Instrumentation.Profiles = "local pprof/runtime-trace files; no HTTP"
		if err := opts.Profiles.Start(); err != nil {
			return report, err
		}
		defer func() { _ = opts.Profiles.Stop() }()
	}

	djxl, djxlSkip := resolveDjxl(opts.Djxl)
	if djxlSkip != "" {
		report.Skips = append(report.Skips, Skip{Backend: backendCLI, Reason: djxlSkip}, Skip{Backend: backendPipe, Reason: djxlSkip})
	}
	helper, helperSkip, compiledHelperDir := resolveHelper(ctx, opts)
	if compiledHelperDir != "" {
		defer func() { _ = os.RemoveAll(compiledHelperDir) }()
	}
	if helperSkip != "" {
		report.Skips = append(report.Skips, Skip{Backend: backendHelper, Reason: helperSkip})
	} else if helper != nil {
		defer func() { finishHelperPtr(&helper) }()
	}

	fixtures, skips := loadJPEGXLFixtures(ctx, opts)
	report.Skips = append(report.Skips, skips...)
	if len(fixtures) == 0 && djxlSkip == "" {
		report.Skips = append(report.Skips, Skip{Backend: backendCLI, Reason: "no JPEG XL fixtures found"})
	}
	report.Skips = append(report.Skips, Skip{
		Cohort: "jpegxl-multiframe",
		Reason: "JPEG XL still-image transfer syntaxes only; multiframe is outside the supported subset",
	})
	if skip := probePACS(); skip.Reason != "" {
		report.Skips = append(report.Skips, skip)
	}
	report.Skips = append(report.Skips,
		Skip{Backend: "jpeg-ls-charls", Reason: "CharLS is already in-process; not a CLI helper candidate in this campaign"},
		Skip{Backend: "jpeg2000-openjpeg", Reason: "OpenJPEG remains a per-frame CLI; helper comparison is JPEG XL only"},
	)
	report.Fixtures = fixturePins(fixtures)
	if err := failRequired(opts.Require, report.Skips); err != nil {
		return report, err
	}

	var backends []namedBackend
	if djxlSkip == "" {
		backends = append(backends,
			namedBackend{name: backendCLI, inner: CLIBackend{Name: backendCLI, Executable: djxl, Timeout: 30 * time.Second, Args: djxlCLIArgs, OutputExt: djxlOutputExtension}},
			namedBackend{name: backendPipe, inner: PipeBackend{Name: backendPipe, Executable: djxl, Timeout: 30 * time.Second, OutputFormat: djxlOutputFormat}},
		)
	}
	if helper != nil {
		helper.Name = backendHelper
		backends = append(backends, namedBackend{name: backendHelper, inner: helper})
	}
	cached := map[string]*CachedBackend{}
	for _, backend := range backends {
		cached[backend.name] = NewCachedBackend(backend.inner)
	}

	references := map[string][]byte{}
	for _, fixture := range fixtures {
		for _, backend := range backends {
			req := DecodeRequest{Codec: "jpegxl", Cohort: fixture.Name, Fragment: fixture.Fragment, Metadata: fixture.Metadata, Mode: ModeWarm}
			result, err := backend.inner.Decode(ctx, req)
			if err != nil {
				return report, fmt.Errorf("%s %s equivalence decode: %w", backend.name, fixture.Name, err)
			}
			if backend.name == backendCLI {
				references[fixture.Name] = append([]byte(nil), result.Pixels...)
				continue
			}
			ref := references[fixture.Name]
			if ref == nil {
				continue
			}
			if err := ComparePixels(result.Pixels, ref, int(fixture.Metadata.BitsAllocated), fixture.Lossy, fixture.MaxAbsError); err != nil {
				return report, fmt.Errorf("%s %s equivalence: %w", backend.name, fixture.Name, err)
			}
		}
	}

	for _, fixture := range fixtures {
		frames, err := measureFixture(ctx, backends, cached, fixture, opts, references)
		if err != nil {
			return report, err
		}
		report.Frames = append(report.Frames, frames...)
	}

	report.Resources = closeAndCheckResources(helper)
	helper = nil
	report.Cohorts = summarize(report.Frames)
	report.Decision = Decide(CampaignReport{Criteria: report.Criteria, Cohorts: report.Cohorts, Skips: report.Skips})
	return report, nil
}

type namedBackend struct {
	name  string
	inner Backend
}

func measureFixture(ctx context.Context, backends []namedBackend, cached map[string]*CachedBackend, fixture jpegxlFixture, opts CampaignOptions, references map[string][]byte) ([]FrameResult, error) {
	var out []FrameResult
	record := func(result FrameResult, equivalence string) {
		result.Equivalence = equivalence
		out = append(out, result)
	}
	label := func(fix jpegxlFixture) string {
		if _, ok := references[fix.Name]; !ok {
			return EquivalenceSkipped
		}
		return fix.Equivalence
	}
	decode := func(backend namedBackend, cohort, mode string, fragment []byte, meta pixeldata.Metadata) (FrameResult, error) {
		return backend.inner.Decode(ctx, DecodeRequest{
			Codec: "jpegxl", Cohort: cohort, Fragment: fragment, Metadata: meta, Mode: mode,
		})
	}
	for _, backend := range backends {
		cold, err := decode(backend, cohortName(fixture, ModeCold), ModeCold, fixture.Fragment, fixture.Metadata)
		if err != nil {
			return nil, fmt.Errorf("%s cold: %w", backend.name, err)
		}
		record(cold, label(fixture))
	}
	for _, backend := range backends {
		for i := 0; i < opts.Warmup; i++ {
			if _, err := decode(backend, fixture.Name, ModeWarm, fixture.Fragment, fixture.Metadata); err != nil {
				return nil, fmt.Errorf("%s warmup: %w", backend.name, err)
			}
		}
	}
	for i := 0; i < opts.Iterations; i++ {
		for _, backend := range backends {
			result, err := decode(backend, cohortName(fixture, ModeWarm), ModeWarm, fixture.Fragment, fixture.Metadata)
			if err != nil {
				return nil, fmt.Errorf("%s warm: %w", backend.name, err)
			}
			record(result, label(fixture))
		}
	}
	if fixture.Name == "jpegxl-gray16" {
		for i := 0; i < 16; i++ {
			for _, backend := range backends {
				result, err := decode(backend, "jpegxl-long-series", ModeWarm, fixture.Fragment, fixture.Metadata)
				if err != nil {
					return nil, fmt.Errorf("%s long-series: %w", backend.name, err)
				}
				record(result, label(fixture))
			}
		}
	}
	for _, backend := range backends {
		cache := cached[backend.name]
		miss, err := cache.Decode(ctx, DecodeRequest{Codec: "jpegxl", Cohort: cohortName(fixture, ModeCacheMiss), Fragment: fixture.Fragment, Metadata: fixture.Metadata, Mode: ModeCacheMiss})
		if err != nil {
			return nil, fmt.Errorf("%s cache-miss: %w", backend.name, err)
		}
		record(miss, label(fixture))
		hit, err := cache.Decode(ctx, DecodeRequest{Codec: "jpegxl", Cohort: cohortName(fixture, ModeCacheHit), Fragment: fixture.Fragment, Metadata: fixture.Metadata, Mode: ModeCacheHit})
		if err != nil {
			return nil, fmt.Errorf("%s cache-hit: %w", backend.name, err)
		}
		record(hit, label(fixture))
	}
	if fixture.Switch != nil {
		for _, backend := range backends {
			_, _ = decode(backend, fixture.Name, ModeWarm, fixture.Fragment, fixture.Metadata)
			switched, err := decode(backend, cohortSeriesSwitch, ModeSeriesSwitch, fixture.Switch.Fragment, fixture.Switch.Metadata)
			if err != nil {
				return nil, fmt.Errorf("%s series-switch: %w", backend.name, err)
			}
			record(switched, label(*fixture.Switch))
		}
	}
	for _, backend := range backends {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		cancelled, err := backend.inner.Decode(cancelCtx, DecodeRequest{Codec: "jpegxl", Cohort: cohortName(fixture, ModeCancel), Fragment: fixture.Fragment, Metadata: fixture.Metadata, Mode: ModeCancel})
		if err == nil {
			return nil, fmt.Errorf("%s cancel did not return an error", backend.name)
		}
		cancelled.Cancelled = true
		record(cancelled, EquivalenceSkipped)
	}
	return out, nil
}

func fixturePins(fixtures []jpegxlFixture) []FixturePin {
	out := make([]FixturePin, 0, len(fixtures))
	for _, fixture := range fixtures {
		out = append(out, FixturePin{
			Name:        fixture.Name,
			SHA256:      fixture.SHA256,
			Rows:        fixture.Metadata.Rows,
			Columns:     fixture.Metadata.Columns,
			Lossy:       fixture.Lossy,
			MaxAbsError: fixture.MaxAbsError,
		})
	}
	return out
}

func closeAndCheckResources(helper *HelperBackend) ResourceCheck {
	check := ResourceCheck{}
	if helper != nil {
		check.HelperPID = helper.pid
		_ = helper.Close()
		rss, err := peekProcessRSS(helper.pid)
		check.HelperAliveAfterClose = err == nil && rss > 0
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "dicom-go-codec-cost-*"))
	check.RetainedCodecCostTempDirs = len(matches)
	return check
}

func finishHelperPtr(helper **HelperBackend) {
	if helper == nil || *helper == nil {
		return
	}
	_ = (*helper).Close()
}

func cohortName(fixture jpegxlFixture, mode string) string {
	if fixture.Name == "jpegxl-gray16" {
		switch mode {
		case ModeWarm:
			return cohortWarm
		case ModeCold:
			return cohortCold
		}
	}
	if mode == ModeSeriesSwitch {
		return cohortSeriesSwitch
	}
	return fixture.Name + "-" + mode
}

type jpegxlFixture struct {
	Name        string
	Fragment    []byte
	Metadata    pixeldata.Metadata
	Lossy       bool
	MaxAbsError int
	Equivalence string
	SHA256      string
	Switch      *jpegxlFixture
}

func loadJPEGXLFixtures(ctx context.Context, opts CampaignOptions) ([]jpegxlFixture, []Skip) {
	var skips []Skip
	var fixtures []jpegxlFixture
	gray, err := loadHashedFile(filepath.Join(opts.CorpusRoot, "jxl", "gray16-lossless.jxl"), "cf7865c694d40074a927b95843a9bbc167bb952f2b0bcc60954c3eb93bbe082d")
	if err != nil {
		skips = append(skips, Skip{Cohort: "jpegxl-gray16", Reason: err.Error()})
	} else {
		fixtures = append(fixtures, jpegxlFixture{
			Name:        "jpegxl-gray16",
			Fragment:    gray,
			SHA256:      "cf7865c694d40074a927b95843a9bbc167bb952f2b0bcc60954c3eb93bbe082d",
			Equivalence: EquivalenceExact,
			Metadata: pixeldata.Metadata{
				Rows: 64, Columns: 64, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 16, HighBit: 15, NumberOfFrames: 1,
				PhotometricInterpretation: "MONOCHROME2",
			},
		})
	}
	color, err := loadHashedFile(filepath.Join(opts.CorpusRoot, "jxl", "rgb8-lossy.jxl"), "f193b56ce6765e08e3d1ecd61af78e455a44a5d049d1524747737d394995d008")
	if err != nil {
		skips = append(skips, Skip{Cohort: "jpegxl-rgb8-lossy", Reason: err.Error()})
	} else {
		colorFix := jpegxlFixture{
			Name:        "jpegxl-rgb8-lossy",
			Fragment:    color,
			SHA256:      "f193b56ce6765e08e3d1ecd61af78e455a44a5d049d1524747737d394995d008",
			Lossy:       true,
			MaxAbsError: 80,
			Equivalence: EquivalenceLossyLimit,
			Metadata: pixeldata.Metadata{
				Rows: 48, Columns: 64, SamplesPerPixel: 3, BitsAllocated: 8, BitsStored: 8, HighBit: 7, NumberOfFrames: 1,
				PhotometricInterpretation: "RGB",
			},
		}
		fixtures = append(fixtures, colorFix)
		if len(fixtures) > 0 && fixtures[0].Name == "jpegxl-gray16" {
			fixtures[0].Switch = &colorFix
		}
	}
	large, skip := maybeLargeGray(ctx, opts)
	if skip.Reason != "" {
		skips = append(skips, skip)
	} else if large.Fragment != nil {
		fixtures = append(fixtures, large)
	}
	return fixtures, skips
}

func loadHashedFile(path, wantSHA string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if wantSHA != "" && got != wantSHA {
		return nil, fmt.Errorf("codeccost: %s sha256=%s want %s", path, got, wantSHA)
	}
	return data, nil
}

func maybeLargeGray(ctx context.Context, opts CampaignOptions) (jpegxlFixture, Skip) {
	if ctx == nil {
		ctx = context.Background()
	}
	cjxl := opts.Cjxl
	if cjxl == "" {
		cjxl, _ = exec.LookPath("cjxl")
	}
	if cjxl == "" {
		return jpegxlFixture{}, Skip{Cohort: "jpegxl-large-gray16", Reason: "cjxl not found; large synthetic cohort skipped"}
	}
	dir, err := os.MkdirTemp("", "codec-cost-large-*")
	if err != nil {
		return jpegxlFixture{}, Skip{Cohort: "jpegxl-large-gray16", Reason: err.Error()}
	}
	defer os.RemoveAll(dir)
	pgm := filepath.Join(dir, "large.pgm")
	if err := writeGray16PGM(pgm, 512, 512); err != nil {
		return jpegxlFixture{}, Skip{Cohort: "jpegxl-large-gray16", Reason: err.Error()}
	}
	out := filepath.Join(dir, "large.jxl")
	cmd := exec.CommandContext(ctx, cjxl, pgm, out, "-d", "0", "-e", "1")
	if output, err := cmd.CombinedOutput(); err != nil {
		return jpegxlFixture{}, Skip{Cohort: "jpegxl-large-gray16", Reason: fmt.Sprintf("cjxl: %s: %s", err, strings.TrimSpace(string(output)))}
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return jpegxlFixture{}, Skip{Cohort: "jpegxl-large-gray16", Reason: err.Error()}
	}
	return jpegxlFixture{
		Name:        "jpegxl-large-gray16",
		Fragment:    data,
		Equivalence: EquivalenceExact,
		Metadata: pixeldata.Metadata{
			Rows: 512, Columns: 512, SamplesPerPixel: 1, BitsAllocated: 16, BitsStored: 16, HighBit: 15, NumberOfFrames: 1,
			PhotometricInterpretation: "MONOCHROME2",
		},
	}, Skip{}
}

func writeGray16PGM(path string, width, height int) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "P5\n%d %d\n65535\n", width, height); err != nil {
		return err
	}
	row := make([]byte, width*2)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value := uint16((x*997 + y*313 + x*y*17) & 0xffff)
			row[x*2] = byte(value >> 8)
			row[x*2+1] = byte(value)
		}
		if _, err := file.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func summarize(frames []FrameResult) []CohortSummary {
	groups := map[string][]FrameResult{}
	for _, frame := range frames {
		if frame.Cancelled {
			continue
		}
		key := frame.Cohort + "\x00" + frame.Backend + "\x00" + frame.Mode
		groups[key] = append(groups[key], frame)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]CohortSummary, 0, len(keys))
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		summary := CohortSummary{Name: parts[0], Backend: parts[1], Mode: parts[2]}
		summary.Median = percentiles(groups[key], 0.50)
		summary.P95 = percentiles(groups[key], 0.95)
		out = append(out, summary)
	}
	return out
}

func percentiles(frames []FrameResult, p float64) StageMedians {
	collect := func(get func(FrameResult) time.Duration) time.Duration {
		values := make([]time.Duration, 0, len(frames))
		for _, frame := range frames {
			values = append(values, get(frame))
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		if len(values) == 0 {
			return 0
		}
		index := int(float64(len(values)-1)*p + 0.5)
		if index >= len(values) {
			index = len(values) - 1
		}
		return values[index]
	}
	return StageMedians{
		Preflight:  collect(func(f FrameResult) time.Duration { return f.Stage(StagePreflight).Duration }),
		Admission:  collect(func(f FrameResult) time.Duration { return f.Stage(StageAdmission).Duration }),
		Prepare:    collect(func(f FrameResult) time.Duration { return f.Stage(StagePrepare).Duration }),
		Launch:     collect(func(f FrameResult) time.Duration { return f.Stage(StageLaunch).Duration }),
		Execute:    collect(func(f FrameResult) time.Duration { return f.Stage(StageExecute).Duration }),
		Convert:    collect(func(f FrameResult) time.Duration { return f.Stage(StageConvert).Duration }),
		Deliver:    collect(func(f FrameResult) time.Duration { return f.Stage(StageDeliver).Duration }),
		FirstFrame: collect(func(f FrameResult) time.Duration { return f.StageRecord.FirstFrame }),
	}
}

func captureEnvironment(ctx context.Context, opts CampaignOptions) Environment {
	if ctx == nil {
		ctx = context.Background()
	}
	brand, _ := exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string").Output()
	runtimes := map[string]string{}
	if opts.Djxl != "" {
		runtimes["djxl"] = versionOutput(ctx, opts.Djxl)
	} else if path, err := exec.LookPath("djxl"); err == nil {
		runtimes["djxl"] = versionOutput(ctx, path)
	}
	if ver, err := exec.CommandContext(ctx, "pkg-config", "--modversion", "libjxl").Output(); err == nil {
		runtimes["libjxl"] = strings.TrimSpace(string(ver))
	}
	commit, _ := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	return Environment{
		GOOS:            runtime.GOOS,
		GOARCH:          runtime.GOARCH,
		GoVersion:       runtime.Version(),
		LogicalCPU:      runtime.NumCPU(),
		GOMAXPROCS:      runtime.GOMAXPROCS(0),
		CPUBrand:        strings.TrimSpace(string(brand)),
		PowerNote:       "frequency scaling and background load are not controlled; results are local evidence",
		Runtimes:        runtimes,
		GitCommit:       strings.TrimSpace(string(commit)),
		CachePolicy:     "hot OS page cache; no cache drop; cache-hit is decoded-frame reuse without relaunch; warm iterations are interleaved across backends",
		ConcurrencyNote: "parent decodes are sequential and interleaved across backends; djxl/libjxl may start internal worker threads; GOMAXPROCS=" + strconv.Itoa(runtime.GOMAXPROCS(0)),
	}
}

func versionOutput(ctx context.Context, executable string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := exec.CommandContext(ctx, executable, "--version").CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out) + " " + err.Error())
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func resolveDjxl(explicit string) (string, string) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", "djxl executable not found: " + explicit
		}
		return explicit, ""
	}
	if env := os.Getenv("DICOM_GO_DJXL"); env != "" {
		return env, ""
	}
	path, err := exec.LookPath("djxl")
	if err != nil {
		return "", "djxl not found in PATH or DICOM_GO_DJXL"
	}
	return path, ""
}

func resolveHelper(ctx context.Context, opts CampaignOptions) (*HelperBackend, string, string) {
	path := opts.HelperPath
	compiledDir := ""
	if path == "" && opts.CompileHelper {
		dir, compiled, err := createCompiledHelperOutput()
		if err != nil {
			return nil, "libjxl helper compile failed: " + err.Error(), ""
		}
		src := opts.HelperSource
		if src == "" {
			src = filepath.Join(discoverModuleRoot(), "examples", "codec-cost", "helperc", "jxl_helper.c")
		}
		if err := compileJXLHelper(ctx, src, compiled); err != nil {
			_ = os.RemoveAll(dir)
			return nil, "libjxl helper compile failed: " + err.Error(), ""
		}
		path = compiled
		compiledDir = dir
	}
	if path == "" {
		return nil, "libjxl helper not requested; pass -compile-helper or -helper", ""
	}
	helper, err := startHelperProcess(ctx, path)
	if err != nil {
		if compiledDir != "" {
			_ = os.RemoveAll(compiledDir)
		}
		return nil, err.Error(), ""
	}
	return helper, "", compiledDir
}

func createCompiledHelperOutput() (dir, binary string, err error) {
	dir, err = os.MkdirTemp("", "dicom-go-jxl-helper-*")
	if err != nil {
		return "", "", err
	}
	return dir, filepath.Join(dir, "dicom-go-jxl-helper"), nil
}

func failRequired(require []string, skips []Skip) error {
	skipped := map[string]string{}
	for _, skip := range skips {
		skipped[skip.Backend] = skip.Reason
		if skip.Cohort != "" {
			skipped[skip.Cohort] = skip.Reason
		}
	}
	for _, name := range require {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if reason, ok := skipped[name]; ok {
			return fmt.Errorf("codeccost: required %s missing: %s", name, reason)
		}
	}
	return nil
}

func discoverCorpusRoot() string {
	if env := os.Getenv("CODEC_COST_CORPUS"); env != "" {
		return env
	}
	root := discoverModuleRoot()
	return filepath.Join(root, "pixeldata", "codecfixture", "testdata", "codecfull")
}

func discoverModuleRoot() string {
	wd, _ := os.Getwd()
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return wd
}

func measureClockOverhead() int64 {
	clock := NewClock(nil)
	clock.Start()
	for _, stage := range ExclusiveStages() {
		clock.Step(stage)
	}
	return clock.Inclusive().Nanoseconds()
}

func djxlCLIArgs(inputPath, outputPath string, _ pixeldata.Metadata) []string {
	return []string{inputPath, outputPath, "--quiet"}
}

func djxlOutputFormat(metadata pixeldata.Metadata) string {
	if metadata.SamplesPerPixel == 3 {
		return "ppm"
	}
	return "pgm"
}

func probePACS() Skip {
	if !pacsProbeEnabled() {
		return Skip{Cohort: "pacs-horos", Reason: "DICOMGO_INTEGRATION/DICOMGO_HOROS_INTEGRATION not set; campaign uses local non-PHI fixtures only"}
	}
	host := strings.TrimSpace(os.Getenv("HOROS_HOST"))
	if host == "" {
		return Skip{Cohort: "pacs-horos", Reason: "HOROS_HOST not set; campaign uses local non-PHI fixtures only"}
	}
	port := strings.TrimSpace(os.Getenv("HOROS_PORT"))
	if port == "" {
		port = "4007"
	}
	target := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", target, 400*time.Millisecond)
	if err != nil {
		return Skip{Cohort: "pacs-horos", Reason: target + " unreachable; campaign uses local non-PHI fixtures only"}
	}
	_ = conn.Close()
	return Skip{Cohort: "pacs-horos", Reason: "reachable but unused; codec-cost does not retrieve from HOROS"}
}

func pacsProbeEnabled() bool {
	if os.Getenv("DICOMGO_HOROS_INTEGRATION") != "" {
		return true
	}
	return os.Getenv("DICOMGO_INTEGRATION") == "1"
}

// WriteReport writes indented JSON.
func WriteReport(path string, report Report) error {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if path == "" {
		_, err = os.Stdout.Write(payload)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}
