package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/internal/netstore"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestArchiveConcurrentDuplicatesAndQueryLimits(t *testing.T) {
	ctx := context.Background()
	limits := defaultArchiveLimits()
	limits.results = 1
	a, err := openArchive(ctx, t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var wg sync.WaitGroup
	outcomes := make(chan uint16, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := a.Store(ctx, storeRequest(syntheticInstance("1.2.1", "1.2.1.1", "1.2.1.1.1")))
			outcomes <- status
		}()
	}
	wg.Wait()
	close(outcomes)
	succeeded := 0
	for status := range outcomes {
		if status == 0 {
			succeeded++
		} else if status != dimse.StatusCStoreDataSetDoesNotMatch {
			t.Fatalf("duplicate status: %04X", status)
		}
	}
	if succeeded != 1 {
		t.Fatalf("published duplicate count: %d", succeeded)
	}
	if status, err := a.Store(ctx, storeRequest(syntheticInstance("1.2.2", "1.2.2.1", "1.2.2.1.1"))); status != 0 || err != nil {
		t.Fatal(err)
	}
	q := object.New(nil)
	q.Put(textElement("StudyInstanceUID", ""))
	if _, err := a.Find(ctx, dimse.CFindRequestContext{Identifier: q, QueryRetrieveLevel: "STUDY"}); !errors.Is(err, errQuota) {
		t.Fatalf("query limit: %v", err)
	}
	if _, err := a.Get(ctx, dimse.CGetRequestContext{Identifier: q, QueryRetrieveLevel: "SERIES"}); !errors.Is(err, errQuery) {
		t.Fatalf("missing ancestor keys: %v", err)
	}
}

func TestArchivePostPublicationFailureRequiresRebuild(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := openArchive(ctx, dir, defaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	original := a.save
	a.save = func(ctx context.Context, dir string, ds *object.Object, syntax transfer.Syntax) (string, error) {
		path, err := original(ctx, dir, ds, syntax)
		if err != nil {
			return path, err
		}
		return path, &netstore.Error{Operation: "injected post-publication close", Published: true}
	}
	if status, err := a.Store(ctx, storeRequest(syntheticInstance("1.2.1", "1.2.1.1", "1.2.1.1.1"))); status == 0 || err == nil {
		t.Fatal("post-publication failure announced success")
	}
	if _, err := a.snapshot(ctx); err == nil {
		t.Fatal("unreconciled index advertised")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = openArchive(ctx, dir, defaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	items, err := a.snapshot(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("complete publication not recovered: %d %v", len(items), err)
	}
}

func TestArchiveDuplicateQuotaAndFailedWriteDoNotAdvertise(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	limits := defaultArchiveLimits()
	limits.instances = 1
	limits.results = 1
	a, err := openArchive(ctx, dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ds := syntheticInstance("1.2.1", "1.2.1.1", "1.2.1.1.1")
	original := a.save
	a.save = func(context.Context, string, *object.Object, transfer.Syntax) (string, error) {
		return "", os.ErrPermission
	}
	if status, err := a.Store(ctx, storeRequest(ds)); err == nil || status == 0 {
		t.Fatal("failed write succeeded")
	}
	if items, err := a.snapshot(ctx); err != nil || len(items) != 0 {
		t.Fatal("failed write advertised")
	}
	a.save = original
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if status, err := a.Store(canceled, storeRequest(ds)); err == nil || status == 0 {
		t.Fatal("canceled write succeeded")
	}
	if status, err := a.Store(ctx, storeRequest(ds)); err != nil || status != 0 {
		t.Fatal(err)
	}
	if status, err := a.Store(ctx, storeRequest(ds)); !errors.Is(err, errDuplicate) || status == 0 {
		t.Fatalf("duplicate policy: %04X %v", status, err)
	}
	if status, err := a.Store(ctx, storeRequest(syntheticInstance("1.2.2", "1.2.2.1", "1.2.2.1.1"))); !errors.Is(err, errQuota) || status == 0 {
		t.Fatalf("instance quota: %04X %v", status, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected files: %v %v", entries, err)
	}
}

func TestArchiveByteQuotaAndInvalidMetadataBeforePublication(t *testing.T) {
	ctx := context.Background()
	limits := defaultArchiveLimits()
	limits.instanceBytes = 256
	limits.totalBytes = 256
	a, err := openArchive(ctx, t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if status, err := a.Store(ctx, storeRequest(syntheticInstance("1.2.1", "1.2.1.1", "1.2.1.1.1"))); status == 0 || !errors.Is(err, errQuota) {
		t.Fatalf("byte quota: %04X %v", status, err)
	}
	ds := syntheticInstance("../invalid", "1.2.1.1", "1.2.1.1.1")
	if status, err := a.Store(ctx, storeRequest(ds)); status != dimse.StatusCStoreDataSetDoesNotMatch || err == nil {
		t.Fatalf("invalid archive identity: %04X %v", status, err)
	}
	if items, err := a.snapshot(ctx); err != nil || len(items) != 0 {
		t.Fatal("invalid/quota data advertised")
	}
	entries, err := os.ReadDir(a.root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("uncommitted files: %v %v", entries, err)
	}
}

func TestArchiveRestartRejectsTruncatedPixelsAndConflictingStudy(t *testing.T) {
	for _, scenario := range []string{"truncated", "conflicting-study"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			ds := syntheticInstance("1.2.1", "1.2.1.1", "1.2.1.1.1")
			path, err := netstore.SavePart10(dir, ds, transfer.ExplicitVRLittleEndian)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "truncated" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, info.Size()-2); err != nil {
					t.Fatal(err)
				}
			} else {
				ds = syntheticInstance("1.2.1", "1.2.1.2", "1.2.1.2.1")
				ds.Put(textElement("PatientID", "CONFLICTING_SYNTHETIC"))
				if _, err := netstore.SavePart10(dir, ds, transfer.ExplicitVRLittleEndian); err != nil {
					t.Fatal(err)
				}
			}
			if a, err := openArchive(ctx, dir, defaultArchiveLimits()); err == nil {
				_ = a.Close()
				t.Fatal("invalid restart advertised instances")
			}
		})
	}
}

func TestArchiveMissingInstanceFailsSelectionAndCanceledLoad(t *testing.T) {
	ctx := context.Background()
	a, err := openArchive(ctx, t.TempDir(), defaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if status, err := a.Store(ctx, storeRequest(syntheticInstance("1.2.1", "1.2.1.1", "1.2.1.1.1"))); status != 0 || err != nil {
		t.Fatal(err)
	}
	items, err := a.snapshot(ctx)
	if err != nil || len(items) != 1 {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.load(canceled, items[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load: %v", err)
	}
	if err := os.Remove(filepath.Join(a.root, items[0].name)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.snapshot(ctx); err == nil {
		t.Fatal("missing instance advertised")
	}
	if _, err := a.load(ctx, items[0]); err == nil {
		t.Fatal("missing instance retrieved")
	}
}
