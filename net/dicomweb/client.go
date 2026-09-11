package dicomweb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	acceptDICOMJSON         = "application/dicom+json, application/json;q=0.9"
	acceptDICOM             = `application/dicom, multipart/related; type="application/dicom"`
	defaultMaxMetadataParts = 10_000
)

// Verify performs a light QIDO-RS study request to verify endpoint reachability.
func (c Client) Verify(ctx context.Context) (VerifyResult, error) {
	if err := c.validateMetadataResponseOptions(); err != nil {
		return VerifyResult{}, err
	}
	params := url.Values{}
	params.Set("limit", "1")
	u, err := c.Endpoint.StudySearchURL(params)
	if err != nil {
		return VerifyResult{}, err
	}
	started := time.Now()
	resp, err := c.doGET(ctx, u, c.metadataAcceptHeader())
	result := VerifyResult{Response: resp, Duration: time.Since(started), StartedAt: started.UTC()}
	if err != nil {
		return result, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return result, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, statusError(resp)
	}
	if len(strings.TrimSpace(string(resp.Body))) == 0 {
		return result, nil
	}
	if _, err := c.datasetsFromMetadataResponse(ctx, resp, false); err != nil {
		return result, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return result, nil
}

// SearchStudies performs a QIDO-RS study search and returns metadata in the
// package's Dataset shape, decoding either negotiated DICOM JSON or DICOM XML.
func (c Client) SearchStudies(ctx context.Context, params url.Values) ([]Dataset, error) {
	if err := c.validateMetadataResponseOptions(); err != nil {
		return nil, err
	}
	u, err := c.Endpoint.StudySearchURL(params)
	if err != nil {
		return nil, err
	}
	resp, err := c.doGET(ctx, u, c.metadataAcceptHeader())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	datasets, err := c.datasetsFromMetadataResponse(ctx, resp, false)
	if err != nil {
		return nil, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return datasets, nil
}

// SearchSeries performs a QIDO-RS series search for one study and returns
// metadata in the package's Dataset shape.
func (c Client) SearchSeries(ctx context.Context, studyInstanceUID string, params url.Values) ([]Dataset, error) {
	if err := c.validateMetadataResponseOptions(); err != nil {
		return nil, err
	}
	u, err := c.Endpoint.SeriesSearchURL(studyInstanceUID, params)
	if err != nil {
		return nil, err
	}
	resp, err := c.doGET(ctx, u, c.metadataAcceptHeader())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	datasets, err := c.datasetsFromMetadataResponse(ctx, resp, false)
	if err != nil {
		return nil, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return datasets, nil
}

// SearchInstances performs a QIDO-RS instance search for one series and
// returns metadata in the package's Dataset shape.
func (c Client) SearchInstances(ctx context.Context, studyInstanceUID, seriesInstanceUID string, params url.Values) ([]Dataset, error) {
	if err := c.validateMetadataResponseOptions(); err != nil {
		return nil, err
	}
	u, err := c.Endpoint.InstanceSearchURL(studyInstanceUID, seriesInstanceUID, params)
	if err != nil {
		return nil, err
	}
	resp, err := c.doGET(ctx, u, c.metadataAcceptHeader())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	datasets, err := c.datasetsFromMetadataResponse(ctx, resp, false)
	if err != nil {
		return nil, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return datasets, nil
}

// StudyMetadata retrieves WADO-RS study metadata and returns instance references.
func (c Client) StudyMetadata(ctx context.Context, studyInstanceUID string) ([]InstanceRef, error) {
	datasets, response, err := c.studyMetadataDatasets(ctx, studyInstanceUID)
	if err != nil {
		return nil, err
	}
	refs, err := instanceRefsFromDatasets(datasets, studyInstanceUID, "")
	if err != nil {
		return nil, newDICOMwebError(ErrorKindDecodeResponse, response.URL, response.StatusCode, err)
	}
	return refs, nil
}

// StudyMetadataDatasets retrieves raw WADO-RS study metadata datasets.
func (c Client) StudyMetadataDatasets(ctx context.Context, studyInstanceUID string) ([]Dataset, error) {
	datasets, _, err := c.studyMetadataDatasets(ctx, studyInstanceUID)
	return datasets, err
}

func (c Client) studyMetadataDatasets(ctx context.Context, studyInstanceUID string) ([]Dataset, Response, error) {
	if err := c.validateMetadataResponseOptions(); err != nil {
		return nil, Response{}, err
	}
	u, err := c.Endpoint.StudyMetadataURL(studyInstanceUID)
	if err != nil {
		return nil, Response{}, err
	}
	resp, err := c.doGET(ctx, u, c.metadataAcceptHeader())
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp, statusError(resp)
	}
	datasets, err := c.datasetsFromMetadataResponse(ctx, resp, true)
	if err != nil {
		return nil, resp, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return datasets, resp, nil
}

// SeriesMetadata retrieves WADO-RS series metadata and returns instance references.
func (c Client) SeriesMetadata(ctx context.Context, studyInstanceUID, seriesInstanceUID string) ([]InstanceRef, error) {
	datasets, response, err := c.seriesMetadataDatasets(ctx, studyInstanceUID, seriesInstanceUID)
	if err != nil {
		return nil, err
	}
	refs, err := instanceRefsFromDatasets(datasets, studyInstanceUID, seriesInstanceUID)
	if err != nil {
		return nil, newDICOMwebError(ErrorKindDecodeResponse, response.URL, response.StatusCode, err)
	}
	return refs, nil
}

// SeriesMetadataDatasets retrieves raw WADO-RS series metadata datasets.
func (c Client) SeriesMetadataDatasets(ctx context.Context, studyInstanceUID, seriesInstanceUID string) ([]Dataset, error) {
	datasets, _, err := c.seriesMetadataDatasets(ctx, studyInstanceUID, seriesInstanceUID)
	return datasets, err
}

func (c Client) seriesMetadataDatasets(ctx context.Context, studyInstanceUID, seriesInstanceUID string) ([]Dataset, Response, error) {
	if err := c.validateMetadataResponseOptions(); err != nil {
		return nil, Response{}, err
	}
	u, err := c.Endpoint.SeriesMetadataURL(studyInstanceUID, seriesInstanceUID)
	if err != nil {
		return nil, Response{}, err
	}
	resp, err := c.doGET(ctx, u, c.metadataAcceptHeader())
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp, statusError(resp)
	}
	datasets, err := c.datasetsFromMetadataResponse(ctx, resp, true)
	if err != nil {
		return nil, resp, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return datasets, resp, nil
}

// InstanceMetadata retrieves raw WADO-RS metadata for one instance.
func (c Client) InstanceMetadata(ctx context.Context, ref InstanceRef) ([]Dataset, error) {
	if err := c.validateMetadataResponseOptions(); err != nil {
		return nil, err
	}
	u, err := c.Endpoint.InstanceMetadataURL(ref)
	if err != nil {
		return nil, err
	}
	resp, err := c.doGET(ctx, u, c.metadataAcceptHeader())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	datasets, err := c.datasetsFromMetadataResponse(ctx, resp, true)
	if err != nil {
		return nil, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return datasets, nil
}

// RetrieveInstance retrieves a WADO-RS DICOM object as one or more object parts.
func (c Client) RetrieveInstance(ctx context.Context, ref InstanceRef) ([]ObjectPart, error) {
	return c.RetrieveInstanceWithOptions(ctx, ref, RetrieveOptions{})
}

// RetrieveInstanceWithOptions retrieves a WADO-RS DICOM object with explicit
// media/transfer-syntax preferences.
func (c Client) RetrieveInstanceWithOptions(ctx context.Context, ref InstanceRef, opts RetrieveOptions) ([]ObjectPart, error) {
	if _, err := responseLimits(c.Options); err != nil {
		return nil, newDICOMwebError(ErrorKindRequestFailure, "", 0, err)
	}
	u, err := c.Endpoint.InstanceURL(ref)
	if err != nil {
		return nil, err
	}
	resp, err := c.doGET(ctx, u, retrieveAcceptHeader(opts))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	parts, err := objectPartsFromResponseWithPolicy(resp, c.Options, dicomObjectResponsePolicy)
	if err != nil {
		return nil, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	return parts, nil
}

// RetrieveInstanceStreamWithOptions retrieves a WADO-RS DICOM object and streams each object part to handle.
func (c Client) RetrieveInstanceStreamWithOptions(ctx context.Context, ref InstanceRef, opts RetrieveOptions, handle func(ObjectPartStream) error) error {
	if handle == nil {
		return fmt.Errorf("WADO-RS stream handler is required")
	}
	u, err := c.Endpoint.InstanceURL(ref)
	if err != nil {
		return err
	}
	return c.retrieveStreamAt(ctx, u, opts, false, func(part LocatedObjectPartStream) error { return handle(part.Part) })
}

// RetrieveStudyStreamWithOptions streams every Part 10 object in a WADO-RS study response.
func (c Client) RetrieveStudyStreamWithOptions(ctx context.Context, studyInstanceUID string, opts RetrieveOptions, handle func(LocatedObjectPartStream) error) error {
	u, err := c.Endpoint.StudyURL(studyInstanceUID)
	if err != nil {
		return err
	}
	return c.retrieveStreamAt(ctx, u, opts, true, handle)
}

// RetrieveSeriesStreamWithOptions streams every Part 10 object in a WADO-RS series response.
func (c Client) RetrieveSeriesStreamWithOptions(ctx context.Context, studyInstanceUID, seriesInstanceUID string, opts RetrieveOptions, handle func(LocatedObjectPartStream) error) error {
	u, err := c.Endpoint.SeriesURL(studyInstanceUID, seriesInstanceUID)
	if err != nil {
		return err
	}
	return c.retrieveStreamAt(ctx, u, opts, true, handle)
}

func (c Client) retrieveStreamAt(ctx context.Context, u *url.URL, opts RetrieveOptions, requireMultipart bool, handle func(LocatedObjectPartStream) error) error {
	if handle == nil {
		return fmt.Errorf("WADO-RS stream handler is required")
	}
	if _, err := responseLimits(c.Options); err != nil {
		return newDICOMwebError(ErrorKindRequestFailure, "", 0, err)
	}
	httpResp, cancel, err := c.doHTTP(ctx, http.MethodGet, u, nil, "", retrieveAcceptHeader(opts))
	if err != nil {
		return err
	}
	defer func() {
		if cancel != nil {
			cancel()
		}
		_ = httpResp.Body.Close()
	}()

	resp := responseFromHTTP(u.String(), httpResp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	policy := dicomObjectResponsePolicy
	policy.requireMultipart = requireMultipart
	return c.streamObjectParts(resp, httpResp.Body, policy, handle)
}

// RetrieveFrames fetches selected one-based frames with WADO-RS. The response
// remains frame-scoped; callers never need to retrieve or decode the complete
// multi-frame instance.
func (c Client) RetrieveFrames(ctx context.Context, ref InstanceRef, frames []int, opts RetrieveOptions) ([]FramePart, error) {
	parts := make([]FramePart, 0, len(frames))
	err := c.RetrieveFramesStreamWithOptions(ctx, ref, frames, opts, func(part FramePartStream) error {
		data, err := io.ReadAll(part.Reader)
		if err != nil {
			return err
		}
		parts = append(parts, FramePart{
			FrameNumber:       part.FrameNumber,
			ContentType:       part.ContentType,
			TransferSyntaxUID: part.TransferSyntaxUID,
			Data:              data,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parts, nil
}

// RetrieveFramesStreamWithOptions retrieves selected one-based frames and
// invokes handle synchronously for each response part in request order. The
// part Reader is valid only until handle returns. Unread bytes are drained;
// returning an error stops retrieval and closes the HTTP response.
func (c Client) RetrieveFramesStreamWithOptions(ctx context.Context, ref InstanceRef, frames []int, opts RetrieveOptions, handle func(FramePartStream) error) error {
	if handle == nil {
		return fmt.Errorf("WADO-RS frame stream handler is required")
	}
	if _, err := responseLimits(c.Options); err != nil {
		return newDICOMwebError(ErrorKindRequestFailure, "", 0, err)
	}
	u, err := c.Endpoint.FramesURL(ref, frames)
	if err != nil {
		return err
	}
	httpResp, cancel, err := c.doHTTP(ctx, http.MethodGet, u, nil, "", retrieveFramesAcceptHeader(opts))
	if err != nil {
		return err
	}
	defer func() {
		if cancel != nil {
			cancel()
		}
		_ = httpResp.Body.Close()
	}()

	resp := responseFromHTTP(u.String(), httpResp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	partCount := 0
	err = c.streamObjectParts(resp, httpResp.Body, frameResponsePolicy, func(part LocatedObjectPartStream) error {
		if partCount >= len(frames) {
			return &ResponseDecodeError{
				Kind: ResponseDecodePartLimit, Limit: int64(len(frames)), Value: int64(partCount + 1),
			}
		}
		frame := FramePartStream{
			FrameNumber:       frames[partCount],
			ContentType:       part.Part.ContentType,
			TransferSyntaxUID: part.Part.TransferSyntaxUID,
			Reader:            part.Part.Reader,
			Size:              -1,
		}
		partCount++
		return handle(frame)
	})
	if err != nil {
		return err
	}
	if partCount != len(frames) {
		return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode,
			fmt.Errorf("RetrieveFrames returned %d parts for %d frames", partCount, len(frames)))
	}
	return nil
}

func (c Client) streamObjectParts(resp Response, body io.Reader, policy responseMediaPolicy, handle func(LocatedObjectPartStream) error) error {
	limits, err := responseLimits(c.Options)
	if err != nil {
		return newDICOMwebError(ErrorKindRequestFailure, "", 0, err)
	}
	contentType, err := responseContentType(resp.Header.Values("Content-Type"))
	if err != nil {
		return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	mediaType, params, err := validateOuterResponseMediaType(contentType, policy, limits)
	if err != nil {
		return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
	}
	maxBodyBytes := c.Options.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	body = &responseBodyLimitReader{reader: body, remaining: maxBodyBytes, limit: maxBodyBytes, resp: resp}
	if mediaType == "multipart/related" {
		boundary := params["boundary"]
		headerLimitedBody := newMultipartHeaderLimitReader(body, boundary, limits.maxHeaderBytes)
		reader := multipart.NewReader(headerLimitedBody, boundary)
		partCount := 0
		for {
			part, err := reader.NextRawPart()
			if errors.Is(err, io.EOF) {
				if partCount == 0 {
					return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode,
						&ResponseDecodeError{Kind: ResponseDecodeMalformedMultipart})
				}
				return nil
			}
			if err != nil {
				if headerLimitedBody.Err() != nil {
					return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, headerLimitedBody.Err())
				}
				var clientErr *Error
				if errors.As(err, &clientErr) {
					return err
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode,
					multipartDecodeError(err))
			}
			partCount++
			if partCount > limits.maxParts {
				return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode,
					&ResponseDecodeError{Kind: ResponseDecodePartLimit, Limit: int64(limits.maxParts), Value: int64(partCount)})
			}
			if err := validatePartHeader(part.Header, limits); err != nil {
				return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
			}
			partContentType := part.Header.Get("Content-Type")
			if _, _, err := validatePartMediaType(partContentType, policy, limits); err != nil {
				return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
			}
			limited := newResponsePartLimitReader(part, limits.maxPartBytes)
			if err := handle(LocatedObjectPartStream{
				Part: ObjectPartStream{
					ContentType:       partContentType,
					TransferSyntaxUID: transferSyntaxFromContentType(partContentType),
					Reader:            limited,
				},
				ContentLocation: part.Header.Get("Content-Location"),
			}); err != nil {
				if responseDecodeFailure(err) {
					return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
				}
				return err
			}
			if _, err := io.Copy(io.Discard, limited); err != nil {
				err = responsePartReadError(err)
				if responseDecodeFailure(err) {
					return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
				}
				return err
			}
			_ = part.Close()
		}
	}
	limited := newResponsePartLimitReader(body, limits.maxPartBytes)
	err = handle(LocatedObjectPartStream{
		Part: ObjectPartStream{
			ContentType:       contentType,
			TransferSyntaxUID: transferSyntaxFromContentType(contentType),
			Reader:            limited,
		},
		ContentLocation: resp.Header.Get("Content-Location"),
	})
	if err != nil {
		if responseDecodeFailure(err) {
			return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
		}
		return err
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		err = responsePartReadError(err)
		if responseDecodeFailure(err) {
			return newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, err)
		}
		return err
	}
	return nil
}

func retrieveAcceptHeader(opts RetrieveOptions) string {
	uids := normalizedTransferSyntaxUIDs(opts.TransferSyntaxUIDs)
	if len(uids) == 0 {
		return acceptDICOM
	}
	values := make([]string, 0, len(uids)*2)
	for _, uid := range uids {
		values = append(values,
			fmt.Sprintf(`multipart/related; type="application/dicom"; transfer-syntax=%s`, uid),
			fmt.Sprintf("application/dicom; transfer-syntax=%s", uid),
		)
	}
	return strings.Join(values, ", ")
}

func retrieveFramesAcceptHeader(opts RetrieveOptions) string {
	uids := normalizedTransferSyntaxUIDs(opts.TransferSyntaxUIDs)
	if len(uids) == 0 {
		return `multipart/related; type="application/octet-stream"`
	}
	values := make([]string, 0, len(uids))
	for _, uid := range uids {
		mediaType := retrieveFrameMediaType(uid)
		values = append(values, fmt.Sprintf(
			`multipart/related; type="%s"; transfer-syntax=%s`,
			mediaType,
			uid,
		))
	}
	return strings.Join(values, ", ")
}

func retrieveFrameMediaType(uid string) string {
	switch uid {
	case transfer.JPEGBaseline.UID,
		transfer.JPEGExtended.UID,
		transfer.JPEGLosslessNonHierarchical.UID,
		transfer.JPEGLosslessSV1.UID:
		return "image/jpeg"
	case transfer.JPEGLSLossless.UID, transfer.JPEGLSNearLossless.UID:
		return "image/jls"
	case transfer.JPEG2000LosslessOnly.UID, transfer.JPEG2000.UID:
		return "image/jp2"
	case transfer.JPEG2000Part2Lossless.UID, transfer.JPEG2000Part2.UID:
		return "image/jpx"
	case transfer.RLELossless.UID:
		return "image/dicom-rle"
	default:
		return "application/octet-stream"
	}
}

func normalizedTransferSyntaxUIDs(uids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		uid = strings.TrimSpace(uid)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out
}

// StoreInstances stores DICOM objects with STOW-RS multipart upload.
func (c Client) StoreInstances(ctx context.Context, instances []StoreInstance) (StoreResult, error) {
	if len(instances) == 0 {
		return StoreResult{}, nil
	}
	u, err := c.Endpoint.StoreStudiesURL()
	if err != nil {
		return StoreResult{}, err
	}
	return c.storeInstancesAt(ctx, u, instances)
}

// StoreInstancesToStudy stores DICOM objects through the study-scoped STOW-RS route.
func (c Client) StoreInstancesToStudy(ctx context.Context, studyInstanceUID string, instances []StoreInstance) (StoreResult, error) {
	if len(instances) == 0 {
		return StoreResult{}, nil
	}
	u, err := c.Endpoint.StoreStudyURL(studyInstanceUID)
	if err != nil {
		return StoreResult{}, err
	}
	return c.storeInstancesAt(ctx, u, instances)
}

func (c Client) storeInstancesAt(ctx context.Context, u *url.URL, instances []StoreInstance) (StoreResult, error) {
	body, contentType, err := stowMultipartReader(instances)
	if err != nil {
		return StoreResult{Response: Response{URL: SafeURL(u.String())}}, err
	}
	defer body.Close()
	resp, err := c.doPOST(ctx, u, body, contentType)
	result := StoreResult{Response: resp}
	if err != nil {
		return result, err
	}
	parsed, parseErr := StoreResultFromDICOMJSON(resp.Body)
	if parseErr != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return result, statusError(resp)
		}
		return result, newDICOMwebError(ErrorKindDecodeResponse, resp.URL, resp.StatusCode, parseErr)
	}
	parsed.Response = resp
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parsed, statusError(resp)
	}
	return parsed, nil
}

func (c Client) doGET(ctx context.Context, u *url.URL, accept string) (Response, error) {
	if _, err := responseLimits(c.Options); err != nil {
		return Response{}, newDICOMwebError(ErrorKindRequestFailure, "", 0, err)
	}
	return c.doRequest(ctx, http.MethodGet, u, nil, "", accept)
}

func (c Client) doPOST(ctx context.Context, u *url.URL, body io.Reader, contentType string) (Response, error) {
	return c.doRequest(ctx, http.MethodPost, u, body, contentType, acceptDICOMJSON)
}

func (c Client) doRequest(ctx context.Context, method string, u *url.URL, body io.Reader, contentType, accept string) (Response, error) {
	httpResp, cancel, err := c.doHTTP(ctx, method, u, body, contentType, accept)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		_ = httpResp.Body.Close()
		if cancel != nil {
			cancel()
		}
	}()

	maxBodyBytes := c.Options.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	response := responseFromHTTP(u.String(), httpResp)
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes+1))
	if err != nil {
		return response, newDICOMwebError(ErrorKindRequestFailure, response.URL, response.StatusCode, err)
	}
	if int64(len(data)) > maxBodyBytes {
		return response, newDICOMwebError(ErrorKindRequestFailure, response.URL, response.StatusCode,
			fmt.Errorf("response body exceeds %d bytes", maxBodyBytes))
	}
	response.Body = data
	return response, nil
}

func (c Client) doHTTP(ctx context.Context, method string, u *url.URL, body io.Reader, contentType, accept string) (*http.Response, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx := ctx
	var cancel context.CancelFunc
	if timeout := c.Options.Timeout; timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
	} else if _, ok := ctx.Deadline(); !ok {
		reqCtx, cancel = context.WithTimeout(ctx, DefaultTimeout)
	}

	httpClient, err := c.httpClientForRequest(u)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: err}
	}

	req, token, sourceAuth, err := c.newHTTPRequest(reqCtx, method, u, body, contentType, accept)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, nil, requestError(u.String(), err)
	}
	if resp.StatusCode == http.StatusUnauthorized && sourceAuth {
		invalidator, ok := c.Options.BearerTokenSource.(bearerTokenInvalidator)
		if ok {
			invalidator.Invalidate(token)
		}
		// Retry only requests whose body can be reproduced. A streaming STOW-RS
		// body is intentionally not replayed after partial transmission.
		if ok && (body == nil || req.GetBody != nil) {
			discardAndCloseResponse(resp)
			var retryBody io.ReadCloser
			if req.GetBody != nil {
				var bodyErr error
				retryBody, bodyErr = req.GetBody()
				if bodyErr != nil {
					if cancel != nil {
						cancel()
					}
					return nil, nil, newDICOMwebError(ErrorKindRequestFailure, u.String(), 0, bodyErr)
				}
			}
			retry, _, _, requestErr := c.newHTTPRequest(reqCtx, method, u, retryBody, contentType, accept)
			if requestErr != nil {
				if retryBody != nil {
					_ = retryBody.Close()
				}
				if cancel != nil {
					cancel()
				}
				return nil, nil, requestErr
			}
			retry.ContentLength = req.ContentLength
			retry.GetBody = req.GetBody
			resp, err = httpClient.Do(retry)
			if err != nil {
				if cancel != nil {
					cancel()
				}
				return nil, nil, requestError(u.String(), err)
			}
		}
	}
	return resp, cancel, nil
}

