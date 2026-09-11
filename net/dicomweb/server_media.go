package dicomweb

import (
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/transfer"
)

func acceptsDICOMJSON(header string) bool {
	if strings.TrimSpace(header) == "" {
		return true
	}
	preference, ok := bestMatchingMediaPreference(parseAcceptHeader(header), "application/dicom+json", "", false)
	return ok && preference.Quality > 0
}

type metadataRepresentation uint8

const (
	metadataRepresentationJSON metadataRepresentation = iota + 1
	metadataRepresentationXML
)

// selectMetadataRepresentation applies HTTP quality and specificity rules to
// the two PS3.18 metadata representations. JSON is the default on an exact
// tie, preserving existing behavior and the standard's default media type.
func selectMetadataRepresentation(header string) (metadataRepresentation, bool) {
	preferences := parseMetadataAcceptHeader(header)
	jsonPreference, jsonOK := bestMatchingMediaPreference(preferences, string(MetadataMediaTypeDICOMJSON), transfer.ExplicitVRLittleEndian.UID, false)
	xmlPreference, xmlOK := bestMatchingMediaPreference(preferences, string(MetadataMediaTypeDICOMXML), transfer.ExplicitVRLittleEndian.UID, true)
	jsonOK = jsonOK && jsonPreference.Quality > 0
	xmlOK = xmlOK && xmlPreference.Quality > 0
	jsonSpecificity, _ := mediaPreferenceSpecificity(jsonPreference, string(MetadataMediaTypeDICOMJSON), transfer.ExplicitVRLittleEndian.UID)
	xmlSpecificity, _ := mediaPreferenceSpecificity(xmlPreference, string(MetadataMediaTypeDICOMXML), transfer.ExplicitVRLittleEndian.UID)
	switch {
	case jsonOK && xmlOK && xmlPreference.Quality > jsonPreference.Quality:
		return metadataRepresentationXML, true
	case jsonOK && xmlOK && xmlPreference.Quality == jsonPreference.Quality && xmlSpecificity > jsonSpecificity:
		return metadataRepresentationXML, true
	case jsonOK:
		return metadataRepresentationJSON, true
	case xmlOK:
		return metadataRepresentationXML, true
	default:
		return 0, false
	}
}

func parseMetadataAcceptHeader(header string) []MediaPreference {
	var result []MediaPreference
	for _, raw := range splitAcceptValues(header) {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		quality := 1.0
		if rawQuality := params["q"]; rawQuality != "" {
			parsed, ok := parseAcceptQValue(rawQuality)
			if !ok {
				continue
			}
			quality = parsed
		}
		multipartRelated := strings.EqualFold(mediaType, "multipart/related")
		if multipartRelated {
			if !metadataParametersSupported(params, "boundary", "charset", "q", "start", "start-info", "transfer-syntax", "type") || params["type"] == "" {
				continue
			}
			mediaType = strings.TrimSpace(params["type"])
		} else if !metadataParametersSupported(params, "charset", "q", "transfer-syntax") {
			continue
		}
		if charset := strings.TrimSpace(params["charset"]); charset != "" && !strings.EqualFold(charset, "utf-8") {
			continue
		}
		result = append(result, MediaPreference{
			MediaType:         strings.ToLower(mediaType),
			TransferSyntaxUID: strings.TrimSpace(params["transfer-syntax"]),
			Quality:           quality,
			Multipart:         multipartRelated,
		})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Quality > result[j].Quality })
	return result
}

func parseMediaPreferences(header, defaultMediaType string) []MediaPreference {
	if strings.TrimSpace(header) == "" {
		return []MediaPreference{{MediaType: defaultMediaType, Quality: 1}}
	}
	return parseAcceptHeader(header)
}

func parseAcceptHeader(header string) []MediaPreference {
	var result []MediaPreference
	for _, raw := range splitAcceptValues(header) {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		quality := 1.0
		if rawQuality := params["q"]; rawQuality != "" {
			parsed, ok := parseAcceptQValue(rawQuality)
			if !ok {
				continue
			}
			quality = parsed
		}
		multipartRelated := strings.EqualFold(mediaType, "multipart/related")
		if multipartRelated && params["type"] != "" {
			mediaType = strings.Trim(params["type"], `"`)
		}
		result = append(result, MediaPreference{MediaType: strings.ToLower(mediaType), TransferSyntaxUID: strings.TrimSpace(params["transfer-syntax"]), Quality: quality, Multipart: multipartRelated})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Quality > result[j].Quality })
	return result
}

