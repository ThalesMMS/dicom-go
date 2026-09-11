package dicomweb

import (
	"context"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	fuzzMetadataMaxBytes       = 4 << 10
	fuzzMetadataMaxContentType = 256
	fuzzMetadataMaxUID         = 64
)

// FuzzDICOMwebMetadataJSONInstanceRefs exercises metadata decoding, reference
// extraction, and response media validation together. The bounds keep failing
// inputs small enough for useful corpus minimization and avoid turning the
// fuzz target itself into an allocation stress test.
func FuzzDICOMwebMetadataJSONInstanceRefs(f *testing.F) {
	const complete = `[{"0020000D":{"vr":"UI","Value":["1.2.3"]},"0020000E":{"vr":"UI","Value":["1.2.3.4"]},"00080018":{"vr":"UI","Value":["1.2.3.4.5"]}}]`
	const fallback = `[{"00080018":{"vr":"UI","Value":["1.2.840.10008.5.1.4.1"]}}]`

	f.Add([]byte(complete), "application/dicom+json", "", "")
	f.Add([]byte(complete), "application/dicom+json; transfer-syntax=1.2.840.10008.1.2.1", "", "")
	f.Add([]byte(fallback), "application/json; charset=utf-8", "1.2.840.1", "1.2.840.1.1")
	f.Add([]byte("[]"), "text/plain", "1.2.3", "1.2.3.4")
	f.Add([]byte(complete), "image/jpeg; transfer-syntax=1.2.840.10008.1.2.4.50", "", "")
	f.Add([]byte(complete), "application/octet-stream; transfer-syntax=1.2.840.10008.1.2.1", "", "")
	f.Add([]byte(complete), "image/jpeg; transfer-syntax=1.2.840.10008.1.2.4.50", "", "")
	f.Add([]byte(complete), "application/octet-stream; transfer-syntax=1.2.840.10008.1.2.1", "", "")

	client := Client{Options: Options{
		MaxBodyBytes:     fuzzMetadataMaxBytes,
		MaxMetadataParts: 8,
		MetadataMediaTypes: []MetadataMediaType{
			MetadataMediaTypeDICOMJSON,
		},
		ResponseLimits: ResponseLimits{
			MaxParts:           8,
			MaxPartBytes:       fuzzMetadataMaxBytes,
			MaxPartHeaderBytes: fuzzMetadataMaxContentType,
			MaxMultipartDepth:  1,
		},
	}}
	limits, err := responseLimits(client.Options)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, metadata []byte, contentType, fallbackStudyUID, fallbackSeriesUID string) {
		if len(metadata) > fuzzMetadataMaxBytes || len(contentType) > fuzzMetadataMaxContentType ||
			len(fallbackStudyUID) > fuzzMetadataMaxUID || len(fallbackSeriesUID) > fuzzMetadataMaxUID {
			return
		}

		datasets, datasetErr := datasetsFromDICOMJSON(metadata)
		repeatedDatasets, repeatedDatasetErr := datasetsFromDICOMJSON(metadata)
		if (datasetErr == nil) != (repeatedDatasetErr == nil) ||
			datasetErr == nil && !reflect.DeepEqual(datasets, repeatedDatasets) {
			t.Fatalf("DICOM JSON decoding is not deterministic")
		}

		refs, refsErr := instanceRefsFromMetadata(metadata, fallbackStudyUID, fallbackSeriesUID)
		if datasetErr != nil {
			if refsErr == nil {
				t.Fatalf("reference extraction accepted metadata rejected by the dataset decoder")
			}
		} else {
			wantRefs, wantErr := instanceRefsFromDatasets(datasets, fallbackStudyUID, fallbackSeriesUID)
			if (refsErr == nil) != (wantErr == nil) {
				t.Fatalf("metadata and dataset reference extraction disagree: metadata error=%v dataset error=%v", refsErr != nil, wantErr != nil)
			}
			if refsErr == nil {
				if !slices.Equal(refs, wantRefs) || len(refs) != len(datasets) {
					t.Fatalf("reference extraction mismatch")
				}
				for _, ref := range refs {
					if strings.TrimSpace(ref.StudyInstanceUID) == "" || strings.TrimSpace(ref.SeriesInstanceUID) == "" || strings.TrimSpace(ref.SOPInstanceUID) == "" {
						t.Fatalf("successful extraction returned an incomplete reference")
					}
				}
			}
		}

		response := Response{
			URL:    "https://pacs.test/dicomweb/metadata",
			Header: http.Header{"Content-Type": []string{contentType}},
			Body:   append([]byte(nil), metadata...),
		}
		responseDatasets, responseErr := client.datasetsFromMetadataResponse(context.Background(), response, false)
		if responseErr == nil {
			if datasetErr != nil {
				t.Fatalf("metadata response accepted JSON rejected by the direct decoder")
			}
			if !reflect.DeepEqual(responseDatasets, datasets) {
				t.Fatalf("metadata response and direct JSON decoding disagree")
			}
		}

		gotTransferSyntax := transferSyntaxFromContentType(contentType)
		_, params, parseErr := mime.ParseMediaType(strings.TrimSpace(contentType))
		if parseErr != nil {
			if gotTransferSyntax != "" {
				t.Fatalf("malformed Content-Type produced transfer syntax %q", gotTransferSyntax)
			}
		} else if want := strings.TrimSpace(params["transfer-syntax"]); gotTransferSyntax != want {
			t.Fatalf("transfer syntax=%q, want %q", gotTransferSyntax, want)
		}

		mediaType, frameParams, frameErr := validatePartMediaType(contentType, frameResponsePolicy, limits)
		if frameErr == nil {
			if !frameMediaTypeMatchesTransferSyntax(mediaType, frameParams["transfer-syntax"]) {
				t.Fatalf("frame media validation accepted an incompatible transfer syntax")
			}
			if gotTransferSyntax != strings.TrimSpace(frameParams["transfer-syntax"]) {
				t.Fatalf("frame and generic transfer syntax parsing disagree")
			}
		}
	})
}
