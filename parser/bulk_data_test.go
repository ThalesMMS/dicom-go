package parser

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestWriterStreamsBulkDataWithVRPadding(t *testing.T) {
	tests := []struct {
		name        string
		vr          core.VR
		wantPadding byte
		validated   bool
	}{
		{name: "binary", vr: core.VROB, wantPadding: 0x00},
		{name: "text", vr: core.VRUT, wantPadding: 0x20},
		{name: "validated writer path", vr: core.VROB, wantPadding: 0x00, validated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &bulkTestReadCloser{reader: bytes.NewReader([]byte{0x01, 0x02, 0x03})}
			var gotValue core.BulkDataValue
			var output bytes.Buffer
			writer := NewWriterWithOptions(&output, transfer.ExplicitVRLittleEndian, WriterOptions{
				BulkDataResolver: func(value core.BulkDataValue) (BulkDataSource, error) {
					gotValue = value
					return BulkDataSource{Reader: source, Size: 3}, nil
				},
			})
			element := core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x1010), VR: tt.vr},
				Value:  core.BulkDataValue{URI: "bulk://value"},
			}
			var err error
			if tt.validated {
				err = writer.writeElementValidated(element, nil)
			} else {
				err = writer.WriteElement(element)
			}
			if err != nil {
				t.Fatalf("write Bulk Data: %v", err)
			}
			if gotValue.URI != "bulk://value" {
				t.Fatalf("resolver value = %#v, want bulk://value", gotValue)
			}
			if source.closeCount != 1 {
				t.Fatalf("Close count = %d, want 1", source.closeCount)
			}
			got := output.Bytes()
			if len(got) < 4 || !bytes.Equal(got[len(got)-4:], []byte{0x01, 0x02, 0x03, tt.wantPadding}) {
				t.Fatalf("encoded value = % X, want 01 02 03 %02X", got, tt.wantPadding)
			}
		})
	}
}

func TestWriterStreamsBulkDataInsideSequence(t *testing.T) {
	source := &bulkTestReadCloser{reader: bytes.NewReader([]byte{0x10, 0x20})}
	sequence := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{{
			Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x1010), VR: core.VROB},
			Value:  core.BulkDataValue{URI: "bulk://nested"},
		}}}}},
	}

	var output bytes.Buffer
	writer := NewWriterWithOptions(&output, transfer.ExplicitVRLittleEndian, WriterOptions{
		BulkDataResolver: func(value core.BulkDataValue) (BulkDataSource, error) {
			if value.URI != "bulk://nested" {
				t.Fatalf("resolver URI = %q, want bulk://nested", value.URI)
			}
			return BulkDataSource{Reader: source, Size: 2}, nil
		},
	})
	if err := writer.WriteElement(sequence); err != nil {
		t.Fatalf("WriteElement() error = %v", err)
	}
	if source.closeCount != 1 {
		t.Fatalf("Close count = %d, want 1", source.closeCount)
	}
	if !bytes.Contains(output.Bytes(), []byte{0x10, 0x20}) {
		t.Fatalf("encoded sequence does not contain streamed value: % X", output.Bytes())
	}
}

func TestWriterBulkDataRejectsSourceSizeMismatchAndCloses(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		size      int64
		wantError string
	}{
		{name: "short", data: []byte{1, 2, 3}, size: 4, wantError: "copied 3 bytes, want 4"},
		{name: "long", data: []byte{1, 2, 3}, size: 2, wantError: "exceeds declared size 2"},
		{name: "negative", data: []byte{1}, size: -1, wantError: "size -1 is negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &bulkTestReadCloser{reader: bytes.NewReader(tt.data)}
			writer := NewWriterWithOptions(io.Discard, transfer.ExplicitVRLittleEndian, WriterOptions{
				BulkDataResolver: func(core.BulkDataValue) (BulkDataSource, error) {
					return BulkDataSource{Reader: source, Size: tt.size}, nil
				},
			})
			err := writer.WriteElement(bulkTestElement())
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("WriteElement() error = %v, want %q", err, tt.wantError)
			}
			if source.closeCount != 1 {
				t.Fatalf("Close count = %d, want 1", source.closeCount)
			}
		})
	}
}

