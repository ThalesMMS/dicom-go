package dicomweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type stagedStorePart struct {
	path              string
	contentType       string
	transferSyntaxUID string
	size              int64
	digest            [32]byte
	ref               InstanceRef
	sopClassUID       string
	info              os.FileInfo
	preflightFailure  uint16
}

func (s *Server) serveStore(w *serverResponseWriter, r *http.Request) (count int, returnedErr error) {
	backend, ok := s.options.Backend.(StoreBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	if !acceptsDICOMJSON(requestAcceptHeader(r)) {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	parts := resourceParts(r.URL.Path)
	routeStudyUID := ""
	if len(parts) == 2 {
		routeStudyUID = parts[1]
		if err := s.validateUID(routeStudyUID); err != nil {
			return 0, err
		}
	}
	staged, err := s.stageStoreRequest(w, r, routeStudyUID)
	if err != nil {
		return 0, err
	}
	cleanupPending := true
	defer func() {
		if cleanupPending {
			if cleanupErr := cleanupStagedParts(staged); cleanupErr != nil && returnedErr == nil {
				count = 0
				returnedErr = cleanupErr
			}
		}
	}()
	outcomes := make([]StoreOutcome, 0, len(staged))
	stored := 0
	failed := 0
	warnings := 0
	createdStudies := make(map[string]struct{})
	for index, stagedPart := range staged {
		if err := r.Context().Err(); err != nil {
			return index, err
		}
		if stagedPart.preflightFailure != 0 {
			failed++
			outcomes = append(outcomes, StoreOutcome{
				Status: StoreStatusFailed, FailureReason: stagedPart.preflightFailure,
				Ref: stagedPart.ref, SOPClassUID: stagedPart.sopClassUID,
			})
			continue
		}
		reader, err := openStagedStorePart(r.Context(), stagedPart)
		if err != nil {
			return index, ErrBackend
		}
		outcome := safeStore(backend, r.Context(), StoreRequest{
			Index: index, RouteStudyUID: routeStudyUID, Ref: stagedPart.ref, SOPClassUID: stagedPart.sopClassUID, ContentType: stagedPart.contentType,
			TransferSyntaxUID: stagedPart.transferSyntaxUID, Policy: s.options.StorePolicy,
			Reader: reader, Size: stagedPart.size, SHA256: stagedPart.digest,
		})
		closeErr := reader.Close()
		if err := r.Context().Err(); err != nil {
			return index, err
		}
		if closeErr != nil && outcome.Err == nil {
			outcome.Err = closeErr
			outcome.Status = StoreStatusFailed
		}
		if outcome.Ref == (InstanceRef{}) {
			outcome.Ref = stagedPart.ref
		}
		if strings.TrimSpace(outcome.SOPClassUID) == "" {
			outcome.SOPClassUID = stagedPart.sopClassUID
		}
		if outcome.Ref != stagedPart.ref || outcome.SOPClassUID != stagedPart.sopClassUID {
			outcome.Status = StoreStatusFailed
			outcome.Err = ErrBackend
			if outcome.FailureReason == 0 {
				outcome.FailureReason = 0x0110
			}
		}
		if outcome.Err != nil {
			return index, classifyBackendError(outcome.Err)
		}
		if outcome.Status == StoreStatusStored || outcome.Status == StoreStatusWarning || outcome.Status == StoreStatusDuplicate {
			if s.validateUID(outcome.SOPClassUID) != nil || s.validateUID(outcome.Ref.StudyInstanceUID) != nil || s.validateUID(outcome.Ref.SeriesInstanceUID) != nil || s.validateUID(outcome.Ref.SOPInstanceUID) != nil {
				outcome.Status = StoreStatusFailed
				outcome.FailureReason = 0x0110
			}
		}
		switch outcome.Status {
		case StoreStatusStored:
			stored++
			createdStudies[outcome.Ref.StudyInstanceUID] = struct{}{}
		case StoreStatusWarning:
			stored++
			warnings++
			if outcome.WarningReason == 0 {
				outcome.Status = StoreStatusFailed
				outcome.FailureReason = 0x0110
				stored--
				warnings--
				failed++
			} else {
				createdStudies[outcome.Ref.StudyInstanceUID] = struct{}{}
			}
		case StoreStatusDuplicate:
			if s.options.StorePolicy == StorePolicyIdempotent {
				stored++
			} else {
				failed++
				outcome.Status = StoreStatusConflict
				if outcome.FailureReason == 0 {
					outcome.FailureReason = 0x0111
				}
			}
		case StoreStatusConflict:
			failed++
			if outcome.FailureReason == 0 {
				outcome.FailureReason = 0x0110
			}
		default:
			failed++
			outcome.Status = StoreStatusFailed
			if outcome.FailureReason == 0 {
				outcome.FailureReason = 0x0110
			}
		}
		outcomes = append(outcomes, outcome)
	}
	if err := cleanupStagedParts(staged); err != nil {
		return 0, err
	}
	cleanupPending = false
	status := http.StatusOK
	if stored > 0 && (failed > 0 || warnings > 0) {
		status = http.StatusAccepted
	}
	if stored == 0 {
		status = http.StatusConflict
	}
	dataset := storeResponseDataset(outcomes, routeStudyUID, s)
	data, err := json.Marshal(dataset)
	if err != nil {
		return 0, ErrBackend
	}
	if int64(len(data)) > s.limits.MaxResponseBytes {
		return 0, ErrResourceLimit
	}
	w.Header().Set("Content-Type", "application/dicom+json")
	if len(createdStudies) == 1 {
		for studyUID := range createdStudies {
			location := s.servicePath("/studies/" + url.PathEscape(studyUID))
			w.Header().Set("Location", location)
			w.Header().Set("Content-Location", location)
		}
	}
	w.WriteHeader(status)
	_, err = w.Write(data)
	return len(outcomes), err
}

func (s *Server) stageStoreRequest(w http.ResponseWriter, r *http.Request, routeStudyUID string) ([]stagedStorePart, error) {
	if len(r.Header.Values("Content-Type")) != 1 {
		return nil, newServerStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", ErrUnsupported)
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/related") || params["boundary"] == "" {
		return nil, newServerStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", ErrUnsupported)
	}
	rootType := strings.ToLower(strings.TrimSpace(strings.Trim(params["type"], `"`)))
	switch rootType {
	case "application/dicom+json", "application/dicom+xml":
		return s.stageStoreMetadataRequest(w, r, routeStudyUID, rootType, params)
	case "application/dicom":
		// Continue through the established PS3.10 staging path below.
	default:
		return nil, newServerStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", ErrUnsupported)
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.limits.MaxRequestBytes)
	reader := multipart.NewReader(r.Body, params["boundary"])
	var staged []stagedStorePart
	for {
		if err := r.Context().Err(); err != nil {
			return nil, cleanupStoreStaging(staged, nil, "", err)
		}
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, cleanupStoreStaging(staged, nil, "", classifyMultipartError(err))
		}
		if len(staged) >= s.limits.MaxParts {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", ErrResourceLimit)
		}
		partType, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || !strings.EqualFold(partType, "application/dicom") {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", newServerStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", ErrUnsupported))
		}
		if requestHeaderBytes(http.Header(part.Header)) > s.limits.MaxPartHeaderBytes {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", ErrResourceLimit)
		}
		temporary, err := os.CreateTemp(s.options.SpoolDirectory, ".dicomweb-stow-*")
		if err != nil {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", newServerStatusError(http.StatusServiceUnavailable, "spool_failure", ErrBackend))
		}
		temporaryPath := temporary.Name()
		hash := sha256.New()
		size, copyErr := copyBounded(r.Context(), io.MultiWriter(temporary, hash), part, s.limits.MaxPartBytes)
		partCloseErr := part.Close()
		if copyErr != nil || partCloseErr != nil {
			if copyErr != nil {
				return nil, cleanupStoreStaging(staged, temporary, temporaryPath, classifyMultipartError(copyErr))
			}
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		if _, err := temporary.Seek(0, io.SeekStart); err != nil {
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		info, err := temporary.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != size {
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		var digest [32]byte
		copy(digest[:], hash.Sum(nil))
		candidate := stagedStorePart{path: temporaryPath, contentType: "application/dicom", transferSyntaxUID: strings.TrimSpace(partParams["transfer-syntax"]), size: size, digest: digest, info: info}
		inspectErr := s.inspectStagedStorePart(r.Context(), &candidate, routeStudyUID, temporary)
		fileCloseErr := temporary.Close()
		if inspectErr != nil || fileCloseErr != nil {
			if inspectErr != nil {
				return nil, cleanupStoreStaging(staged, temporary, temporaryPath, inspectErr)
			}
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		staged = append(staged, candidate)
	}
	if len(staged) == 0 {
		return nil, ErrInvalidRequest
	}
	return staged, nil
}

func (s *Server) inspectStagedStorePart(ctx context.Context, staged *stagedStorePart, routeStudyUID string, reader io.ReadSeeker) error {
	options := index.DefaultOptions()
	options.Limits.MaxTotalBytes = s.limits.MaxDecodedPartBytes
	result, err := index.ReadSeeker(ctx, "staged-part", reader, staged.size, options)
	if err != nil {
		if errors.Is(err, index.ErrResourceLimit) {
			return ErrResourceLimit
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if errors.Is(err, transfer.ErrUnknownTransferSyntax) || errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
			staged.preflightFailure = 0xC122
			return nil
		}
		return ErrInvalidRequest
	}
	record := result.Record
	ref := InstanceRef{StudyInstanceUID: record.Study.InstanceUID, SeriesInstanceUID: record.Series.InstanceUID, SOPInstanceUID: record.Instance.SOPInstanceUID}
	for _, uid := range []string{ref.StudyInstanceUID, ref.SeriesInstanceUID, ref.SOPInstanceUID, record.Instance.SOPClassUID, record.FileMeta.TransferSyntaxUID} {
		if err := s.validateUID(uid); err != nil {
			return ErrInvalidRequest
		}
	}
	if record.FileMeta.MediaStorageSOPClassUID != record.Instance.SOPClassUID || record.FileMeta.MediaStorageSOPInstanceUID != ref.SOPInstanceUID {
		return ErrInvalidRequest
	}
	if staged.transferSyntaxUID != "" && staged.transferSyntaxUID != record.FileMeta.TransferSyntaxUID {
		return newServerStatusError(http.StatusUnsupportedMediaType, "transfer_syntax_mismatch", ErrUnsupported)
	}
	if routeStudyUID != "" && ref.StudyInstanceUID != routeStudyUID {
		return ErrConflict
	}
	staged.ref = ref
	staged.sopClassUID = record.Instance.SOPClassUID
	staged.transferSyntaxUID = record.FileMeta.TransferSyntaxUID
	if !webServiceTransferSyntaxAllowed(staged.transferSyntaxUID) {
		staged.preflightFailure = 0xC122
	}
	return nil
}

func openStagedStorePart(ctx context.Context, staged stagedStorePart) (*os.File, error) {
	file, err := os.Open(staged.path)
	if err != nil {
		return nil, ErrBackend
	}
	closeOnError := func() (*os.File, error) {
		_ = file.Close()
		return nil, ErrBackend
	}
	info, err := file.Stat()
	if err != nil || staged.info == nil || !info.Mode().IsRegular() || !os.SameFile(info, staged.info) || info.Size() != staged.size {
		return closeOnError()
	}
	hash := sha256.New()
	written, err := copyBounded(ctx, hash, file, staged.size)
	if err != nil || written != staged.size {
		return closeOnError()
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	if digest != staged.digest {
		return closeOnError()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return closeOnError()
	}
	return file, nil
}

func cleanupStagedParts(parts []stagedStorePart) error {
	failed := false
	for _, part := range parts {
		if err := os.Remove(part.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = true
		}
	}
	if failed {
		return ErrBackend
	}
	return nil
}

func cleanupStoreStaging(parts []stagedStorePart, temporary *os.File, temporaryPath string, preferred error) error {
	failed := false
	if temporary != nil {
		if err := temporary.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			failed = true
		}
	}
	if temporaryPath != "" {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = true
		}
	}
	if err := cleanupStagedParts(parts); err != nil {
		failed = true
	}
	if failed {
		return ErrBackend
	}
	return preferred
}

func storeResponseDataset(outcomes []StoreOutcome, routeStudyUID string, s *Server) Dataset {
	storedItems := make([]any, 0)
	failedItems := make([]any, 0)
	otherFailureItems := make([]any, 0)
	for _, outcome := range outcomes {
		item := Dataset{
			"00081150": {VR: "UI", Value: stringValues(outcome.SOPClassUID)},
			"00081155": {VR: "UI", Value: stringValues(outcome.Ref.SOPInstanceUID)},
		}
		if location, err := s.instanceLocation(outcome.Ref); err == nil {
			item["00081190"] = Element{VR: "UR", Value: []any{location}}
		}
		switch outcome.Status {
		case StoreStatusStored, StoreStatusDuplicate:
			storedItems = append(storedItems, item)
		case StoreStatusWarning:
			item["00081196"] = Element{VR: "US", Value: []any{outcome.WarningReason}}
			storedItems = append(storedItems, item)
		default:
			item["00081197"] = Element{VR: "US", Value: []any{outcome.FailureReason}}
			if strings.TrimSpace(outcome.SOPClassUID) == "" || strings.TrimSpace(outcome.Ref.SOPInstanceUID) == "" {
				otherFailureItems = append(otherFailureItems, Dataset{"00081197": item["00081197"]})
			} else {
				failedItems = append(failedItems, item)
			}
		}
	}
	response := Dataset{
		"00081190": {VR: "UR"},
	}
	if routeStudyUID != "" {
		response["00081190"] = Element{VR: "UR", Value: []any{s.servicePath("/studies/" + url.PathEscape(routeStudyUID))}}
	}
	if len(storedItems) > 0 {
		response["00081199"] = Element{VR: "SQ", Value: storedItems}
	}
	if len(failedItems) > 0 {
		response["00081198"] = Element{VR: "SQ", Value: failedItems}
	}
	if len(otherFailureItems) > 0 {
		response["0008119A"] = Element{VR: "SQ", Value: otherFailureItems}
	}
	return response
}

func stringValues(value string) []any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []any{strings.TrimSpace(value)}
}
