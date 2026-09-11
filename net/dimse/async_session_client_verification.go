package dimse

import (
	"context"
	"fmt"
)

// StartCEcho invokes C-ECHO using the accepted Verification context.
func (s *AsyncSession) StartCEcho(ctx context.Context) (*AsyncOperation, error) {
	if s == nil {
		return nil, ErrAsyncSessionClosed
	}
	pc, ok := AcceptedVerificationContext(s.assoc)
	if !ok {
		return nil, fmt.Errorf("dicom dimse: verification presentation context not accepted")
	}
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pc.ID, Command: (CEchoRequest{}).CommandSet()})
}
