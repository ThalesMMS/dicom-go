package dimse

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

var storageCommitmentTestReferences = []StorageCommitmentSOPReference{
	{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.826.0.1.3680043.10.543.101"},
	{SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", SOPInstanceUID: "1.2.826.0.1.3680043.10.543.102"},
}

func newStorageCommitmentTestStore(t *testing.T) *MemoryStorageCommitmentStore {
	t.Helper()
	store, err := NewMemoryStorageCommitmentStore(100)
	if err != nil {
		t.Fatalf("NewMemoryStorageCommitmentStore() error = %v", err)
	}
	return store
}

func newStorageCommitmentTestWorkflow(t *testing.T, options StorageCommitmentWorkflowOptions) *StorageCommitmentWorkflow {
	t.Helper()
	if options.Store == nil {
		options.Store = newStorageCommitmentTestStore(t)
	}
	workflow, err := NewStorageCommitmentWorkflow(options)
	if err != nil {
		t.Fatalf("NewStorageCommitmentWorkflow() error = %v", err)
	}
	return workflow
}

func acceptStorageCommitmentTestRequest(t *testing.T, workflow *StorageCommitmentWorkflow, transactionUID string) StorageCommitmentTransaction {
	t.Helper()
	transaction, err := workflow.acceptRequest(context.Background(), &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 1, StorageCommitmentActionInformation{
		TransactionUID: transactionUID, ReferencedSOPs: storageCommitmentTestReferences,
	})
	if err == nil {
		transaction, err = workflow.markCommitterAccepted(context.Background(), transaction.TransactionUID)
	}
	if err != nil {
		t.Fatalf("accept test request: %v", err)
	}
	return transaction
}

func TestMemoryStorageCommitmentStoreClonesAndUsesVersionedCAS(t *testing.T) {
	store, err := NewMemoryStorageCommitmentStore(1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	references := append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...)
	transaction := StorageCommitmentTransaction{
		TransactionUID: "2.25.1", Direction: StorageCommitmentDirectionCommitter,
		State: StorageCommitmentStateAccepted, DeliveryMode: StorageCommitmentDeliveryManual,
		ReferencedSOPs: references, CreatedAt: now, UpdatedAt: now,
	}
	transaction.RequestDigest = storageCommitmentRequestDigest(transaction)
	created, err := store.Create(context.Background(), transaction)
	if err != nil || !created {
		t.Fatalf("Create() = %v, %v", created, err)
	}
	references[0].SOPInstanceUID = "9.9.9"
	loaded, err := store.Get(context.Background(), transaction.TransactionUID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ReferencedSOPs[0].SOPInstanceUID == "9.9.9" || loaded.Version != 1 {
		t.Fatalf("Get() did not return detached versioned record: %#v", loaded)
	}
	next := cloneStorageCommitmentTransaction(loaded)
	next.State = StorageCommitmentStateProcessing
	updated, err := store.CompareAndSwap(context.Background(), loaded.TransactionUID, loaded.Version, next)
	if err != nil || updated.Version != 2 {
		t.Fatalf("CompareAndSwap() = version %d, %v", updated.Version, err)
	}
	if _, err := store.CompareAndSwap(context.Background(), loaded.TransactionUID, loaded.Version, next); !errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
		t.Fatalf("stale CompareAndSwap() error = %v", err)
	}
	second := transaction
	second.TransactionUID = "2.25.2"
	second.RequestDigest = storageCommitmentRequestDigest(second)
	if _, err := store.Create(context.Background(), second); !errors.Is(err, ErrStorageCommitmentResourceLimit) {
		t.Fatalf("Create() above capacity error = %v", err)
	}
}

type storageCommitmentRecordingStore struct {
	StorageCommitmentTransactionStore
	created atomic.Bool
}

type storageCommitmentFailingListStore struct {
	StorageCommitmentTransactionStore
	err error
}

type storageCommitmentStaleListStore struct {
	StorageCommitmentTransactionStore
	items []StorageCommitmentTransaction
}

func (s storageCommitmentStaleListStore) List(context.Context, StorageCommitmentTransactionQuery) ([]StorageCommitmentTransaction, error) {
	items := make([]StorageCommitmentTransaction, len(s.items))
	for i := range s.items {
		items[i] = cloneStorageCommitmentTransaction(s.items[i])
	}
	return items, nil
}

func (s storageCommitmentFailingListStore) List(context.Context, StorageCommitmentTransactionQuery) ([]StorageCommitmentTransaction, error) {
	return nil, s.err
}

func (s *storageCommitmentRecordingStore) Create(ctx context.Context, transaction StorageCommitmentTransaction) (bool, error) {
	created, err := s.StorageCommitmentTransactionStore.Create(ctx, transaction)
	if err == nil && created {
		s.created.Store(true)
	}
	return created, err
}

func TestStorageCommitmentActionPersistsBeforeSuccessResponseAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	baseStore := newStorageCommitmentTestStore(t)
	recording := &storageCommitmentRecordingStore{StorageCommitmentTransactionStore: baseStore}
	committer := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{Store: recording})
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "COMMITTER", Context: ctx,
			SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer assoc.Close()
		_, err = committer.ServeAction(ctx, assoc, 1)
		serverDone <- err
	}()
	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle: "COMMITTER", CallingAETitle: "REQUESTOR",
		Contexts: []ul.PresentationContext{{AbstractSyntaxUID: StorageCommitmentPushModelSOPClassUID, TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer assoc.Close()
	requestor := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{Store: newStorageCommitmentTestStore(t)})
	transactionUID := "2.25.101"
	transaction, err := requestor.Request(ctx, assoc, storageCommitmentTestReferences, StorageCommitmentRequestOptions{TransactionUID: transactionUID})
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	if transaction.State != StorageCommitmentStateAccepted || !recording.created.Load() {
		t.Fatalf("Request() transaction = %#v", transaction)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAction() error = %v", err)
	}
	duplicateInfo := StorageCommitmentActionInformation{TransactionUID: transactionUID, ReferencedSOPs: storageCommitmentTestReferences}
	duplicate, err := committer.acceptRequest(ctx, &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 99, duplicateInfo)
	if err != nil || duplicate.TransactionUID != transactionUID {
		t.Fatalf("duplicate accept = %#v, %v", duplicate, err)
	}
	conflict := duplicateInfo
	conflict.ReferencedSOPs = storageCommitmentTestReferences[:1]
	if _, err := committer.acceptRequest(ctx, &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 100, conflict); !errors.Is(err, ErrStorageCommitmentTransactionConflict) {
		t.Fatalf("conflicting duplicate error = %v", err)
	}
}

