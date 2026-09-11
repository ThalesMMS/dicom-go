package dicomweb

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

const (
	wadoFuzzMaxInputBytes       = 64 << 10
	wadoFuzzMaxContentTypeBytes = 4 << 10
	wadoFuzzMaxPartBytes        = 32 << 10
	wadoFuzzMaxHeaderBytes      = 4 << 10
)

var wadoFuzzRef = InstanceRef{
	StudyInstanceUID:  "1.2.3",
	SeriesInstanceUID: "1.2.3.4",
	SOPInstanceUID:    "1.2.3.4.5",
}

type wadoFuzzRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip wadoFuzzRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

// FuzzDICOMwebMetadataReferences exercises bounded DICOM JSON metadata parsing.
// A short local smoke run is:
//
//	go test ./net/dicomweb -run '^$' -fuzz '^FuzzDICOMwebMetadataReferences$' -fuzztime=10s
func FuzzDICOMwebMetadataReferences(f *testing.F) {
	f.Add([]byte(`[]`))
	f.Add([]byte(`[{
		"0020000D":{"vr":"UI","Value":["1.2.3"]},
		"0020000E":{"vr":"UI","Value":["1.2.3.4"]},
		"00080018":{"vr":"UI","Value":["1.2.3.4.5"]}
	}]`))
	// Study and series fallbacks are part of the metadata-reference contract.
	f.Add([]byte(`[{
		"00080018":{"vr":"UI","Value":["1.2.840.1"]}
	}]`))
	f.Add([]byte(`[{"0020000D":`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > wadoFuzzMaxInputBytes {
			t.Skip()
		}
		refs, err := instanceRefsFromMetadata(data, "9.8.7", "9.8.7.6")
		if err != nil {
			_ = err.Error()
			return
		}
		for _, ref := range refs {
			if ref.StudyInstanceUID == "" || ref.SeriesInstanceUID == "" || ref.SOPInstanceUID == "" {
				t.Fatalf("successful metadata parse returned an incomplete reference: %#v", ref)
			}
		}
	})
}

