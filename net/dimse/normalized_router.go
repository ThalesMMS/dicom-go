package dimse

import (
	"context"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// NormalizedRequestInfo identifies the accepted presentation context that
// carried a request. Service-specific handlers use it to enforce operation
// matrices for Meta SOP Classes without changing the legacy handler shapes.
type NormalizedRequestInfo struct {
	PresentationContext ul.AcceptedContext
	TransferSyntax      transfer.Syntax
}

type normalizedRequestInfoContextKey struct{}

func withNormalizedRequestInfo(ctx context.Context, info NormalizedRequestInfo) context.Context {
	return context.WithValue(ctx, normalizedRequestInfoContextKey{}, info)
}

// ContextWithNormalizedRequestInfo is primarily useful to service adapters and
// tests that invoke handlers without an association receive loop.
func ContextWithNormalizedRequestInfo(ctx context.Context, info NormalizedRequestInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return withNormalizedRequestInfo(ctx, info)
}

func NormalizedRequestInfoFromContext(ctx context.Context) (NormalizedRequestInfo, bool) {
	if ctx == nil {
		return NormalizedRequestInfo{}, false
	}
	info, ok := ctx.Value(normalizedRequestInfoContextKey{}).(NormalizedRequestInfo)
	return info, ok
}

// NormalizedSCPRoute binds one command SOP Class to its service-specific
// handlers. Routes are composed without mutable package globals.
type NormalizedSCPRoute struct {
	SOPClassUID string
	Options     NormalizedSCPOptions
}

// CombineNormalizedSCPOptions composes multiple service-specific normalized
// handlers into the single options value consumed by AssociationSCPOptions.
func CombineNormalizedSCPOptions(routes ...NormalizedSCPRoute) (NormalizedSCPOptions, error) {
	if len(routes) == 0 {
		return NormalizedSCPOptions{}, fmt.Errorf("dicom dimse: no normalized SCP routes")
	}
	byClass := make(map[string]NormalizedSCPOptions, len(routes))
	combined := NormalizedSCPOptions{}
	var hasEvent, hasGet, hasSet, hasAction, hasCreate, hasDelete bool
	for _, route := range routes {
		uid := strings.TrimSpace(route.SOPClassUID)
		if uid == "" {
			return NormalizedSCPOptions{}, fmt.Errorf("dicom dimse: normalized SCP route SOP Class UID is required")
		}
		if _, exists := byClass[uid]; exists {
			return NormalizedSCPOptions{}, fmt.Errorf("dicom dimse: duplicate normalized SCP route")
		}
		byClass[uid] = route.Options
		if route.Options.MaxDataSetBytes > combined.MaxDataSetBytes {
			combined.MaxDataSetBytes = route.Options.MaxDataSetBytes
		}
		hasEvent = hasEvent || route.Options.EventReportHandler != nil
		hasGet = hasGet || route.Options.GetHandler != nil
		hasSet = hasSet || route.Options.SetHandler != nil
		hasAction = hasAction || route.Options.ActionHandler != nil
		hasCreate = hasCreate || route.Options.CreateHandler != nil
		hasDelete = hasDelete || route.Options.DeleteHandler != nil
	}
	combined.PresentationContextPolicy = func(pc ul.AcceptedContext, commandSOPClassUID string) error {
		options, ok := byClass[commandSOPClassUID]
		if !ok {
			return &NormalizedPresentationContextError{PresentationContextID: pc.ID, AbstractSyntaxUID: pc.AbstractSyntaxUID, CommandSOPClassUID: commandSOPClassUID}
		}
		if options.PresentationContextPolicy != nil {
			return options.PresentationContextPolicy(pc, commandSOPClassUID)
		}
		if pc.AbstractSyntaxUID != commandSOPClassUID {
			return &NormalizedPresentationContextError{PresentationContextID: pc.ID, AbstractSyntaxUID: pc.AbstractSyntaxUID, CommandSOPClassUID: commandSOPClassUID}
		}
		return nil
	}
	if hasEvent {
		combined.EventReportHandler = func(ctx context.Context, request NormalizedEventReportRequest, dataSet *object.Object) (NormalizedEventReportSCPResult, error) {
			options := byClass[request.AffectedSOPClassUID]
			if options.EventReportHandler == nil {
				return NormalizedEventReportSCPResult{Response: NormalizedEventReportResponse{Status: StatusUnrecognizedOperation}}, nil
			}
			return options.EventReportHandler(ctx, request, dataSet)
		}
	}
	if hasGet {
		combined.GetHandler = func(ctx context.Context, request NormalizedGetRequest) (NormalizedGetSCPResult, error) {
			options := byClass[request.RequestedSOPClassUID]
			if options.GetHandler == nil {
				return NormalizedGetSCPResult{Response: NormalizedGetResponse{Status: StatusUnrecognizedOperation}}, nil
			}
			return options.GetHandler(ctx, request)
		}
	}
	if hasSet {
		combined.SetHandler = func(ctx context.Context, request NormalizedSetRequest, dataSet *object.Object) (NormalizedSetSCPResult, error) {
			options := byClass[request.RequestedSOPClassUID]
			if options.SetHandler == nil {
				return NormalizedSetSCPResult{Response: NormalizedSetResponse{Status: StatusUnrecognizedOperation}}, nil
			}
			return options.SetHandler(ctx, request, dataSet)
		}
	}
	if hasAction {
		combined.ActionHandler = func(ctx context.Context, request NormalizedActionRequest, dataSet *object.Object) (NormalizedActionSCPResult, error) {
			options := byClass[request.RequestedSOPClassUID]
			if options.ActionHandler == nil {
				return NormalizedActionSCPResult{Response: NormalizedActionResponse{Status: StatusUnrecognizedOperation}}, nil
			}
			return options.ActionHandler(ctx, request, dataSet)
		}
	}
	if hasCreate {
		combined.CreateHandler = func(ctx context.Context, request NormalizedCreateRequest, dataSet *object.Object) (NormalizedCreateSCPResult, error) {
			options := byClass[request.AffectedSOPClassUID]
			if options.CreateHandler == nil {
				return NormalizedCreateSCPResult{Response: NormalizedCreateResponse{Status: StatusUnrecognizedOperation}}, nil
			}
			return options.CreateHandler(ctx, request, dataSet)
		}
	}
	if hasDelete {
		combined.DeleteHandler = func(ctx context.Context, request NormalizedDeleteRequest) (NormalizedDeleteSCPResult, error) {
			options := byClass[request.RequestedSOPClassUID]
			if options.DeleteHandler == nil {
				return NormalizedDeleteSCPResult{Response: NormalizedDeleteResponse{Status: StatusUnrecognizedOperation}}, nil
			}
			return options.DeleteHandler(ctx, request)
		}
	}
	return combined, nil
}