func TestStorageCommitmentPreparedRequestIsNotProcessedBeforeResponsePublication(t *testing.T) {
	processorCalls := atomic.Int32{}
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Processor: StorageCommitmentProcessorFunc(func(_ context.Context, transaction StorageCommitmentTransaction) (StorageCommitmentResult, error) {
			processorCalls.Add(1)
			return StorageCommitmentResult{TransactionUID: transaction.TransactionUID, ReferencedSOPs: transaction.ReferencedSOPs}, nil
		}),
	})
	transaction, err := workflow.acceptRequest(context.Background(), &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 1, StorageCommitmentActionInformation{
		TransactionUID: "2.25.112", ReferencedSOPs: storageCommitmentTestReferences,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.State != StorageCommitmentStateAcceptancePrepared {
		t.Fatalf("prepared transaction = %#v", transaction)
	}
	if err := workflow.ProcessDue(context.Background(), 10); err != nil || processorCalls.Load() != 0 {
		t.Fatalf("ProcessDue before response = %v, calls=%d", err, processorCalls.Load())
	}
	if _, err := workflow.markCommitterAccepted(context.Background(), transaction.TransactionUID); err != nil {
		t.Fatal(err)
	}
	if err := workflow.ProcessDue(context.Background(), 10); err != nil || processorCalls.Load() != 1 {
		t.Fatalf("ProcessDue after response = %v, calls=%d", err, processorCalls.Load())
	}
}

func TestStorageCommitmentProcessDueRecoversPreparedRequestAfterRestart(t *testing.T) {
	store := newStorageCommitmentTestStore(t)
	now := time.Unix(2_000, 0)
	committer := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: store, LeaseDuration: time.Second, Now: func() time.Time { return now },
	})
	transaction, err := committer.acceptRequest(context.Background(), &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 1, StorageCommitmentActionInformation{
		TransactionUID: "2.25.115", ReferencedSOPs: storageCommitmentTestReferences,
	})
	if err != nil || transaction.State != StorageCommitmentStateAcceptancePrepared {
		t.Fatalf("prepared transaction = %#v, %v", transaction, err)
	}
	now = now.Add(2 * time.Second)
	processorCalls := atomic.Int32{}
	restarted := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: store, LeaseDuration: time.Second, Now: func() time.Time { return now },
		Processor: StorageCommitmentProcessorFunc(func(_ context.Context, transaction StorageCommitmentTransaction) (StorageCommitmentResult, error) {
			processorCalls.Add(1)
			return StorageCommitmentResult{TransactionUID: transaction.TransactionUID, ReferencedSOPs: transaction.ReferencedSOPs}, nil
		}),
	})
	if err := restarted.ProcessDue(context.Background(), 10); err != nil {
		t.Fatalf("ProcessDue after restart: %v", err)
	}
	ready, err := restarted.Get(context.Background(), transaction.TransactionUID)
	if err != nil || ready.State != StorageCommitmentStateResultReady || processorCalls.Load() != 1 {
		t.Fatalf("recovered transaction = %#v, calls=%d, err=%v", ready, processorCalls.Load(), err)
	}
}

func TestStorageCommitmentRenewedAcceptanceLeaseBlocksStaleRecovery(t *testing.T) {
	store := newStorageCommitmentTestStore(t)
	now := time.Unix(3_000, 0)
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: store, LeaseDuration: time.Second, Now: func() time.Time { return now },
	})
	transaction, err := workflow.acceptRequest(context.Background(), &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 1, StorageCommitmentActionInformation{
		TransactionUID: "2.25.116", ReferencedSOPs: storageCommitmentTestReferences,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if !workflow.renewCommitterAcceptance(context.Background(), transaction.TransactionUID, transaction.LeaseToken) {
		t.Fatal("renewCommitterAcceptance returned false")
	}
	if _, err := workflow.recoverCommitterAcceptance(context.Background(), transaction.TransactionUID); !errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
		t.Fatalf("recovery during renewed lease error = %v", err)
	}
	now = now.Add(2 * time.Second)
	recovered, err := workflow.recoverCommitterAcceptance(context.Background(), transaction.TransactionUID)
	if err != nil || recovered.State != StorageCommitmentStateAccepted {
		t.Fatalf("recovery after lease expiry = %#v, %v", recovered, err)
	}
}

