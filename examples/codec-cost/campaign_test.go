package codeccost

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunCampaignSkipsMissingDjxlExplicitly(t *testing.T) {
	report, err := RunCampaign(context.Background(), CampaignOptions{
		Djxl:          "/nonexistent/dicom-go-djxl",
		Iterations:    1,
		Warmup:        1,
		CompileHelper: false,
		CorpusRoot:    t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skips) == 0 {
		t.Fatal("missing djxl produced no skips")
	}
	found := false
	for _, skip := range report.Skips {
		if skip.Backend == backendCLI && skip.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skips = %+v, want jpegxl-cli reason", report.Skips)
	}
	if report.Decision.Choice == "" {
		t.Fatal("decision missing")
	}
}

func TestFailRequiredIgnoresEmptyNamesFromTrailingComma(t *testing.T) {
	err := failRequired([]string{backendCLI, "", "  "}, []Skip{
		{Cohort: "jpegxl-multiframe", Reason: "JPEG XL still-image transfer syntaxes only"},
	})
	if err != nil {
		t.Fatalf("empty -require names failed closed: %v", err)
	}
}

func TestFailRequiredStillFailsNamedSkipWithEmptyEntries(t *testing.T) {
	err := failRequired([]string{"", backendCLI}, []Skip{
		{Backend: backendCLI, Reason: "djxl not found"},
	})
	if err == nil {
		t.Fatal("required jpegxl-cli skip succeeded")
	}
	if !strings.Contains(err.Error(), "jpegxl-cli") {
		t.Fatalf("error = %v, want jpegxl-cli", err)
	}
}

func TestRunCampaignRequireMissingRuntimeFails(t *testing.T) {
	_, err := RunCampaign(context.Background(), CampaignOptions{
		Djxl:       "/nonexistent/dicom-go-djxl",
		Iterations: 1,
		Require:    []string{backendCLI},
		CorpusRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("required missing runtime succeeded")
	}
	if !strings.Contains(err.Error(), "jpegxl-cli") {
		t.Fatalf("error = %v, want jpegxl-cli", err)
	}
}

func TestRunCampaignRecordsNonJPEGXLSkips(t *testing.T) {
	report, err := RunCampaign(context.Background(), CampaignOptions{
		Djxl:       "/nonexistent/dicom-go-djxl",
		Iterations: 1,
		CorpusRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"jpeg-ls-charls": false, "jpeg2000-openjpeg": false, "jpegxl-multiframe": false}
	for _, skip := range report.Skips {
		if _, ok := want[skip.Backend]; ok {
			want[skip.Backend] = true
		}
		if _, ok := want[skip.Cohort]; ok {
			want[skip.Cohort] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing skip %s in %+v", name, report.Skips)
		}
	}
}

func TestProbePACSSkipsWithoutDialWhenIntegrationUnset(t *testing.T) {
	t.Setenv("DICOMGO_INTEGRATION", "")
	t.Setenv("DICOMGO_HOROS_INTEGRATION", "")
	t.Setenv("HOROS_HOST", "192.0.2.1")
	t.Setenv("HOROS_PORT", "4007")
	started := time.Now()
	skip := probePACS()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("probePACS took %s without an integration gate; likely dialed", elapsed)
	}
	if skip.Cohort != "pacs-horos" || skip.Reason == "" {
		t.Fatalf("skip = %+v, want pacs-horos reason", skip)
	}
	if strings.Contains(skip.Reason, "unreachable") || strings.Contains(skip.Reason, "reachable") {
		t.Fatalf("reason %q implies a dial", skip.Reason)
	}
}

func TestProbePACSSkipsWithoutDialWhenHostUnset(t *testing.T) {
	t.Setenv("DICOMGO_HOROS_INTEGRATION", "1")
	t.Setenv("DICOMGO_INTEGRATION", "")
	t.Setenv("HOROS_HOST", "")
	t.Setenv("HOROS_PORT", "")
	started := time.Now()
	skip := probePACS()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("probePACS took %s without HOROS_HOST; likely dialed a hardcoded address", elapsed)
	}
	if skip.Cohort != "pacs-horos" || skip.Reason == "" {
		t.Fatalf("skip = %+v, want pacs-horos reason", skip)
	}
	if strings.Contains(skip.Reason, "unreachable") || strings.Contains(skip.Reason, "reachable but") {
		t.Fatalf("reason %q implies a dial", skip.Reason)
	}
}

func TestProbePACSDialsWhenHorosGateAndHostAreSet(t *testing.T) {
	if os.Getenv("DICOMGO_HOROS_INTEGRATION") == "" {
		t.Skip("set DICOMGO_HOROS_INTEGRATION to exercise the PACS dial path")
	}
	t.Setenv("HOROS_HOST", "127.0.0.1")
	t.Setenv("HOROS_PORT", "1")
	skip := probePACS()
	if skip.Cohort != "pacs-horos" {
		t.Fatalf("skip = %+v, want pacs-horos", skip)
	}
	if !strings.Contains(skip.Reason, "unreachable") && !strings.Contains(skip.Reason, "reachable but unused") {
		t.Fatalf("reason %q, want a dial result", skip.Reason)
	}
}

func TestProbePACSDialsWhenDicomgoIntegrationAndHostAreSet(t *testing.T) {
	if os.Getenv("DICOMGO_INTEGRATION") == "" {
		t.Skip("set DICOMGO_INTEGRATION=1 to exercise the PACS dial path")
	}
	t.Setenv("HOROS_HOST", "127.0.0.1")
	t.Setenv("HOROS_PORT", "1")
	skip := probePACS()
	if skip.Cohort != "pacs-horos" {
		t.Fatalf("skip = %+v, want pacs-horos", skip)
	}
	if !strings.Contains(skip.Reason, "unreachable") && !strings.Contains(skip.Reason, "reachable but unused") {
		t.Fatalf("reason %q, want a dial result", skip.Reason)
	}
}

func TestMeasureFixtureMarksEquivalenceSkippedWithoutReference(t *testing.T) {
	backend := namedBackend{name: "stub", inner: &cancelAwareBackend{pixels: []byte{1, 2}}}
	cached := map[string]*CachedBackend{"stub": NewCachedBackend(backend.inner)}
	fixture := jpegxlFixture{
		Name:        "jpegxl-gray16",
		Fragment:    []byte("encoded"),
		Equivalence: EquivalenceExact,
		Metadata:    gray8x2Metadata(),
	}
	frames, err := measureFixture(context.Background(), []namedBackend{backend}, cached, fixture, CampaignOptions{Iterations: 1, Warmup: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	for _, frame := range frames {
		if frame.Equivalence != EquivalenceSkipped {
			t.Fatalf("equivalence = %q, want skipped without a reference decode", frame.Equivalence)
		}
	}
}

func TestMeasureFixtureKeepsExactWhenReferenceExists(t *testing.T) {
	backend := namedBackend{name: "stub", inner: &cancelAwareBackend{pixels: []byte{1, 2}}}
	cached := map[string]*CachedBackend{"stub": NewCachedBackend(backend.inner)}
	fixture := jpegxlFixture{
		Name:        "jpegxl-gray16",
		Fragment:    []byte("encoded"),
		Equivalence: EquivalenceExact,
		Metadata:    gray8x2Metadata(),
	}
	frames, err := measureFixture(context.Background(), []namedBackend{backend}, cached, fixture, CampaignOptions{Iterations: 1, Warmup: 1}, map[string][]byte{"jpegxl-gray16": {1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, frame := range frames {
		if frame.Cancelled {
			if frame.Equivalence != EquivalenceSkipped {
				t.Fatalf("cancelled equivalence = %q, want skipped", frame.Equivalence)
			}
			continue
		}
		found = true
		if frame.Equivalence != EquivalenceExact {
			t.Fatalf("equivalence = %q, want exact when a reference exists", frame.Equivalence)
		}
	}
	if !found {
		t.Fatal("no non-cancelled frames")
	}
}

type cancelAwareBackend struct {
	pixels []byte
}

func (b *cancelAwareBackend) Decode(ctx context.Context, req DecodeRequest) (FrameResult, error) {
	result := FrameResult{Mode: req.Mode, Pixels: append([]byte(nil), b.pixels...), Launches: 1}
	if err := ctx.Err(); err != nil {
		result.Cancelled = true
		return result, err
	}
	return result, nil
}

func TestMaybeLargeGrayHonorsCanceledContext(t *testing.T) {
	hanging := writeHangingCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan Skip, 1)
	go func() {
		_, skip := maybeLargeGray(ctx, CampaignOptions{Cjxl: hanging})
		done <- skip
	}()
	select {
	case skip := <-done:
		if skip.Reason == "" {
			t.Fatal("canceled cjxl produced a fixture instead of a skip")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("maybeLargeGray ignored ctx; cjxl can hang the campaign")
	}
}

func TestCaptureEnvironmentHonorsCanceledContext(t *testing.T) {
	hanging := writeHangingCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		_ = captureEnvironment(ctx, CampaignOptions{Djxl: hanging})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("captureEnvironment ignored ctx; sysctl/pkg-config/git/djxl can hang")
	}
}

func TestCreateCompiledHelperOutputUsesPrivateUniqueDir(t *testing.T) {
	dir1, bin1, err := createCompiledHelperOutput()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir1) })
	dir2, bin2, err := createCompiledHelperOutput()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir2) })

	shared := filepath.Join(os.TempDir(), "dicom-go-jxl-helper")
	if dir1 == shared || bin1 == shared || dir2 == shared || bin2 == shared {
		t.Fatalf("compiled helper used shared path %s", shared)
	}
	if dir1 == dir2 {
		t.Fatalf("compiled helper dirs collided: %s", dir1)
	}
	if filepath.Dir(bin1) != dir1 || filepath.Base(bin1) != "dicom-go-jxl-helper" {
		t.Fatalf("binary %s is not inside private dir %s", bin1, dir1)
	}
	if filepath.Dir(bin2) != dir2 {
		t.Fatalf("binary %s is not inside private dir %s", bin2, dir2)
	}
}

