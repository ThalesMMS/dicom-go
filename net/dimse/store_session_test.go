package dimse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type descriptorStoreSource struct {
	descriptor StoreDescriptor
	err        error
}

func (s descriptorStoreSource) Inspect(context.Context) (StoreDescriptor, error) {
	return s.descriptor, s.err
}

func (descriptorStoreSource) Open(context.Context) (OpenedStoreSource, error) {
	return OpenedStoreSource{}, errors.New("not used by planner test")
}

func TestPlanStoreBatchReusesPairsAndKeepsMixedSyntaxSeparate(t *testing.T) {
	sources := []StoreSource{
		descriptorStoreSource{descriptor: testStoreDescriptor("1.2.3", "1.2.3.1", transfer.JPEGBaseline.UID)},
		descriptorStoreSource{descriptor: testStoreDescriptor("1.2.3", "1.2.3.2", transfer.ExplicitVRLittleEndian.UID)},
		descriptorStoreSource{descriptor: testStoreDescriptor("1.2.3", "1.2.3.3", transfer.JPEGBaseline.UID)},
	}

	plan, err := PlanStoreBatch(context.Background(), sources, StorePlanOptions{})
	if err != nil {
		t.Fatalf("PlanStoreBatch() error = %v", err)
	}
	if got := len(plan.Associations); got != 1 {
		t.Fatalf("associations = %d, want 1", got)
	}
	association := plan.Associations[0]
	if got := len(association.Contexts); got != 2 {
		t.Fatalf("contexts = %d, want 2", got)
	}
	if got := []byte{association.Contexts[0].ID, association.Contexts[1].ID}; got[0] != 1 || got[1] != 3 {
		t.Fatalf("context IDs = %v, want [1 3]", got)
	}
	if association.Contexts[0].TransferSyntaxUIDs[0] != transfer.JPEGBaseline.UID ||
		association.Contexts[1].TransferSyntaxUIDs[0] != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("contexts = %#v, want source syntax order", association.Contexts)
	}
	if got := len(association.Items); got != len(sources) {
		t.Fatalf("items = %d, want %d", got, len(sources))
	}
	if association.Items[0].PresentationContextID != association.Items[2].PresentationContextID {
		t.Fatalf("repeated pair PCIDs = %d and %d, want reuse", association.Items[0].PresentationContextID, association.Items[2].PresentationContextID)
	}
}

func TestPlanStoreBatchSplitsAfter128ContextsDeterministically(t *testing.T) {
	sources := make([]StoreSource, ul.MaxPresentationContexts+1)
	for i := range sources {
		sources[i] = descriptorStoreSource{descriptor: testStoreDescriptor(
			fmt.Sprintf("1.2.840.10008.5.1.4.%d", i+1),
			fmt.Sprintf("1.2.826.0.1.%d", i+1),
			transfer.ExplicitVRLittleEndian.UID,
		)}
	}

	first, err := PlanStoreBatch(context.Background(), sources, StorePlanOptions{})
	if err != nil {
		t.Fatalf("PlanStoreBatch() error = %v", err)
	}
	second, err := PlanStoreBatch(context.Background(), sources, StorePlanOptions{})
	if err != nil {
		t.Fatalf("second PlanStoreBatch() error = %v", err)
	}
	if got := len(first.Associations); got != 2 {
		t.Fatalf("associations = %d, want 2", got)
	}
	if got := len(first.Associations[0].Contexts); got != ul.MaxPresentationContexts {
		t.Fatalf("first contexts = %d, want %d", got, ul.MaxPresentationContexts)
	}
	if got := len(first.Associations[1].Contexts); got != 1 {
		t.Fatalf("second contexts = %d, want 1", got)
	}
	if first.Associations[0].Contexts[ul.MaxPresentationContexts-1].ID != 255 || first.Associations[1].Contexts[0].ID != 1 {
		t.Fatalf("split context IDs = %d/%d, want 255/1", first.Associations[0].Contexts[ul.MaxPresentationContexts-1].ID, first.Associations[1].Contexts[0].ID)
	}
	if fmt.Sprint(first.Associations) != fmt.Sprint(second.Associations) {
		t.Fatal("PlanStoreBatch() is not deterministic")
	}
}

