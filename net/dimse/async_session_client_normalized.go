package dimse

import (
	"context"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

// StartNormalized invokes any DIMSE-N request. The caller supplies one of the
// existing Normalized*Request command sets and its optional dataset.
func (s *AsyncSession) StartNormalized(ctx context.Context, pcID byte, command []core.Element, dataSet *object.Object) (*AsyncOperation, error) {
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: command, DataSet: dataSet})
}
