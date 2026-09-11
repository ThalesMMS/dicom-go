package dicomweb

import (
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/textproto"
	"strings"
)

// ResponseDecodeErrorKind classifies structural WADO-RS response failures.
type ResponseDecodeErrorKind string

const (
	ResponseDecodeMalformedMediaType ResponseDecodeErrorKind = "malformed_media_type"
	ResponseDecodeMissingBoundary    ResponseDecodeErrorKind = "missing_boundary"
	ResponseDecodeUnexpectedMedia    ResponseDecodeErrorKind = "unexpected_media_type"
	ResponseDecodePartLimit          ResponseDecodeErrorKind = "part_limit"
	ResponseDecodePartBytes          ResponseDecodeErrorKind = "part_bytes"
	ResponseDecodeHeaderBytes        ResponseDecodeErrorKind = "header_bytes"
	ResponseDecodeMultipartDepth     ResponseDecodeErrorKind = "multipart_depth"
	ResponseDecodeMalformedMultipart ResponseDecodeErrorKind = "malformed_multipart"
	ResponseDecodeTransferEncoding   ResponseDecodeErrorKind = "transfer_encoding"
)

// ResponseDecodeError reports a bounded, non-sensitive structural response
// failure. Client methods wrap it in ErrorKindDecodeResponse.
type ResponseDecodeError struct {
	Kind  ResponseDecodeErrorKind
	Limit int64
	Value int64
}

func (e *ResponseDecodeError) Error() string {
	if e == nil {
		return "invalid DICOMweb response"
	}
	switch e.Kind {
	case ResponseDecodeMalformedMediaType:
		return "DICOMweb response has a malformed Content-Type"
	case ResponseDecodeMissingBoundary:
		return "DICOMweb multipart response has no boundary"
	case ResponseDecodeUnexpectedMedia:
		return "DICOMweb response has an incompatible media type"
	case ResponseDecodePartLimit:
		return fmt.Sprintf("DICOMweb multipart response exceeds %d parts", e.Limit)
	case ResponseDecodePartBytes:
		return fmt.Sprintf("DICOMweb response part exceeds %d bytes", e.Limit)
	case ResponseDecodeHeaderBytes:
		return fmt.Sprintf("DICOMweb response part headers exceed %d bytes", e.Limit)
	case ResponseDecodeMultipartDepth:
		return fmt.Sprintf("DICOMweb multipart response exceeds depth %d", e.Limit)
	case ResponseDecodeMalformedMultipart:
		return "DICOMweb multipart response is malformed or truncated"
	case ResponseDecodeTransferEncoding:
		return "DICOMweb response part uses an unsupported Content-Transfer-Encoding"
	default:
		return "invalid DICOMweb response"
	}
}

type responseParseLimits struct {
	maxParts       int
	maxPartBytes   int64
	maxHeaderBytes int
	maxDepth       int
	allowedMedia   map[string]struct{}
}

func responseLimits(options Options) (responseParseLimits, error) {
	configured := options.ResponseLimits
	if configured.MaxParts < 0 || configured.MaxPartBytes < 0 || configured.MaxPartHeaderBytes < 0 || configured.MaxMultipartDepth < 0 {
		return responseParseLimits{}, errors.New("dicomweb: response limits cannot be negative")
	}
	if configured.MaxMultipartDepth > DefaultMaxMultipartDepth {
		return responseParseLimits{}, errors.New("dicomweb: WADO-RS multipart depth must be zero or one")
	}
	limits := responseParseLimits{
		maxParts:       options.ResponseLimits.MaxParts,
		maxPartBytes:   options.ResponseLimits.MaxPartBytes,
		maxHeaderBytes: options.ResponseLimits.MaxPartHeaderBytes,
		maxDepth:       options.ResponseLimits.MaxMultipartDepth,
	}
	if limits.maxParts <= 0 {
		limits.maxParts = DefaultMaxResponseParts
	}
	if limits.maxPartBytes <= 0 {
		limits.maxPartBytes = maximumResponseBodyBytes(options)
	}
	maximumBody := maximumResponseBodyBytes(options)
	if limits.maxPartBytes > maximumBody {
		limits.maxPartBytes = maximumBody
	}
	if limits.maxHeaderBytes <= 0 {
		limits.maxHeaderBytes = DefaultMaxPartHeaderBytes
	}
	if limits.maxDepth <= 0 {
		limits.maxDepth = DefaultMaxMultipartDepth
	}
	if len(options.ResponseLimits.AllowedMediaTypes) > 0 {
		limits.allowedMedia = make(map[string]struct{}, len(options.ResponseLimits.AllowedMediaTypes))
		for _, value := range options.ResponseLimits.AllowedMediaTypes {
			mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
			if err != nil || mediaType == "" {
				return responseParseLimits{}, errors.New("dicomweb: allowed WADO media type is invalid")
			}
			limits.allowedMedia[strings.ToLower(mediaType)] = struct{}{}
		}
	}
	return limits, nil
}