func requestAcceptHeader(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.Join(r.Header.Values("Accept"), ",")
}

func parseAcceptQValue(raw string) (float64, bool) {
	if raw == "0" || raw == "1" {
		parsed, err := strconv.ParseFloat(raw, 64)
		return parsed, err == nil
	}
	if len(raw) < 2 || len(raw) > 5 || raw[1] != '.' || (raw[0] != '0' && raw[0] != '1') {
		return 0, false
	}
	for index := 2; index < len(raw); index++ {
		if raw[index] < '0' || raw[index] > '9' || (raw[0] == '1' && raw[index] != '0') {
			return 0, false
		}
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	return parsed, err == nil
}

func splitAcceptValues(header string) []string {
	var values []string
	start := 0
	quoted := false
	for index, char := range header {
		if char == '"' {
			quoted = !quoted
		}
		if char == ',' && !quoted {
			values = append(values, header[start:index])
			start = index + 1
		}
	}
	values = append(values, header[start:])
	return values
}

func mediaPreferenceAccepts(preferences []MediaPreference, contentType, transferSyntaxUID string) bool {
	_, ok := matchingMediaPreference(preferences, contentType, transferSyntaxUID, false)
	return ok
}

func matchingMediaPreference(preferences []MediaPreference, contentType, transferSyntaxUID string, requireMultipart bool) (MediaPreference, bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return MediaPreference{}, false
	}
	if transferSyntaxUID == "" {
		transferSyntaxUID = params["transfer-syntax"]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if requireMultipart {
		preference, ok := bestMatchingMediaPreference(preferences, mediaType, transferSyntaxUID, true)
		return preference, ok && preference.Quality > 0
	}
	direct, directOK := bestMatchingMediaPreference(preferences, mediaType, transferSyntaxUID, false)
	multipartPreference, multipartOK := bestMatchingMediaPreference(preferences, mediaType, transferSyntaxUID, true)
	if !directOK || direct.Quality <= 0 {
		if multipartOK && multipartPreference.Quality > 0 {
			return multipartPreference, true
		}
		return MediaPreference{}, false
	}
	if multipartOK && multipartPreference.Quality > direct.Quality {
		return multipartPreference, true
	}
	return direct, true
}

func bestMatchingMediaPreference(preferences []MediaPreference, mediaType, transferSyntaxUID string, multipart bool) (MediaPreference, bool) {
	bestSpecificity := -1
	var best MediaPreference
	found := false
	for _, preference := range preferences {
		if preference.Multipart != multipart && !(multipart && !preference.Multipart && preference.MediaType == "*/*") {
			continue
		}
		specificity, ok := mediaPreferenceSpecificity(preference, mediaType, transferSyntaxUID)
		if !ok {
			continue
		}
		if specificity > bestSpecificity || (specificity == bestSpecificity && preference.Quality > best.Quality) {
			best = preference
			best.Multipart = multipart
			bestSpecificity = specificity
			found = true
		}
	}
	return best, found
}

func mediaPreferenceSpecificity(preference MediaPreference, mediaType, transferSyntaxUID string) (int, bool) {
	specificity := 0
	switch {
	case preference.MediaType == mediaType:
		specificity = 200
	case preference.MediaType == "*/*":
		specificity = 0
	case strings.HasSuffix(preference.MediaType, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(preference.MediaType, "*")):
		specificity = 100
	default:
		return 0, false
	}
	if preference.TransferSyntaxUID != "" {
		if preference.TransferSyntaxUID == "*" {
			specificity++
		} else {
			if preference.TransferSyntaxUID != transferSyntaxUID {
				return 0, false
			}
			specificity += 2
		}
	}
	return specificity, true
}

func normalizedDICOMPartContentType(contentType, transferSyntaxUID string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil && strings.TrimSpace(contentType) != "" {
		return "", err
	}
	if mediaType == "" {
		mediaType = "application/dicom"
	}
	if !strings.EqualFold(mediaType, "application/dicom") {
		return "", ErrUnsupported
	}
	return mediaTypeWithTransferSyntax("application/dicom", transferSyntaxUID), nil
}

func normalizedFrameContentType(contentType string) (string, error) {
	if strings.TrimSpace(contentType) == "" {
		return "application/octet-stream", nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || len(params) != 0 {
		return "", ErrUnsupported
	}
	mediaType = strings.ToLower(mediaType)
	if isFrameMediaType(mediaType) {
		return mediaType, nil
	}
	return "", ErrUnsupported
}

func isFrameMediaType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "application/octet-stream", "application/x-deflate", "image/jpeg", "image/jls", "image/jp2", "image/jpx", "image/jphc", "image/jxl", "image/dicom-rle", "video/mpeg", "video/mp4", "video/h265":
		return true
	default:
		return false
	}
}