func TestPlanStoreBatchLimitsAndInspectionFailures(t *testing.T) {
	descriptor := testStoreDescriptor("1.2.3", "1.2.3.1", transfer.ExplicitVRLittleEndian.UID)
	descriptor.Size = 10
	sources := []StoreSource{
		descriptorStoreSource{descriptor: descriptor},
		descriptorStoreSource{err: errors.New("contains sensitive source details")},
	}

	plan, err := PlanStoreBatch(context.Background(), sources, StorePlanOptions{})
	if err != nil {
		t.Fatalf("PlanStoreBatch() error = %v", err)
	}
	if got := len(plan.Failures); got != 1 || plan.Failures[0].SourceIndex != 1 {
		t.Fatalf("failures = %#v, want source 1", plan.Failures)
	}
	if strings := plan.Failures[0].Err.Error(); strings == "contains sensitive source details" {
		t.Fatalf("failure error exposed source details: %q", strings)
	}

	_, err = PlanStoreBatch(context.Background(), sources[:1], StorePlanOptions{Limits: StoreLimits{MaxItemBytes: 9}})
	if !errors.Is(err, ErrStoreResourceLimit) {
		t.Fatalf("MaxItemBytes error = %v, want ErrStoreResourceLimit", err)
	}
}

func TestPlanStoreBatchRejectsMalformedUIDs(t *testing.T) {
	tests := []StoreDescriptor{
		testStoreDescriptor("1.02.3", "1.2.3.1", transfer.ExplicitVRLittleEndian.UID),
		testStoreDescriptor("1.2.3", "1.2..3", transfer.ExplicitVRLittleEndian.UID),
		testStoreDescriptor("3.2.3", "1.2.3.1", transfer.ExplicitVRLittleEndian.UID),
	}
	for i, descriptor := range tests {
		plan, err := PlanStoreBatch(context.Background(), []StoreSource{descriptorStoreSource{descriptor: descriptor}}, StorePlanOptions{})
		if err != nil {
			t.Fatalf("case %d PlanStoreBatch() error = %v", i, err)
		}
		if len(plan.Failures) != 1 || !errors.Is(plan.Failures[0].Err, ErrStoreInvalidSource) {
			t.Fatalf("case %d failures = %#v, want invalid source", i, plan.Failures)
		}
	}
}

func TestPlanStoreBatchRejectsPixelHeaderMismatchedWithTransferSyntax(t *testing.T) {
	descriptor := testStoreDescriptor("1.2.3", "1.2.3.1", transfer.JPEGBaseline.UID)
	descriptor.PixelDataHeaderSet = true
	descriptor.PixelDataHeader = core.ElementHeader{
		Tag:       core.TagPixelData,
		VR:        core.VROB,
		Length:    core.Length(8),
		LengthSet: true,
	}
	plan, err := PlanStoreBatch(context.Background(), []StoreSource{descriptorStoreSource{descriptor: descriptor}}, StorePlanOptions{})
	if err != nil {
		t.Fatalf("PlanStoreBatch() error = %v", err)
	}
	if len(plan.Failures) != 1 || !errors.Is(plan.Failures[0].Err, ErrStoreInvalidSource) {
		t.Fatalf("failures = %#v, want invalid source", plan.Failures)
	}
}

func testStoreDescriptor(sopClassUID, sopInstanceUID, syntaxUID string) StoreDescriptor {
	return StoreDescriptor{
		SOPClassUID:                sopClassUID,
		SOPInstanceUID:             sopInstanceUID,
		TransferSyntaxUID:          syntaxUID,
		WritableTransferSyntaxUIDs: []string{syntaxUID},
		Size:                       1,
	}
}

