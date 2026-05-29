package jpip

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/net/dicomweb"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const (
	defaultMaxResponseBytes int64 = 256 << 20
	defaultMaxRetries             = 2
	maxRedirects                  = 10
	maxRetryDelay                 = 5 * time.Second
)

var supportedContentTypes = map[string]bool{
	"image/jp2":        true,
	"image/jpp-stream": true,
	"image/jpt-stream": true,
	"image/jph":        true,
	"image/jphc":       true,
}

// Size requests a decoded resolution. Zero dimensions leave fsiz unspecified.
type Size struct {
	Width  int
	Height int
}

// Region requests a source region. Width and Height must both be positive when
// the region is present.
type Region struct {
	X      int
	Y      int
	Width  int
	Height int
}

// ByteRange requests an inclusive HTTP byte range. It is useful for servers
// that expose a complete representation in addition to JPIP query controls.
type ByteRange struct {
	Start int64
	End   int64
}

// Request describes one JPIP representation. Frame is zero-based and becomes
// the DICOM/JPIP one-based stream query parameter.
type Request struct {
	Reference pixeldata.JPIPReference
	Frame     int
	Size      Size
	Region    Region
	Range     *ByteRange
}

// Response is one bounded server representation.
type Response struct {
	Data        []byte
	ContentType string
	Partial     bool
	CacheHit    bool
}

// Config controls a Client. Supplying HTTPClient is primarily useful for
// custom trust stores and tests; its transport remains the caller's
// responsibility. The default transport enforces TLS 1.2 or newer.
type Config struct {
	HTTPClient       *http.Client
	Policy           Policy
	MaxResponseBytes int64
	MaxCacheBytes    int64
	MaxRetries       int
	Registry         pixeldata.Registry
	RetryDelay       func(attempt int, response *http.Response) time.Duration
}

// Client is safe for concurrent use.
type Client struct {
	httpClient       *http.Client
	policy           compiledPolicy
	maxResponseBytes int64
	maxRetries       int
	registry         pixeldata.Registry
	retryDelay       func(int, *http.Response) time.Duration
	cache            *responseCache
}

// NewClient validates config and returns a bounded, policy-enforcing client.
func NewClient(config Config) (*Client, error) {
	policy, err := compilePolicy(config.Policy)
	if err != nil {
		return nil, err
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	maxRetries := config.MaxRetries
	if maxRetries < 0 {
		return nil, fmt.Errorf("jpip: max retries cannot be negative")
	}
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2: true,
				TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			},
			Timeout: 45 * time.Second,
		}
	} else {
		clone := *httpClient
		// Never inherit ambient browser-like cookies. JPIP credentials are
		// applied solely by the exact-origin policy below.
		clone.Jar = nil
		httpClient = &clone
	}
	client := &Client{
		httpClient:       httpClient,
		policy:           policy,
		maxResponseBytes: maxResponseBytes,
		maxRetries:       maxRetries,
		registry:         config.Registry,
		retryDelay:       config.RetryDelay,
		cache:            newResponseCache(config.MaxCacheBytes),
	}
	if client.registry == nil {
		client.registry = pixeldata.DefaultRegistry
	}
	client.installRedirectPolicy()
	return client, nil
}

// CacheStats returns cache occupancy and counters without exposing target URLs.
func (c *Client) CacheStats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	return c.cache.stats()
}

