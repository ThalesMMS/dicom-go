package dicomweb

import (
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

func (s *Server) serveMetadata(w *serverResponseWriter, r *http.Request) (int, error) {
	backend, ok := s.options.Backend.(MetadataBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	representation, ok := selectMetadataRepresentation(requestAcceptHeader(r))
	if !ok {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	w.Header().Set("Vary", "Accept")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	request, err := s.metadataRequest(r.URL.Path)
	if err != nil {
		return 0, err
	}
	spool, err := newDatasetResponseSpool(s.options.SpoolDirectory, s.limits.MaxResponseBytes, representation)
	if err != nil {
		return 0, ErrBackend
	}
	spoolClosed := false
	defer func() {
		if !spoolClosed {
			_ = spool.Close()
		}
	}()
	err = safeMetadata(backend, r.Context(), request, func(dataset Dataset) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if spool.count >= s.limits.MaxResults {
			return ErrResourceLimit
		}
		location := ""
		if representation == metadataRepresentationXML {
			location, err = s.metadataContentLocation(request, dataset)
			if err != nil {
				return err
			}
		}
		return spool.Append(r.Context(), s, dataset, location)
	})
	if err != nil {
		return 0, classifyBackendError(err)
	}
	if err := spool.Finalize(r.Context(), s, false); err != nil {
		return 0, err
	}
	w.Header().Set("Content-Type", spool.contentType)
	_, err = copyBounded(r.Context(), w, spool.Reader(), s.limits.MaxResponseBytes)
	closeErr := spool.Close()
	spoolClosed = true
	if err != nil {
		return spool.count, err
	}
	return spool.count, closeErr
}

func (s *Server) metadataRequest(path string) (MetadataRequest, error) {
	parts := resourceParts(path)
	request := MetadataRequest{Level: MetadataLevelStudy}
	if len(parts) >= 2 {
		request.StudyInstanceUID = parts[1]
	}
	if len(parts) >= 4 {
		request.Level = MetadataLevelSeries
		request.SeriesInstanceUID = parts[3]
	}
	if len(parts) >= 6 {
		request.Level = MetadataLevelInstance
		request.SOPInstanceUID = parts[5]
	}
	for _, uid := range []string{request.StudyInstanceUID, request.SeriesInstanceUID, request.SOPInstanceUID} {
		if uid != "" {
			if err := s.validateUID(uid); err != nil {
				return MetadataRequest{}, err
			}
		}
	}
	return request, nil
}

func (s *Server) serveRetrieve(w *serverResponseWriter, r *http.Request, operation Operation) (int, error) {
	backend, ok := s.options.Backend.(RetrieveBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	request, err := s.retrieveRequest(r, operation)
	if err != nil {
		return 0, err
	}
	var writer *multipart.Writer
	responseMultipart := request.Level != RetrieveLevelInstance
	count := 0
	err = safeRetrieve(backend, r.Context(), request, func(part RetrievePart) error {
		if part.Reader == nil {
			return ErrBackend
		}
		return withReaderClosed(part.Reader, func() error {
			if err := r.Context().Err(); err != nil {
				return err
			}
			if count >= s.limits.MaxResults || part.Size < -1 || part.Size > s.limits.MaxPartBytes {
				return ErrResourceLimit
			}
			if request.Level == RetrieveLevelInstance && count > 0 {
				return ErrBackend
			}
			if part.TransferSyntaxUID != "" {
				if err := s.validateUID(part.TransferSyntaxUID); err != nil {
					return ErrBackend
				}
				if !webServiceTransferSyntaxAllowed(part.TransferSyntaxUID) {
					return ErrBackend
				}
			}
			contentType, err := normalizedDICOMPartContentType(part.ContentType, part.TransferSyntaxUID)
			if err != nil {
				return ErrUnsupported
			}
			preference, ok := matchingMediaPreference(request.Accept, contentType, part.TransferSyntaxUID, responseMultipart)
			if !ok {
				return ErrUnsupported
			}
			if request.Level == RetrieveLevelInstance && count == 0 {
				responseMultipart = preference.Multipart
			}
			ref := part.Ref
			if request.Level == RetrieveLevelInstance && ref == (InstanceRef{}) {
				ref = InstanceRef{StudyInstanceUID: request.StudyInstanceUID, SeriesInstanceUID: request.SeriesInstanceUID, SOPInstanceUID: request.SOPInstanceUID}
			}
			if !retrieveRefMatches(request, ref) {
				return ErrBackend
			}
			location, err := s.instanceLocation(ref)
			if err != nil {
				return ErrBackend
			}
			var destination io.Writer = w
			if responseMultipart {
				if writer == nil {
					writer = multipart.NewWriter(w)
					w.Header().Set("Content-Type", `multipart/related; type="application/dicom"; boundary=`+writer.Boundary())
				}
				header := textproto.MIMEHeader{}
				header.Set("Content-Type", contentType)
				header.Set("Content-Location", location)
				destination, err = writer.CreatePart(header)
				if err != nil {
					return err
				}
			} else {
				if count != 0 {
					return ErrBackend
				}
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Content-Location", location)
			}
			if _, err := copyBounded(r.Context(), destination, part.Reader, s.limits.MaxPartBytes); err != nil {
				return err
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
	if writer != nil {
		if err := writer.Close(); err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Server) retrieveRequest(r *http.Request, operation Operation) (RetrieveRequest, error) {
	parts := resourceParts(r.URL.Path)
	request := RetrieveRequest{}
	switch operation {
	case OperationRetrieveStudy:
		request.Level = RetrieveLevelStudy
	case OperationRetrieveSeries:
		request.Level = RetrieveLevelSeries
	case OperationRetrieveInstance:
		request.Level = RetrieveLevelInstance
	}
	acceptHeader := requestAcceptHeader(r)
	request.Accept = parseMediaPreferences(acceptHeader, "application/dicom")
	if strings.TrimSpace(acceptHeader) == "" {
		request.Accept[0].Multipart = true
	}
	if len(request.Accept) == 0 || (request.Level != RetrieveLevelInstance && !hasMultipartDICOMPreference(request.Accept)) {
		return RetrieveRequest{}, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	if len(parts) >= 2 {
		request.StudyInstanceUID = parts[1]
	}
	if len(parts) >= 4 {
		request.SeriesInstanceUID = parts[3]
	}
	if len(parts) >= 6 {
		request.SOPInstanceUID = parts[5]
	}
	for _, uid := range []string{request.StudyInstanceUID, request.SeriesInstanceUID, request.SOPInstanceUID} {
		if uid != "" {
			if err := s.validateUID(uid); err != nil {
				return RetrieveRequest{}, err
			}
		}
	}
	return request, nil
}

func retrieveRefMatches(request RetrieveRequest, ref InstanceRef) bool {
	if ref.StudyInstanceUID != request.StudyInstanceUID {
		return false
	}
	if request.Level == RetrieveLevelStudy {
		return ref.SeriesInstanceUID != "" && ref.SOPInstanceUID != ""
	}
	if ref.SeriesInstanceUID != request.SeriesInstanceUID {
		return false
	}
	return request.Level != RetrieveLevelInstance || ref.SOPInstanceUID == request.SOPInstanceUID
}

func hasMultipartDICOMPreference(preferences []MediaPreference) bool {
	for _, preference := range preferences {
		if preference.Quality > 0 && ((preference.Multipart && preference.MediaType == "application/dicom") || preference.MediaType == "*/*") {
			return true
		}
	}
	return false
}

func hasMultipartFramePreference(preferences []MediaPreference) bool {
	for _, preference := range preferences {
		if preference.Quality <= 0 {
			continue
		}
		if preference.MediaType == "*/*" || (preference.Multipart && (isFrameMediaType(preference.MediaType) || preference.MediaType == "image/*" || preference.MediaType == "video/*")) {
			return true
		}
	}
	return false
}

func (s *Server) serveFrames(w *serverResponseWriter, r *http.Request) (int, error) {
	backend, ok := s.options.Backend.(FrameBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	parts := resourceParts(r.URL.Path)
	ref := InstanceRef{StudyInstanceUID: parts[1], SeriesInstanceUID: parts[3], SOPInstanceUID: parts[5]}
	for _, uid := range []string{ref.StudyInstanceUID, ref.SeriesInstanceUID, ref.SOPInstanceUID} {
		if err := s.validateUID(uid); err != nil {
			return 0, err
		}
	}
	frames, err := parseFrames(parts[7], s.limits.MaxFrames)
	if err != nil {
		return 0, err
	}
	acceptHeader := requestAcceptHeader(r)
	preferences := parseMediaPreferences(acceptHeader, "application/octet-stream")
	if strings.TrimSpace(acceptHeader) == "" {
		preferences[0].Multipart = true
	}
	if len(preferences) == 0 || !hasMultipartFramePreference(preferences) {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	request := FrameRequest{Ref: ref, Frames: frames, Accept: preferences}
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", `multipart/related; type="application/octet-stream"; boundary=`+writer.Boundary())
	count := 0
	err = safeFrames(backend, r.Context(), request, func(part FramePartStream) error {
		if part.Reader == nil {
			return ErrBackend
		}
		return withReaderClosed(part.Reader, func() error {
			if part.Size < -1 || part.Size > s.limits.MaxPartBytes {
				return ErrResourceLimit
			}
			if count >= len(frames) || part.FrameNumber != frames[count] {
				return ErrBackend
			}
			if part.TransferSyntaxUID != "" {
				if err := s.validateUID(part.TransferSyntaxUID); err != nil {
					return ErrBackend
				}
			}
			contentType, err := normalizedFrameContentType(part.ContentType)
			if err != nil {
				return ErrBackend
			}
			if !frameMediaTypeMatchesTransferSyntax(contentType, part.TransferSyntaxUID) {
				return ErrBackend
			}
			if _, ok := matchingMediaPreference(preferences, contentType, part.TransferSyntaxUID, true); !ok {
				return ErrUnsupported
			}
			partMediaType := mediaTypeWithTransferSyntax(contentType, part.TransferSyntaxUID)
			if count == 0 {
				w.Header().Set("Content-Type", `multipart/related; type="`+contentType+`"; boundary=`+writer.Boundary())
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Type", partMediaType)
			header.Set("Content-Location", s.frameLocation(ref, part.FrameNumber))
			destination, err := writer.CreatePart(header)
			if err != nil {
				return err
			}
			if _, err := copyBounded(r.Context(), destination, part.Reader, s.limits.MaxPartBytes); err != nil {
				return err
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
	if count != len(frames) {
		return count, ErrBackend
	}
	if err := writer.Close(); err != nil {
		return count, err
	}
	return count, nil
}

func (s *Server) serveBulkData(w *serverResponseWriter, r *http.Request) (int, error) {
	backend, ok := s.options.Backend.(BulkDataBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	parts := resourceParts(r.URL.Path)
	if len(parts) < 2 {
		return 0, ErrInvalidRequest
	}
	token, err := bulkDataToken(r.URL.Path, r.URL.RawPath, "/bulkdata/")
	if err != nil {
		return 0, err
	}
	acceptHeader := requestAcceptHeader(r)
	request := BulkDataRequest{Token: token, Accept: parseMediaPreferences(acceptHeader, "application/octet-stream")}
	if strings.TrimSpace(acceptHeader) == "" {
		request.Accept[0].Multipart = true
	}
	part, err := safeBulkData(backend, r.Context(), request)
	if err != nil {
		return 0, classifyBackendError(err)
	}
	if part.Reader == nil {
		return 0, ErrBackend
	}
	err = withReaderClosed(part.Reader, func() error {
		if part.Size < -1 || part.Size > s.limits.MaxPartBytes {
			return ErrResourceLimit
		}
		contentType, contentErr := normalizedBulkContentType(part.ContentType)
		if contentErr != nil {
			return ErrBackend
		}
		preference, ok := matchingMediaPreference(request.Accept, contentType, "", false)
		if !ok {
			return newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
		}
		multipartResponse := preference.Multipart || preference.MediaType == "*/*"
		if !multipartResponse {
			w.Header().Set("Content-Type", contentType)
			_, copyErr := copyBounded(r.Context(), w, part.Reader, s.limits.MaxPartBytes)
			return copyErr
		}
		writer := multipart.NewWriter(w)
		w.Header().Set("Content-Type", `multipart/related; type="application/octet-stream"; boundary=`+writer.Boundary())
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", contentType)
		header.Set("Content-Location", s.servicePath(r.URL.EscapedPath()))
		destination, createErr := writer.CreatePart(header)
		if createErr != nil {
			return createErr
		}
		if _, copyErr := copyBounded(r.Context(), destination, part.Reader, s.limits.MaxPartBytes); copyErr != nil {
			return copyErr
		}
		return writer.Close()
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}