func maximumResponseBodyBytes(options Options) int64 {
	if options.MaxBodyBytes > 0 {
		return options.MaxBodyBytes
	}
	return DefaultMaxBodyBytes
}

type responseMediaPolicy struct {
	requireMultipart bool
	frames           bool
	single           map[string]struct{}
	parts            map[string]struct{}
}

var (
	dicomObjectResponsePolicy = responseMediaPolicy{
		single: mediaTypeSet("application/dicom"),
		parts:  mediaTypeSet("application/dicom"),
	}
	frameResponsePolicy = responseMediaPolicy{
		requireMultipart: true,
		frames:           true,
		parts: mediaTypeSet(
			"application/octet-stream", "application/x-deflate", "image/jpeg", "image/jls",
			"image/jp2", "image/jpx", "image/jphc", "image/jxl", "image/dicom-rle",
			"video/mpeg", "video/mp4", "video/h265",
		),
	}
)

func mediaTypeSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[strings.ToLower(value)] = struct{}{}
	}
	return result
}

func parseResponseMediaType(value string) (string, map[string]string, error) {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil || mediaType == "" {
		return "", nil, &ResponseDecodeError{Kind: ResponseDecodeMalformedMediaType}
	}
	return strings.ToLower(mediaType), params, nil
}

func responseContentType(values []string) (string, error) {
	if len(values) != 1 {
		return "", &ResponseDecodeError{Kind: ResponseDecodeMalformedMediaType}
	}
	return strings.TrimSpace(values[0]), nil
}

func validateOuterResponseMediaType(contentType string, policy responseMediaPolicy, limits responseParseLimits) (string, map[string]string, error) {
	mediaType, params, err := parseResponseMediaType(contentType)
	if err != nil {
		return "", nil, err
	}
	if mediaType == "multipart/related" {
		if limits.maxDepth < 1 {
			return "", nil, &ResponseDecodeError{Kind: ResponseDecodeMultipartDepth, Limit: int64(limits.maxDepth), Value: 1}
		}
		if strings.TrimSpace(params["boundary"]) == "" {
			return "", nil, &ResponseDecodeError{Kind: ResponseDecodeMissingBoundary}
		}
		if !validMultipartBoundary(params["boundary"]) {
			return "", nil, &ResponseDecodeError{Kind: ResponseDecodeMalformedMultipart}
		}
		declaredPart, _, parseErr := parseResponseMediaType(params["type"])
		if parseErr != nil || !mediaAllowed(declaredPart, policy.parts, limits.allowedMedia) {
			return "", nil, &ResponseDecodeError{Kind: ResponseDecodeUnexpectedMedia}
		}
		return mediaType, params, nil
	}
	if policy.requireMultipart || !mediaAllowed(mediaType, policy.single, limits.allowedMedia) {
		return "", nil, &ResponseDecodeError{Kind: ResponseDecodeUnexpectedMedia}
	}
	return mediaType, params, nil
}

func validMultipartBoundary(boundary string) bool {
	if len(boundary) == 0 || len(boundary) > 70 {
		return false
	}
	for index, character := range boundary {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("'()+_,-./:=?", character) {
			continue
		}
		if character == ' ' && index != len(boundary)-1 {
			continue
		}
		return false
	}
	return true
}

