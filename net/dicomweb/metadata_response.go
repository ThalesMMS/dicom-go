package dicomweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dicomxml"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type datasetResponseSpool struct {
	representation metadataRepresentation
	json           *jsonArraySpool
	xml            *responseSpool
	xmlWriter      *multipart.Writer
	contentType    string
	count          int
}

func newDatasetResponseSpool(directory string, maximum int64, representation metadataRepresentation) (*datasetResponseSpool, error) {
	spool := &datasetResponseSpool{representation: representation}
	switch representation {
	case metadataRepresentationJSON:
		jsonSpool, err := newJSONArraySpool(directory, maximum)
		if err != nil {
			return nil, err
		}
		spool.json = jsonSpool
		spool.contentType = string(MetadataMediaTypeDICOMJSON)
	case metadataRepresentationXML:
		xmlSpool, err := newResponseSpool(directory, maximum)
		if err != nil {
			return nil, err
		}
		spool.xml = xmlSpool
		spool.xmlWriter = multipart.NewWriter(xmlSpool)
		spool.contentType = mime.FormatMediaType("multipart/related", map[string]string{
			"type":     string(MetadataMediaTypeDICOMXML),
			"boundary": spool.xmlWriter.Boundary(),
		})
	default:
		return nil, ErrUnsupported
	}
	return spool, nil
}

func (spool *datasetResponseSpool) Append(ctx context.Context, server *Server, dataset Dataset, contentLocation string) error {
	if spool == nil || server == nil {
		return ErrBackend
	}
	switch spool.representation {
	case metadataRepresentationJSON:
		data, err := server.encodeResponseDataset(ctx, dataset)
		if err != nil {
			return err
		}
		if err := spool.json.Append(data); err != nil {
			return err
		}
	case metadataRepresentationXML:
		obj, err := server.responseDatasetObject(ctx, dataset)
		if err != nil {
			return err
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", string(MetadataMediaTypeDICOMXML))
		if contentLocation != "" {
			header.Set("Content-Location", contentLocation)
		}
		part, err := spool.xmlWriter.CreatePart(header)
		if err != nil {
			return err
		}
		if err := dicomxml.EncodeContext(ctx, part, obj, dicomxml.Options{
			OmitGroupLength: true,
			Limits: dicomxml.Limits{
				MaxXMLBytes:          server.limits.MaxResponseBytes,
				MaxValueBytes:        server.limits.MaxJSONValueBytes,
				MaxInlineBinaryBytes: int64(server.limits.MaxJSONValueBytes),
				MaxTotalBinaryBytes:  server.limits.MaxResponseBytes,
				MaxSequenceDepth:     server.limits.MaxJSONDepth,
				MaxElements:          server.limits.MaxJSONValues,
				MaxSequenceItems:     server.limits.MaxJSONValues,
			},
		}); err != nil {
			return classifyMetadataConversionError(err)
		}
	default:
		return ErrUnsupported
	}
	spool.count++
	return nil
}

func (spool *datasetResponseSpool) Finalize(ctx context.Context, server *Server, emptyXMLRoot bool) error {
	if spool == nil || server == nil {
		return ErrBackend
	}
	switch spool.representation {
	case metadataRepresentationJSON:
		return spool.json.Finalize()
	case metadataRepresentationXML:
		if spool.count == 0 && emptyXMLRoot {
			header := textproto.MIMEHeader{}
			header.Set("Content-Type", string(MetadataMediaTypeDICOMXML))
			part, err := spool.xmlWriter.CreatePart(header)
			if err != nil {
				return err
			}
			if err := dicomxml.EncodeContext(ctx, part, nil, dicomxml.Options{
				OmitGroupLength: true,
				Limits:          dicomxml.Limits{MaxXMLBytes: server.limits.MaxResponseBytes},
			}); err != nil {
				return classifyMetadataConversionError(err)
			}
		}
		if err := spool.xmlWriter.Close(); err != nil {
			return err
		}
		return spool.xml.Rewind()
	default:
		return ErrUnsupported
	}
}

func (spool *datasetResponseSpool) Reader() io.Reader {
	if spool == nil {
		return nil
	}
	if spool.json != nil {
		return spool.json.file
	}
	if spool.xml != nil {
		return spool.xml.file
	}
	return nil
}

func (spool *datasetResponseSpool) Close() error {
	if spool == nil {
		return nil
	}
	if spool.json != nil {
		return spool.json.Close()
	}
	if spool.xml != nil {
		return spool.xml.Close()
	}
	return nil
}

func (s *Server) responseDatasetObject(ctx context.Context, dataset Dataset) (*object.Object, error) {
	if err := s.validateResponseDataset(ctx, dataset); err != nil {
		return nil, err
	}
	data, err := json.Marshal(dataset)
	if err != nil {
		return nil, ErrBackend
	}
	if int64(len(data)) > s.limits.MaxResponseBytes {
		return nil, ErrResourceLimit
	}
	obj, err := dicomjson.UnmarshalContext(ctx, data, std.Dictionary, dicomjson.UnmarshalOptions{Limits: dicomjson.Limits{
		MaxJSONBytes:         s.limits.MaxResponseBytes,
		MaxInlineBinaryBytes: int64(s.limits.MaxJSONValueBytes),
		MaxTotalBinaryBytes:  s.limits.MaxResponseBytes,
		MaxSequenceDepth:     s.limits.MaxJSONDepth,
		MaxElements:          s.limits.MaxJSONValues,
		MaxSequenceItems:     s.limits.MaxJSONValues,
	}})
	if err != nil {
		return nil, classifyMetadataConversionError(err)
	}
	return obj, nil
}

func classifyMetadataConversionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrResourceLimit) || errors.Is(err, dicomjson.ErrMaxJSONBytesExceeded) ||
		errors.Is(err, dicomjson.ErrMaxInlineBinaryBytesExceeded) || errors.Is(err, dicomjson.ErrMaxTotalBinaryBytesExceeded) ||
		errors.Is(err, dicomjson.ErrMaxSequenceDepthExceeded) || errors.Is(err, dicomjson.ErrMaxElementsExceeded) ||
		errors.Is(err, dicomjson.ErrMaxSequenceItemsExceeded) || errors.Is(err, dicomxml.ErrMaxXMLBytesExceeded) ||
		errors.Is(err, dicomxml.ErrMaxValueBytesExceeded) || errors.Is(err, dicomxml.ErrMaxInlineBytesExceeded) ||
		errors.Is(err, dicomxml.ErrMaxBinaryBytesExceeded) || errors.Is(err, dicomxml.ErrMaxDepthExceeded) ||
		errors.Is(err, dicomxml.ErrMaxElementsExceeded) || errors.Is(err, dicomxml.ErrMaxItemsExceeded) {
		return ErrResourceLimit
	}
	return ErrBackend
}