func BenchmarkPlanStoreBatch(b *testing.B) {
	for _, count := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("same_context_%d", count), func(b *testing.B) {
			sources := make([]StoreSource, count)
			for i := range sources {
				sources[i] = descriptorStoreSource{descriptor: testStoreDescriptor(
					dicomtest.TestSOPClassUID, fmt.Sprintf("1.2.826.0.1.%d", i+1), transfer.ExplicitVRLittleEndian.UID,
				)}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := PlanStoreBatch(context.Background(), sources, StorePlanOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("split_129_contexts", func(b *testing.B) {
		sources := make([]StoreSource, ul.MaxPresentationContexts+1)
		for i := range sources {
			sources[i] = descriptorStoreSource{descriptor: testStoreDescriptor(
				fmt.Sprintf("1.2.840.10008.5.1.4.%d", i+1), fmt.Sprintf("1.2.826.0.2.%d", i+1), transfer.ExplicitVRLittleEndian.UID,
			)}
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := PlanStoreBatch(context.Background(), sources, StorePlanOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("batch_reuse_vs_per_file_1000", func(b *testing.B) {
		sources := make([]StoreSource, 1_000)
		for i := range sources {
			sources[i] = descriptorStoreSource{descriptor: testStoreDescriptor(
				dicomtest.TestSOPClassUID, fmt.Sprintf("1.2.826.0.3.%d", i+1), transfer.ExplicitVRLittleEndian.UID,
			)}
		}
		b.Run("reused_batch", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := PlanStoreBatch(context.Background(), sources, StorePlanOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("per_file_plans", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, source := range sources {
					if _, err := PlanStoreBatch(context.Background(), []StoreSource{source}, StorePlanOptions{}); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	})
}

func BenchmarkStoreSessionAssociationReuse(b *testing.B) {
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		b.Fatal(err)
	}
	fixture, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		b.Fatal(err)
	}
	for _, count := range []int{1, 10, 100, 1_000} {
		sources := make([]StoreSource, count)
		for i := range sources {
			sources[i] = NewDataSetStoreSource(fixture.Dataset, fixture.TransferSyntax, int64(len(data)))
		}
		b.Run(fmt.Sprintf("reused/%d", count), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				benchmarkStoreSessionRoundTrip(b, sources, []int{count})
			}
		})
		b.Run(fmt.Sprintf("per_file/%d", count), func(b *testing.B) {
			associations := make([]int, count)
			for i := range associations {
				associations[i] = 1
			}
			for i := 0; i < b.N; i++ {
				benchmarkStoreSessionRoundTrip(b, sources, associations)
			}
		})
	}
}

func benchmarkStoreSessionRoundTrip(b *testing.B, sources []StoreSource, associationItems []int) {
	b.Helper()
	b.StopTimer()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		b.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		for _, itemCount := range associationItems {
			assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
				AETitle: "STORESCP", Context: ctx,
				SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
				SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
			})
			if err != nil {
				serverDone <- err
				return
			}
			for i := 0; i < itemCount; i++ {
				pcID, request, err := ReceiveCStoreRequestAny(assoc)
				if err == nil {
					_, err = ReceiveDataSet(assoc, pcID, transfer.ExplicitVRLittleEndian)
				}
				if err == nil {
					err = SendCStoreResponse(assoc, pcID, CStoreResponse{
						AffectedSOPClassUID: request.AffectedSOPClassUID, MessageIDBeingRespondedTo: request.MessageID,
						AffectedSOPInstanceUID: request.AffectedSOPInstanceUID, Status: StatusSuccess,
					})
				}
				if err != nil {
					_ = assoc.Close()
					serverDone <- err
					return
				}
			}
			if err := respondToStoreRelease(assoc); err != nil {
				_ = assoc.Close()
				serverDone <- err
				return
			}
			_ = assoc.Close()
		}
		serverDone <- nil
	}()
	b.StartTimer()
	if len(associationItems) == 1 {
		session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
			DialOptions: ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		})
		if err == nil {
			var result StoreBatchResult
			result, err = session.StoreBatch(ctx, sources)
			if err == nil && result.Succeeded != len(sources) {
				err = errors.New("incomplete benchmark batch")
			}
		}
		if err == nil {
			err = session.Close(ctx)
		}
		if err != nil {
			b.Fatal(err)
		}
	} else {
		for _, source := range sources {
			session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
				DialOptions: ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
			})
			if err == nil {
				_, err = session.Store(ctx, source)
			}
			if err == nil {
				err = session.Close(ctx)
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
	b.StopTimer()
	if err := <-serverDone; err != nil {
		b.Fatal(err)
	}
	_ = listener.Close()
	b.StartTimer()
}

func TestPathStoreSourceStreamsOriginalDataSetBytes(t *testing.T) {
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "source.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	direct, err := openStorePathNoFollow(path)
	if err != nil {
		t.Fatalf("openStorePathNoFollow() error = %v", err)
	}
	_ = direct.Close()

	source := NewPathStoreSource(path)
	descriptor, err := source.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if descriptor.SOPInstanceUID != dicomtest.TestSOPInstanceUID || descriptor.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	opened, err := source.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var streamed bytes.Buffer
	if err := opened.WriteDataSet(context.Background(), &streamed, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatalf("WriteDataSet() error = %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reader := bytes.NewReader(data)
	header, err := object.ReadPart10Header(reader, object.ReadFileOptions{})
	if err != nil {
		t.Fatalf("ReadPart10Header() error = %v", err)
	}
	want := data[header.DataSetOffset:]
	if !bytes.Equal(streamed.Bytes(), want) {
		t.Fatalf("streamed dataset differs: got %d bytes, want %d", streamed.Len(), len(want))
	}
}

func TestPathStoreSourceReencodesNativeFallback(t *testing.T) {
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "source.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	source := NewPathStoreSource(path, StorePathSourceOptions{NativePreference: transfer.NativeStoreImplicitLittleEndianFirst})
	descriptor, err := source.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got := descriptor.WritableTransferSyntaxUIDs; len(got) < 2 || got[0] != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("writable syntaxes = %v, want implicit first", got)
	}
	opened, err := source.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = opened.Close() }()
	var encoded bytes.Buffer
	if err := opened.WriteDataSet(context.Background(), &encoded, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("WriteDataSet(implicit) error = %v", err)
	}
	parsed, err := object.ReadDataSet(bytes.NewReader(encoded.Bytes()), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReadDataSet(implicit) error = %v", err)
	}
	if got, ok := parsed.GetUID(tags.SOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("implicit SOP Instance UID = %q, %v", got, ok)
	}
}

func TestObjectStoreSourcesValidatePixelRepresentation(t *testing.T) {
	fixture := readIntegrationFixture(t)
	fixture.Dataset.Put(dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2, 3, 4}))
	fixture.Meta = nil
	fixture.TransferSyntax = transfer.JPEGBaseline

	_, err := NewFileStoreSource(fixture, 4).Inspect(context.Background())
	if !errors.Is(err, ErrStoreInvalidSource) {
		t.Fatalf("NewFileStoreSource().Inspect() error = %v, want ErrStoreInvalidSource", err)
	}
	_, err = NewDataSetStoreSource(fixture.Dataset, transfer.JPEGBaseline, 4).Inspect(context.Background())
	if !errors.Is(err, ErrStoreInvalidSource) {
		t.Fatalf("NewDataSetStoreSource().Inspect() error = %v, want ErrStoreInvalidSource", err)
	}

	fixture.TransferSyntax = transfer.ExplicitVRLittleEndian
	descriptor, err := NewFileStoreSource(fixture, 4).Inspect(context.Background())
	if err != nil {
		t.Fatalf("native NewFileStoreSource().Inspect() error = %v", err)
	}
	if !descriptor.PixelDataHeaderSet || descriptor.PixelDataHeader.Tag != core.TagPixelData || descriptor.PixelDataHeader.Length.IsUndefined() {
		t.Fatalf("native descriptor pixel header = %#v, want defined Pixel Data", descriptor)
	}
}

func TestPathStoreSourceRejectsSymbolicLinks(t *testing.T) {
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.dcm")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.dcm")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err = NewPathStoreSource(link).Inspect(context.Background())
	if !errors.Is(err, ErrStoreInvalidSource) {
		t.Fatalf("Inspect() error = %v, want ErrStoreInvalidSource", err)
	}
}

