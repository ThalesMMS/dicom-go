package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// FindClient provides a small SCU facade over an established association.
type FindClient struct {
	Assoc             *ul.Association
	SOPClassUID       string
	PresentationCtxID byte
	IdentifierSyntax  transfer.Syntax
}

// NewFindClientForSOPClass derives the accepted presentation context and
// identifier transfer syntax for a C-FIND SOP Class.
func NewFindClientForSOPClass(assoc *ul.Association, sopClassUID string) (*FindClient, error) {
	pc, err := AcceptedContextForSOPClass(assoc, sopClassUID)
	if err != nil {
		return nil, err
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return nil, err
	}
	return &FindClient{
		Assoc:             assoc,
		SOPClassUID:       sopClassUID,
		PresentationCtxID: pc.ID,
		IdentifierSyntax:  syntax,
	}, nil
}

func (c *FindClient) validate(req CFindRequest) (string, error) {
	if c == nil {
		return "", fmt.Errorf("dicom dimse: find client is nil")
	}
	if c.Assoc == nil {
		return "", fmt.Errorf("dicom dimse: find client association is nil")
	}
	if c.PresentationCtxID == 0 {
		return "", fmt.Errorf("dicom dimse: find client presentation context ID is required")
	}
	if c.IdentifierSyntax == (transfer.Syntax{}) {
		return "", fmt.Errorf("dicom dimse: find client identifier transfer syntax is required")
	}
	requestedSOPClassUID := transfer.NormalizeUID(req.AffectedSOPClassUID)
	clientSOPClassUID := transfer.NormalizeUID(c.SOPClassUID)
	if requestedSOPClassUID != "" && requestedSOPClassUID != clientSOPClassUID {
		return "", fmt.Errorf("dicom dimse: C-FIND request AffectedSOPClassUID %q does not match find client SOPClassUID %q", requestedSOPClassUID, clientSOPClassUID)
	}
	if requestedSOPClassUID == "" {
		requestedSOPClassUID = clientSOPClassUID
	}
	return requestedSOPClassUID, nil
}

// Find executes a C-FIND exchange with the client's accepted presentation
// context and identifier syntax.
func (c *FindClient) Find(req CFindRequest, identifier *object.Object) (<-chan FindResult, <-chan error) {
	return c.FindWithOptions(OperationOptions{}, req, identifier)
}

// FindWithOptions executes a C-FIND exchange using operation options for
// cancellation and response deadlines.
func (c *FindClient) FindWithOptions(options OperationOptions, req CFindRequest, identifier *object.Object) (<-chan FindResult, <-chan error) {
	errCh := make(chan error, 1)
	normalizedSOPClassUID, err := c.validate(req)
	if err != nil {
		results := make(chan FindResult)
		close(results)
		errCh <- err
		close(errCh)
		return results, errCh
	}
	req.AffectedSOPClassUID = normalizedSOPClassUID
	return FindWithOptions(options, c.Assoc, c.PresentationCtxID, req, identifier, c.IdentifierSyntax)
}