func TestStorageCommitmentRequestPathsRejectSCPOnlyAssociationRequestor(t *testing.T) {
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{})
	assoc := &ul.Association{
		CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER",
		AcceptedRoleSelections: []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}},
	}
	if _, err := workflow.Request(context.Background(), assoc, storageCommitmentTestReferences, StorageCommitmentRequestOptions{TransactionUID: "2.25.117"}); !errors.Is(err, ErrStorageCommitmentInvalidRequest) {
		t.Fatalf("Request on SCP-only role error = %v", err)
	}
	if _, err := workflow.ServeAction(context.Background(), assoc, 1); !errors.Is(err, ErrStorageCommitmentInvalidRequest) {
		t.Fatalf("ServeAction on SCP-only role error = %v", err)
	}
}

func TestStorageCommitmentEarlyCallbackWinsRaceWithActionResponsePersistence(t *testing.T) {
	store := newStorageCommitmentTestStore(t)
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{Store: store})
	now := time.Now()
	transaction := StorageCommitmentTransaction{
		TransactionUID: "2.25.113", Direction: StorageCommitmentDirectionRequestor,
		State: StorageCommitmentStateRequestPending, DeliveryMode: StorageCommitmentDeliveryCallback,
		RequestorAETitle: "REQUESTOR", CommitterAETitle: "COMMITTER",
		ReferencedSOPs: append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...),
		CreatedAt:      now, UpdatedAt: now,
	}
	transaction.RequestDigest = storageCommitmentRequestDigest(transaction)
	if created, err := store.Create(context.Background(), transaction); err != nil || !created {
		t.Fatalf("Create() = %v, %v", created, err)
	}
	callbackAssociation := &ul.Association{
		CallingAETitle: "COMMITTER", CalledAETitle: "REQUESTOR",
		AcceptedRoleSelections: []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}},
	}
	result := StorageCommitmentResult{TransactionUID: transaction.TransactionUID, ReferencedSOPs: transaction.ReferencedSOPs}
	if err := workflow.receiveResult(context.Background(), callbackAssociation, result); err != nil {
		t.Fatalf("receive early callback: %v", err)
	}
	completed, err := workflow.markRequestorAccepted(context.Background(), transaction.TransactionUID)
	if err != nil || completed.State != StorageCommitmentStateResultReceived {
		t.Fatalf("mark request accepted after callback = %#v, %v", completed, err)
	}
}

func TestStorageCommitmentDefinitiveResponsePersistsAfterOperationCancellation(t *testing.T) {
	store := newStorageCommitmentTestStore(t)
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{Store: store})
	now := time.Now()
	transaction := StorageCommitmentTransaction{
		TransactionUID: "2.25.114", Direction: StorageCommitmentDirectionRequestor,
		State: StorageCommitmentStateRequestPending, DeliveryMode: StorageCommitmentDeliveryCallback,
		ReferencedSOPs: append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...),
		CreatedAt:      now, UpdatedAt: now,
	}
	transaction.RequestDigest = storageCommitmentRequestDigest(transaction)
	if created, err := store.Create(context.Background(), transaction); err != nil || !created {
		t.Fatalf("Create() = %v, %v", created, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	accepted, err := workflow.markRequestorAccepted(canceled, transaction.TransactionUID)
	if err != nil || accepted.State != StorageCommitmentStateAccepted {
		t.Fatalf("mark accepted after cancellation = %#v, %v", accepted, err)
	}
}

func TestStorageCommitmentRolesFollowAssociationRequestorSemantics(t *testing.T) {
	transaction := StorageCommitmentTransaction{RequestorAETitle: "REQUESTOR", CommitterAETitle: "COMMITTER"}
	if !storageCommitmentAssociationRolesValid(&ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, transaction) {
		t.Fatal("default SCU role rejected on original association")
	}
	if storageCommitmentAssociationRolesValid(&ul.Association{
		CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER",
		AcceptedRoleSelections: []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}},
	}, transaction) {
		t.Fatal("SCP-only requestor accepted on original association")
	}
	if !storageCommitmentAssociationRolesValid(&ul.Association{
		CallingAETitle: "COMMITTER", CalledAETitle: "REQUESTOR",
		AcceptedRoleSelections: []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}},
	}, transaction) {
		t.Fatal("explicit callback SCP role rejected")
	}
	if storageCommitmentAssociationRolesValid(&ul.Association{CallingAETitle: "COMMITTER", CalledAETitle: "REQUESTOR"}, transaction) {
		t.Fatal("callback association accepted without SCP role selection")
	}
}