func TestStoreByteLimitWriterEnforcesItemAndBatchBoundaries(t *testing.T) {
	var destination bytes.Buffer
	total := int64(4)
	writer := &storeByteLimitWriter{destination: &destination, remaining: 3, remainingTotal: &total}
	if n, err := writer.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("exact Write() = %d, %v", n, err)
	}
	if n, err := writer.Write([]byte("d")); n != 0 || !errors.Is(err, ErrStoreResourceLimit) {
		t.Fatalf("overflow Write() = %d, %v, want resource limit", n, err)
	}
	if got := destination.String(); got != "abc" {
		t.Fatalf("destination = %q, want abc", got)
	}

	destination.Reset()
	total = 2
	writer = &storeByteLimitWriter{destination: &destination, remaining: 3, remainingTotal: &total}
	if _, err := writer.Write([]byte("abc")); !errors.Is(err, ErrStoreResourceLimit) {
		t.Fatalf("batch overflow error = %v, want resource limit", err)
	}
}

func TestValidateOpenedStoreSourceDetectsMetadataChanges(t *testing.T) {
	planned := testStoreDescriptor("1.2.3", "1.2.3.1", transfer.ExplicitVRLittleEndian.UID)
	changed := cloneStoreDescriptor(planned)
	changed.SOPInstanceUID = "1.2.3.2"
	err := validateOpenedStoreSource(planned, OpenedStoreSource{
		Descriptor: changed,
		WriteDataSet: func(context.Context, io.Writer, transfer.Syntax) error {
			return nil
		},
		Close: func() error { return nil },
	})
	if !errors.Is(err, ErrStoreSourceChanged) {
		t.Fatalf("validateOpenedStoreSource() error = %v, want ErrStoreSourceChanged", err)
	}
}

