package dicomweb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/dicomjson"
)

func datasetsFromDICOMJSON(data []byte) ([]Dataset, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var datasets []Dataset
	if err := decodeJSON(data, &datasets); err != nil {
		return nil, err
	}
	return datasets, nil
}

func jsonDatasets(datasets []Dataset) ([]byte, error) {
	return json.Marshal(datasets)
}

func decodeJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("DICOM JSON response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func instanceRefsFromMetadata(data []byte, fallbackStudyUID, fallbackSeriesUID string) ([]InstanceRef, error) {
	datasets, err := datasetsFromDICOMJSON(data)
	if err != nil {
		return nil, err
	}
	return instanceRefsFromDatasets(datasets, fallbackStudyUID, fallbackSeriesUID)
}

func instanceRefsFromDatasets(datasets []Dataset, fallbackStudyUID, fallbackSeriesUID string) ([]InstanceRef, error) {
	refs := make([]InstanceRef, 0, len(datasets))
	for _, dataset := range datasets {
		ref := InstanceRef{
			StudyInstanceUID:  firstNonEmpty(dicomjson.ElementString(dataset, "0020000D"), fallbackStudyUID),
			SeriesInstanceUID: firstNonEmpty(dicomjson.ElementString(dataset, "0020000E"), fallbackSeriesUID),
			SOPInstanceUID:    dicomjson.ElementString(dataset, "00080018"),
		}
		if ref.StudyInstanceUID == "" || ref.SeriesInstanceUID == "" || ref.SOPInstanceUID == "" {
			return nil, fmt.Errorf("metadata missing StudyInstanceUID, SeriesInstanceUID, or SOPInstanceUID")
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func objectPartsFromResponse(resp Response) ([]ObjectPart, error) {
	parts, err := objectPartsFromResponseWithPolicy(resp, Options{}, dicomObjectResponsePolicy)
	if err != nil {
		return nil, fmt.Errorf("%s returned an invalid DICOMweb response: %w", SafeURL(resp.URL), err)
	}
	return parts, nil
}

func objectPartsFromResponseWithPolicy(resp Response, options Options, policy responseMediaPolicy) ([]ObjectPart, error) {
	limits, err := responseLimits(options)
	if err != nil {
		return nil, err
	}
	contentType, err := responseContentType(resp.Header.Values("Content-Type"))
	if err != nil {
		return nil, err
	}
	mediaType, params, err := validateOuterResponseMediaType(contentType, policy, limits)
	if err != nil {
		return nil, err
	}
	if mediaType == "multipart/related" {
		boundary := params["boundary"]
		body := newMultipartHeaderLimitReader(bytes.NewReader(resp.Body), boundary, limits.maxHeaderBytes)
		reader := multipart.NewReader(body, boundary)
		var parts []ObjectPart
		for {
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
			if len(parts) >= limits.maxParts {
				_ = part.Close()
				return nil, &ResponseDecodeError{Kind: ResponseDecodePartLimit, Limit: int64(limits.maxParts), Value: int64(len(parts) + 1)}
			}
			if err := validatePartHeader(part.Header, limits); err != nil {
				_ = part.Close()
				return nil, err
			}
			partContentType := part.Header.Get("Content-Type")
			if _, _, err := validatePartMediaType(partContentType, policy, limits); err != nil {
				_ = part.Close()
				return nil, err
			}
			data, err := readResponsePart(part, limits)
			_ = part.Close()
			if err != nil {
				return nil, responsePartReadError(err)
			}
			parts = append(parts, ObjectPart{
				ContentType:       partContentType,
				TransferSyntaxUID: transferSyntaxFromContentType(partContentType),
				Data:              data,
			})
		}
		if len(parts) == 0 {
			return nil, &ResponseDecodeError{Kind: ResponseDecodeMalformedMultipart}
		}
		return parts, nil
	}
	if int64(len(resp.Body)) > limits.maxPartBytes {
		return nil, &ResponseDecodeError{Kind: ResponseDecodePartBytes, Limit: limits.maxPartBytes, Value: int64(len(resp.Body))}
	}
	return []ObjectPart{{
		ContentType:       contentType,
		TransferSyntaxUID: transferSyntaxFromContentType(contentType),
		Data:              resp.Body,
	}}, nil
}

func transferSyntaxFromContentType(contentType string) string {
	_, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["transfer-syntax"])
}

// StoreResultFromDICOMJSON parses a STOW-RS DICOM JSON response dataset.
func StoreResultFromDICOMJSON(data []byte) (StoreResult, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return StoreResult{}, nil
	}
	dataset, err := firstDICOMJSONDataset(data)
	if err != nil {
		return StoreResult{}, err
	}
	stored, err := storeItemsFromSequence(dataset, "00081199")
	if err != nil {
		return StoreResult{}, err
	}
	failed, err := storeItemsFromSequence(dataset, "00081198")
	if err != nil {
		return StoreResult{}, err
	}
	otherFailures, err := storeItemsFromSequence(dataset, "0008119A")
	if err != nil {
		return StoreResult{}, err
	}
	// Preserve StoreResult's source-compatible shape while retaining failures
	// that are not associated with a specific SOP Instance.
	failed = append(failed, otherFailures...)
	return StoreResult{Stored: stored, Failed: failed}, nil
}

func firstDICOMJSONDataset(data []byte) (Dataset, error) {
	var dataset Dataset
	if err := decodeJSON(data, &dataset); err == nil {
		return dataset, nil
	}
	datasets, err := datasetsFromDICOMJSON(data)
	if err != nil {
		return nil, err
	}
	if len(datasets) == 0 {
		return Dataset{}, nil
	}
	return datasets[0], nil
}

func storeItemsFromSequence(dataset Dataset, tag string) ([]StoreItem, error) {
	elem, ok := dataset[strings.ToUpper(tag)]
	if !ok {
		return nil, nil
	}
	items := make([]StoreItem, 0, len(elem.Value))
	for _, value := range elem.Value {
		itemDataset, err := sequenceItemDataset(value)
		if err != nil {
			return nil, err
		}
		warningReason, err := elementUint16(itemDataset, "00081196")
		if err != nil {
			return nil, err
		}
		failureReason, err := elementUint16(itemDataset, "00081197")
		if err != nil {
			return nil, err
		}
		items = append(items, StoreItem{
			SOPClassUID:    dicomjson.ElementString(itemDataset, "00081150"),
			SOPInstanceUID: dicomjson.ElementString(itemDataset, "00081155"),
			RetrieveURL:    dicomjson.ElementString(itemDataset, "00081190"),
			WarningReason:  warningReason,
			FailureReason:  failureReason,
		})
	}
	return items, nil
}

func sequenceItemDataset(value any) (Dataset, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var dataset Dataset
	if err := decodeJSON(data, &dataset); err != nil {
		return nil, err
	}
	return dataset, nil
}

func elementUint16(dataset Dataset, tag string) (uint16, error) {
	value := dicomjson.ElementString(dataset, tag)
	if value == "" {
		return 0, nil
	}
	base := 10
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		base = 0
	} else if strings.ContainsAny(strings.ToUpper(trimmed), "ABCDEF") {
		base = 16
	}
	parsed, err := strconv.ParseUint(trimmed, base, 16)
	if err != nil {
		return 0, err
	}
	return uint16(parsed), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