func TestStorageCommitmentResultMustBeExactDisjointPartition(t *testing.T) {
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{})
	transaction := acceptStorageCommitmentTestRequest(t, workflow, "2.25.202")
	valid := StorageCommitmentResult{
		TransactionUID: transaction.TransactionUID,
		ReferencedSOPs: []StorageCommitmentSOPReference{storageCommitmentTestReferences[0]},
		FailedSOPs: []StorageCommitmentSOPReference{{
			SOPClassUID:    storageCommitmentTestReferences[1].SOPClassUID,
			SOPInstanceUID: storageCommitmentTestReferences[1].SOPInstanceUID,
			FailureReason:  0x0112,
		}},
	}
	stored, err := workflow.SetResult(context.Background(), valid)
	if err != nil || stored.State != StorageCommitmentStateResultReady {
		t.Fatalf("SetResult(valid) = %#v, %v", stored, err)
	}
	if _, err := workflow.SetResult(context.Background(), valid); err != nil {
		t.Fatalf("SetResult(duplicate) error = %v", err)
	}
	badStore := newStorageCommitmentTestStore(t)
	badWorkflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{Store: badStore})
	_ = acceptStorageCommitmentTestRequest(t, badWorkflow, "2.25.203")
	cases := []StorageCommitmentResult{
		{TransactionUID: "2.25.203", ReferencedSOPs: storageCommitmentTestReferences[:1]},
		{TransactionUID: "2.25.203", ReferencedSOPs: []StorageCommitmentSOPReference{storageCommitmentTestReferences[0]}, FailedSOPs: []StorageCommitmentSOPReference{{SOPClassUID: storageCommitmentTestReferences[0].SOPClassUID, SOPInstanceUID: storageCommitmentTestReferences[0].SOPInstanceUID, FailureReason: 0x0110}}},
		{TransactionUID: "2.25.203", FailedSOPs: []StorageCommitmentSOPReference{{SOPClassUID: storageCommitmentTestReferences[0].SOPClassUID, SOPInstanceUID: storageCommitmentTestReferences[0].SOPInstanceUID, FailureReason: 0x9999}, {SOPClassUID: storageCommitmentTestReferences[1].SOPClassUID, SOPInstanceUID: storageCommitmentTestReferences[1].SOPInstanceUID, FailureReason: 0x0112}}},
	}
	for i, result := range cases {
		if _, err := badWorkflow.SetResult(context.Background(), result); !errors.Is(err, ErrStorageCommitmentInvalidResult) {
			t.Fatalf("case %d error = %v", i, err)
		}
	}
}

func TestStorageCommitmentSameAssociationSuccessUsesSingleReaders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	committer := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: newStorageCommitmentTestStore(t), DefaultDeliveryMode: StorageCommitmentDeliverySameAssociation,
	})
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "COMMITTER", Context: ctx,
			SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer assoc.Close()
		transaction, err := committer.ServeAction(ctx, assoc, 1)
		if err == nil {
			_, err = committer.SetResult(ctx, StorageCommitmentResult{TransactionUID: transaction.TransactionUID, ReferencedSOPs: transaction.ReferencedSOPs})
		}
		if err == nil {
			_, err = committer.DeliverOnAssociation(ctx, transaction.TransactionUID, assoc)
		}
		if err == nil {
			err = assoc.Release(ctx)
		}
		serverDone <- err
	}()
	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle: "COMMITTER", CallingAETitle: "REQUESTOR",
		Contexts: []ul.PresentationContext{{AbstractSyntaxUID: StorageCommitmentPushModelSOPClassUID, TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer assoc.Close()
	requestStore := newStorageCommitmentTestStore(t)
	requestor := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: requestStore, DefaultDeliveryMode: StorageCommitmentDeliverySameAssociation,
	})
	transaction, err := requestor.Request(ctx, assoc, storageCommitmentTestReferences, StorageCommitmentRequestOptions{
		TransactionUID: "2.25.303",
	})
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	if transaction.DeliveryMode != StorageCommitmentDeliverySameAssociation {
		t.Fatalf("stored DeliveryMode = %q, want configured default", transaction.DeliveryMode)
	}
	if err := requestor.ServeAssociation(ctx, assoc, SCPControls{}); err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	completed, err := requestStore.Get(context.Background(), transaction.TransactionUID)
	if err != nil || completed.State != StorageCommitmentStateResultReceived || len(completed.Result.ReferencedSOPs) != 2 {
		t.Fatalf("completed = %#v, %v", completed, err)
	}
}

func TestStorageCommitmentCallbackAfterOriginalAssociationReleased(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	requestorStore := newStorageCommitmentTestStore(t)
	consumerCalls := atomic.Int32{}
	callbackListener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer callbackListener.Close()
	transactionUID := "2.25.404"
	requestTransaction := StorageCommitmentTransaction{
		TransactionUID: transactionUID, Direction: StorageCommitmentDirectionRequestor,
		State: StorageCommitmentStateAccepted, DeliveryMode: StorageCommitmentDeliveryCallback,
		RequestorAETitle: "REQUESTOR", CommitterAETitle: "COMMITTER",
		ReferencedSOPs: append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...),
		CreatedAt:      time.Now(), UpdatedAt: time.Now(),
	}
	requestTransaction.RequestDigest = storageCommitmentRequestDigest(requestTransaction)
	if created, err := requestorStore.Create(ctx, requestTransaction); err != nil || !created {
		t.Fatalf("requestor Create() = %v, %v", created, err)
	}
	// Reconstruct the workflow owner after durable creation. The callback must
	// not depend on process-local correlation state or the original association.
	requestor := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: requestorStore,
		Consumer: StorageCommitmentResultConsumerFunc(func(context.Context, StorageCommitmentTransaction) error {
			consumerCalls.Add(1)
			return nil
		}),
	})
	receiverDone := make(chan error, 1)
	go func() {
		assoc, err := callbackListener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "REQUESTOR", Context: ctx, RequireMatchingCalledAE: true,
			SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ExplicitVRLittleEndian, ul.ImplicitVRLittleEndian},
			RoleSelections:            []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}},
		})
		if err == nil {
			err = requestor.ServeAssociation(ctx, assoc, SCPControls{})
			_ = assoc.Close()
		}
		receiverDone <- err
	}()
	committerStore := newStorageCommitmentTestStore(t)
	committer := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: committerStore,
		Resolver: StorageCommitmentCallbackResolverFunc(func(context.Context, StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error) {
			return StorageCommitmentCallbackTarget{
				Address:     callbackListener.Addr().String(),
				DialOptions: ul.DialOptions{CallingAETitle: "COMMITTER", CalledAETitle: "REQUESTOR"},
			}, nil
		}),
	})
	commitTransaction := acceptStorageCommitmentTestRequest(t, committer, transactionUID)
	_, err = committer.SetResult(ctx, StorageCommitmentResult{
		TransactionUID: transactionUID,
		ReferencedSOPs: []StorageCommitmentSOPReference{storageCommitmentTestReferences[0]},
		FailedSOPs: []StorageCommitmentSOPReference{{
			SOPClassUID:    storageCommitmentTestReferences[1].SOPClassUID,
			SOPInstanceUID: storageCommitmentTestReferences[1].SOPInstanceUID,
			FailureReason:  0x0112,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	committer = newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: committerStore,
		Resolver: StorageCommitmentCallbackResolverFunc(func(context.Context, StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error) {
			return StorageCommitmentCallbackTarget{
				Address:     callbackListener.Addr().String(),
				DialOptions: ul.DialOptions{CallingAETitle: "COMMITTER", CalledAETitle: "REQUESTOR"},
			}, nil
		}),
	})
	delivered, err := committer.DeliverCallback(ctx, commitTransaction.TransactionUID)
	if err != nil || delivered.State != StorageCommitmentStateDelivered {
		t.Fatalf("DeliverCallback() = %#v, %v", delivered, err)
	}
	if err := <-receiverDone; err != nil {
		t.Fatalf("receiver error = %v", err)
	}
	received, err := requestorStore.Get(ctx, transactionUID)
	if err != nil || received.State != StorageCommitmentStateResultReceived || len(received.Result.FailedSOPs) != 1 {
		t.Fatalf("received = %#v, %v", received, err)
	}
	peer := &ul.Association{
		CallingAETitle: "COMMITTER", CalledAETitle: "REQUESTOR",
		AcceptedRoleSelections: []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}},
	}
	if err := requestor.receiveResult(ctx, peer, *received.Result); err != nil {
		t.Fatalf("duplicate receiveResult() error = %v", err)
	}
	if consumerCalls.Load() != 1 {
		t.Fatalf("consumer calls = %d, want 1", consumerCalls.Load())
	}
}