// FuzzWADOResponseParsing drives the public buffered object, streaming object,
// and streaming frame APIs through an in-memory RoundTripper. Mutations cover
// the outer Content-Type, multipart boundary and raw headers, truncation,
// representation/transfer-syntax compatibility, and response/part limits. No
// socket or external service is used.
//
// A bounded local or CI smoke run is:
//
//	go test ./net/dicomweb -run '^$' -fuzz '^FuzzWADOResponseParsing$' -fuzztime=10s
func FuzzWADOResponseParsing(f *testing.F) {
	const (
		objectBoundary = "fuzz-object"
		frameBoundary  = "fuzz-frame"
		largeLimit     = uint16(65535)
		largeHeader    = uint16(wadoFuzzMaxHeaderBytes - 1)
		fourParts      = uint8(3)
	)
	validSingle := []byte("DICM-single")
	validObjectMultipart := []byte(
		"--" + objectBoundary + "\r\nContent-Type: application/dicom; transfer-syntax=1.2.840.10008.1.2.1\r\n\r\nDICM-one\r\n" +
			"--" + objectBoundary + "\r\nContent-Type: application/dicom\r\n\r\nDICM-two\r\n" +
			"--" + objectBoundary + "--\r\n",
	)
	validFrameMultipart := []byte(
		"--" + frameBoundary + "\r\nContent-Type: image/jpeg; transfer-syntax=1.2.840.10008.1.2.4.50\r\n\r\nJPEG-one\r\n" +
			"--" + frameBoundary + "\r\nContent-Type: image/jpeg; transfer-syntax=1.2.840.10008.1.2.4.50\r\n\r\nJPEG-two\r\n" +
			"--" + frameBoundary + "--\r\n",
	)
	mismatchedFrameTransferSyntax := []byte(
		"--" + frameBoundary + "\r\nContent-Type: image/jpeg; transfer-syntax=1.2.840.10008.1.2.1\r\n\r\nnot-jpeg\r\n" +
			"--" + frameBoundary + "--\r\n",
	)

	// Mode 0 is buffered object retrieval, mode 1 is streaming object
	// retrieval, and mode 2 is streaming frame retrieval.
	f.Add("application/dicom; transfer-syntax=1.2.840.10008.1.2.1", validSingle, uint8(0), largeLimit, largeLimit, largeHeader, fourParts)
	f.Add(`multipart/related; type="application/dicom"; boundary="`+objectBoundary+`"`, validObjectMultipart, uint8(0), largeLimit, largeLimit, largeHeader, fourParts)
	f.Add(`multipart/related; type="application/dicom"; boundary="`+objectBoundary+`"`, validObjectMultipart, uint8(1), largeLimit, largeLimit, largeHeader, fourParts)
	f.Add(`multipart/related; type="image/jpeg"; boundary="`+frameBoundary+`"`, validFrameMultipart, uint8(2), largeLimit, largeLimit, largeHeader, fourParts)

	// Structural and limit seeds complement mutations of the valid corpus.
	truncated := validObjectMultipart[:len(validObjectMultipart)-len("--"+objectBoundary+"--\r\n")]
	f.Add(`multipart/related; type="application/dicom"; boundary="`+objectBoundary+`"`, truncated, uint8(0), largeLimit, largeLimit, largeHeader, fourParts)
	f.Add(`multipart/related; type="application/dicom"; boundary="trailing "`, validObjectMultipart, uint8(0), largeLimit, largeLimit, largeHeader, fourParts)
	f.Add("application/dicom", validSingle, uint8(0), uint16(len(validSingle)-2), largeLimit, largeHeader, fourParts)
	f.Add(`multipart/related; type="application/dicom"; boundary="`+objectBoundary+`"`, validObjectMultipart, uint8(1), largeLimit, uint16(3), largeHeader, fourParts)
	f.Add(`multipart/related; type="application/dicom"; boundary="`+objectBoundary+`"`, validObjectMultipart, uint8(1), largeLimit, largeLimit, uint16(15), fourParts)
	f.Add(`multipart/related; type="application/dicom"; boundary="`+objectBoundary+`"`, validObjectMultipart, uint8(0), largeLimit, largeLimit, largeHeader, uint8(0))
	f.Add(`multipart/related; type="application/octet-stream"; boundary="`+frameBoundary+`"`, validFrameMultipart, uint8(2), largeLimit, largeLimit, largeHeader, fourParts)
	f.Add(`multipart/related; type="image/jpeg"; boundary="`+frameBoundary+`"`, mismatchedFrameTransferSyntax, uint8(2), largeLimit, largeLimit, largeHeader, fourParts)

	f.Fuzz(func(t *testing.T, contentType string, body []byte, mode uint8, bodyLimitSeed, partLimitSeed, headerLimitSeed uint16, partCountSeed uint8) {
		if len(body) > wadoFuzzMaxInputBytes || len(contentType) > wadoFuzzMaxContentTypeBytes {
			t.Skip()
		}

		maxBodyBytes := boundedWADOFuzzLimit(bodyLimitSeed, wadoFuzzMaxInputBytes)
		maxPartBytes := boundedWADOFuzzLimit(partLimitSeed, wadoFuzzMaxPartBytes)
		maxHeaderBytes := int(boundedWADOFuzzLimit(headerLimitSeed, wadoFuzzMaxHeaderBytes))
		maxParts := 1 + int(partCountSeed%4)
		effectivePartBytes := maxPartBytes
		if effectivePartBytes > maxBodyBytes {
			effectivePartBytes = maxBodyBytes
		}

		client := Client{
			Endpoint: Endpoint{BaseURL: "http://dicomweb.invalid", WADOPath: "wado"},
			Options: Options{
				MaxBodyBytes: maxBodyBytes,
				ResponseLimits: ResponseLimits{
					MaxParts:           maxParts,
					MaxPartBytes:       maxPartBytes,
					MaxPartHeaderBytes: maxHeaderBytes,
					MaxMultipartDepth:  1,
				},
			},
		}
		client.Options.HTTPClient = &http.Client{Transport: wadoFuzzRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        http.Header{"Content-Type": {contentType}},
				Body:          io.NopCloser(bytes.NewReader(body)),
				ContentLength: int64(len(body)),
				Request:       request,
			}, nil
		})}

		switch mode % 3 {
		case 0:
			parts, err := client.RetrieveInstanceWithOptions(context.Background(), wadoFuzzRef, RetrieveOptions{})
			if err != nil {
				_ = err.Error()
				return
			}
			if len(parts) == 0 || len(parts) > maxParts {
				t.Fatalf("successful buffered parse returned %d parts with limit %d", len(parts), maxParts)
			}
			var total int64
			for _, part := range parts {
				if int64(len(part.Data)) > effectivePartBytes {
					t.Fatalf("successful buffered part has %d bytes with limit %d", len(part.Data), effectivePartBytes)
				}
				total += int64(len(part.Data))
			}
			if total > maxBodyBytes {
				t.Fatalf("successful buffered payload has %d bytes with body limit %d", total, maxBodyBytes)
			}
		case 1:
			count, total, err := fuzzStreamWADOObjects(t, client, effectivePartBytes)
			if err != nil {
				_ = err.Error()
				return
			}
			if count == 0 || count > maxParts || total > maxBodyBytes {
				t.Fatalf("successful object stream returned parts=%d bytes=%d with limits parts=%d body=%d", count, total, maxParts, maxBodyBytes)
			}
		case 2:
			count := 0
			var total int64
			err := client.RetrieveFramesStreamWithOptions(context.Background(), wadoFuzzRef, []int{1, 2}, RetrieveOptions{}, func(part FramePartStream) error {
				n, err := io.Copy(io.Discard, part.Reader)
				if n > effectivePartBytes {
					t.Fatalf("successful frame callback read %d bytes with part limit %d", n, effectivePartBytes)
				}
				count++
				total += n
				return err
			})
			if err != nil {
				_ = err.Error()
				return
			}
			if count != 2 || count > maxParts || total > maxBodyBytes {
				t.Fatalf("successful frame stream returned parts=%d bytes=%d with limits parts=%d body=%d", count, total, maxParts, maxBodyBytes)
			}
		}
	})
}

func fuzzStreamWADOObjects(t *testing.T, client Client, maxPartBytes int64) (int, int64, error) {
	t.Helper()
	count := 0
	var total int64
	err := client.RetrieveInstanceStreamWithOptions(context.Background(), wadoFuzzRef, RetrieveOptions{}, func(part ObjectPartStream) error {
		n, err := io.Copy(io.Discard, part.Reader)
		if n > maxPartBytes {
			t.Fatalf("successful object callback read %d bytes with part limit %d", n, maxPartBytes)
		}
		count++
		total += n
		return err
	})
	return count, total, err
}

func boundedWADOFuzzLimit(seed uint16, ceiling int64) int64 {
	return 1 + int64(seed)%ceiling
}
