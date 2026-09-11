package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/internal/netstore"
	"github.com/ThalesMMS/dicom-go/internal/nofollow"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	errArchive   = errors.New("reference archive: unavailable or inconsistent storage")
	errQuota     = errors.New("reference archive: resource limit")
	errDuplicate = errors.New("reference archive: duplicate SOP instance")
	errQuery     = errors.New("reference archive: unsupported or invalid query")
)

type archiveLimits struct {
	instances, results        int
	instanceBytes, totalBytes int64
}

func defaultArchiveLimits() archiveLimits { return archiveLimits{1000, 1000, 16 << 20, 1 << 30} }
func (l archiveLimits) valid() bool {
	return l.instances > 0 && l.instances <= 10000 && l.results > 0 && l.results <= l.instances && l.instanceBytes > 0 && l.instanceBytes <= 64<<20 && l.totalBytes >= l.instanceBytes && l.totalBytes <= 16<<30
}

type storedInstance struct {
	name   string
	info   os.FileInfo
	record index.Record
}
type archive struct {
	root              string
	rootInfo          os.FileInfo
	lock              *os.File
	gate              chan struct{}
	limits            archiveLimits
	items             map[string]storedInstance
	bytes             int64
	closed, unhealthy bool
	// Per-instance injection for fault tests; normal operation always uses #913.
	save func(context.Context, string, *object.Object, transfer.Syntax) (string, error)
}