func TestStorageCommitmentReferenceLimitExactAndPlusOne(t *testing.T) {
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Limits: StorageCommitmentLimits{MaxReferences: len(storageCommitmentTestReferences)},
	})
	peer := &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}
	if _, err := workflow.acceptRequest(context.Background(), peer, 1, StorageCommitmentActionInformation{
		TransactionUID: "2.25.405", ReferencedSOPs: storageCommitmentTestReferences,
	}); err != nil {
		t.Fatalf("accept exact MaxReferences error = %v", err)
	}
	tooMany := append(append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...), StorageCommitmentSOPReference{
		SOPClassUID: storageCommitmentTestReferences[0].SOPClassUID, SOPInstanceUID: "1.2.826.0.1.3680043.10.543.103",
	})
	if _, err := workflow.acceptRequest(context.Background(), peer, 2, StorageCommitmentActionInformation{
		TransactionUID: "2.25.406", ReferencedSOPs: tooMany,
	}); !errors.Is(err, ErrStorageCommitmentResourceLimit) {
		t.Fatalf("accept above MaxReferences error = %v", err)
	}
}

func TestStorageCommitmentRejectsReferenceLimitAboveHardParserCap(t *testing.T) {
	_, err := NewStorageCommitmentWorkflow(StorageCommitmentWorkflowOptions{
		Store:  newStorageCommitmentTestStore(t),
		Limits: StorageCommitmentLimits{MaxReferences: defaultStorageCommitmentMaxReferences + 1},
	})
	if !errors.Is(err, ErrStorageCommitmentResourceLimit) {
		t.Fatalf("NewStorageCommitmentWorkflow above hard reference cap error = %v", err)
	}
	_, err = NewStorageCommitmentWorkflow(StorageCommitmentWorkflowOptions{
		Store: newStorageCommitmentTestStore(t), LeaseDuration: minimumStorageCommitmentLeaseDuration - time.Nanosecond,
	})
	if !errors.Is(err, ErrStorageCommitmentResourceLimit) {
		t.Fatalf("NewStorageCommitmentWorkflow below minimum lease error = %v", err)
	}
}

func TestStorageCommitmentRejectsDuplicateSOPInstanceAcrossClasses(t *testing.T) {
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{})
	references := []StorageCommitmentSOPReference{
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.826.0.1.3680043.10.543.888"},
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", SOPInstanceUID: "1.2.826.0.1.3680043.10.543.888"},
	}
	if _, err := workflow.acceptRequest(context.Background(), &ul.Association{}, 1, StorageCommitmentActionInformation{
		TransactionUID: "2.25.888", ReferencedSOPs: references,
	}); !errors.Is(err, ErrStorageCommitmentInvalidRequest) {
		t.Fatalf("duplicate SOP Instance UID error = %v", err)
	}
}

func TestStorageCommitmentStoreErrorsArePHIFreeAndPreserveCause(t *testing.T) {
	cause := errors.New("PATIENT^NAME persistent backend")
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: storageCommitmentFailingListStore{StorageCommitmentTransactionStore: newStorageCommitmentTestStore(t), err: cause},
	})
	err := workflow.ProcessDue(context.Background(), 1)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "PATIENT") {
		t.Fatalf("store error = %v", err)
	}
}

