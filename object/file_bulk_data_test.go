package object

import (
	"bytes"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestWriteFileWithOptionsStreamsTopLevelAndNestedBulkData(t *testing.T) {
	topLevelTag := core.NewTag(0x0042, 0x0011)
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nestedTag := core.NewTag(0x0011, 0x1010)
	dataset := canonicalMinimalDataSet()
	dataset.Elements = append(dataset.Elements,
		core.Element{
			Header: core.ElementHeader{Tag: topLevelTag, VR: core.VROB},
			Value:  core.BulkDataValue{URI: "bulk://top-level"},
		},
		core.Element{
			Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{{
				Header: core.ElementHeader{Tag: nestedTag, VR: core.VROB},
				Value:  core.BulkDataValue{URI: "bulk://nested"},
			}}}}},
		},
	)
	file := &File{
		Dataset:        FromDataSet(dataset, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	sources := make(map[string]*objectBulkReadCloser)
	resolver := func(value core.BulkDataValue) (BulkDataSource, error) {
		var data []byte
		switch value.URI {
		case "bulk://top-level":
			data = []byte{0x01, 0x02, 0x03}
		case "bulk://nested":
			data = []byte{0x10, 0x20}
		default:
			t.Fatalf("unexpected Bulk Data URI %q", value.URI)
		}
		source := &objectBulkReadCloser{Reader: bytes.NewReader(data)}
		sources[value.URI] = source
		return BulkDataSource{Reader: source, Size: int64(len(data))}, nil
	}

	var encoded bytes.Buffer
	if err := WriteFileWithOptions(&encoded, file, WriteFileOptions{BulkDataResolver: resolver}); err != nil {
		t.Fatalf("WriteFileWithOptions() error = %v", err)
	}
	for uri, source := range sources {
		if source.closeCount != 1 {
			t.Fatalf("%s Close count = %d, want 1", uri, source.closeCount)
		}
	}
	if len(sources) != 2 {
		t.Fatalf("resolved sources = %d, want 2", len(sources))
	}

	roundTrip, err := ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	gotTop, ok := roundTrip.Dataset.GetRaw(topLevelTag)
	if !ok || !bytes.Equal(gotTop, []byte{0x01, 0x02, 0x03, 0x00}) {
		t.Fatalf("top-level Bulk Data = % X ok=%v, want 01 02 03 00", gotTop, ok)
	}
	items, ok := roundTrip.Dataset.GetSequence(sequenceTag)
	if !ok || len(items) != 1 {
		t.Fatalf("sequence items = %d ok=%v, want 1", len(items), ok)
	}
	gotNested, ok := items[0].GetRaw(nestedTag)
	if !ok || !bytes.Equal(gotNested, []byte{0x10, 0x20}) {
		t.Fatalf("nested Bulk Data = % X ok=%v, want 10 20", gotNested, ok)
	}
}

func TestWriteFileBulkDataWithoutResolverPreservesFailureBehavior(t *testing.T) {
	dataset := canonicalMinimalDataSet()
	dataset.Elements = append(dataset.Elements, core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0042, 0x0011), VR: core.VROB},
		Value:  core.BulkDataValue{URI: "bulk://missing"},
	})
	file := &File{
		Dataset:        FromDataSet(dataset, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	if err := WriteFile(io.Discard, file); err == nil {
		t.Fatal("WriteFile() error = nil, want unresolved Bulk Data failure")
	}
}

type objectBulkReadCloser struct {
	io.Reader
	closeCount int
}

func (r *objectBulkReadCloser) Close() error {
	r.closeCount++
	return nil
}