func openArchive(ctx context.Context, root string, limits archiveLimits) (*archive, error) {
	if !limits.valid() {
		return nil, errQuota
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, errArchive
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, errArchive
	}
	parent, err := nofollow.OpenDirectory(abs)
	if err != nil {
		return nil, errArchive
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil {
		return nil, errArchive
	}
	lock, err := nofollow.CreateAt(parent, ".archive.lock")
	if errors.Is(err, os.ErrExist) {
		lock, err = nofollow.OpenAt(parent, ".archive.lock")
	}
	if err != nil {
		return nil, errArchive
	}
	if err := lockArchiveFile(lock); err != nil {
		_ = lock.Close()
		return nil, errArchive
	}
	a := &archive{root: abs, rootInfo: info, lock: lock, gate: make(chan struct{}, 1), limits: limits, items: map[string]storedInstance{}, save: netstore.SavePart10WithContext}
	a.gate <- struct{}{}
	failed := true
	defer func() {
		if failed {
			_ = lock.Close()
		}
	}()
	// One extra entry beyond the allowed instances plus the persistent lock lets
	// us detect an overfull directory without an unbounded ReadDir allocation.
	entries, err := parent.ReadDir(limits.instances + 2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errArchive
	}
	if len(entries) > limits.instances+1 {
		return nil, errQuota
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Name() == ".archive.lock" {
			continue
		}
		// Incomplete transactions require operator reconciliation, never an
		// automatic declaration that their data is available or disposable.
		if !strings.HasSuffix(entry.Name(), ".dcm") || entry.IsDir() {
			return nil, errArchive
		}
		item, err := a.inspect(ctx, entry.Name())
		if err != nil {
			return nil, err
		}
		if entry.Name() != item.record.Instance.SOPInstanceUID+".dcm" {
			return nil, errArchive
		}
		if err := a.add(item); err != nil {
			return nil, err
		}
	}
	failed = false
	return a, nil
}

func (a *archive) enter(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.gate:
	}
	if a.closed || a.unhealthy {
		a.leave()
		return errArchive
	}
	return nil
}
func (a *archive) leave() { a.gate <- struct{}{} }
func (a *archive) Close() error {
	<-a.gate
	defer a.leave()
	if a.closed {
		return nil
	}
	a.closed = true
	if err := a.lock.Close(); err != nil {
		return errArchive
	}
	return nil
}
func (a *archive) openEntry(name string) (*os.File, error) {
	parent, err := nofollow.OpenDirectory(a.root)
	if err != nil {
		return nil, errArchive
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil || !os.SameFile(info, a.rootInfo) {
		return nil, errArchive
	}
	f, err := nofollow.OpenAt(parent, name)
	if err != nil {
		return nil, errArchive
	}
	return f, nil
}
func (a *archive) readOptions(skip bool) object.ReadFileOptions {
	return object.ReadFileOptions{MaxTotalBytes: a.limits.instanceBytes, MaxElementBytes: 1 << 20, MaxPixelDataBytes: a.limits.instanceBytes, MaxElements: 10000, MaxSequenceDepth: 32, MaxFragments: 10000, SkipPixelData: skip}
}
func (a *archive) indexOptions() index.Options {
	o := index.DefaultOptions()
	o.Profile = index.ProfileCore | index.ProfilePatient | index.ProfileDescriptions
	o.Limits.MaxTotalBytes = a.limits.instanceBytes
	o.Limits.MaxSelectedValueBytes = 1 << 16
	o.Limits.MaxElements = 10000
	o.Limits.MaxTokens = 100000
	o.Limits.MaxSequenceDepth = 32
	return o
}

// inspect uses the existing parser to check the entire file, discarding pixels
// through a non-seekable reader so truncated bulk data cannot be skipped past EOF.
// The existing indexer then extracts only detached query metadata.
func (a *archive) inspect(ctx context.Context, name string) (storedInstance, error) {
	f, err := a.openEntry(name)
	if err != nil {
		return storedInstance{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > a.limits.instanceBytes {
		return storedInstance{}, errQuota
	}
	parsed, err := object.ReadFileWithOptions(contextReader{ctx, io.LimitReader(f, a.limits.instanceBytes+1)}, a.readOptions(true))
	if err != nil {
		return storedInstance{}, errArchive
	}
	defer parsed.Close()
	if _, err := incomingRecord(parsed.Dataset); err != nil {
		return storedInstance{}, errArchive
	}
	if parsed.TransferSyntax.UID != transfer.ImplicitVRLittleEndian.UID && parsed.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		return storedInstance{}, errArchive
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return storedInstance{}, errArchive
	}
	r, err := index.Read(ctx, index.ReaderAtSource("", f, info.Size()), a.indexOptions())
	if err != nil {
		return storedInstance{}, errArchive
	}
	record := r.Record
	if !validRecord(record) {
		return storedInstance{}, errArchive
	}
	for _, d := range record.Diagnostics {
		if d.Severity == index.SeverityError {
			return storedInstance{}, errArchive
		}
	}
	if record.FileMeta.MediaStorageSOPClassUID != record.Instance.SOPClassUID || record.FileMeta.MediaStorageSOPInstanceUID != record.Instance.SOPInstanceUID {
		return storedInstance{}, errArchive
	}
	return storedInstance{name: name, info: info, record: record}, nil
}
func validRecord(r index.Record) bool {
	supported := false
	for _, uid := range storageClasses {
		if r.Instance.SOPClassUID == uid {
			supported = true
			break
		}
	}
	return supported && core.IsValidUID(r.Study.InstanceUID) && core.IsValidUID(r.Series.InstanceUID) && core.IsValidUID(r.Instance.SOPClassUID) && core.IsValidUID(r.Instance.SOPInstanceUID) && r.Patient != nil && r.Patient.ID != ""
}
func (a *archive) add(item storedInstance) error {
	r := item.record
	if _, exists := a.items[r.Instance.SOPInstanceUID]; exists {
		return errDuplicate
	}
	if len(a.items) >= a.limits.instances || item.info.Size() > a.limits.totalBytes-a.bytes {
		return errQuota
	}
	if err := a.recordConflict(r); err != nil {
		return err
	}
	a.items[r.Instance.SOPInstanceUID] = item
	a.bytes += item.info.Size()
	return nil
}

func (a *archive) recordConflict(r index.Record) error {
	for _, prior := range a.items {
		p := prior.record
		if p.Study.InstanceUID == r.Study.InstanceUID && (p.Study != r.Study || *p.Patient != *r.Patient) {
			return errArchive
		}
		if p.Series.InstanceUID == r.Series.InstanceUID && (p.Study.InstanceUID != r.Study.InstanceUID || p.Series != r.Series) {
			return errArchive
		}
	}
	return nil
}

func (a *archive) Store(ctx context.Context, request dimse.CStoreRequestContext) (uint16, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := netstore.ValidateCStoreDataSet(request.Request.AffectedSOPClassUID, request.Request.AffectedSOPInstanceUID, request.PresentationContext, request.DataSet); err != nil {
		return dimse.StatusCStoreDataSetDoesNotMatch, err
	}
	if err := a.enter(ctx); err != nil {
		return dimse.StatusCStoreOutOfResources, err
	}
	defer a.leave()
	if _, exists := a.items[request.Request.AffectedSOPInstanceUID]; exists {
		return dimse.StatusCStoreDataSetDoesNotMatch, errDuplicate
	}
	if len(a.items) >= a.limits.instances {
		return dimse.StatusCStoreOutOfResources, errQuota
	}
	preflight, err := incomingRecord(request.DataSet)
	if err != nil {
		return dimse.StatusCStoreDataSetDoesNotMatch, err
	}
	if err := a.recordConflict(preflight); err != nil {
		return dimse.StatusCStoreDataSetDoesNotMatch, err
	}
	if request.DataSetSyntax.UID != transfer.ImplicitVRLittleEndian.UID && request.DataSetSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		return dimse.StatusCStoreCannotUnderstand, errArchive
	}
	// Validate/index the representation without keeping an encoded byte buffer.
	// The bounded temporary publication itself is provided by netstore.
	file := &object.File{Dataset: request.DataSet, TransferSyntax: request.DataSetSyntax}
	count := &quotaWriter{ctx: ctx, remaining: min(a.limits.instanceBytes, a.limits.totalBytes-a.bytes)}
	if err := object.WriteFile(count, file); err != nil {
		return dimse.StatusCStoreOutOfResources, errQuota
	}
	path, err := a.save(ctx, a.root, request.DataSet, request.DataSetSyntax)
	if err != nil {
		var outcome *netstore.Error
		if errors.As(err, &outcome) && outcome.Published {
			a.unhealthy = true
		}
		return dimse.StatusCStoreOutOfResources, err
	}
	item, err := a.inspect(ctx, filepath.Base(path))
	if err == nil {
		err = a.add(item)
	}
	if err != nil {
		// A complete file has been committed but cannot be advertised. Preserve
		// it for reconciliation and fail closed until a successful restart.
		a.unhealthy = true
		return dimse.StatusCStoreCannotUnderstand, errArchive
	}
	return dimse.StatusSuccess, nil
}

func (a *archive) snapshot(ctx context.Context) ([]storedInstance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.enter(ctx); err != nil {
		return nil, err
	}
	defer a.leave()
	items := make([]storedInstance, 0, len(a.items))
	for _, item := range a.items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := a.openEntry(item.name)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		closeErr := f.Close()
		if err != nil || closeErr != nil || !sameStoredFile(info, item.info) {
			return nil, errArchive
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].record.Instance.SOPInstanceUID < items[j].record.Instance.SOPInstanceUID
	})
	return items, nil
}
func sameStoredFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func (a *archive) load(ctx context.Context, item storedInstance) (*object.Object, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := a.openEntry(item.name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !sameStoredFile(info, item.info) {
		return nil, errArchive
	}
	file, err := object.ReadFileWithOptions(contextReader{ctx, io.LimitReader(f, a.limits.instanceBytes+1)}, a.readOptions(false))
	if err != nil {
		return nil, errArchive
	}
	defer file.Close()
	class, ok := file.GetUID(core.NewTag(0x0008, 0x0016))
	uid, uidOK := file.GetUID(core.NewTag(0x0008, 0x0018))
	if !ok || !uidOK || class != item.record.Instance.SOPClassUID || uid != item.record.Instance.SOPInstanceUID {
		return nil, errArchive
	}
	// No deferred values: the returned dataset owns bounded pixel bytes and
	// neither the file nor index retains the source descriptor.
	return file.Dataset, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type quotaWriter struct {
	ctx       context.Context
	remaining int64
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, errQuota
	}
	w.remaining -= int64(len(p))
	return len(p), nil
}
