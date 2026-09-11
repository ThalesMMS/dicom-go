package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/object"
)

// StartCStore invokes C-STORE and sends dataset as the same atomic message.
func (s *AsyncSession) StartCStore(ctx context.Context, pcID byte, request CStoreRequest, dataset *object.Object) (*AsyncOperation, error) {
	ctx, cancel := s.suboperationContext(ctx)
	operation, err := s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: dataset})
	if err != nil {
		cancel()
		return nil, err
	}
	operation.setContextCancel(cancel)
	return operation, nil
}

// StartCStoreEncoded invokes C-STORE and writes a pre-encoded dataset through
// writeDataSet. The session never materializes an *object.Object for the
// payload; Message ID assignment and invoked-window accounting match StartCStore.
func (s *AsyncSession) StartCStoreEncoded(ctx context.Context, pcID byte, request CStoreRequest, writeDataSet CStoreDataSetWriter) (*AsyncOperation, error) {
	if writeDataSet == nil {
		return nil, fmt.Errorf("dicom dimse: C-STORE dataset writer is nil")
	}
	ctx, cancel := s.suboperationContext(ctx)
	operation, err := s.invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet()}, writeDataSet)
	if err != nil {
		cancel()
		return nil, err
	}
	operation.setContextCancel(cancel)
	return operation, nil
}