func TestStorageCommitmentDueWorkersTreatAlreadyAdvancedItemsAsBenign(t *testing.T) {
	for _, test := range []struct {
		name       string
		staleState StorageCommitmentState
		state      StorageCommitmentState
		deliver    bool
	}{
		{"processing", StorageCommitmentStateAccepted, StorageCommitmentStateResultReady, false},
		{"delivery", StorageCommitmentStateResultReady, StorageCommitmentStateDelivered, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newStorageCommitmentTestStore(t)
			now := time.Now()
			result := StorageCommitmentResult{TransactionUID: "2.25.889", ReferencedSOPs: append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...)}
			actual := StorageCommitmentTransaction{
				TransactionUID: result.TransactionUID, Direction: StorageCommitmentDirectionCommitter,
				State: test.state, DeliveryMode: StorageCommitmentDeliveryCallback,
				ReferencedSOPs: append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...),
				Result:         &result, CreatedAt: now, UpdatedAt: now, NextAttemptAt: now,
			}
			actual.RequestDigest = storageCommitmentRequestDigest(actual)
			actual.ResultDigest = storageCommitmentResultDigest(result)
			if created, err := base.Create(context.Background(), actual); err != nil || !created {
				t.Fatalf("Create() = %v, %v", created, err)
			}
			stale := cloneStorageCommitmentTransaction(actual)
			stale.State = test.staleState
			store := storageCommitmentStaleListStore{StorageCommitmentTransactionStore: base, items: []StorageCommitmentTransaction{stale}}
			workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
				Store: store,
				Processor: StorageCommitmentProcessorFunc(func(context.Context, StorageCommitmentTransaction) (StorageCommitmentResult, error) {
					return result, nil
				}),
			})
			var err error
			if test.deliver {
				err = workflow.DeliverDue(context.Background(), 1)
			} else {
				err = workflow.ProcessDue(context.Background(), 1)
			}
			if err != nil {
				t.Fatalf("due worker returned stale-list conflict: %v", err)
			}
		})
	}
}

func TestStorageCommitmentDueWorkersRevalidateBackoffAfterStaleList(t *testing.T) {
	for _, deliver := range []bool{false, true} {
		name := "processing"
		if deliver {
			name = "delivery"
		}
		t.Run(name, func(t *testing.T) {
			base := newStorageCommitmentTestStore(t)
			now := time.Now()
			result := StorageCommitmentResult{TransactionUID: "2.25.890", ReferencedSOPs: append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...)}
			state := StorageCommitmentStateAccepted
			if deliver {
				state = StorageCommitmentStateDeliveryFailed
			}
			actual := StorageCommitmentTransaction{
				TransactionUID: result.TransactionUID, Direction: StorageCommitmentDirectionCommitter,
				State: state, DeliveryMode: StorageCommitmentDeliveryCallback,
				ReferencedSOPs: append([]StorageCommitmentSOPReference(nil), storageCommitmentTestReferences...),
				Result:         &result, CreatedAt: now, UpdatedAt: now, NextAttemptAt: now.Add(time.Hour),
			}
			actual.RequestDigest = storageCommitmentRequestDigest(actual)
			actual.ResultDigest = storageCommitmentResultDigest(result)
			if created, err := base.Create(context.Background(), actual); err != nil || !created {
				t.Fatalf("Create() = %v, %v", created, err)
			}
			stale := cloneStorageCommitmentTransaction(actual)
			stale.NextAttemptAt = now.Add(-time.Second)
			store := storageCommitmentStaleListStore{StorageCommitmentTransactionStore: base, items: []StorageCommitmentTransaction{stale}}
			processorCalls := atomic.Int32{}
			resolverCalls := atomic.Int32{}
			workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
				Store: store,
				Processor: StorageCommitmentProcessorFunc(func(context.Context, StorageCommitmentTransaction) (StorageCommitmentResult, error) {
					processorCalls.Add(1)
					return result, nil
				}),
				Resolver: StorageCommitmentCallbackResolverFunc(func(context.Context, StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error) {
					resolverCalls.Add(1)
					return StorageCommitmentCallbackTarget{}, ErrStorageCommitmentCallbackUnknown
				}),
			})
			var err error
			if deliver {
				err = workflow.DeliverDue(context.Background(), 1)
			} else {
				err = workflow.ProcessDue(context.Background(), 1)
			}
			if err != nil || processorCalls.Load() != 0 || resolverCalls.Load() != 0 {
				t.Fatalf("due worker ignored refreshed backoff: err=%v processor=%d resolver=%d", err, processorCalls.Load(), resolverCalls.Load())
			}
		})
	}
}

func TestStorageCommitmentWorkflowAcceptsAnyNonNullDataSetType(t *testing.T) {
	if err := validateStorageCommitmentActionCommand(NormalizedActionRequest{
		RequestedSOPClassUID: StorageCommitmentPushModelSOPClassUID, RequestedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		ActionTypeID: StorageCommitmentActionTypeID, CommandDataSetType: 0x0001,
	}); err != nil {
		t.Fatalf("validate N-ACTION non-null dataset type: %v", err)
	}
	if err := validateStorageCommitmentEventCommand(NormalizedEventReportRequest{
		AffectedSOPClassUID: StorageCommitmentPushModelSOPClassUID, AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		EventTypeID: StorageCommitmentEventTypeFailures, CommandDataSetType: 0x0001,
	}); err != nil {
		t.Fatalf("validate N-EVENT-REPORT non-null dataset type: %v", err)
	}
}