func (s *Server) metadataContentLocation(request MetadataRequest, dataset Dataset) (string, error) {
	ref := InstanceRef{
		StudyInstanceUID:  dicomjson.ElementString(dataset, "0020000D"),
		SeriesInstanceUID: dicomjson.ElementString(dataset, "0020000E"),
		SOPInstanceUID:    dicomjson.ElementString(dataset, "00080018"),
	}
	if ref.StudyInstanceUID != request.StudyInstanceUID ||
		(request.SeriesInstanceUID != "" && ref.SeriesInstanceUID != request.SeriesInstanceUID) ||
		(request.SOPInstanceUID != "" && ref.SOPInstanceUID != request.SOPInstanceUID) {
		return "", ErrBackend
	}
	location, err := s.instanceLocation(ref)
	if err != nil {
		return "", ErrBackend
	}
	return location + "/metadata", nil
}

func (c Client) metadataAcceptHeader() string {
	mediaTypes := c.Options.MetadataMediaTypes
	if len(mediaTypes) == 0 {
		return acceptDICOMJSON
	}
	seen := make(map[MetadataMediaType]bool, len(mediaTypes))
	values := make([]string, 0, len(mediaTypes)+1)
	for _, mediaType := range mediaTypes {
		if seen[mediaType] {
			continue
		}
		seen[mediaType] = true
		quality := 1.0 - float64(len(values))/10
		var value string
		switch mediaType {
		case MetadataMediaTypeDICOMJSON:
			value = string(mediaType)
		case MetadataMediaTypeDICOMXML:
			value = `multipart/related; type="application/dicom+xml"`
		default:
			continue
		}
		if quality < 1 {
			value += fmt.Sprintf(";q=%.1f", quality)
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return acceptDICOMJSON
	}
	return strings.Join(values, ", ")
}

func (c Client) validateMetadataResponseOptions() error {
	if c.Options.MaxMetadataParts < 0 {
		return newDICOMwebError(ErrorKindRequestFailure, "", 0, fmt.Errorf("MaxMetadataParts must not be negative"))
	}
	if _, err := responseLimits(c.Options); err != nil {
		return newDICOMwebError(ErrorKindRequestFailure, "", 0, err)
	}
	return nil
}

func metadataRefFromContentLocation(responseURL, value string) (InstanceRef, error) {
	if strings.TrimSpace(value) == "" {
		return InstanceRef{}, fmt.Errorf("DICOM XML metadata part is missing Content-Location")
	}
	base, err := url.Parse(responseURL)
	if err != nil {
		return InstanceRef{}, err
	}
	location, err := url.Parse(value)
	if err != nil || location.User != nil || location.Fragment != "" || location.RawQuery != "" || location.RawPath != "" {
		return InstanceRef{}, fmt.Errorf("invalid DICOM XML metadata Content-Location")
	}
	resolved := base.ResolveReference(location)
	if !strings.EqualFold(resolved.Scheme, base.Scheme) || !strings.EqualFold(resolved.Host, base.Host) || resolved.Path == "" {
		return InstanceRef{}, fmt.Errorf("DICOM XML metadata Content-Location is not same-origin")
	}
	parts := strings.Split(strings.Trim(resolved.Path, "/"), "/")
	if len(parts) < 7 {
		return InstanceRef{}, fmt.Errorf("DICOM XML metadata Content-Location does not identify an instance metadata resource")
	}
	parts = parts[len(parts)-7:]
	if parts[0] != "studies" || parts[2] != "series" || parts[4] != "instances" || parts[6] != "metadata" ||
		!core.IsValidUID(parts[1]) || !core.IsValidUID(parts[3]) || !core.IsValidUID(parts[5]) {
		return InstanceRef{}, fmt.Errorf("DICOM XML metadata Content-Location does not identify an instance metadata resource")
	}
	return InstanceRef{StudyInstanceUID: parts[1], SeriesInstanceUID: parts[3], SOPInstanceUID: parts[5]}, nil
}

func (c Client) datasetsFromMetadataResponse(ctx context.Context, response Response, requireContentLocation bool) ([]Dataset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, err := responseLimits(c.Options)
	if err != nil {
		return nil, err
	}
	contentTypeValues := response.Header.Values("Content-Type")
	if len(bytes.TrimSpace(response.Body)) == 0 && len(contentTypeValues) == 0 {
		return nil, nil
	}
	contentType, err := responseContentType(contentTypeValues)
	if err != nil {
		return nil, err
	}
	mediaType, params, err := parseResponseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	if _, err := metadataTransferSyntax(params); err != nil || !metadataCharsetSupported(params) {
		return nil, fmt.Errorf("unsupported metadata Content-Type parameters")
	}
	switch strings.ToLower(mediaType) {
	case string(MetadataMediaTypeDICOMJSON), "application/json", "text/plain":
		if !mediaAllowed(mediaType, metadataJSONResponseMediaTypes, limits.allowedMedia) {
			return nil, &ResponseDecodeError{Kind: ResponseDecodeUnexpectedMedia}
		}
		if !c.metadataMediaTypeConfigured(MetadataMediaTypeDICOMJSON) {
			return nil, fmt.Errorf("server returned an unrequested DICOM JSON representation")
		}
		if !metadataParametersSupported(params, "charset", "transfer-syntax") {
			return nil, fmt.Errorf("unsupported metadata Content-Type parameters")
		}
		if len(bytes.TrimSpace(response.Body)) == 0 {
			return nil, nil
		}
		// application/json and text/plain preserve interoperability with legacy
		// servers that return the DICOM JSON array under a generic media type.
		return datasetsFromDICOMJSON(response.Body)
	case "multipart/related":
		if limits.maxDepth < 1 {
			return nil, &ResponseDecodeError{Kind: ResponseDecodeMultipartDepth, Limit: int64(limits.maxDepth), Value: 1}
		}
		if !c.metadataMediaTypeConfigured(MetadataMediaTypeDICOMXML) {
			return nil, fmt.Errorf("server returned an unrequested DICOM XML representation")
		}
		if !metadataParametersSupported(params, "boundary", "charset", "start", "start-info", "transfer-syntax", "type") {
			return nil, fmt.Errorf("unsupported multipart metadata Content-Type parameters")
		}
		declaredPart, _, typeErr := parseResponseMediaType(params["type"])
		if typeErr != nil || !mediaAllowed(declaredPart, metadataXMLResponsePolicy.parts, limits.allowedMedia) {
			return nil, &ResponseDecodeError{Kind: ResponseDecodeUnexpectedMedia}
		}
		boundary := strings.TrimSpace(params["boundary"])
		if boundary == "" {
			return nil, &ResponseDecodeError{Kind: ResponseDecodeMissingBoundary}
		}
		if !validMultipartBoundary(boundary) {
			return nil, &ResponseDecodeError{Kind: ResponseDecodeMalformedMultipart}
		}
		return c.datasetsFromDICOMXMLMultipart(ctx, response, boundary, params["transfer-syntax"], requireContentLocation, limits)
	default:
		return nil, &ResponseDecodeError{Kind: ResponseDecodeUnexpectedMedia}
	}
}

var (
	metadataJSONResponseMediaTypes = mediaTypeSet(string(MetadataMediaTypeDICOMJSON), "application/json", "text/plain")
	metadataXMLResponsePolicy      = responseMediaPolicy{requireMultipart: true, parts: mediaTypeSet(string(MetadataMediaTypeDICOMXML))}
)

func (c Client) metadataMediaTypeConfigured(want MetadataMediaType) bool {
	if len(c.Options.MetadataMediaTypes) == 0 {
		return want == MetadataMediaTypeDICOMJSON
	}
	valid := false
	for _, mediaType := range c.Options.MetadataMediaTypes {
		if mediaType == MetadataMediaTypeDICOMJSON || mediaType == MetadataMediaTypeDICOMXML {
			valid = true
		}
		if mediaType == want {
			return true
		}
	}
	return !valid && want == MetadataMediaTypeDICOMJSON
}

func (c Client) datasetsFromDICOMXMLMultipart(ctx context.Context, response Response, boundary, outerTransferSyntax string, requireContentLocation bool, limits responseParseLimits) ([]Dataset, error) {
	maximumParts := c.Options.MaxMetadataParts
	if maximumParts < 0 {
		return nil, fmt.Errorf("MaxMetadataParts must not be negative")
	}
	if maximumParts == 0 {
		maximumParts = defaultMaxMetadataParts
	}
	if limits.maxParts < maximumParts {
		maximumParts = limits.maxParts
	}
	maximumBytes := limits.maxPartBytes
	body := newMultipartHeaderLimitReader(bytes.NewReader(response.Body), boundary, limits.maxHeaderBytes)
	reader := multipart.NewReader(body, boundary)
	datasets := make([]Dataset, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if body.Err() != nil {
				return nil, body.Err()
			}
			return nil, multipartDecodeError(err)
		}
		if len(datasets) >= maximumParts {
			_ = part.Close()
			return nil, &ResponseDecodeError{Kind: ResponseDecodePartLimit, Limit: int64(maximumParts), Value: int64(len(datasets) + 1)}
		}
		if err := validatePartHeader(part.Header, limits); err != nil {
			_ = part.Close()
			return nil, err
		}
		partMediaType, partParams, err := validatePartMediaType(part.Header.Get("Content-Type"), metadataXMLResponsePolicy, limits)
		if err != nil || !strings.EqualFold(partMediaType, string(MetadataMediaTypeDICOMXML)) || !metadataCharsetSupported(partParams) ||
			!metadataParametersSupported(partParams, "charset", "transfer-syntax") {
			_ = part.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("multipart DICOM XML response contains an invalid or heterogeneous part")
		}
		partTransferSyntax := strings.TrimSpace(partParams["transfer-syntax"])
		if partTransferSyntax == "" {
			partTransferSyntax = outerTransferSyntax
		} else if outerTransferSyntax != "" && transfer.NormalizeUID(partTransferSyntax) != transfer.NormalizeUID(outerTransferSyntax) {
			return nil, fmt.Errorf("multipart DICOM XML transfer syntax parameters conflict")
		}
		syntax, err := metadataTransferSyntax(map[string]string{"transfer-syntax": partTransferSyntax})
		if err != nil {
			return nil, err
		}
		var locationRef InstanceRef
		if requireContentLocation {
			locationRef, err = metadataRefFromContentLocation(response.URL, part.Header.Get("Content-Location"))
			if err != nil {
				return nil, err
			}
		}
		data, err := readResponsePart(part, limits)
		_ = part.Close()
		if err != nil {
			return nil, responsePartReadError(err)
		}
		dataset, err := c.datasetFromDICOMXML(ctx, data, maximumBytes, syntax)
		if err != nil {
			return nil, err
		}
		if requireContentLocation {
			datasetRef := InstanceRef{
				StudyInstanceUID:  dicomjson.ElementString(dataset, "0020000D"),
				SeriesInstanceUID: dicomjson.ElementString(dataset, "0020000E"),
				SOPInstanceUID:    dicomjson.ElementString(dataset, "00080018"),
			}
			if datasetRef != locationRef {
				return nil, fmt.Errorf("DICOM XML metadata Content-Location does not match the dataset instance UIDs")
			}
		}
		datasets = append(datasets, dataset)
	}
	if len(datasets) == 0 {
		if requireContentLocation {
			return nil, nil
		}
		return nil, fmt.Errorf("multipart DICOM XML response contains no parts")
	}
	if !requireContentLocation && len(datasets) == 1 && len(datasets[0]) == 0 {
		return nil, nil
	}
	return datasets, nil
}

