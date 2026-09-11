package dicomweb

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

func (s *Server) validateUID(uid string) error {
	if len(uid) > s.limits.MaxUIDBytes || !core.IsValidUID(uid) {
		return ErrInvalidRequest
	}
	return nil
}

func (s *Server) instanceLocation(ref InstanceRef) (string, error) {
	for _, uid := range []string{ref.StudyInstanceUID, ref.SeriesInstanceUID, ref.SOPInstanceUID} {
		if err := s.validateUID(uid); err != nil {
			return "", err
		}
	}
	return s.servicePath("/studies/" + url.PathEscape(ref.StudyInstanceUID) + "/series/" + url.PathEscape(ref.SeriesInstanceUID) + "/instances/" + url.PathEscape(ref.SOPInstanceUID)), nil
}

func (s *Server) frameLocation(ref InstanceRef, frame int) string {
	location, _ := s.instanceLocation(ref)
	return location + "/frames/" + strconv.Itoa(frame)
}

func parseFrames(value string, maximum int) ([]int, error) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > maximum {
		return nil, ErrResourceLimit
	}
	frames := make([]int, len(parts))
	seen := make(map[int]bool, len(parts))
	for index, raw := range parts {
		frame, err := strconv.ParseInt(raw, 10, 31)
		if err != nil || frame <= 0 || seen[int(frame)] {
			return nil, ErrInvalidRequest
		}
		seen[int(frame)] = true
		frames[index] = int(frame)
	}
	return frames, nil
}