func TestValidateOpenedStoreSourceDetectsWritableSyntaxChanges(t *testing.T) {
	planned := testStoreDescriptor("1.2.3", "1.2.3.1", transfer.ExplicitVRLittleEndian.UID)
	planned.WritableTransferSyntaxUIDs = []string{transfer.ExplicitVRLittleEndian.UID, transfer.JPEGBaseline.UID}
	actual := cloneStoreDescriptor(planned)
	actual.WritableTransferSyntaxUIDs = []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID}
	err := validateOpenedStoreSource(planned, OpenedStoreSource{
		Descriptor: actual,
		WriteDataSet: func(context.Context, io.Writer, transfer.Syntax) error {
			return nil
		},
		Close: func() error { return nil },
	})
	if !errors.Is(err, ErrStoreSourceChanged) {
		t.Fatalf("validateOpenedStoreSource() error = %v, want ErrStoreSourceChanged", err)
	}
}

func TestStoreSessionRejectsInvalidPriority(t *testing.T) {
	_, err := NewStoreSession("127.0.0.1:1", StoreSessionOptions{Priority: 3})
	if !errors.Is(err, ErrStoreInvalidOptions) {
		t.Fatalf("NewStoreSession() error = %v, want ErrStoreInvalidOptions", err)
	}
}

func TestStoreSessionCompletesTerminalDialFailure(t *testing.T) {
	session, err := NewStoreSession("127.0.0.1:1", StoreSessionOptions{})
	if err != nil {
		t.Fatalf("NewStoreSession() error = %v", err)
	}
	session.dial = func(context.Context, string, ul.DialOptions) (*ul.Association, error) {
		return nil, errors.New("dial failed")
	}
	source := descriptorStoreSource{descriptor: testStoreDescriptor(
		dicomtest.TestSOPClassUID, dicomtest.TestSOPInstanceUID, transfer.ExplicitVRLittleEndian.UID,
	)}
	result, err := session.StoreBatch(context.Background(), []StoreSource{source})
	if err == nil {
		t.Fatal("StoreBatch() error = nil, want dial failure")
	}
	if !result.Complete || result.Failed != 1 || result.Items[0].Outcome != StoreOutcomeFailure {
		t.Fatalf("StoreBatch() result = %#v, want complete terminal failure", result)
	}
}

func TestStoreSessionReusesAssociationForMultiplePaths(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	dir := t.TempDir()
	paths := make([]string, 3)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("source-%d.dcm", i))
		if err := os.WriteFile(paths[i], data, 0o600); err != nil {
			t.Fatalf("WriteFile(%d) error = %v", i, err)
		}
	}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()
	acceptedCounts := make(chan int, 1)
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		acceptedCounts <- len(assoc.AcceptedContexts)
		for range paths {
			pcID, request, err := ReceiveCStoreRequestAny(assoc)
			if err != nil {
				serverDone <- err
				return
			}
			if _, err := ReceiveDataSet(assoc, pcID, transfer.ExplicitVRLittleEndian); err != nil {
				serverDone <- err
				return
			}
			if err := SendCStoreResponse(assoc, pcID, CStoreResponse{
				AffectedSOPClassUID:       request.AffectedSOPClassUID,
				MessageIDBeingRespondedTo: request.MessageID,
				AffectedSOPInstanceUID:    request.AffectedSOPInstanceUID,
				Status:                    StatusSuccess,
			}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- respondToStoreRelease(assoc)
	}()

	session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
		DialOptions: ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
	})
	if err != nil {
		t.Fatalf("NewStoreSession() error = %v", err)
	}
	sources := make([]StoreSource, len(paths))
	for i := range paths {
		sources[i] = NewPathStoreSource(paths[i])
	}
	result, err := session.StoreBatch(ctx, sources)
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Associations != 1 || result.Succeeded != len(paths) || !result.Complete {
		t.Fatalf("StoreBatch() result = %#v", result)
	}
	if contexts := <-acceptedCounts; contexts != 1 {
		t.Fatalf("accepted contexts = %d, want 1", contexts)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

type trackingStoreSource struct {
	descriptor StoreDescriptor
	dataSet    *object.Object
	openCount  int
	closeCount int
}

func (s *trackingStoreSource) Inspect(context.Context) (StoreDescriptor, error) {
	return cloneStoreDescriptor(s.descriptor), nil
}

func (s *trackingStoreSource) Open(context.Context) (OpenedStoreSource, error) {
	s.openCount++
	return OpenedStoreSource{
		Descriptor: cloneStoreDescriptor(s.descriptor),
		WriteDataSet: func(_ context.Context, destination io.Writer, syntax transfer.Syntax) error {
			return object.WriteDataSet(destination, s.dataSet, syntax)
		},
		Close: func() error { s.closeCount++; return nil },
	}, nil
}

func TestStoreSessionContinuesAfterRemoteFailureAndClosesSources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fixture := readIntegrationFixture(t)
	descriptor, err := descriptorFromFile(fixture, 1, "", true)
	if err != nil {
		t.Fatalf("descriptorFromFile() error = %v", err)
	}
	first := &trackingStoreSource{descriptor: descriptor, dataSet: fixture.Dataset}
	second := &trackingStoreSource{descriptor: descriptor, dataSet: fixture.Dataset}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		statuses := []uint16{StatusCStoreOutOfResources, StatusSuccess}
		for _, status := range statuses {
			pcID, request, err := ReceiveCStoreRequestAny(assoc)
			if err != nil {
				serverDone <- err
				return
			}
			if _, err := ReceiveDataSet(assoc, pcID, transfer.ExplicitVRLittleEndian); err != nil {
				serverDone <- err
				return
			}
			if err := SendCStoreResponse(assoc, pcID, CStoreResponse{
				AffectedSOPClassUID: request.AffectedSOPClassUID, MessageIDBeingRespondedTo: request.MessageID,
				AffectedSOPInstanceUID: request.AffectedSOPInstanceUID, Status: status,
			}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- respondToStoreRelease(assoc)
	}()

	session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
		DialOptions:     ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		ContinueOnError: true,
	})
	if err != nil {
		t.Fatalf("NewStoreSession() error = %v", err)
	}
	result, err := session.StoreBatch(ctx, []StoreSource{first, second})
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Failed != 1 || result.Succeeded != 1 || result.Items[0].Outcome != StoreOutcomeFailure || result.Items[1].Outcome != StoreOutcomeSuccess {
		t.Fatalf("StoreBatch() result = %#v", result)
	}
	if first.openCount != 1 || first.closeCount != 1 || second.openCount != 1 || second.closeCount != 1 {
		t.Fatalf("source ownership first=%d/%d second=%d/%d, want 1/1 each", first.openCount, first.closeCount, second.openCount, second.closeCount)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
	if result.Items[0].Err == nil || !errors.Is(result.Items[0].Err, ErrStoreRemoteFailure) {
		t.Fatalf("first error = %v, want ErrStoreRemoteFailure", result.Items[0].Err)
	}
	if result.Items[0].Descriptor.Origin != "" {
		t.Fatal("unexpected sensitive origin in test descriptor")
	}
	if got, ok := fixture.Dataset.GetUID(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("borrowed dataset mutated: UID=%q ok=%v", got, ok)
	}
}