// Retrieve obtains one bounded representation. Completed and explicitly ranged
// representations are cached independently by canonical request.
func (c *Client) Retrieve(ctx context.Context, request Request) (Response, error) {
	if c == nil {
		return Response{}, &Error{Kind: ErrorKindInvalidRequest, Operation: "retrieve", Err: ErrInvalidRequest}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := requestURL(request)
	if err != nil {
		return Response{}, err
	}
	credential, err := c.policy.authorize(target)
	if err != nil {
		return Response{}, err
	}
	key := cacheKey(target, request.Range)
	if response, ok := c.cache.get(key); ok {
		return response, nil
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		response, retry, serverResponse, err := c.retrieveOnce(ctx, target, request.Range, credential)
		if err == nil {
			c.cache.put(key, response)
			return response, nil
		}
		lastErr = err
		if !retry || attempt == c.maxRetries {
			return Response{}, err
		}
		delay := c.delay(attempt, serverResponse)
		if err := waitContext(ctx, delay); err != nil {
			return Response{}, err
		}
	}
	return Response{}, lastErr
}

func (c *Client) retrieveOnce(ctx context.Context, target *url.URL, byteRange *ByteRange, credential normalizedCredential) (Response, bool, *http.Response, error) {
	return c.retrieveOnceWithChallenge(ctx, target, byteRange, credential, true)
}

func (c *Client) retrieveOnceWithChallenge(
	ctx context.Context,
	target *url.URL,
	byteRange *ByteRange,
	credential normalizedCredential,
	allowBearerReplay bool,
) (Response, bool, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Response{}, false, nil, &Error{Kind: ErrorKindInvalidRequest, Operation: "build request", Err: ErrInvalidRequest}
	}
	req.Header.Set("Accept", "image/jp2, image/jph, image/jphc, image/jpp-stream, image/jpt-stream")
	if err := applyCredential(req, credential); err != nil {
		return Response{}, false, nil, err
	}
	if byteRange != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", byteRange.Start, byteRange.End))
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, false, nil, ctx.Err()
		}
		var policyErr *Error
		if errors.As(err, &policyErr) {
			return Response{}, false, nil, policyErr
		}
		return Response{}, retryableNetworkError(err), nil, &Error{Kind: ErrorKindOffline, Operation: "retrieve", Err: ErrOffline}
	}
	if resp.StatusCode == http.StatusUnauthorized && allowBearerReplay && c.invalidateBearerChallenge(resp) {
		discardAndClose(resp)
		return c.retrieveOnceWithChallenge(ctx, target, byteRange, credential, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		retry := retryableStatus(resp.StatusCode)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Response{}, retry, resp, &Error{Kind: ErrorKindHTTPStatus, Operation: "retrieve", StatusCode: resp.StatusCode, Err: ErrHTTPStatus}
	}
	if byteRange == nil && resp.StatusCode == http.StatusPartialContent {
		return Response{}, false, resp, &Error{Kind: ErrorKindPartialResponse, Operation: "retrieve", StatusCode: resp.StatusCode, Err: ErrPartialResponse}
	}
	partial := resp.StatusCode == http.StatusPartialContent
	if byteRange != nil && partial && strings.TrimSpace(resp.Header.Get("Content-Range")) == "" {
		return Response{}, false, resp, &Error{Kind: ErrorKindPartialResponse, Operation: "retrieve", StatusCode: resp.StatusCode, Err: ErrPartialResponse}
	}
	contentType := normalizeContentType(resp.Header.Get("Content-Type"))
	if !supportedContentTypes[contentType] {
		return Response{}, false, resp, &Error{
			Kind:        ErrorKindUnsupportedContentType,
			Operation:   "retrieve",
			ContentType: contentType,
			Err:         ErrUnsupportedContentType,
		}
	}
	if resp.ContentLength > c.maxResponseBytes {
		return Response{}, false, resp, &Error{
			Kind: ErrorKindResponseTooLarge, Operation: "retrieve",
			Limit: c.maxResponseBytes, Size: resp.ContentLength, Err: ErrResponseTooLarge,
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return Response{}, false, resp, &Error{Kind: ErrorKindPartialResponse, Operation: "read response", Err: ErrPartialResponse}
	}
	if int64(len(data)) > c.maxResponseBytes {
		return Response{}, false, resp, &Error{
			Kind: ErrorKindResponseTooLarge, Operation: "retrieve",
			Limit: c.maxResponseBytes, Size: int64(len(data)), Err: ErrResponseTooLarge,
		}
	}
	if resp.ContentLength >= 0 && int64(len(data)) != resp.ContentLength {
		return Response{}, false, resp, &Error{
			Kind: ErrorKindPartialResponse, Operation: "read response",
			Size: int64(len(data)), Err: ErrPartialResponse,
		}
	}
	if partial {
		if err := validateContentRange(resp.Header.Get("Content-Range"), byteRange, int64(len(data))); err != nil {
			return Response{}, false, resp, err
		}
	}
	if err := validateRepresentation(contentType, data, partial); err != nil {
		return Response{}, false, resp, err
	}
	return Response{Data: data, ContentType: contentType, Partial: partial}, false, resp, nil
}

type bearerTokenInvalidator interface {
	Invalidate(dicomweb.AccessToken)
}

func applyCredential(req *http.Request, credential normalizedCredential) error {
	if credential.bearer != nil {
		token, err := credential.bearer.Token(req.Context())
		if err != nil || strings.TrimSpace(token.Value()) == "" {
			return &Error{
				Kind: ErrorKindAuthentication, Operation: "authorize",
				Err: ErrBearerTokenUnavailable,
			}
		}
		req.Header.Set("Authorization", "Bearer "+token.Value())
		return nil
	}
	if credential.username != "" || credential.password != "" {
		req.SetBasicAuth(credential.username, credential.password)
	}
	return nil
}

func (c *Client) invalidateBearerChallenge(resp *http.Response) bool {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return false
	}
	credential, err := c.policy.authorize(resp.Request.URL)
	if err != nil || credential.bearer == nil {
		return false
	}
	invalidator, ok := credential.bearer.(bearerTokenInvalidator)
	if !ok {
		return false
	}
	const prefix = "Bearer "
	authorization := resp.Request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	challenged, err := dicomweb.NewAccessToken(strings.TrimSpace(strings.TrimPrefix(authorization, prefix)), time.Time{})
	if err != nil {
		return false
	}
	invalidator.Invalidate(challenged)
	return true
}

func discardAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

func validateContentRange(value string, requested *ByteRange, size int64) error {
	if requested == nil {
		return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Err: ErrPartialResponse}
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "bytes ") {
		return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Err: ErrPartialResponse}
	}
	parts := strings.SplitN(strings.TrimSpace(value[len("bytes "):]), "/", 2)
	if len(parts) != 2 {
		return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Err: ErrPartialResponse}
	}
	bounds := strings.SplitN(parts[0], "-", 2)
	if len(bounds) != 2 {
		return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Err: ErrPartialResponse}
	}
	start, startErr := strconv.ParseInt(bounds[0], 10, 64)
	end, endErr := strconv.ParseInt(bounds[1], 10, 64)
	if startErr != nil || endErr != nil || start != requested.Start || end < start || end > requested.End || size != end-start+1 {
		return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Size: size, Err: ErrPartialResponse}
	}
	if parts[1] == "*" {
		if end != requested.End {
			return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Size: size, Err: ErrPartialResponse}
		}
		return nil
	}
	total, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || total <= end {
		return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Size: size, Err: ErrPartialResponse}
	}
	if end != requested.End && end != total-1 {
		return &Error{Kind: ErrorKindPartialResponse, Operation: "validate content range", Size: size, Err: ErrPartialResponse}
	}
	return nil
}

func (c *Client) installRedirectPolicy() {
	previous := c.httpClient.CheckRedirect
	c.httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return &Error{Kind: ErrorKindRedirectDenied, Operation: "redirect", Err: ErrRedirectDenied}
		}
		if previous != nil {
			if err := previous(req, via); err != nil {
				return err
			}
		}
		credential, err := c.policy.authorize(req.URL)
		if err != nil {
			return &Error{Kind: ErrorKindRedirectDenied, Operation: "redirect", Err: errors.Join(ErrRedirectDenied, err)}
		}
		req.Header.Del("Authorization")
		if err := applyCredential(req, credential); err != nil {
			return err
		}
		return nil
	}
}

