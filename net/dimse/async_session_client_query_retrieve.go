package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/object"
)

// StartCFind invokes a cancelable C-FIND operation.
func (s *AsyncSession) StartCFind(ctx context.Context, pcID byte, request CFindRequest, identifier *object.Object) (*AsyncOperation, error) {
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: identifier})
}

// StartCMove invokes a cancelable C-MOVE operation.
func (s *AsyncSession) StartCMove(ctx context.Context, pcID byte, request CMoveRequest, identifier *object.Object) (*AsyncOperation, error) {
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: identifier})
}

// StartCGet invokes a cancelable C-GET operation. Reverse C-STORE requests are
// dispatched through the same session and consume its performed window.
func (s *AsyncSession) StartCGet(ctx context.Context, pcID byte, request CGetRequest, identifier *object.Object) (*AsyncOperation, error) {
	if !s.localHasReverseStoreRole(request.AffectedSOPClassUID) {
		return nil, ErrCGetStorageRoleNotAccepted
	}
	s.mu.Lock()
	storeHandler := s.handlers[CStoreRQ]
	s.mu.Unlock()
	if storeHandler == nil {
		return nil, fmt.Errorf("dicom dimse: C-GET requires a reverse C-STORE handler")
	}
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: identifier})
}