func TestWriterBulkDataPropagatesReaderResolverAndCloseErrors(t *testing.T) {
	readErr := errors.New("read failed")
	resolveErr := errors.New("resolve failed")
	closeErr := errors.New("close failed")
	tests := []struct {
		name        string
		source      *bulkTestReadCloser
		size        int64
		resolverErr error
		wantErr     error
	}{
		{
			name:    "read",
			source:  &bulkTestReadCloser{reader: errorReader{err: readErr}},
			size:    1,
			wantErr: readErr,
		},
		{
			name:    "close",
			source:  &bulkTestReadCloser{reader: bytes.NewReader([]byte{1, 2}), closeErr: closeErr},
			size:    2,
			wantErr: closeErr,
		},
		{
			name:        "resolver still transfers ownership",
			source:      &bulkTestReadCloser{reader: bytes.NewReader(nil)},
			size:        0,
			resolverErr: resolveErr,
			wantErr:     resolveErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := NewWriterWithOptions(io.Discard, transfer.ExplicitVRLittleEndian, WriterOptions{
				BulkDataResolver: func(core.BulkDataValue) (BulkDataSource, error) {
					return BulkDataSource{Reader: tt.source, Size: tt.size}, tt.resolverErr
				},
			})
			err := writer.WriteElement(bulkTestElement())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("WriteElement() error = %v, want errors.Is(_, %v)", err, tt.wantErr)
			}
			if tt.source.closeCount != 1 {
				t.Fatalf("Close count = %d, want 1", tt.source.closeCount)
			}
		})
	}
}

func TestWriterBulkDataErrorsDoNotExposeURI(t *testing.T) {
	sensitiveURI := "https://example.test/patients/JANE-DOE/studies/123"
	resolverErr := errors.New("backend failed for " + sensitiveURI)
	tests := []struct {
		name     string
		resolver BulkDataResolver
	}{
		{
			name: "resolver error",
			resolver: func(core.BulkDataValue) (BulkDataSource, error) {
				return BulkDataSource{}, resolverErr
			},
		},
		{
			name: "nil reader",
			resolver: func(core.BulkDataValue) (BulkDataSource, error) {
				return BulkDataSource{Size: 1}, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := NewWriterWithOptions(io.Discard, transfer.ExplicitVRLittleEndian, WriterOptions{BulkDataResolver: tt.resolver})
			element := bulkTestElement()
			element.Value = core.BulkDataValue{URI: sensitiveURI}
			err := writer.WriteElement(element)
			if err == nil {
				t.Fatal("WriteElement() error = nil, want failure")
			}
			if strings.Contains(err.Error(), sensitiveURI) {
				t.Fatalf("WriteElement() error exposed Bulk Data URI: %v", err)
			}
			if tt.name == "resolver error" && !errors.Is(err, resolverErr) {
				t.Fatalf("WriteElement() error = %v, want errors.Is(_, resolverErr)", err)
			}
		})
	}
}

func TestWriterBulkDataRequiresResolverAndDefinedNativeEncoding(t *testing.T) {
	element := bulkTestElement()
	err := NewWriter(io.Discard, transfer.ExplicitVRLittleEndian).WriteElement(element)
	if err == nil || !strings.Contains(err.Error(), "requires a BulkDataResolver") {
		t.Fatalf("WriteElement() error = %v, want missing resolver", err)
	}

	resolverCalls := 0
	writer := NewWriterWithOptions(io.Discard, transfer.JPEGBaseline, WriterOptions{
		BulkDataResolver: func(core.BulkDataValue) (BulkDataSource, error) {
			resolverCalls++
			return BulkDataSource{}, nil
		},
	})
	element.Header.Tag = core.TagPixelData
	err = writer.WriteElement(element)
	if err == nil || !strings.Contains(err.Error(), "cannot synthesize encapsulated Pixel Data") {
		t.Fatalf("WriteElement() error = %v, want encapsulated rejection", err)
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolverCalls)
	}
}

func bulkTestElement() core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x1010), VR: core.VROB},
		Value:  core.BulkDataValue{URI: "bulk://value"},
	}
}

type bulkTestReadCloser struct {
	reader     io.Reader
	closeErr   error
	closeCount int
}

func (r *bulkTestReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *bulkTestReadCloser) Close() error {
	r.closeCount++
	return r.closeErr
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
