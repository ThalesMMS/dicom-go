package dicomweb

import (
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

func (s *Server) serveRendered(w *serverResponseWriter, r *http.Request, thumbnail bool) (int, error) {
	renderer := s.options.Renderer
	if renderer == nil {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}

	request, warning, err := s.renderRequest(r, thumbnail)
	if err != nil {
		return 0, err
	}
	if warning {
		w.Header().Set("Warning", `299 dicomweb "Some annotation values are not supported"`)
	}
	requireMultipart := request.Level == RenderLevelStudy || request.Level == RenderLevelSeries || (request.Level == RenderLevelFrames && len(request.Frames) > 1)
	if thumbnail {
		requireMultipart = false
		if !hasDirectRenderedPreference(request.Accept) {
			return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
		}
	} else if requireMultipart && !hasMultipartRenderedPreference(request.Accept) {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	spool, err := newResponseSpool(s.options.SpoolDirectory, s.limits.MaxResponseBytes)
	if err != nil {
		return 0, err
	}
	spoolClosed := false
	defer func() {
		if !spoolClosed {
			_ = spool.Close()
		}
	}()

	count := 0
	var renderedPixels int64
	responseMultipart := requireMultipart
	var writer *multipart.Writer
	partMediaType := ""
	responseContentType := ""
	responseContentLocation := ""
	err = safeRender(renderer, r.Context(), request, func(part RenderedPart) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if part.Reader == nil {
			return ErrBackend
		}
		return withReaderClosed(part.Reader, func() error {
			if part.Size < -1 || part.Size > s.limits.MaxPartBytes || count >= s.limits.MaxFrames {
				return ErrResourceLimit
			}
			if thumbnail && count > 0 {
				return ErrBackend
			}
			contentType, err := normalizedRenderedContentType(part.ContentType, thumbnail)
			if err != nil {
				return ErrBackend
			}
			if request.QualitySet && !renderedMediaSupportsQuality(contentType) {
				return ErrInvalidRequest
			}
			if strings.HasPrefix(contentType, "image/") {
				if part.Width <= 0 || part.Height <= 0 || int64(part.Width) > request.MaxPixels/int64(part.Height) {
					return ErrResourceLimit
				}
				pixels := int64(part.Width) * int64(part.Height)
				if pixels > request.MaxPixels-renderedPixels {
					return ErrResourceLimit
				}
				if request.Viewport != nil && (part.Width > request.Viewport.Width || part.Height > request.Viewport.Height) {
					return ErrBackend
				}
				renderedPixels += pixels
			}
			if request.Level == RenderLevelFrames && !thumbnail && (count >= len(request.Frames) || part.FrameNumber != request.Frames[count]) {
				return ErrBackend
			}
			preference, ok := matchingMediaPreference(request.Accept, contentType, "", requireMultipart)
			if !ok {
				return ErrUnsupported
			}
			if count == 0 && !thumbnail && !requireMultipart {
				responseMultipart = preference.Multipart
			}
			if thumbnail && preference.Multipart {
				return ErrUnsupported
			}
			location, err := s.renderedPartLocation(r, request, part)
			if err != nil {
				return ErrBackend
			}
			if responseMultipart {
				if partMediaType != "" && partMediaType != contentType {
					return ErrBackend
				}
				if writer == nil {
					writer = multipart.NewWriter(spool)
					partMediaType = contentType
					responseContentType = `multipart/related; type="` + contentType + `"; boundary=` + writer.Boundary()
				}
				header := textproto.MIMEHeader{}
				header.Set("Content-Type", contentType)
				header.Set("Content-Location", location)
				destination, err := writer.CreatePart(header)
				if err != nil {
					return err
				}
				if _, err := copyBounded(r.Context(), destination, part.Reader, s.limits.MaxPartBytes); err != nil {
					return err
				}
			} else {
				if count != 0 {
					return ErrBackend
				}
				responseContentType = contentType
				responseContentLocation = location
				if _, err := copyBounded(r.Context(), spool, part.Reader, minRenderLimit(s.limits.MaxPartBytes, request.MaxOutputBytes)); err != nil {
					return err
				}
			}
			count++
			return nil
		})
	})
	if err != nil {
		if count == 0 {
			return 0, classifyBackendError(err)
		}
		return count, err
	}
	if count == 0 {
		return 0, ErrNotFound
	}
	if request.Level == RenderLevelFrames && !thumbnail && count != len(request.Frames) {
		return count, ErrBackend
	}
	if writer != nil {
		if err := writer.Close(); err != nil {
			return count, err
		}
	}
	if err := spool.Rewind(); err != nil {
		return count, err
	}
	w.Header().Set("Content-Type", responseContentType)
	if responseContentLocation != "" {
		w.Header().Set("Content-Location", responseContentLocation)
	}
	if _, err := copyBounded(r.Context(), w, spool.file, s.limits.MaxResponseBytes); err != nil {
		return count, err
	}
	closeErr := spool.Close()
	spoolClosed = true
	if closeErr != nil {
		return count, closeErr
	}
	return count, nil
}

func (s *Server) renderRequest(r *http.Request, thumbnail bool) (RenderRequest, bool, error) {
	parts := resourceParts(r.URL.Path)
	request := RenderRequest{Thumbnail: thumbnail, MaxPixels: s.limits.MaxRenderedPixels, MaxOutputBytes: s.limits.MaxResponseBytes}
	switch len(parts) {
	case 3:
		request.Level = RenderLevelStudy
		request.Ref.StudyInstanceUID = parts[1]
	case 5:
		request.Level = RenderLevelSeries
		request.Ref.StudyInstanceUID = parts[1]
		request.Ref.SeriesInstanceUID = parts[3]
	case 7:
		request.Level = RenderLevelInstance
		request.Ref = InstanceRef{StudyInstanceUID: parts[1], SeriesInstanceUID: parts[3], SOPInstanceUID: parts[5]}
	case 9:
		request.Level = RenderLevelFrames
		request.Ref = InstanceRef{StudyInstanceUID: parts[1], SeriesInstanceUID: parts[3], SOPInstanceUID: parts[5]}
		frames, err := parseFrames(parts[7], s.limits.MaxFrames)
		if err != nil {
			return RenderRequest{}, false, err
		}
		request.Frames = frames
	default:
		return RenderRequest{}, false, ErrInvalidRequest
	}
	for _, uid := range []string{request.Ref.StudyInstanceUID, request.Ref.SeriesInstanceUID, request.Ref.SOPInstanceUID} {
		if uid != "" {
			if err := s.validateUID(uid); err != nil {
				return RenderRequest{}, false, err
			}
		}
	}

	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return RenderRequest{}, false, ErrInvalidRequest
	}
	if len(query) > s.limits.MaxQueryFields {
		return RenderRequest{}, false, ErrResourceLimit
	}
	for _, values := range query {
		for _, value := range values {
			if len(value) > s.limits.MaxQueryValueBytes {
				return RenderRequest{}, false, ErrResourceLimit
			}
		}
	}
	for _, name := range []string{"accept", "annotation", "quality", "viewport", "window", "iccprofile"} {
		if len(query[name]) > 1 {
			return RenderRequest{}, false, ErrInvalidRequest
		}
	}

	preferences, err := renderedPreferences(requestAcceptHeader(r), query["accept"], thumbnail)
	if err != nil {
		return RenderRequest{}, false, err
	}
	request.Accept = preferences
	if raw, ok := singleQueryValue(query, "viewport"); ok {
		viewport, err := s.parseRenderViewport(raw, thumbnail)
		if err != nil {
			return RenderRequest{}, false, err
		}
		request.Viewport = viewport
		items := int64(1)
		if request.Level == RenderLevelFrames && !thumbnail {
			items = int64(len(request.Frames))
		}
		pixels := int64(viewport.Width) * int64(viewport.Height)
		if items > 0 && pixels > request.MaxPixels/items {
			return RenderRequest{}, false, newServerStatusError(http.StatusBadRequest, "invalid_viewport", ErrResourceLimit)
		}
	}
	if !thumbnail {
		if raw, ok := singleQueryValue(query, "quality"); ok {
			quality, err := strconv.Atoi(raw)
			if err != nil || quality < 1 || quality > 100 {
				return RenderRequest{}, false, ErrInvalidRequest
			}
			request.Quality, request.QualitySet = quality, true
			if !preferencesSupportQuality(preferences) {
				return RenderRequest{}, false, ErrInvalidRequest
			}
		}
		if raw, ok := singleQueryValue(query, "window"); ok {
			window, err := parseRenderWindow(raw)
			if err != nil {
				return RenderRequest{}, false, err
			}
			request.Window = window
		}
		if raw, ok := singleQueryValue(query, "iccprofile"); ok {
			switch raw {
			case "no", "yes", "srgb", "adobergb", "rommrgb", "displayp3":
				request.ICCProfile = raw
			default:
				return RenderRequest{}, false, ErrInvalidRequest
			}
		}
		annotations, warning, err := parseRenderAnnotations(query["annotation"])
		if err != nil {
			return RenderRequest{}, false, err
		}
		request.Annotations = annotations
		return request, warning, nil
	}
	return request, false, nil
}

