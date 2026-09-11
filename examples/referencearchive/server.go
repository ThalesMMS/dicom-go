package main

import (
	"context"
	"errors"
	"time"

	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var storageClasses = []string{"1.2.840.10008.5.1.4.1.1.7", "1.2.840.10008.5.1.4.1.1.2", "1.2.840.10008.5.1.4.1.1.4"}

type archiveServices struct {
	*archive
	aeTitle      string
	destinations map[string]string
}

func newArchiveServer(listener *ul.Listener, a *archive, aeTitle string, destinations map[string]string, observe ...func(error)) (*ul.AssociationServer, error) {
	allowed := make(map[string]string, len(destinations))
	for ae, address := range destinations {
		allowed[ae] = address
	}
	services := &archiveServices{a, aeTitle, allowed}
	sops := append([]string{dimse.VerificationSOPClassUID, dimse.StudyRootFindSOPClassUID, dimse.StudyRootGetSOPClassUID, dimse.StudyRootMoveSOPClassUID}, storageClasses...)
	roles := make([]ul.RoleSelectionItem, 0, len(storageClasses))
	for _, sop := range storageClasses {
		roles = append(roles, ul.RoleSelectionItem{SopClassUID: sop, SCURole: true, SCPRole: true})
	}
	return ul.NewAssociationServer(listener, ul.AssociationServerOptions{
		MaxConcurrentAssociations: 4, SaturationPolicy: ul.SaturationReject,
		Accept: ul.AcceptOptions{AETitle: aeTitle, SupportedAbstractSyntaxes: sops, SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID}, RoleSelections: roles, MaxPDU: 64 << 10, NegotiationTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ReadProgressTimeout: 10 * time.Second, WriteProgressTimeout: 10 * time.Second, ReleaseTimeout: 2 * time.Second},
		Handler: func(ctx context.Context, assoc *ul.Association) error {
			defer assoc.Close()
			err := dimse.ServeAssociation(ctx, assoc, dimse.AssociationSCPOptions{
				StorageSCPOptions: dimse.StorageSCPOptions{StoreHandler: a, MaxDataSetBytes: a.limits.instanceBytes, MaxElementBytes: 1 << 20, MaxElements: 10000, MaxSequenceDepth: 32, MaxPixelDataBytes: a.limits.instanceBytes, MaxPixelDataFragments: 10000},
				Controls:          dimse.SCPControls{CommandProgressTimeout: 10 * time.Second, DataSetProgressTimeout: 10 * time.Second, OperationTimeout: 30 * time.Second, CancelGrace: 2 * time.Second},
				CFindHandler:      a, CGetHandler: a, CMoveHandler: services,
			})
			if len(observe) > 0 && observe[0] != nil {
				observe[0](err)
			}
			return err
		},
	})
}

func (s *archiveServices) moveSelection(ctx context.Context, req dimse.CMoveRequestContext) (string, []storedInstance, error) {
	address, ok := s.destinations[req.Request.MoveDestination]
	if !ok {
		return "", nil, dimse.NewCMoveSCPError(0xA801, "destination is not configured", errQuery)
	}
	items, err := s.selectInstances(ctx, req.QueryRetrieveLevel, req.Identifier, true)
	if err != nil {
		return "", nil, dimse.NewCMoveSCPError(0xC000, "reference archive retrieve failed", err)
	}
	return address, items, nil
}

func (s *archiveServices) send(ctx context.Context, address, calledAE string, items []storedInstance, yield func(dimse.CMoveSubOperationResult) error) error {
	session, err := dimse.NewStoreSession(address, dimse.StoreSessionOptions{
		DialOptions:      ul.DialOptions{CallingAETitle: s.aeTitle, CalledAETitle: calledAE, NegotiationTimeout: 5 * time.Second, ReadProgressTimeout: 10 * time.Second, WriteProgressTimeout: 10 * time.Second},
		PlanOptions:      dimse.StorePlanOptions{Limits: dimse.StoreLimits{MaxItems: s.limits.results, MaxItemBytes: s.limits.instanceBytes, MaxTotalBytes: s.limits.totalBytes, MaxAssociations: 4, MaxTransferSyntaxes: 2}},
		MaxInFlightBytes: s.limits.instanceBytes, MaxAssociationAttempts: 1, MaxStoreAttempts: 1, ContinueOnError: true, ReleaseTimeout: 2 * time.Second,
	})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = session.Close(cleanup)
	}()
	sources := make([]dimse.StoreSource, 0, len(items))
	for _, item := range items {
		sources = append(sources, dimse.NewRootedPathStoreSource(s.root, item.name, dimse.StorePathSourceOptions{IndexOptions: s.indexOptions(), ReadOptions: s.readOptions(false)}))
	}
	result, batchErr := session.StoreBatch(ctx, sources)
	for _, item := range result.Items {
		status := item.Status
		if !item.StatusSet {
			status = dimse.StatusCStoreOutOfResources
		}
		if err := yield(dimse.CMoveSubOperationResult{Status: status, Err: item.Err}); err != nil {
			return err
		}
	}
	return batchErr
}

func (s *archiveServices) PrepareCMoveBatch(ctx context.Context, req dimse.CMoveRequestContext) (dimse.CMoveBatch, error) {
	address, items, err := s.moveSelection(ctx, req)
	if err != nil {
		return dimse.CMoveBatch{}, err
	}
	descriptors := make([]dimse.CMoveSubOperationDescriptor, 0, len(items))
	for _, item := range items {
		descriptors = append(descriptors, dimse.CMoveSubOperationDescriptor{AffectedSOPClassUID: item.record.Instance.SOPClassUID, AffectedSOPInstanceUID: item.record.Instance.SOPInstanceUID})
	}
	return dimse.CMoveBatch{SubOperations: descriptors, Run: func(ctx context.Context, yield func(dimse.CMoveSubOperationResult) error) error {
		return s.send(ctx, address, req.Request.MoveDestination, items, yield)
	}}, nil
}

func (s *archiveServices) Move(ctx context.Context, req dimse.CMoveRequestContext) ([]dimse.CMoveSubOperation, error) {
	address, items, err := s.moveSelection(ctx, req)
	if err != nil {
		return nil, err
	}
	ops := make([]dimse.CMoveSubOperation, 0, len(items))
	for _, item := range items {
		item := item
		ops = append(ops, dimse.CMoveSubOperation{AffectedSOPClassUID: item.record.Instance.SOPClassUID, AffectedSOPInstanceUID: item.record.Instance.SOPInstanceUID, Store: func(ctx context.Context) dimse.CMoveSubOperationResult {
			result := dimse.CMoveSubOperationResult{Status: dimse.StatusCStoreOutOfResources}
			err := s.send(ctx, address, req.Request.MoveDestination, []storedInstance{item}, func(r dimse.CMoveSubOperationResult) error { result = r; return nil })
			result.Err = errors.Join(result.Err, err)
			return result
		}})
	}
	return ops, nil
}