func requestURL(request Request) (*url.URL, error) {
	if request.Frame < 0 {
		return nil, &Error{Kind: ErrorKindInvalidRequest, Operation: "build request", Err: ErrInvalidRequest}
	}
	if request.Size.Width < 0 || request.Size.Height < 0 ||
		request.Size.Width == 0 != (request.Size.Height == 0) {
		return nil, &Error{Kind: ErrorKindInvalidRequest, Operation: "build request", Err: ErrInvalidRequest}
	}
	hasRegion := request.Region.Width != 0 || request.Region.Height != 0 || request.Region.X != 0 || request.Region.Y != 0
	if hasRegion && (request.Region.X < 0 || request.Region.Y < 0 || request.Region.Width <= 0 || request.Region.Height <= 0) {
		return nil, &Error{Kind: ErrorKindInvalidRequest, Operation: "build request", Err: ErrInvalidRequest}
	}
	if request.Range != nil && (request.Range.Start < 0 || request.Range.End < request.Range.Start) {
		return nil, &Error{Kind: ErrorKindInvalidRequest, Operation: "build request", Err: ErrInvalidRequest}
	}
	u, err := parseEndpointURL(request.Reference.PixelDataProviderURL)
	if err != nil {
		return nil, &Error{Kind: ErrorKindInvalidRequest, Operation: "build request", Err: ErrInvalidRequest}
	}
	query := u.Query()
	query.Set("stream", strconv.Itoa(request.Frame+1))
	if request.Size.Width > 0 {
		query.Set("fsiz", fmt.Sprintf("%d,%d,closest", request.Size.Width, request.Size.Height))
	}
	if hasRegion {
		query.Set("roff", fmt.Sprintf("%d,%d", request.Region.X, request.Region.Y))
		query.Set("rsiz", fmt.Sprintf("%d,%d", request.Region.Width, request.Region.Height))
	}
	u.RawQuery = query.Encode()
	return u, nil
}

func cacheKey(u *url.URL, byteRange *ByteRange) string {
	key := u.String()
	if byteRange != nil {
		key += fmt.Sprintf("\x00%d-%d", byteRange.Start, byteRange.End)
	}
	return key
}

func normalizeContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	}
	return strings.ToLower(mediaType)
}

func validateRepresentation(contentType string, data []byte, partial bool) error {
	if len(data) == 0 {
		return &Error{Kind: ErrorKindCorruptResponse, Operation: "validate response", ContentType: contentType, Err: ErrCorruptResponse}
	}
	if partial {
		// An explicit byte range can begin in the middle of a valid container or
		// codestream. Content-Range was validated by retrieveOnce; full
		// signatures are checked only when a complete representation is read.
		return nil
	}
	switch contentType {
	case "image/jp2", "image/jph", "image/jphc":
		isCodestream := len(data) >= 2 && data[0] == 0xff && data[1] == 0x4f
		isContainer := len(data) >= 12 &&
			string(data[:4]) == "\x00\x00\x00\x0c" &&
			string(data[4:8]) == "jP  " &&
			string(data[8:12]) == "\r\n\x87\n"
		if !isCodestream && !isContainer {
			return &Error{Kind: ErrorKindCorruptResponse, Operation: "validate response", ContentType: contentType, Err: ErrCorruptResponse}
		}
	}
	return nil
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func retryableNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (c *Client) delay(attempt int, response *http.Response) time.Duration {
	if c.retryDelay != nil {
		delay := c.retryDelay(attempt, response)
		if delay < 0 {
			return 0
		}
		if delay > maxRetryDelay {
			return maxRetryDelay
		}
		return delay
	}
	if response != nil {
		if delay, ok := retryAfter(response.Header.Get("Retry-After")); ok {
			return delay
		}
	}
	delay := 100 * time.Millisecond * time.Duration(1<<min(attempt, 5))
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func retryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		delay := time.Duration(seconds) * time.Second
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		return delay, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		return delay, true
	}
	return 0, false
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
