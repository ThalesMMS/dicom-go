package dicomweb

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// StudySearchURL builds the QIDO-RS study search URL.
func (e Endpoint) StudySearchURL(params url.Values) (*url.URL, error) {
	return e.serviceURL(e.QIDOPath, []string{"studies"}, params)
}

// SeriesSearchURL builds the QIDO-RS series search URL for a study.
func (e Endpoint) SeriesSearchURL(studyInstanceUID string, params url.Values) (*url.URL, error) {
	studyInstanceUID = strings.TrimSpace(studyInstanceUID)
	if studyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	return e.serviceURL(e.QIDOPath, []string{"studies", studyInstanceUID, "series"}, params)
}

// InstanceSearchURL builds the QIDO-RS instance search URL for a series.
func (e Endpoint) InstanceSearchURL(studyInstanceUID, seriesInstanceUID string, params url.Values) (*url.URL, error) {
	studyInstanceUID = strings.TrimSpace(studyInstanceUID)
	seriesInstanceUID = strings.TrimSpace(seriesInstanceUID)
	if studyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	if seriesInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("series instance UID is required")}
	}
	return e.serviceURL(e.QIDOPath, []string{"studies", studyInstanceUID, "series", seriesInstanceUID, "instances"}, params)
}

// StudyMetadataURL builds the WADO-RS study metadata URL.
func (e Endpoint) StudyMetadataURL(studyInstanceUID string) (*url.URL, error) {
	studyInstanceUID = strings.TrimSpace(studyInstanceUID)
	if studyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	return e.serviceURL(e.WADOPath, []string{"studies", studyInstanceUID, "metadata"}, nil)
}

// StudyURL builds the WADO-RS study retrieval URL.
func (e Endpoint) StudyURL(studyInstanceUID string) (*url.URL, error) {
	studyInstanceUID = strings.TrimSpace(studyInstanceUID)
	if studyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	return e.serviceURL(e.WADOPath, []string{"studies", studyInstanceUID}, nil)
}

// SeriesMetadataURL builds the WADO-RS series metadata URL.
func (e Endpoint) SeriesMetadataURL(studyInstanceUID, seriesInstanceUID string) (*url.URL, error) {
	studyInstanceUID = strings.TrimSpace(studyInstanceUID)
	seriesInstanceUID = strings.TrimSpace(seriesInstanceUID)
	if studyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	if seriesInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("series instance UID is required")}
	}
	return e.serviceURL(e.WADOPath, []string{"studies", studyInstanceUID, "series", seriesInstanceUID, "metadata"}, nil)
}

// SeriesURL builds the WADO-RS series retrieval URL.
func (e Endpoint) SeriesURL(studyInstanceUID, seriesInstanceUID string) (*url.URL, error) {
	studyInstanceUID = strings.TrimSpace(studyInstanceUID)
	seriesInstanceUID = strings.TrimSpace(seriesInstanceUID)
	if studyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	if seriesInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("series instance UID is required")}
	}
	return e.serviceURL(e.WADOPath, []string{"studies", studyInstanceUID, "series", seriesInstanceUID}, nil)
}

// InstanceMetadataURL builds the WADO-RS instance metadata URL.
func (e Endpoint) InstanceMetadataURL(ref InstanceRef) (*url.URL, error) {
	instanceURL, err := e.InstanceURL(ref)
	if err != nil {
		return nil, err
	}
	instanceURL.Path = joinURLPath(instanceURL.Path, "", "metadata")
	return instanceURL, nil
}

// InstanceURL builds the WADO-RS object retrieval URL.
func (e Endpoint) InstanceURL(ref InstanceRef) (*url.URL, error) {
	ref.StudyInstanceUID = strings.TrimSpace(ref.StudyInstanceUID)
	ref.SeriesInstanceUID = strings.TrimSpace(ref.SeriesInstanceUID)
	ref.SOPInstanceUID = strings.TrimSpace(ref.SOPInstanceUID)
	if ref.StudyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	if ref.SeriesInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("series instance UID is required")}
	}
	if ref.SOPInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("SOP instance UID is required")}
	}
	return e.serviceURL(e.WADOPath, []string{"studies", ref.StudyInstanceUID, "series", ref.SeriesInstanceUID, "instances", ref.SOPInstanceUID}, nil)
}

// FramesURL builds the WADO-RS RetrieveFrames URL. Frame numbers are one-based
// and duplicates are rejected to keep response-to-request mapping unambiguous.
func (e Endpoint) FramesURL(ref InstanceRef, frames []int) (*url.URL, error) {
	instanceURL, err := e.InstanceURL(ref)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("at least one frame number is required")}
	}
	seen := make(map[int]bool, len(frames))
	values := make([]string, len(frames))
	for index, frame := range frames {
		if frame <= 0 {
			return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("frame numbers are one-based")}
		}
		if seen[frame] {
			return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("duplicate frame number %d", frame)}
		}
		seen[frame] = true
		values[index] = strconv.Itoa(frame)
	}
	instanceURL.Path = joinURLPath(instanceURL.Path, "", "frames/"+strings.Join(values, ","))
	return instanceURL, nil
}

// StoreStudiesURL builds the STOW-RS study storage URL.
func (e Endpoint) StoreStudiesURL() (*url.URL, error) {
	return e.serviceURL(e.STOWPath, []string{"studies"}, nil)
}

// StoreStudyURL builds the study-scoped STOW-RS storage URL.
func (e Endpoint) StoreStudyURL(studyInstanceUID string) (*url.URL, error) {
	studyInstanceUID = strings.TrimSpace(studyInstanceUID)
	if studyInstanceUID == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("study instance UID is required")}
	}
	return e.serviceURL(e.STOWPath, []string{"studies", studyInstanceUID}, nil)
}

func (e Endpoint) serviceURL(servicePath string, parts []string, params url.Values) (*url.URL, error) {
	rawBase := strings.TrimSpace(e.BaseURL)
	if rawBase == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("base URL cannot be empty")}
	}
	u, err := url.Parse(rawBase)
	if err != nil {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: err}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("base URL must use http or https")}
	}
	if u.Host == "" {
		return nil, &Error{Kind: ErrorKindInvalidEndpoint, Err: fmt.Errorf("base URL must include a host")}
	}
	u.Path = joinURLPath(u.Path, servicePath, escapedResource(parts...))
	u.RawQuery = params.Encode()
	return u, nil
}

func escapedResource(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), "/")
		if part == "" {
			continue
		}
		out = append(out, url.PathEscape(part))
	}
	return strings.Join(out, "/")
}

func joinURLPath(basePath, servicePath, resource string) string {
	var parts []string
	add := func(value string) {
		for _, part := range strings.Split(strings.Trim(value, "/"), "/") {
			if part != "" {
				parts = append(parts, part)
			}
		}
	}
	add(basePath)
	add(servicePath)
	resource = strings.Trim(resource, "/")
	if resource != "" {
		last := ""
		if len(parts) > 0 {
			last = parts[len(parts)-1]
		}
		first := resource
		if idx := strings.Index(first, "/"); idx >= 0 {
			first = first[:idx]
		}
		if strings.EqualFold(last, first) {
			resource = strings.TrimPrefix(resource[len(first):], "/")
		}
		if resource != "" {
			parts = append(parts, resource)
		}
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + path.Join(parts...)
}
