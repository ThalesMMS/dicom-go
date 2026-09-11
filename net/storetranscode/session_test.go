package storetranscode

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestStoreTranscodePipelineLifecycle(t *testing.T) {
	for _, mode := range []string{"complete", "cancel", "abort", "refused"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			batchCtx, batchCancel := context.WithCancel(ctx)
			defer batchCancel()
			opts := testOptions(t, transfer.RLELossless)
			var sources []dimse.StoreSource
			for i := range 3 {
				file := testFile()
				file.Dataset.Put(core.Element{Header: core.ElementHeader{Tag: tags.SOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{fmt.Sprintf("1.2.826.0.1.3680043.10.543.916.%d", i+1)}})
				s, err := NewSource(dimse.NewFileStoreSource(file, 0), opts)
				if err != nil {
					t.Fatal(err)
				}
				sources = append(sources, s)
			}
			listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverDone := make(chan error, 1)
			var arrivals atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			go func() {
				assoc, err := listener.AcceptAssociation(ul.AcceptOptions{AETitle: "STORE", Context: ctx,
					SupportedAbstractSyntaxes: []string{"1.2.840.10008.5.1.4.1.1.7"}, SupportedTransferSyntaxes: []string{opts.Target.UID},
					AsynchronousOperationsWindow: &ul.AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 2}})
				if err != nil {
					serverDone <- err
					return
				}
				async, err := dimse.NewAsyncSession(assoc, dimse.AsyncSessionOptions{Handlers: map[uint16]dimse.AsyncRequestHandler{
					dimse.CStoreRQ: func(handlerCtx context.Context, session *dimse.AsyncSession, message dimse.AsyncMessage) error {
						request, err := dimse.ParseCStoreRequest(message.Command)
						if err != nil {
							return err
						}
						if message.DataSet == nil {
							return errors.New("missing complete dataset")
						}
						uid, _ := message.DataSet.GetUID(tags.SOPInstanceUID)
						if uid != request.AffectedSOPInstanceUID {
							return errors.New("command/dataset identity differs")
						}
						if arrivals.Add(1) == 2 {
							once.Do(func() {
								if mode == "cancel" {
									batchCancel()
								}
								if mode == "abort" {
									_ = assoc.Abort(0)
								}
								close(release)
							})
						}
						select {
						case <-release:
						case <-handlerCtx.Done():
							return handlerCtx.Err()
						}
						if mode == "cancel" || mode == "abort" {
							<-handlerCtx.Done()
							return handlerCtx.Err()
						}
						status := dimse.StatusSuccess
						if mode == "refused" {
							status = dimse.StatusCStoreOutOfResources
						}
						return session.Respond(handlerCtx, message, (dimse.CStoreResponse{AffectedSOPClassUID: request.AffectedSOPClassUID, AffectedSOPInstanceUID: uid, Status: status}).CommandSet(), nil)
					},
				}})
				if err != nil {
					_ = assoc.Close()
					serverDone <- err
					return
				}
				<-async.Done()
				err = async.Err()
				_ = async.Close()
				serverDone <- err
			}()
			session, err := dimse.NewStoreSession(listener.Addr().String(), dimse.StoreSessionOptions{MaxInvokedOperations: 2, MaxInFlightBytes: 1 << 20,
				DialOptions: ul.DialOptions{CalledAETitle: "STORE", CallingAETitle: "TEST", NegotiationTimeout: 3 * time.Second, ReadProgressTimeout: 3 * time.Second, WriteProgressTimeout: 3 * time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := session.StoreBatch(batchCtx, sources)
			_ = session.Close(ctx)
			if mode == "complete" {
				if err != nil || result.Succeeded != 3 || arrivals.Load() != 3 {
					t.Fatalf("pipeline did not complete: %v %+v", err, result)
				}
			} else if mode == "refused" {
				if err == nil || result.Succeeded != 0 || result.Failed != 2 || result.Unknown != 0 || !result.Items[0].StatusSet || result.Items[2].Attempted {
					t.Fatalf("remote failure counted as success: %v %+v", err, result)
				}
			} else {
				if err == nil || arrivals.Load() != 2 || result.Unknown != 2 {
					t.Fatalf("uncertain operations retried or misclassified: %v %+v arrivals=%d", err, result, arrivals.Load())
				}
				if mode == "cancel" && (!errors.Is(err, context.Canceled) || result.Items[2].Outcome != dimse.StoreOutcomeCanceled) {
					t.Fatalf("cancel outcome: %v %+v", err, result.Items)
				}
				if mode == "abort" && (result.Items[2].Attempted || result.Items[2].Outcome != dimse.StoreOutcomeNotSent || !errors.Is(err, dimse.ErrStoreUncertain)) {
					t.Fatal("unsent remainder was retried after transport failure or uncertainty lost")
				}
			}
			assertEmpty(t, opts.SpoolDirectory)
			select {
			case <-serverDone:
			case <-ctx.Done():
				t.Fatal("peer goroutine did not stop")
			}
		})
	}
}