func TestResolveHelperCompileFailureRemovesPrivateDir(t *testing.T) {
	pattern := filepath.Join(os.TempDir(), "dicom-go-jxl-helper-*")
	before, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	helper, reason, compiledDir := resolveHelper(context.Background(), CampaignOptions{
		CompileHelper: true,
		HelperSource:  filepath.Join(t.TempDir(), "missing.c"),
	})
	if compiledDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(compiledDir) })
	}
	if helper != nil {
		_ = helper.Close()
		t.Fatal("helper started from missing source")
	}
	if reason == "" {
		t.Fatal("compile failure produced no skip reason")
	}
	if compiledDir != "" {
		t.Fatalf("failed compile left dir %s", compiledDir)
	}
	after, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("leaked helper dirs: before %v after %v", before, after)
	}
}

func TestFinishHelperPtrSkipsAfterCampaignClearsHelper(t *testing.T) {
	calls := 0
	h := &HelperBackend{closer: closeFunc(func() error {
		calls++
		return nil
	})}
	_ = closeAndCheckResources(h)
	if calls != 1 {
		t.Fatalf("closeAndCheckResources closer calls = %d, want 1", calls)
	}
	h = nil
	finishHelperPtr(&h)
	if calls != 1 {
		t.Fatalf("deferred close after nil assignment called closer again (%d)", calls)
	}
}

func TestFinishHelperPtrClosesOpenHelper(t *testing.T) {
	calls := 0
	h := &HelperBackend{closer: closeFunc(func() error {
		calls++
		return nil
	})}
	finishHelperPtr(&h)
	if calls != 1 {
		t.Fatalf("closer calls = %d, want 1 on early campaign return", calls)
	}
}

func TestCompileJXLHelperHonorsCanceledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH override uses a POSIX pkg-config stub")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "pkg-config")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- compileJXLHelper(ctx, "missing.c", filepath.Join(dir, "out"))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled compile succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compileJXLHelper ignored ctx; pkg-config/cc can hang")
	}
}

func writeHangingCommand(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hanging command stub is POSIX-specific")
	}
	path := filepath.Join(t.TempDir(), "hang")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProfileFilesDoNotStartHTTP(t *testing.T) {
	dir := t.TempDir()
	profiles := ProfileFiles{
		CPU:   dir + "/cpu.pprof",
		Heap:  dir + "/heap.pprof",
		Trace: dir + "/trace.out",
	}
	if err := profiles.Start(); err != nil {
		t.Fatal(err)
	}
	if err := profiles.Stop(); err != nil {
		t.Fatal(err)
	}
}
