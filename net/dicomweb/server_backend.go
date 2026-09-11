package dicomweb

import "context"

func safeSearch(backend SearchBackend, ctx context.Context, req SearchRequest, yield func(Dataset) error) (result SearchResult, err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.Search(ctx, req, yield)
}
func safeMetadata(backend MetadataBackend, ctx context.Context, req MetadataRequest, yield func(Dataset) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.Metadata(ctx, req, yield)
}
func safeRetrieve(backend RetrieveBackend, ctx context.Context, req RetrieveRequest, yield func(RetrievePart) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.Retrieve(ctx, req, yield)
}
func safeFrames(backend FrameBackend, ctx context.Context, req FrameRequest, yield func(FramePartStream) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.RetrieveFrames(ctx, req, yield)
}
func safeRender(renderer Renderer, ctx context.Context, req RenderRequest, yield func(RenderedPart) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return renderer.Render(ctx, req, yield)
}
func safeBulkData(backend BulkDataBackend, ctx context.Context, req BulkDataRequest) (part BulkDataPart, err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.RetrieveBulkData(ctx, req)
}
func safeStore(backend StoreBackend, ctx context.Context, req StoreRequest) (outcome StoreOutcome) {
	defer func() {
		if recover() != nil {
			outcome = StoreOutcome{Status: StoreStatusFailed, Err: ErrBackend}
		}
	}()
	return backend.Store(ctx, req)
}