func (c Client) datasetFromDICOMXML(ctx context.Context, data []byte, maximumBytes int64, syntax transfer.Syntax) (Dataset, error) {
	valueLimit := maximumBytes
	if valueLimit > int64(^uint(0)>>1) {
		valueLimit = int64(^uint(0) >> 1)
	}
	obj, err := dicomxml.UnmarshalContext(ctx, data, std.Dictionary, dicomxml.UnmarshalOptions{
		TransferSyntax: syntax,
		Limits: dicomxml.Limits{
			MaxXMLBytes:          maximumBytes,
			MaxValueBytes:        int(valueLimit),
			MaxInlineBinaryBytes: maximumBytes,
			MaxTotalBinaryBytes:  maximumBytes,
		},
	})
	if err != nil {
		return nil, err
	}
	encoded, err := dicomjson.MarshalContext(ctx, obj, dicomjson.Options{
		OmitGroupLength: true,
		Limits: dicomjson.Limits{
			MaxJSONBytes:         maximumBytes,
			MaxInlineBinaryBytes: maximumBytes,
			MaxTotalBinaryBytes:  maximumBytes,
			MaxSequenceDepth:     64,
			MaxElements:          defaultMaxMetadataParts * 100,
			MaxSequenceItems:     defaultMaxMetadataParts * 100,
		},
	})
	if err != nil {
		return nil, err
	}
	var dataset Dataset
	if err := decodeJSON(encoded, &dataset); err != nil {
		return nil, err
	}
	return dataset, nil
}

func metadataTransferSyntax(params map[string]string) (transfer.Syntax, error) {
	uid := transfer.NormalizeUID(strings.TrimSpace(params["transfer-syntax"]))
	if uid == "" {
		return transfer.Syntax{}, nil
	}
	if !webServiceTransferSyntaxAllowed(uid) {
		return transfer.Syntax{}, fmt.Errorf("unsupported metadata transfer syntax %q", uid)
	}
	syntax, ok := transfer.DefaultRegistry.Get(uid)
	if !ok {
		return transfer.Syntax{}, fmt.Errorf("unsupported metadata transfer syntax %q", uid)
	}
	return syntax, nil
}

func metadataCharsetSupported(params map[string]string) bool {
	charset := strings.TrimSpace(params["charset"])
	return charset == "" || strings.EqualFold(charset, "utf-8")
}

func metadataParametersSupported(params map[string]string, allowed ...string) bool {
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	for name := range params {
		if !set[strings.ToLower(name)] {
			return false
		}
	}
	return true
}