func frameMediaTypeMatchesTransferSyntax(mediaType, transferSyntaxUID string) bool {
	uid := transfer.NormalizeUID(transferSyntaxUID)
	if uid == "" {
		return true
	}
	switch strings.ToLower(mediaType) {
	case "application/octet-stream":
		return uid == transfer.ExplicitVRLittleEndian.UID || uid == transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID
	case "application/x-deflate":
		return uid == transfer.DeflatedImageFrameCompression.UID
	case "image/jpeg":
		switch uid {
		case transfer.JPEGBaseline.UID, transfer.JPEGExtended.UID, transfer.JPEGLosslessNonHierarchical.UID, transfer.JPEGLosslessSV1.UID:
			return true
		}
	case "image/dicom-rle":
		return uid == transfer.RLELossless.UID
	case "image/jls":
		return transfer.IsJPEGLSTransferSyntax(uid)
	case "image/jp2":
		return uid == transfer.JPEG2000LosslessOnly.UID || uid == transfer.JPEG2000.UID
	case "image/jpx":
		return uid == transfer.JPEG2000Part2Lossless.UID || uid == transfer.JPEG2000Part2.UID
	case "image/jphc":
		return uid == transfer.HTJ2KLossless.UID || uid == transfer.HTJ2KLosslessRPCL.UID || uid == transfer.HTJ2K.UID
	case "image/jxl":
		return transfer.IsJPEGXLTransferSyntax(uid)
	case "video/mpeg":
		switch uid {
		case transfer.MPEG2MPML.UID, transfer.MPEG2MPMLF.UID, transfer.MPEG2MPHL.UID, transfer.MPEG2MPHLF.UID:
			return true
		}
	case "video/mp4":
		switch uid {
		case transfer.MPEG4HP41.UID, transfer.MPEG4HP41F.UID, transfer.MPEG4HP41BD.UID, transfer.MPEG4HP41BDF.UID,
			transfer.MPEG4HP422D.UID, transfer.MPEG4HP422DF.UID, transfer.MPEG4HP423D.UID, transfer.MPEG4HP423DF.UID,
			transfer.MPEG4HP42STEREO.UID, transfer.MPEG4HP42STEREOF.UID:
			return true
		}
	case "video/h265":
		return uid == transfer.HEVCMP51.UID || uid == transfer.HEVCM10P51.UID
	}
	return false
}

func webServiceTransferSyntaxAllowed(transferSyntaxUID string) bool {
	uid := transfer.NormalizeUID(transferSyntaxUID)
	if uid == "" || uid == transfer.ImplicitVRLittleEndian.UID || uid == transfer.ExplicitVRBigEndian.UID {
		return false
	}
	_, ok := transfer.DefaultRegistry.Get(uid)
	return ok
}

func normalizedBulkContentType(contentType string) (string, error) {
	if strings.TrimSpace(contentType) == "" {
		return "application/octet-stream", nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || len(params) != 0 || !strings.EqualFold(mediaType, "application/octet-stream") {
		return "", ErrUnsupported
	}
	return "application/octet-stream", nil
}

func mediaTypeWithTransferSyntax(mediaType, transferSyntaxUID string) string {
	if strings.TrimSpace(transferSyntaxUID) == "" {
		return mediaType
	}
	return mime.FormatMediaType(mediaType, map[string]string{"transfer-syntax": strings.TrimSpace(transferSyntaxUID)})
}