func renderedPreferences(header string, queryAccept []string, thumbnail bool) ([]MediaPreference, error) {
	parsedHeader := parseAcceptHeader(header)
	if len(parsedHeader) != len(splitAcceptValues(header)) {
		return nil, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	headerDICOM, headerRendered := false, false
	for _, preference := range parsedHeader {
		if preference.Quality <= 0 {
			continue
		}
		if preference.MediaType == "application/dicom" {
			headerDICOM = true
		} else if isRenderedMediaType(preference.MediaType, thumbnail) || preference.MediaType == "*/*" || preference.MediaType == "image/*" {
			headerRendered = true
		}
	}
	if headerDICOM && headerRendered {
		return nil, ErrInvalidRequest
	}
	headerPreferences := concreteRenderedPreferences(parsedHeader, thumbnail)
	compatibleHeader := make([]MediaPreference, 0, len(parsedHeader))
	for _, preference := range parsedHeader {
		if preference.Quality > 0 && preference.TransferSyntaxUID == "" {
			compatibleHeader = append(compatibleHeader, preference)
		}
	}
	if len(queryAccept) == 0 {
		if len(headerPreferences) == 0 {
			return nil, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
		}
		return headerPreferences, nil
	}
	if len(queryAccept) != 1 || strings.TrimSpace(queryAccept[0]) == "" {
		return nil, ErrInvalidRequest
	}
	rawValues := splitAcceptValues(queryAccept[0])
	queryPreferences := parseAcceptHeader(queryAccept[0])
	if len(queryPreferences) != len(rawValues) || len(queryPreferences) == 0 {
		return nil, ErrInvalidRequest
	}
	hasDICOM, hasRendered := false, false
	for _, preference := range queryPreferences {
		if preference.MediaType == "application/dicom" {
			hasDICOM = true
			continue
		}
		if strings.Contains(preference.MediaType, "*") || preference.TransferSyntaxUID != "" || !isRenderedMediaType(preference.MediaType, thumbnail) {
			return nil, ErrInvalidRequest
		}
		hasRendered = true
		if _, ok := matchingMediaPreference(compatibleHeader, preference.MediaType, "", preference.Multipart); !ok && strings.TrimSpace(header) != "" {
			return nil, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
		}
	}
	if hasDICOM && hasRendered {
		return nil, ErrInvalidRequest
	}
	if hasDICOM {
		return nil, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	return queryPreferences, nil
}

func concreteRenderedPreferences(preferences []MediaPreference, thumbnail bool) []MediaPreference {
	result := make([]MediaPreference, 0, len(preferences))
	for _, preference := range preferences {
		if preference.Quality <= 0 || preference.TransferSyntaxUID != "" {
			continue
		}
		if preference.MediaType == "*/*" || preference.MediaType == "image/*" {
			preference.MediaType = "image/jpeg"
		}
		if isRenderedMediaType(preference.MediaType, thumbnail) {
			result = append(result, preference)
		}
	}
	return result
}

func isRenderedMediaType(mediaType string, thumbnail bool) bool {
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "image/") {
		switch mediaType {
		case "image/jpeg", "image/png", "image/gif", "image/jp2", "image/jph", "image/jphc", "image/jxl":
			return true
		}
	}
	if thumbnail {
		return false
	}
	switch mediaType {
	case "video/mpeg", "video/mp4", "video/h265", "text/html", "text/plain", "application/pdf":
		return true
	default:
		return false
	}
}

func normalizedRenderedContentType(contentType string, thumbnail bool) (string, error) {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || len(params) != 0 || !isRenderedMediaType(mediaType, thumbnail) {
		return "", ErrUnsupported
	}
	return strings.ToLower(mediaType), nil
}

func hasDirectRenderedPreference(preferences []MediaPreference) bool {
	for _, preference := range preferences {
		if preference.Quality > 0 && !preference.Multipart {
			return true
		}
	}
	return false
}

func hasMultipartRenderedPreference(preferences []MediaPreference) bool {
	for _, preference := range preferences {
		if preference.Quality > 0 && preference.Multipart {
			return true
		}
	}
	return false
}

func singleQueryValue(query url.Values, name string) (string, bool) {
	values, ok := query[name]
	if !ok {
		return "", false
	}
	if len(values) != 1 || values[0] == "" {
		return "", true
	}
	return values[0], true
}

func (s *Server) parseRenderViewport(raw string, thumbnail bool) (*RenderViewport, error) {
	parts := strings.Split(raw, ",")
	if len(parts) < 2 || len(parts) > 6 || (thumbnail && len(parts) != 2) || (len(parts) > 2 && parts[len(parts)-1] == "") {
		return nil, ErrInvalidRequest
	}
	width, err := strconv.ParseInt(parts[0], 10, 31)
	if err != nil || width <= 0 {
		return nil, ErrInvalidRequest
	}
	height, err := strconv.ParseInt(parts[1], 10, 31)
	if err != nil || height <= 0 || uint64(width) > uint64(s.limits.MaxRenderedPixels)/uint64(height) {
		return nil, newServerStatusError(http.StatusBadRequest, "invalid_viewport", ErrResourceLimit)
	}
	viewport := &RenderViewport{Width: int(width), Height: int(height)}
	targets := []**float64{&viewport.SourceX, &viewport.SourceY, &viewport.SourceWidth, &viewport.SourceHeight}
	for index := 2; index < len(parts); index++ {
		if parts[index] == "" {
			continue
		}
		value, err := strconv.ParseFloat(parts[index], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > float64(s.limits.MaxRenderedPixels) || (index >= 4 && value == 0) {
			return nil, ErrInvalidRequest
		}
		copy := value
		*targets[index-2] = &copy
	}
	return viewport, nil
}

func parseRenderWindow(raw string) (*RenderWindow, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return nil, ErrInvalidRequest
	}
	center, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || math.IsNaN(center) || math.IsInf(center, 0) {
		return nil, ErrInvalidRequest
	}
	width, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || math.IsNaN(width) || math.IsInf(width, 0) || width <= 0 {
		return nil, ErrInvalidRequest
	}
	if parts[2] != "linear" && parts[2] != "linear-exact" && parts[2] != "sigmoid" {
		return nil, ErrInvalidRequest
	}
	return &RenderWindow{Center: center, Width: width, Function: parts[2]}, nil
}

func parseRenderAnnotations(values []string) ([]string, bool, error) {
	if len(values) == 0 {
		return nil, false, nil
	}
	if len(values) != 1 || values[0] == "" {
		return nil, false, ErrInvalidRequest
	}
	seen := map[string]bool{}
	result := make([]string, 0, 2)
	warning := false
	for _, value := range strings.Split(values[0], ",") {
		if value == "" || !isRenderKeyword(value) {
			return nil, false, ErrInvalidRequest
		}
		if value != "patient" && value != "technique" {
			warning = true
			continue
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, warning, nil
}

func isRenderKeyword(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func preferencesSupportQuality(preferences []MediaPreference) bool {
	for _, preference := range preferences {
		if renderedMediaSupportsQuality(preference.MediaType) {
			return true
		}
	}
	return false
}

func renderedMediaSupportsQuality(mediaType string) bool {
	mediaType = strings.ToLower(mediaType)
	return mediaType == "image/jpeg" || mediaType == "image/jp2" || mediaType == "image/jph" || mediaType == "image/jphc" || mediaType == "image/jxl" || strings.HasPrefix(mediaType, "video/")
}

func minRenderLimit(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func (s *Server) renderedPartLocation(r *http.Request, request RenderRequest, part RenderedPart) (string, error) {
	if request.Thumbnail {
		return s.servicePath(r.URL.EscapedPath()), nil
	}
	ref := part.Ref
	if request.Level == RenderLevelInstance || request.Level == RenderLevelFrames {
		if ref == (InstanceRef{}) {
			ref = request.Ref
		}
	}
	switch request.Level {
	case RenderLevelStudy:
		if ref.StudyInstanceUID != request.Ref.StudyInstanceUID || ref.SeriesInstanceUID == "" || ref.SOPInstanceUID == "" {
			return "", ErrBackend
		}
	case RenderLevelSeries:
		if ref.StudyInstanceUID != request.Ref.StudyInstanceUID || ref.SeriesInstanceUID != request.Ref.SeriesInstanceUID || ref.SOPInstanceUID == "" {
			return "", ErrBackend
		}
	case RenderLevelInstance, RenderLevelFrames:
		if ref != request.Ref {
			return "", ErrBackend
		}
	}
	if request.Level == RenderLevelFrames {
		if part.FrameNumber <= 0 {
			return "", ErrBackend
		}
		index := 0
		// The callback is synchronous and handlers require requested frame order.
		for index < len(request.Frames) && request.Frames[index] != part.FrameNumber {
			index++
		}
		if index == len(request.Frames) {
			return "", ErrBackend
		}
	}
	if part.FrameNumber > 0 {
		return s.frameLocation(ref, part.FrameNumber) + "/rendered", nil
	}
	location, err := s.instanceLocation(ref)
	if err != nil {
		return "", err
	}
	return location + "/rendered", nil
}
