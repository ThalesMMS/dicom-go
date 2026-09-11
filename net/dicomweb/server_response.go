package dicomweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
)

type responseDatasetValidation struct {
	values    int
	estimated int64
}

func (s *Server) validateResponseDataset(ctx context.Context, dataset Dataset) error {
	state := responseDatasetValidation{}
	return s.validateResponseDatasetAtDepth(ctx, dataset, 1, &state)
}

func (s *Server) encodeResponseDataset(ctx context.Context, dataset Dataset) ([]byte, error) {
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
	if _, err := dicomjson.Unmarshal(data, std.Dictionary); err != nil {
		return nil, ErrBackend
	}
	return data, nil
}

func (s *Server) validateResponseDatasetAtDepth(ctx context.Context, dataset Dataset, depth int, state *responseDatasetValidation) error {
	if depth > s.limits.MaxJSONDepth {
		return ErrResourceLimit
	}
	for tag, element := range dataset {
		if err := ctx.Err(); err != nil {
			return err
		}
		state.values++
		if state.values > s.limits.MaxJSONValues {
			return ErrResourceLimit
		}
		state.estimated += int64(len(tag) + len(element.VR) + len(element.InlineBinary) + len(element.BulkDataURI) + 32)
		if len(element.InlineBinary) > s.limits.MaxJSONValueBytes || len(element.BulkDataURI) > s.limits.MaxJSONValueBytes || state.estimated > s.limits.MaxResponseBytes {
			return ErrResourceLimit
		}
		if len(tag) != 8 || tag != strings.ToUpper(tag) {
			return ErrBackend
		}
		parsedTag, err := core.ParseTag(tag)
		if err != nil || parsedTag.Group == 0x0002 {
			return ErrBackend
		}
		vr, err := core.ParseVR(element.VR)
		if err != nil {
			return ErrBackend
		}
		representations := 0
		if len(element.Value) > 0 {
			representations++
		}
		if element.InlineBinary != "" {
			representations++
		}
		if element.BulkDataURI != "" {
			representations++
		}
		if element.InlineBinary != "" {
			decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(element.InlineBinary))
			if _, err := io.Copy(io.Discard, decoder); err != nil {
				return ErrBackend
			}
		}
		if representations > 1 {
			return ErrBackend
		}
		if element.BulkDataURI != "" {
			parsed, err := url.Parse(element.BulkDataURI)
			if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return ErrBackend
			}
			if _, err := bulkDataToken(parsed.Path, parsed.RawPath, s.servicePath("/bulkdata/")); err != nil {
				return ErrBackend
			}
		}
		if vr == core.VRSQ {
			if element.InlineBinary != "" || element.BulkDataURI != "" {
				return ErrBackend
			}
			for _, value := range element.Value {
				state.values++
				if state.values > s.limits.MaxJSONValues {
					return ErrResourceLimit
				}
				switch item := value.(type) {
				case Dataset:
					if err := s.validateResponseDatasetAtDepth(ctx, item, depth+1, state); err != nil {
						return err
					}
				case map[string]Element:
					if err := s.validateResponseDatasetAtDepth(ctx, Dataset(item), depth+1, state); err != nil {
						return err
					}
				case map[string]any:
					data, err := json.Marshal(item)
					if err != nil {
						return ErrBackend
					}
					var nested Dataset
					if err := json.Unmarshal(data, &nested); err != nil {
						return ErrBackend
					}
					if err := s.validateResponseDatasetAtDepth(ctx, nested, depth+1, state); err != nil {
						return err
					}
				default:
					return ErrBackend
				}
			}
			continue
		}
		for _, value := range element.Value {
			state.values++
			if state.values > s.limits.MaxJSONValues {
				return ErrResourceLimit
			}
			size, ok := responseJSONScalarSize(value)
			if !ok {
				return ErrBackend
			}
			if size > s.limits.MaxJSONValueBytes {
				return ErrResourceLimit
			}
			state.estimated += int64(size*6 + 8)
			if state.estimated > s.limits.MaxResponseBytes {
				return ErrResourceLimit
			}
		}
	}
	return nil
}

func bulkDataToken(path, rawPath, prefix string) (string, error) {
	if rawPath != "" || !strings.HasPrefix(path, prefix) {
		return "", ErrInvalidRequest
	}
	token := strings.TrimPrefix(path, prefix)
	if token == "" {
		return "", ErrInvalidRequest
	}
	for _, segment := range strings.Split(token, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidRequest
		}
	}
	return token, nil
}

func responseJSONScalarSize(value any) (int, bool) {
	switch typed := value.(type) {
	case nil:
		return 4, true
	case string:
		return len(typed), true
	case json.Number:
		return len(typed), true
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return 32, true
	case map[string]any:
		total := 0
		for key, nested := range typed {
			if key != "Alphabetic" && key != "Ideographic" && key != "Phonetic" {
				return 0, false
			}
			text, ok := nested.(string)
			if !ok {
				return 0, false
			}
			total += len(key) + len(text)
		}
		return total, true
	case dicomjson.PersonNameComponents:
		return len(typed.Alphabetic) + len(typed.Ideographic) + len(typed.Phonetic), true
	case *dicomjson.PersonNameComponents:
		if typed == nil {
			return 4, true
		}
		return len(typed.Alphabetic) + len(typed.Ideographic) + len(typed.Phonetic), true
	default:
		return 0, false
	}
}