func (c Client) newHTTPRequest(
	ctx context.Context,
	method string,
	u *url.URL,
	body io.Reader,
	contentType string,
	accept string,
) (*http.Request, AccessToken, bool, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, AccessToken{}, false,
			newDICOMwebError(ErrorKindRequestFailure, u.String(), 0, err)
	}
	req.Header.Set("Accept", accept)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if source := c.Options.BearerTokenSource; source != nil {
		token, err := accessTokenFromSource(ctx, u.String(), source)
		if err != nil {
			return nil, AccessToken{}, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token.Value())
		return req, token, true, nil
	}
	if token := strings.TrimSpace(c.Options.BearerToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if user := strings.TrimSpace(c.Options.BasicUsername); user != "" {
		req.SetBasicAuth(user, c.Options.BasicPassword)
	}
	return req, AccessToken{}, false, nil
}

func discardAndCloseResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

func responseFromHTTP(rawURL string, httpResp *http.Response) Response {
	return Response{
		URL:        SafeURL(rawURL),
		StatusCode: httpResp.StatusCode,
		Status:     safeHTTPStatus(httpResp.StatusCode),
		Header:     httpResp.Header.Clone(),
	}
}

type responseBodyLimitReader struct {
	reader    io.Reader
	remaining int64
	limit     int64
	resp      Response
}

