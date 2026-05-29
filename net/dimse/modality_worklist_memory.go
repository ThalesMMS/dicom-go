package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

type inMemoryModalityWorklistHandler struct {
	records []*object.Object
}

// NewInMemoryModalityWorklistHandler snapshots synthetic/test MWL records and
// returns a deterministic streaming handler. Production RIS/PACS adapters can
// implement ModalityWorklistHandler directly without materializing results.
func NewInMemoryModalityWorklistHandler(records []*object.Object) (ModalityWorklistHandler, error) {
	cloned := make([]*object.Object, len(records))
	for i, record := range records {
		if record == nil {
			return nil, fmt.Errorf("dicom dimse: nil MWL record at index %d", i)
		}
		cloned[i] = cloneMWLObject(record)
	}
	return &inMemoryModalityWorklistHandler{records: cloned}, nil
}

func (handler *inMemoryModalityWorklistHandler) Find(ctx context.Context, request ModalityWorklistRequest, yield ModalityWorklistYield) error {
	if handler == nil || yield == nil {
		return ErrModalityWorklistProvider
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !modalityWorklistHasMatchingKey(request.Identifier.Query) {
		return nil
	}
	for _, record := range handler.records {
		if err := ctx.Err(); err != nil {
			return err
		}
		matched, err := MatchModalityWorklist(request.Identifier.Query, record)
		if err != nil {
			return ErrModalityWorklistProvider
		}
		if !matched {
			continue
		}
		if err := yield(cloneMWLObject(record)); err != nil {
			return err
		}
	}
	return nil
}

func cloneMWLObject(source *object.Object) *object.Object {
	elements := source.Elements()
	cloned := make([]core.Element, len(elements))
	for i, element := range elements {
		cloned[i] = cloneMWLElement(element)
	}
	return object.FromElements(cloned, std.Dictionary)
}
