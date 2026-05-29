package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// RetrieveClient provides a small, stable API surface for Query/Retrieve
// C-MOVE and C-GET workflows.
//
// This is intentionally minimal and designed for the scoped implementation in
// this roadmap task.
//
// Limitations:
//   - No C-CANCEL support (cancel by context cancellation only).
//   - No multiplexing of multiple outstanding operations on one association.
//   - The Identifier dataset transfer syntax must be explicitly provided.
//
// The caller is responsible for:
//   - Establishing the underlying UL association with the desired presentation
//     context(s) for the selected Query/Retrieve Information Model.
//   - Choosing an appropriate Presentation Context ID (PCID) or using
//     NewRetrieveClientForSOPClass to derive it from an accepted SOP Class.
//
// In typical usage, create one RetrieveClient per association.
//
// Example (pseudo):
//
//	client := dimse.NewRetrieveClientForSOPClass(assoc, dimse.StudyRootMoveSOPClassUID)
//	rsp, err := client.Move(ctx, dimse.CMoveRequest{...}, identifierObj)
//
// See docs for more details.
type RetrieveClient struct {
	Assoc             *ul.Association
	PresentationCtxID byte
	// IdentifierSyntax is the transfer syntax used to encode the Identifier
	// dataset sent alongside a C-MOVE or C-GET request.
	IdentifierSyntax transfer.Syntax
}

// NewRetrieveClient constructs a RetrieveClient.
func NewRetrieveClient(assoc *ul.Association, pcID byte, identifierSyntax transfer.Syntax) *RetrieveClient {
	return &RetrieveClient{Assoc: assoc, PresentationCtxID: pcID, IdentifierSyntax: identifierSyntax}
}

// NewRetrieveClientForSOPClass derives the accepted presentation context and
// identifier transfer syntax for a C-MOVE or C-GET SOP Class.
func NewRetrieveClientForSOPClass(assoc *ul.Association, sopClassUID string) (*RetrieveClient, error) {
	pc, err := AcceptedContextForSOPClass(assoc, sopClassUID)
	if err != nil {
		return nil, err
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return nil, err
	}
	return NewRetrieveClient(assoc, pc.ID, syntax), nil
}

func (c *RetrieveClient) validate() error {
	if c == nil {
		return fmt.Errorf("dicom dimse: retrieve client is nil")
	}
	if c.Assoc == nil {
		return fmt.Errorf("dicom dimse: retrieve client association is nil")
	}
	if c.PresentationCtxID == 0 {
		return fmt.Errorf("dicom dimse: retrieve client presentation context ID is required")
	}
	if c.IdentifierSyntax == (transfer.Syntax{}) {
		return fmt.Errorf("dicom dimse: retrieve client identifier transfer syntax is required")
	}
	return nil
}

// Move executes a complete C-MOVE exchange and returns the final response.
func (c *RetrieveClient) Move(ctx context.Context, req CMoveRequest, identifier *object.Object) (*CMoveResponse, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	return SendCMove(ctx, c.Assoc, c.PresentationCtxID, req, identifier, c.IdentifierSyntax)
}

// MoveWithProgress executes a C-MOVE exchange and streams responses over a
// channel, including pending responses.
func (c *RetrieveClient) MoveWithProgress(ctx context.Context, req CMoveRequest, identifier *object.Object) (<-chan CMoveProgress, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	return SendCMoveWithProgress(ctx, c.Assoc, c.PresentationCtxID, req, identifier, c.IdentifierSyntax)
}

// Get executes a complete C-GET exchange and returns the final response.
func (c *RetrieveClient) Get(ctx context.Context, req CGetRequest, identifier *object.Object, storeHandler CGetStoreHandler) (*CGetResponse, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	return SendCGet(ctx, c.Assoc, c.PresentationCtxID, req, identifier, c.IdentifierSyntax, storeHandler)
}

// GetWithProgress executes a C-GET exchange and streams C-GET response events.
func (c *RetrieveClient) GetWithProgress(ctx context.Context, req CGetRequest, identifier *object.Object, storeHandler CGetStoreHandler) (<-chan CGetProgress, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	return SendCGetWithProgress(ctx, c.Assoc, c.PresentationCtxID, req, identifier, c.IdentifierSyntax, storeHandler)
}