func TestStorageCommitmentNormalizedHandlersReturnSpecificCommandStatuses(t *testing.T) {
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{})
	options := workflow.NormalizedOptions(&ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"})
	actionBase := NormalizedActionRequest{
		RequestedSOPClassUID: StorageCommitmentPushModelSOPClassUID, RequestedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		ActionTypeID: StorageCommitmentActionTypeID, CommandDataSetType: DataSetPresent,
	}
	actionInstance := actionBase
	actionInstance.RequestedSOPInstanceUID = "1.2.3"
	actionType := actionBase
	actionType.ActionTypeID++
	for _, test := range []struct {
		name    string
		request NormalizedActionRequest
		status  uint16
	}{{"instance", actionInstance, StatusNoSuchSOPInstance}, {"action", actionType, StatusNoSuchActionType}} {
		result, err := options.ActionHandler(context.Background(), test.request, nil)
		if err == nil || result.Response.Status != test.status {
			t.Fatalf("N-ACTION %s = status %04x, %v", test.name, result.Response.Status, err)
		}
	}
	eventBase := NormalizedEventReportRequest{
		AffectedSOPClassUID: StorageCommitmentPushModelSOPClassUID, AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		EventTypeID: StorageCommitmentEventTypeSuccess, CommandDataSetType: DataSetPresent,
	}
	eventInstance := eventBase
	eventInstance.AffectedSOPInstanceUID = "1.2.3"
	eventType := eventBase
	eventType.EventTypeID = 3
	for _, test := range []struct {
		name    string
		request NormalizedEventReportRequest
		status  uint16
	}{{"instance", eventInstance, StatusNoSuchSOPInstance}, {"event", eventType, StatusNoSuchEventType}} {
		result, err := options.EventReportHandler(context.Background(), test.request, nil)
		if err == nil || result.Response.Status != test.status {
			t.Fatalf("N-EVENT-REPORT %s = status %04x, %v", test.name, result.Response.Status, err)
		}
	}
}

func TestStorageCommitmentRetryIsPersistedBoundedAndPHIFree(t *testing.T) {
	var nowMu sync.Mutex
	now := time.Unix(1_000, 0)
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	advance := func(duration time.Duration) {
		nowMu.Lock()
		now = now.Add(duration)
		nowMu.Unlock()
	}
	resolverCalls := atomic.Int32{}
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: newStorageCommitmentTestStore(t), Now: clock,
		RetryPolicy: StorageCommitmentRetryPolicy{MaxAttempts: 2, InitialBackoff: time.Second, MaxBackoff: time.Second},
		Resolver: StorageCommitmentCallbackResolverFunc(func(context.Context, StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error) {
			resolverCalls.Add(1)
			return StorageCommitmentCallbackTarget{}, errors.New("PATIENT^NAME callback unavailable")
		}),
	})
	transaction := acceptStorageCommitmentTestRequest(t, workflow, "2.25.505")
	_, err := workflow.SetResult(context.Background(), StorageCommitmentResult{TransactionUID: transaction.TransactionUID, ReferencedSOPs: storageCommitmentTestReferences})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := workflow.DeliverCallback(context.Background(), transaction.TransactionUID)
	if err == nil || strings.Contains(err.Error(), "PATIENT") || failed.DeliveryAttempts != 1 || failed.LastFailure != StorageCommitmentFailureCallbackOffline {
		t.Fatalf("first delivery = %#v, %v", failed, err)
	}
	if err := workflow.DeliverDue(context.Background(), 10); err != nil {
		t.Fatalf("DeliverDue(before backoff) error = %v", err)
	}
	if resolverCalls.Load() != 1 {
		t.Fatalf("resolver calls before due = %d", resolverCalls.Load())
	}
	advance(time.Second)
	if err := workflow.DeliverDue(context.Background(), 10); err == nil || strings.Contains(err.Error(), "PATIENT") {
		t.Fatalf("DeliverDue(after backoff) error = %v", err)
	}
	exhausted, err := workflow.Get(context.Background(), transaction.TransactionUID)
	if err != nil || exhausted.DeliveryAttempts != 2 || !exhausted.NextAttemptAt.IsZero() {
		t.Fatalf("exhausted = %#v, %v", exhausted, err)
	}
	if err := workflow.DeliverDue(context.Background(), 10); err != nil {
		t.Fatalf("DeliverDue(exhausted) error = %v", err)
	}
	if resolverCalls.Load() != 2 {
		t.Fatalf("resolver calls = %d, want 2", resolverCalls.Load())
	}
}