func TestStoreSessionReconnectsRemainingItemsWithoutRetryingUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fixture := readIntegrationFixture(t)
	descriptor, err := descriptorFromFile(fixture, 1, "", true)
	if err != nil {
		t.Fatalf("descriptorFromFile() error = %v", err)
	}
	first := &trackingStoreSource{descriptor: descriptor, dataSet: fixture.Dataset}
	second := &trackingStoreSource{descriptor: descriptor, dataSet: fixture.Dataset}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		firstAssoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		pcID, _, err := ReceiveCStoreRequestAny(firstAssoc)
		if err == nil {
			_, err = ReceiveDataSet(firstAssoc, pcID, transfer.ExplicitVRLittleEndian)
		}
		_ = firstAssoc.Close()
		if err != nil {
			serverDone <- err
			return
		}

		secondAssoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = secondAssoc.Close() }()
		pcID, request, err := ReceiveCStoreRequestAny(secondAssoc)
		if err == nil {
			_, err = ReceiveDataSet(secondAssoc, pcID, transfer.ExplicitVRLittleEndian)
		}
		if err == nil {
			err = SendCStoreResponse(secondAssoc, pcID, CStoreResponse{
				AffectedSOPClassUID: request.AffectedSOPClassUID, MessageIDBeingRespondedTo: request.MessageID,
				AffectedSOPInstanceUID: request.AffectedSOPInstanceUID, Status: StatusSuccess,
			})
		}
		if err == nil {
			err = respondToStoreRelease(secondAssoc)
		}
		serverDone <- err
	}()

	session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
		DialOptions:     ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		ContinueOnError: true,
	})
	if err != nil {
		t.Fatalf("NewStoreSession() error = %v", err)
	}
	result, err := session.StoreBatch(ctx, []StoreSource{first, second})
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Associations != 2 || result.Unknown != 1 || result.Succeeded != 1 {
		t.Fatalf("StoreBatch() result = %#v", result)
	}
	if result.Items[0].Outcome != StoreOutcomeUnknown || result.Items[1].Outcome != StoreOutcomeSuccess {
		t.Fatalf("item outcomes = %s/%s, want unknown/success", result.Items[0].Outcome, result.Items[1].Outcome)
	}
	if first.openCount != 1 || second.openCount != 1 {
		t.Fatalf("open counts = %d/%d, want no retry of unknown", first.openCount, second.openCount)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestStoreSessionRetriesUncertainItemOnlyWhenEnabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fixture := readIntegrationFixture(t)
	descriptor, err := descriptorFromFile(fixture, 1, "", true)
	if err != nil {
		t.Fatal(err)
	}
	source := &trackingStoreSource{descriptor: descriptor, dataSet: fixture.Dataset}
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		first, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		pcID, _, err := ReceiveCStoreRequestAny(first)
		if err == nil {
			_, err = ReceiveDataSet(first, pcID, transfer.ExplicitVRLittleEndian)
		}
		_ = first.Close()
		if err != nil {
			serverDone <- err
			return
		}

		second, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = second.Close() }()
		pcID, request, err := ReceiveCStoreRequestAny(second)
		if err == nil {
			_, err = ReceiveDataSet(second, pcID, transfer.ExplicitVRLittleEndian)
		}
		if err == nil {
			err = SendCStoreResponse(second, pcID, CStoreResponse{
				AffectedSOPClassUID: request.AffectedSOPClassUID, MessageIDBeingRespondedTo: request.MessageID,
				AffectedSOPInstanceUID: request.AffectedSOPInstanceUID, Status: StatusSuccess,
			})
		}
		if err == nil {
			err = respondToStoreRelease(second)
		}
		serverDone <- err
	}()

	session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
		DialOptions:      ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		ContinueOnError:  true,
		RetryUncertain:   true,
		MaxStoreAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.StoreBatch(ctx, []StoreSource{source})
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Succeeded != 1 || result.Unknown != 0 || result.Associations != 2 || result.Items[0].Attempt != 2 {
		t.Fatalf("StoreBatch() result = %#v", result)
	}
	if source.openCount != 2 || source.closeCount != 2 {
		t.Fatalf("source opens/closes = %d/%d, want 2/2", source.openCount, source.closeCount)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestStoreSessionDisableReconnectPreventsUncertainRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fixture := readIntegrationFixture(t)
	descriptor, err := descriptorFromFile(fixture, 1, "", true)
	if err != nil {
		t.Fatal(err)
	}
	source := &trackingStoreSource{descriptor: descriptor, dataSet: fixture.Dataset}
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		pcID, _, err := ReceiveCStoreRequestAny(assoc)
		if err == nil {
			_, err = ReceiveDataSet(assoc, pcID, transfer.ExplicitVRLittleEndian)
		}
		_ = assoc.Close()
		serverDone <- err
	}()

	session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
		DialOptions:     ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		ContinueOnError: true, RetryUncertain: true, DisableReconnect: true, MaxStoreAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.StoreBatch(ctx, []StoreSource{source})
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Unknown != 1 || result.Associations != 1 || result.Items[0].Attempt != 1 {
		t.Fatalf("StoreBatch() result = %#v, want one unknown attempt on one association", result)
	}
	if source.openCount != 1 || source.closeCount != 1 {
		t.Fatalf("source opens/closes = %d/%d, want 1/1", source.openCount, source.closeCount)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

type cancelingStoreSource struct {
	descriptor StoreDescriptor
	cancel     context.CancelFunc
	closed     int
}

func (s *cancelingStoreSource) Inspect(context.Context) (StoreDescriptor, error) {
	return cloneStoreDescriptor(s.descriptor), nil
}

func (s *cancelingStoreSource) Open(context.Context) (OpenedStoreSource, error) {
	return OpenedStoreSource{
		Descriptor: cloneStoreDescriptor(s.descriptor),
		WriteDataSet: func(ctx context.Context, _ io.Writer, _ transfer.Syntax) error {
			s.cancel()
			return ctx.Err()
		},
		Close: func() error { s.closed++; return nil },
	}, nil
}

func TestStoreSessionCancellationDuringPayloadAbortsAndCloses(t *testing.T) {
	serverCtx, stopServer := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopServer()
	storeCtx, cancelStore := context.WithCancel(context.Background())
	descriptor := testStoreDescriptor(dicomtest.TestSOPClassUID, dicomtest.TestSOPInstanceUID, transfer.ExplicitVRLittleEndian.UID)
	source := &cancelingStoreSource{descriptor: descriptor, cancel: cancelStore}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: serverCtx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: serverCtx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		if _, _, err := ReceiveCStoreRequestAny(assoc); err != nil {
			serverDone <- err
			return
		}
		pdu, err := assoc.ReadPDU()
		if errors.Is(err, ul.ErrAssociationAborted) {
			err = nil
		}
		if err == nil {
			if pdu != nil {
				if _, ok := pdu.(*ul.AbortRQ); !ok {
					err = fmt.Errorf("got %T, want A-ABORT", pdu)
				}
			}
		}
		serverDone <- err
	}()

	session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
		DialOptions:     ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		ContinueOnError: true,
	})
	if err != nil {
		t.Fatalf("NewStoreSession() error = %v", err)
	}
	result, err := session.StoreBatch(storeCtx, []StoreSource{source})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StoreBatch() error = %v, want context.Canceled", err)
	}
	if result.Items[0].Outcome != StoreOutcomeUnknown || source.closed != 1 {
		t.Fatalf("result=%#v closed=%d, want unknown/1", result.Items[0], source.closed)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestStoreSessionCancellationMarksFutureAssociationsCanceled(t *testing.T) {
	first := testStoreDescriptor("1.2.3", "1.2.3.1", transfer.ExplicitVRLittleEndian.UID)
	second := testStoreDescriptor("1.2.4", "1.2.4.1", transfer.ExplicitVRLittleEndian.UID)
	plan := StorePlan{
		SourceCount: 2,
		Associations: []StoreAssociationPlan{
			{Items: []StorePlannedItem{{SourceIndex: 0, Descriptor: first}}},
			{Items: []StorePlannedItem{{SourceIndex: 1, Descriptor: second}}},
		},
	}
	result := newStoreBatchResult(plan)
	(&StoreSession{}).cancelAllNotSent(&result, plan, context.Canceled)
	if !storeBatchComplete(result.Items) || result.Canceled != 2 {
		t.Fatalf("result = %#v, want two terminal cancellations", result)
	}
	for i, item := range result.Items {
		if item.Outcome != StoreOutcomeCanceled || !errors.Is(item.Err, context.Canceled) || item.Descriptor.SOPClassUID == "" {
			t.Fatalf("item %d = %#v, want populated canceled result", i, item)
		}
	}
}

func TestStoreSessionProgressPanicIsRedacted(t *testing.T) {
	session := &StoreSession{options: StoreSessionOptions{
		OnProgress: func(context.Context, StoreProgress) error {
			panic("SECRET PATIENT CALLBACK")
		},
	}}
	err := session.reportProgress(context.Background(), StoreItemResult{SourceIndex: 3}, 1, 1)
	if !errors.Is(err, ErrStoreCallback) {
		t.Fatalf("reportProgress() error = %v, want ErrStoreCallback", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "PATIENT") {
		t.Fatalf("reportProgress() leaked panic payload: %q", err)
	}
}

func TestStoreSessionReportsRejectedContextWithoutOpeningPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fixture := readIntegrationFixture(t)
	explicitDescriptor, err := descriptorFromFile(fixture, 1, "", true)
	if err != nil {
		t.Fatalf("descriptorFromFile() error = %v", err)
	}
	explicit := &trackingStoreSource{descriptor: explicitDescriptor, dataSet: fixture.Dataset}
	jpegDescriptor := explicitDescriptor
	jpegDescriptor.TransferSyntaxUID = transfer.JPEGBaseline.UID
	jpegDescriptor.WritableTransferSyntaxUIDs = []string{transfer.JPEGBaseline.UID}
	jpeg := &trackingStoreSource{descriptor: jpegDescriptor, dataSet: fixture.Dataset}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		pcID, request, err := ReceiveCStoreRequestAny(assoc)
		if err == nil {
			_, err = ReceiveDataSet(assoc, pcID, transfer.ExplicitVRLittleEndian)
		}
		if err == nil {
			err = SendCStoreResponse(assoc, pcID, CStoreResponse{
				AffectedSOPClassUID: request.AffectedSOPClassUID, MessageIDBeingRespondedTo: request.MessageID,
				AffectedSOPInstanceUID: request.AffectedSOPInstanceUID, Status: StatusSuccess,
			})
		}
		if err == nil {
			err = respondToStoreRelease(assoc)
		}
		serverDone <- err
	}()

	session, err := NewStoreSession(listener.Addr().String(), StoreSessionOptions{
		DialOptions:     ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		ContinueOnError: true,
	})
	if err != nil {
		t.Fatalf("NewStoreSession() error = %v", err)
	}
	result, err := session.StoreBatch(ctx, []StoreSource{explicit, jpeg})
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Succeeded != 1 || result.Failed != 1 || !errors.Is(result.Items[1].Err, ErrStorePresentationContextRejected) {
		t.Fatalf("StoreBatch() result = %#v", result)
	}
	if jpeg.openCount != 0 || jpeg.closeCount != 0 {
		t.Fatalf("rejected source opened/closed = %d/%d, want 0/0", jpeg.openCount, jpeg.closeCount)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}