func multipartDecodeError(err error) error {
	var decodeErr *ResponseDecodeError
	if errors.As(err, &decodeErr) {
		return decodeErr
	}
	return &ResponseDecodeError{Kind: ResponseDecodeMalformedMultipart}
}

func validatePartMediaType(contentType string, policy responseMediaPolicy, limits responseParseLimits) (string, map[string]string, error) {
	mediaType, params, err := parseResponseMediaType(contentType)
	if err != nil {
		return "", nil, err
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		return "", nil, &ResponseDecodeError{Kind: ResponseDecodeMultipartDepth, Limit: int64(limits.maxDepth), Value: 2}
	}
	if !mediaAllowed(mediaType, policy.parts, limits.allowedMedia) {
		return "", nil, &ResponseDecodeError{Kind: ResponseDecodeUnexpectedMedia}
	}
	if policy.frames && !frameMediaTypeMatchesTransferSyntax(mediaType, params["transfer-syntax"]) {
		return "", nil, &ResponseDecodeError{Kind: ResponseDecodeUnexpectedMedia}
	}
	return mediaType, params, nil
}

func mediaAllowed(mediaType string, operation, configured map[string]struct{}) bool {
	if _, ok := operation[mediaType]; !ok {
		return false
	}
	if configured == nil {
		return true
	}
	_, ok := configured[mediaType]
	return ok
}

func responseHeaderBytes(header textproto.MIMEHeader) int {
	total := 2
	for name, values := range header {
		for _, value := range values {
			total += len(name) + 2 + len(value) + 2
		}
	}
	return total
}

func validatePartHeader(header textproto.MIMEHeader, limits responseParseLimits) error {
	size := responseHeaderBytes(header)
	if size > limits.maxHeaderBytes {
		return &ResponseDecodeError{Kind: ResponseDecodeHeaderBytes, Limit: int64(limits.maxHeaderBytes), Value: int64(size)}
	}
	if encoding := strings.TrimSpace(header.Get("Content-Transfer-Encoding")); encoding != "" && !strings.EqualFold(encoding, "binary") {
		return &ResponseDecodeError{Kind: ResponseDecodeTransferEncoding}
	}
	if len(header.Values("Content-Type")) != 1 {
		return &ResponseDecodeError{Kind: ResponseDecodeMalformedMediaType}
	}
	if len(header.Values("Content-Transfer-Encoding")) > 1 {
		return &ResponseDecodeError{Kind: ResponseDecodeTransferEncoding}
	}
	return nil
}

func readResponsePart(part io.Reader, limits responseParseLimits) ([]byte, error) {
	readLimit := limits.maxPartBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	data, err := io.ReadAll(io.LimitReader(part, readLimit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.maxPartBytes {
		return nil, &ResponseDecodeError{Kind: ResponseDecodePartBytes, Limit: limits.maxPartBytes, Value: int64(len(data))}
	}
	return data, nil
}

type responsePartLimitReader struct {
	reader    io.Reader
	remaining int64
	limit     int64
}

func newResponsePartLimitReader(reader io.Reader, limit int64) *responsePartLimitReader {
	return &responsePartLimitReader{reader: reader, remaining: limit, limit: limit}
}

func (r *responsePartLimitReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.reader.Read(extra[:])
		if n > 0 {
			value := r.limit
			if value < math.MaxInt64 {
				value++
			}
			return 0, &ResponseDecodeError{Kind: ResponseDecodePartBytes, Limit: r.limit, Value: value}
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.reader.Read(buffer)
	r.remaining -= int64(n)
	return n, responsePartReadError(err)
}

func responseDecodeFailure(err error) bool {
	var decodeErr *ResponseDecodeError
	return errors.As(err, &decodeErr)
}

func responsePartReadError(err error) error {
	if err == nil {
		return nil
	}
	var clientErr *Error
	if responseDecodeFailure(err) || errors.As(err, &clientErr) {
		return err
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return &ResponseDecodeError{Kind: ResponseDecodeMalformedMultipart}
	}
	return err
}