func TestStorageCommitmentCancellationDuringCallbackIsPersisted(t *testing.T) {
	started := make(chan struct{})
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: newStorageCommitmentTestStore(t),
		Resolver: StorageCommitmentCallbackResolverFunc(func(context.Context, StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error) {
			return StorageCommitmentCallbackTarget{
				Address:     "127.0.0.1:1",
				DialOptions: ul.DialOptions{CallingAETitle: "COMMITTER", CalledAETitle: "REQUESTOR"},
			}, nil
		}),
		Dialer: StorageCommitmentAssociationDialerFunc(func(ctx context.Context, _ string, _ ul.DialOptions) (*ul.Association, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	transaction := acceptStorageCommitmentTestRequest(t, workflow, "2.25.506")
	if _, err := workflow.SetResult(context.Background(), StorageCommitmentResult{
		TransactionUID: transaction.TransactionUID, ReferencedSOPs: storageCommitmentTestReferences,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := workflow.DeliverCallback(ctx, transaction.TransactionUID)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), transaction.TransactionUID) {
		t.Fatalf("DeliverCallback() error = %v", err)
	}
	persisted, err := workflow.Get(context.Background(), transaction.TransactionUID)
	if err != nil || persisted.State != StorageCommitmentStateDeliveryFailed || persisted.DeliveryAttempts != 1 || persisted.LastFailure != StorageCommitmentFailureCanceled || persisted.NextAttemptAt.IsZero() {
		t.Fatalf("persisted cancellation = %#v, %v", persisted, err)
	}
}

func TestStorageCommitmentTLSHostnameFailureIsClassified(t *testing.T) {
	failure := newStorageCommitmentDeliveryError(x509.HostnameError{Certificate: nil, Host: "sensitive.example"}, 0)
	if failure.Class != StorageCommitmentFailureTLS || !failure.Retryable || strings.Contains(failure.Error(), "sensitive") {
		t.Fatalf("hostname failure = %#v, %v", failure, failure)
	}
}

func TestStorageCommitmentManualDeliveryCanComplete(t *testing.T) {
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		DefaultDeliveryMode: StorageCommitmentDeliveryManual,
	})
	transaction, err := workflow.acceptRequest(context.Background(), &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 1, StorageCommitmentActionInformation{
		TransactionUID: "2.25.507", ReferencedSOPs: storageCommitmentTestReferences,
	})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = workflow.markCommitterAccepted(context.Background(), transaction.TransactionUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.SetResult(context.Background(), StorageCommitmentResult{
		TransactionUID: transaction.TransactionUID, ReferencedSOPs: transaction.ReferencedSOPs,
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := workflow.CompleteManualDelivery(context.Background(), transaction.TransactionUID)
	if err != nil || completed.State != StorageCommitmentStateDelivered || completed.CompletedAt.IsZero() {
		t.Fatalf("CompleteManualDelivery() = %#v, %v", completed, err)
	}
	if duplicate, err := workflow.CompleteManualDelivery(context.Background(), transaction.TransactionUID); err != nil || duplicate.State != StorageCommitmentStateDelivered {
		t.Fatalf("duplicate CompleteManualDelivery() = %#v, %v", duplicate, err)
	}
}

func TestStorageCommitmentProcessingLeaseAllowsOnlyOneWorker(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := atomic.Int32{}
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: newStorageCommitmentTestStore(t),
		Processor: StorageCommitmentProcessorFunc(func(_ context.Context, transaction StorageCommitmentTransaction) (StorageCommitmentResult, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return StorageCommitmentResult{TransactionUID: transaction.TransactionUID, ReferencedSOPs: transaction.ReferencedSOPs}, nil
		}),
	})
	transaction := acceptStorageCommitmentTestRequest(t, workflow, "2.25.606")
	firstDone := make(chan error, 1)
	go func() {
		_, err := workflow.Process(context.Background(), transaction.TransactionUID)
		firstDone <- err
	}()
	<-started
	if _, err := workflow.Process(context.Background(), transaction.TransactionUID); !errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
		t.Fatalf("second Process() error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Process() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("processor calls = %d, want 1", calls.Load())
	}
	ready, err := workflow.Get(context.Background(), transaction.TransactionUID)
	if err != nil || ready.State != StorageCommitmentStateResultReady {
		t.Fatalf("ready = %#v, %v", ready, err)
	}
}

func TestStorageCommitmentWaitCancellationPreservesTransaction(t *testing.T) {
	store := newStorageCommitmentTestStore(t)
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{Store: store})
	transaction, err := workflow.acceptRequest(context.Background(), &ul.Association{CallingAETitle: "REQUESTOR", CalledAETitle: "COMMITTER"}, 1, StorageCommitmentActionInformation{
		TransactionUID: "2.25.707", ReferencedSOPs: storageCommitmentTestReferences,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workflow.Wait(waitCtx, transaction.TransactionUID, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v", err)
	}
	if _, err := store.Get(context.Background(), transaction.TransactionUID); err != nil {
		t.Fatalf("transaction removed after canceled Wait: %v", err)
	}
}

func TestStorageCommitmentPurgeCompletedIsBoundedAndPreservesActive(t *testing.T) {
	store := newStorageCommitmentTestStore(t)
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{
		Store: store, Limits: StorageCommitmentLimits{MaxListBatch: 1},
	})
	old := time.Unix(100, 0)
	for _, transaction := range []StorageCommitmentTransaction{
		{TransactionUID: "2.25.801", Direction: StorageCommitmentDirectionCommitter, State: StorageCommitmentStateDelivered, UpdatedAt: old, CompletedAt: old},
		{TransactionUID: "2.25.802", Direction: StorageCommitmentDirectionCommitter, State: StorageCommitmentStateAccepted, UpdatedAt: old},
	} {
		transaction.CreatedAt = old
		transaction.RequestDigest = storageCommitmentRequestDigest(transaction)
		if created, err := store.Create(context.Background(), transaction); err != nil || !created {
			t.Fatalf("Create(%s) = %v, %v", transaction.TransactionUID, created, err)
		}
	}
	removed, err := workflow.PurgeCompleted(context.Background(), old.Add(time.Second), 1)
	if err != nil || removed != 1 {
		t.Fatalf("PurgeCompleted() = %d, %v", removed, err)
	}
	if _, err := store.Get(context.Background(), "2.25.801"); !errors.Is(err, ErrStorageCommitmentTransactionNotFound) {
		t.Fatalf("terminal transaction still present: %v", err)
	}
	if _, err := store.Get(context.Background(), "2.25.802"); err != nil {
		t.Fatalf("active transaction was removed: %v", err)
	}
	if _, err := workflow.PurgeCompleted(context.Background(), old.Add(time.Second), 2); !errors.Is(err, ErrStorageCommitmentResourceLimit) {
		t.Fatalf("PurgeCompleted above batch limit error = %v", err)
	}
}
