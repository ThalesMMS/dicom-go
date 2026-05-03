package dimse

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStorageCommitmentTransactionTracker_DeliverAndWait(t *testing.T) {
	tr := NewStorageCommitmentTransactionTracker()
	uid := "1.2.3.4.5"
	if err := tr.Track(uid, 7); err != nil {
		t.Fatalf("Track: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		time.Sleep(10 * time.Millisecond)
		ok, err := tr.Deliver(uid)
		if err != nil || !ok {
			t.Errorf("Deliver: ok=%v err=%v", ok, err)
		}
		close(done)
	}()

	ev, err := tr.Wait(ctx, uid, 0)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if ev.TransactionUID != uid {
		t.Fatalf("TransactionUID=%q, want %q", ev.TransactionUID, uid)
	}
	if ev.RequestMessageID != 7 {
		t.Fatalf("RequestMessageID=%d, want %d", ev.RequestMessageID, 7)
	}

	<-done

	// After successful wait, it should be removed.
	_, err = tr.Wait(ctx, uid, 0)
	if !errors.Is(err, ErrStorageCommitmentTransactionUnknown) {
		t.Fatalf("Wait after remove err=%v, want unknown", err)
	}
}

func TestStorageCommitmentTransactionTracker_Timeout(t *testing.T) {
	tr := NewStorageCommitmentTransactionTracker()
	uid := "9.8.7"
	if err := tr.Track(uid, 0); err != nil {
		t.Fatalf("Track: %v", err)
	}
	ctx := context.Background()
	_, err := tr.Wait(ctx, uid, 20*time.Millisecond)
	if !errors.Is(err, ErrStorageCommitmentTransactionTimeout) {
		t.Fatalf("Wait err=%v, want timeout", err)
	}
}

func TestStorageCommitmentTransactionTracker_Unknown(t *testing.T) {
	tr := NewStorageCommitmentTransactionTracker()
	ctx := context.Background()
	_, err := tr.Wait(ctx, "nope", 0)
	if !errors.Is(err, ErrStorageCommitmentTransactionUnknown) {
		t.Fatalf("Wait err=%v, want unknown", err)
	}
	ok, err := tr.Deliver("nope")
	if ok || !errors.Is(err, ErrStorageCommitmentTransactionUnknown) {
		t.Fatalf("Deliver ok=%v err=%v, want ok=false unknown", ok, err)
	}
}

func TestStorageCommitmentTransactionTracker_DuplicateTrack(t *testing.T) {
	tr := NewStorageCommitmentTransactionTracker()
	uid := "1.2.3"
	if err := tr.Track(uid, 1); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := tr.Track(uid, 2); err == nil {
		t.Fatalf("expected error on duplicate Track")
	}
}

func TestStorageCommitmentTransactionTracker_ContextCancel(t *testing.T) {
	tr := NewStorageCommitmentTransactionTracker()
	uid := "1.2.840"
	if err := tr.Track(uid, 0); err != nil {
		t.Fatalf("Track: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tr.Wait(ctx, uid, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err=%v, want context.Canceled", err)
	}
	if err := tr.Track(uid, 0); err != nil {
		t.Fatalf("Track after canceled Wait: %v", err)
	}
}