func (r *responseBodyLimitReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.reader.Read(extra[:])
		if n > 0 {
			return 0, newDICOMwebError(ErrorKindRequestFailure, r.resp.URL, r.resp.StatusCode,
				fmt.Errorf("response body exceeds %d bytes", r.limit))
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func requestError(rawURL string, err error) error {
	if isTimeout(err) {
		return newDICOMwebError(ErrorKindTimeout, rawURL, 0, err)
	}
	return newDICOMwebError(ErrorKindRequestFailure, rawURL, 0, err)
}

func statusError(resp Response) error {
	kind := ErrorKindHTTPStatus
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		kind = ErrorKindAuthStatus
	}
	return newDICOMwebError(kind, resp.URL, resp.StatusCode, nil)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func stowMultipartReader(instances []StoreInstance) (io.ReadCloser, string, error) {
	for _, instance := range instances {
		if !storeInstanceHasPayload(instance) {
			return nil, "", fmt.Errorf("STOW-RS object %s is empty", firstNonEmpty(instance.Path, instance.SOPInstanceUID))
		}
	}

	reader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	contentType := `multipart/related; type="application/dicom"; boundary=` + writer.Boundary()
	go func() {
		err := writeSTOWMultipart(writer, instances)
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = pipeWriter.CloseWithError(err)
			return
		}
		_ = pipeWriter.Close()
	}()
	return reader, contentType, nil
}

func writeSTOWMultipart(writer *multipart.Writer, instances []StoreInstance) error {
	for _, instance := range instances {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", "application/dicom")
		part, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		payload, closePayload, err := storeInstancePayload(instance)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(part, payload)
		if closePayload != nil {
			if closeErr := closePayload.Close(); copyErr == nil {
				copyErr = closeErr
			}
		}
		if copyErr != nil {
			return copyErr
		}
		if written == 0 {
			return fmt.Errorf("STOW-RS object %s is empty", firstNonEmpty(instance.Path, instance.SOPInstanceUID))
		}
	}
	return nil
}

func storeInstanceHasPayload(instance StoreInstance) bool {
	return instance.Open != nil || instance.Reader != nil || len(instance.Data) > 0
}

func storeInstancePayload(instance StoreInstance) (io.Reader, io.Closer, error) {
	if instance.Open != nil {
		reader, err := instance.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("open STOW-RS object %s: %w", firstNonEmpty(instance.Path, instance.SOPInstanceUID), err)
		}
		if reader == nil {
			return nil, nil, fmt.Errorf("open STOW-RS object %s: returned nil reader", firstNonEmpty(instance.Path, instance.SOPInstanceUID))
		}
		return reader, reader, nil
	}
	if instance.Reader != nil {
		return instance.Reader, nil, nil
	}
	return bytes.NewReader(instance.Data), nil, nil
}
