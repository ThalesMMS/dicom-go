package index

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestDefaultScanOptionsAreFinite(t *testing.T) {
	opts := DefaultScanOptions()
	if opts.Workers <= 0 || opts.Workers > MaxScanWorkers {
		t.Fatalf("Workers = %d, want within 1..%d", opts.Workers, MaxScanWorkers)
	}
	if opts.QueueDepth <= 0 || opts.MaxFiles <= 0 || opts.MaxDirectories <= 0 ||
		opts.MaxDepth <= 0 || opts.MaxPathBytes <= 0 || opts.MaxErrors <= 0 {
		t.Fatalf("defaults must all be finite and positive: %+v", opts)
	}
	if opts.ReadOptions.Limits.MaxTokens <= 0 || opts.ReadOptions.Limits.MaxFragments <= 0 {
		t.Fatalf("reader token/fragment defaults must be finite: %+v", opts.ReadOptions.Limits)
	}
	if opts.SymlinkPolicy != SymlinkIgnore {
		t.Fatalf("SymlinkPolicy = %v, want SymlinkIgnore", opts.SymlinkPolicy)
	}
}

func TestScanYieldsFilteredFilesIncrementally(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one.dcm", "two.dcm"} {
		path := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
			textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
			textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		})
		if err := os.Rename(path, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeTestFile(filepath.Join(dir, "ignored.txt"), []byte("not DICOM")); err != nil {
		t.Fatal(err)
	}

	opts := DefaultScanOptions()
	opts.Workers = 1
	opts.QueueDepth = 1
	opts.Filter = func(path string, _ fs.FileInfo) bool { return filepath.Ext(path) == ".dcm" }
	var got []string
	if err := Scan(context.Background(), dir, opts, func(result ScanResult) error {
		if result.Err != nil {
			t.Fatalf("unexpected result error: %v", result.Err)
		}
		if result.Result.Record.Instance.SOPInstanceUID != testSOPInstanceUID {
			t.Fatalf("record = %+v", result.Result.Record)
		}
		if result.Result.Record.Source.Origin != "" {
			t.Fatalf("record retained source origin %q", result.Result.Record.Source.Origin)
		}
		got = append(got, filepath.Base(result.Path))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if want := []string{"one.dcm", "two.dcm"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestScanSymlinkPoliciesNeverFollowDirectoriesOrEscapeRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.dcm")
	fixture := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	if err := os.Rename(fixture, inside); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(root, "inside-link.dcm")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	outside := t.TempDir()
	escapeTarget := filepath.Join(outside, "OUTSIDE-PATIENT.dcm")
	fixture = writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.626.99"),
	})
	if err := os.Rename(fixture, escapeTarget); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(root, "escape-link.dcm")
	if err := os.Symlink(escapeTarget, escapeLink); err != nil {
		t.Fatal(err)
	}
	directoryLink := filepath.Join(root, "directory-link")
	if err := os.Symlink(outside, directoryLink); err != nil {
		t.Fatal(err)
	}
	cycleOne := filepath.Join(root, "cycle-one")
	cycleTwo := filepath.Join(root, "cycle-two")
	if err := os.Symlink(cycleTwo, cycleOne); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cycleOne, cycleTwo); err != nil {
		t.Fatal(err)
	}

	opts := DefaultScanOptions()
	var ignored []ScanResult
	if err := Scan(context.Background(), root, opts, func(result ScanResult) error {
		ignored = append(ignored, result)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 || ignored[0].Path != inside {
		t.Fatalf("default no-follow results = %+v, want only the regular file", ignored)
	}

	opts.SymlinkPolicy = SymlinkFollowFiles
	var followed []ScanResult
	if err := Scan(context.Background(), root, opts, func(result ScanResult) error {
		followed = append(followed, result)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seenInsideLink := false
	symlinkErrors := 0
	for _, result := range followed {
		if result.Path == insideLink && result.Err == nil {
			seenInsideLink = true
		}
		if result.Path == escapeLink || result.Path == directoryLink || result.Path == cycleOne || result.Path == cycleTwo {
			var scanErr *ScanError
			if !errors.As(result.Err, &scanErr) || scanErr.Code != ScanErrorSymlink {
				t.Fatalf("link result = %+v, want redacted symlink error", result)
			}
			if strings.Contains(result.Err.Error(), "OUTSIDE-PATIENT") {
				t.Fatalf("symlink error leaked path: %v", result.Err)
			}
			symlinkErrors++
		}
	}
	if !seenInsideLink || symlinkErrors != 4 {
		t.Fatalf("follow results = %+v, want contained file link and four rejected links", followed)
	}
}

func TestScanRootSymlinkPolicyIsExplicit(t *testing.T) {
	target := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	link := filepath.Join(t.TempDir(), "root-link.dcm")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	for _, tc := range []struct {
		policy SymlinkPolicy
		count  int
		failed bool
	}{
		{policy: SymlinkIgnore, count: 0},
		{policy: SymlinkReject, count: 1, failed: true},
		{policy: SymlinkFollowFiles, count: 1},
	} {
		opts := DefaultScanOptions()
		opts.SymlinkPolicy = tc.policy
		var got []ScanResult
		if err := Scan(context.Background(), link, opts, func(result ScanResult) error {
			got = append(got, result)
			return nil
		}); err != nil {
			t.Fatalf("policy %v: %v", tc.policy, err)
		}
		if len(got) != tc.count {
			t.Fatalf("policy %v yielded %d, want %d", tc.policy, len(got), tc.count)
		}
		if tc.count == 1 && (got[0].Err != nil) != tc.failed {
			t.Fatalf("policy %v result error = %v", tc.policy, got[0].Err)
		}
	}

	directoryLink := filepath.Join(t.TempDir(), "root-directory-link")
	if err := os.Symlink(filepath.Dir(target), directoryLink); err != nil {
		t.Fatal(err)
	}
	opts := DefaultScanOptions()
	opts.SymlinkPolicy = SymlinkFollowFiles
	var got ScanResult
	if err := Scan(context.Background(), directoryLink, opts, func(result ScanResult) error {
		got = result
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertRedactedScanError(t, got.Err, ScanErrorSymlink, filepath.Base(target))
}

func TestScanRejectsFileReplacedBySymlinkAfterFiltering(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim.dcm")
	fixture := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	if err := os.Rename(fixture, victim); err != nil {
		t.Fatal(err)
	}
	outside := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.626.100"),
	})
	probe := filepath.Join(root, "symlink-probe")
	if err := os.Symlink(outside, probe); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}

	opts := DefaultScanOptions()
	opts.Workers = 1
	var mutationErr error
	opts.Filter = func(path string, _ fs.FileInfo) bool {
		if err := os.Remove(path); err != nil {
			mutationErr = err
			return false
		}
		if err := os.Symlink(outside, path); err != nil {
			mutationErr = err
			return false
		}
		return true
	}
	var got ScanResult
	if err := Scan(context.Background(), root, opts, func(result ScanResult) error {
		got = result
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if mutationErr != nil {
		t.Fatal(mutationErr)
	}
	assertRedactedScanError(t, got.Err, ScanErrorFilesystem, filepath.Base(outside))
	if got.Result.Record.Instance.SOPInstanceUID != "" {
		t.Fatalf("scanner followed replacement symlink: %+v", got.Result.Record)
	}
}

func TestScanRedactsCallbackAndFilesystemErrors(t *testing.T) {
	const canary = "SECRET-PATIENT-626"
	file := filepath.Join(t.TempDir(), canary+".dcm")
	if err := writeTestFile(file, []byte("invalid")); err != nil {
		t.Fatal(err)
	}

	opts := DefaultScanOptions()
	opts.Filter = func(string, fs.FileInfo) bool { panic(canary) }
	err := Scan(context.Background(), file, opts, func(ScanResult) error { return nil })
	assertRedactedScanError(t, err, ScanErrorFilter, canary)

	opts.Filter = nil
	err = Scan(context.Background(), file, opts, func(ScanResult) error { return errors.New(canary) })
	assertRedactedScanError(t, err, ScanErrorYield, canary)

	err = Scan(context.Background(), file, opts, func(ScanResult) error { panic(canary) })
	assertRedactedScanError(t, err, ScanErrorYield, canary)

	var invalid ScanResult
	if err := Scan(context.Background(), file, opts, func(result ScanResult) error {
		invalid = result
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertRedactedScanError(t, invalid.Err, ScanErrorRead, canary)
	for cause := invalid.Err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), canary) {
			t.Fatalf("unwrapped error leaked canary: %q", cause)
		}
	}

	missing := filepath.Join(t.TempDir(), canary)
	var got ScanResult
	if err := Scan(context.Background(), missing, opts, func(result ScanResult) error {
		got = result
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got.Path != missing {
		t.Fatalf("structured path = %q, want %q", got.Path, missing)
	}
	assertRedactedScanError(t, got.Err, ScanErrorFilesystem, canary)
}

func TestScanEnforcesFileAndDirectoryBudgetsAtExactBoundary(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one", "two"} {
		if err := writeTestFile(filepath.Join(dir, name), []byte("not DICOM")); err != nil {
			t.Fatal(err)
		}
	}

	opts := DefaultScanOptions()
	opts.MaxFiles = 2
	opts.MaxErrors = 2
	count := 0
	if err := Scan(context.Background(), dir, opts, func(result ScanResult) error {
		if result.Err == nil {
			t.Fatal("invalid fixture unexpectedly parsed")
		}
		count++
		return nil
	}); err != nil {
		t.Fatalf("exact MaxFiles/MaxErrors boundary: %v", err)
	}
	if count != 2 {
		t.Fatalf("yield count = %d, want 2", count)
	}
	opts.MaxErrors = 1
	err := Scan(context.Background(), dir, opts, func(ScanResult) error { return nil })
	assertScanLimit(t, err, "MaxErrors")

	opts.MaxFiles = 1
	opts.MaxErrors = 2
	err = Scan(context.Background(), dir, opts, func(ScanResult) error { return nil })
	assertScanLimit(t, err, "MaxFiles")

	emptyRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(emptyRoot, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	opts = DefaultScanOptions()
	opts.MaxDirectories = 2
	if err := Scan(context.Background(), emptyRoot, opts, func(ScanResult) error { return nil }); err != nil {
		t.Fatalf("exact MaxDirectories boundary: %v", err)
	}
	opts.MaxDirectories = 1
	err = Scan(context.Background(), emptyRoot, opts, func(ScanResult) error { return nil })
	assertScanLimit(t, err, "MaxDirectories")

	symlinkRoot := t.TempDir()
	if err := os.Symlink("missing-one", filepath.Join(symlinkRoot, "one")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := os.Symlink("missing-two", filepath.Join(symlinkRoot, "two")); err != nil {
		t.Fatal(err)
	}
	opts = DefaultScanOptions()
	opts.MaxFiles = 2
	if err := Scan(context.Background(), symlinkRoot, opts, func(ScanResult) error { return nil }); err != nil {
		t.Fatalf("exact MaxFiles symlink boundary: %v", err)
	}
	opts.MaxFiles = 1
	err = Scan(context.Background(), symlinkRoot, opts, func(ScanResult) error { return nil })
	assertScanLimit(t, err, "MaxFiles")
}

func TestScanEnforcesDepthAndPathBudgetsAtExactBoundary(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	grandchild := filepath.Join(child, "grandchild")
	if err := os.MkdirAll(grandchild, 0o700); err != nil {
		t.Fatal(err)
	}

	opts := DefaultScanOptions()
	opts.MaxDepth = 2
	if err := Scan(context.Background(), dir, opts, func(ScanResult) error { return nil }); err != nil {
		t.Fatalf("exact MaxDepth boundary: %v", err)
	}
	opts.MaxDepth = 1
	err := Scan(context.Background(), dir, opts, func(ScanResult) error { return nil })
	assertScanLimit(t, err, "MaxDepth")

	file := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	opts = DefaultScanOptions()
	opts.MaxPathBytes = len([]byte(file))
	if err := Scan(context.Background(), file, opts, func(ScanResult) error { return nil }); err != nil {
		t.Fatalf("exact MaxPathBytes boundary: %v", err)
	}
	opts.MaxPathBytes--
	err = Scan(context.Background(), file, opts, func(ScanResult) error { return nil })
	assertScanLimit(t, err, "MaxPathBytes")
}

func TestScanRejectsUnboundedConcurrencyOptions(t *testing.T) {
	for _, mutate := range []func(*ScanOptions){
		func(opts *ScanOptions) { opts.Workers = MaxScanWorkers + 1 },
		func(opts *ScanOptions) { opts.QueueDepth = MaxScanQueueDepth + 1 },
	} {
		opts := DefaultScanOptions()
		mutate(&opts)
		err := Scan(context.Background(), t.TempDir(), opts, func(ScanResult) error { return nil })
		var scanErr *ScanError
		if !errors.As(err, &scanErr) || scanErr.Code != ScanErrorInvalidOptions {
			t.Fatalf("error = %v, want invalid-options ScanError", err)
		}
	}
}

func TestScanNeverExceedsConfiguredWorkers(t *testing.T) {
	const workers = 3
	dir := t.TempDir()
	encoded := encodeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	for index := 0; index < 9; index++ {
		if err := writeTestFile(filepath.Join(dir, strconv.Itoa(index)+".dcm"), encoded); err != nil {
			t.Fatal(err)
		}
	}

	entered := make(chan struct{}, 9)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	var active atomic.Int32
	var peak atomic.Int32
	opts := DefaultScanOptions()
	opts.Workers = workers
	opts.QueueDepth = workers
	opts.ReadOptions.Selectors = []Selector{{
		ID:         "concurrency-gate",
		Tag:        tags.SOPInstanceUID,
		Occurrence: OccurrenceFirst,
		Handle: func(ctx context.Context, _ SelectedElement) error {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}
			entered <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}}

	done := make(chan error, 1)
	go func() {
		done <- Scan(context.Background(), dir, opts, func(ScanResult) error { return nil })
	}()
	for range workers {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("configured workers did not reach selector callback")
		}
	}
	if got := peak.Load(); got != workers {
		t.Fatalf("peak readers = %d, want %d", got, workers)
	}
	releaseAll()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got > workers {
		t.Fatalf("peak readers = %d, exceeded %d", got, workers)
	}
}

func TestScanCancellationUnblocksBoundedPipeline(t *testing.T) {
	dir := t.TempDir()
	for index := 0; index < 64; index++ {
		if err := writeTestFile(filepath.Join(dir, strconv.Itoa(index)+".dcm"), []byte("invalid")); err != nil {
			t.Fatal(err)
		}
	}
	opts := DefaultScanOptions()
	opts.Workers = 4
	opts.QueueDepth = 1
	opts.MaxErrors = 64

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Scan(ctx, dir, opts, func(ScanResult) error {
			select {
			case <-entered:
			default:
				close(entered)
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("yield was not invoked incrementally")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not unblock bounded scanner pipeline")
	}
}

func TestScanClassifiesPerFileReadLimits(t *testing.T) {
	file := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	opts := DefaultScanOptions()
	opts.ReadOptions.Limits.MaxTotalBytes = 1
	var resultErr error
	if err := Scan(context.Background(), file, opts, func(result ScanResult) error {
		resultErr = result.Err
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var scanErr *ScanError
	if !errors.As(resultErr, &scanErr) || scanErr.Code != ScanErrorLimit || scanErr.Limit != "ReadOptions.Limits" ||
		!errors.Is(resultErr, ErrResourceLimit) {
		t.Fatalf("per-file limit error = %#v", resultErr)
	}
}

func TestScanDoesNotRetainFilesOrGoroutines(t *testing.T) {
	file := writeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
	})
	beforeGoroutines := runtime.NumGoroutine()
	beforeFDs := openFileDescriptorCount()
	opts := DefaultScanOptions()
	opts.Workers = 4
	opts.QueueDepth = 1
	for iteration := 0; iteration < 25; iteration++ {
		if err := Scan(context.Background(), file, opts, func(ScanResult) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > beforeGoroutines+2 && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > beforeGoroutines+2 {
		t.Fatalf("goroutines grew from %d to %d", beforeGoroutines, after)
	}
	if beforeFDs >= 0 {
		if after := openFileDescriptorCount(); after > beforeFDs+2 {
			t.Fatalf("open descriptors grew from %d to %d", beforeFDs, after)
		}
	}
	if err := os.Rename(file, file+".closed"); err != nil {
		t.Fatalf("source remained unavailable after Scan: %v", err)
	}
}

func assertScanLimit(t *testing.T, err error, limit string) {
	t.Helper()
	var scanErr *ScanError
	if !errors.As(err, &scanErr) || scanErr.Code != ScanErrorLimit || scanErr.Limit != limit {
		t.Fatalf("error = %#v, want ScanErrorLimit(%s)", err, limit)
	}
}

func assertRedactedScanError(t *testing.T, err error, code ScanErrorCode, canary string) {
	t.Helper()
	var scanErr *ScanError
	if !errors.As(err, &scanErr) || scanErr.Code != code {
		t.Fatalf("error = %#v, want ScanError(%s)", err, code)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error leaked canary: %q", err)
	}
}

func openFileDescriptorCount() int {
	for _, directory := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(directory)
		if err == nil {
			return len(entries)
		}
	}
	return -1
}
