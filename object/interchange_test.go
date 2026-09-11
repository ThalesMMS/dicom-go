package object_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dicomxml"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func interchangeFixture() *object.Object {
	return object.FromDataSet(dicomtest.InterchangeDataSet(), std.Dictionary)
}

func TestInMemoryInterchangePreservesTypedAndRawSemantics(t *testing.T) {
	for _, syntax := range []transfer.Syntax{transfer.ExplicitVRLittleEndian, transfer.ExplicitVRBigEndian} {
		t.Run(syntax.UID, func(t *testing.T) {
			source := interchangeFixture()
			want, err := dicomtest.SemanticDataSet(source.ToDataSet(), binary.LittleEndian)
			if err != nil {
				t.Fatal(err)
			}
			var part10 bytes.Buffer
			if err := object.WriteFile(&part10, &object.File{Dataset: source, TransferSyntax: syntax}); err != nil {
				t.Fatal(err)
			}
			file, err := object.ReadFile(bytes.NewReader(part10.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			for _, origin := range []struct {
				name string
				obj  *object.Object
			}{{"memory", source}, {"read-file", file.Dataset}} {
				for _, path := range []string{"part10", "dataset", "json", "xml"} {
					t.Run(origin.name+"/"+path, func(t *testing.T) {
						got, err := interchangeRoundTrip(origin.obj, syntax, path)
						if err != nil {
							t.Fatal(err)
						}
						defer got.Close()
						actual, err := dicomtest.SemanticDataSet(got.ToDataSet(), got.ValueByteOrder())
						if err != nil {
							t.Fatal(err)
						}
						if diff := dicomtest.DiffSemantic(actual, want); diff != "" {
							t.Fatal(diff)
						}
					})
				}
				unchanged, err := dicomtest.SemanticDataSet(origin.obj.ToDataSet(), origin.obj.ValueByteOrder())
				if err != nil {
					t.Fatal(err)
				}
				if diff := dicomtest.DiffSemantic(unchanged, want); diff != "" {
					t.Fatalf("mutated %s source: %s", origin.name, diff)
				}
			}
			unchanged, err := dicomtest.SemanticDataSet(source.ToDataSet(), source.ValueByteOrder())
			if err != nil {
				t.Fatal(err)
			}
			if diff := dicomtest.DiffSemantic(unchanged, want); diff != "" {
				t.Fatalf("mutated source: %s", diff)
			}
		})
	}
}

func interchangeRoundTrip(obj *object.Object, syntax transfer.Syntax, path string) (*object.Object, error) {
	var b bytes.Buffer
	switch path {
	case "part10":
		if err := object.WriteFile(&b, &object.File{Dataset: obj, TransferSyntax: syntax}); err != nil {
			return nil, err
		}
		f, err := object.ReadFile(&b)
		if err != nil {
			return nil, err
		}
		return f.Dataset, nil
	case "dataset":
		if err := object.WriteDataSet(&b, obj, syntax); err != nil {
			return nil, err
		}
		return object.ReadDataSet(&b, syntax)
	case "json":
		data, err := dicomjson.Marshal(obj, dicomjson.DefaultOptions())
		if err != nil {
			return nil, err
		}
		return dicomjson.Unmarshal(data, std.Dictionary)
	case "xml":
		data, err := dicomxml.Marshal(obj, dicomxml.Options{})
		if err != nil {
			return nil, err
		}
		return dicomxml.Unmarshal(data, std.Dictionary)
	default:
		return nil, fmt.Errorf("unknown interchange path %q", path)
	}
}

func TestInterchangeBulkDataURINeverResolvesImplicitly(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = w.Write([]byte{1, 2}) }))
	defer server.Close()
	for _, uri := range []string{server.URL + "/synthetic", "file:///synthetic-must-not-be-opened-901"} {
		obj := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB}, Value: core.BulkDataValue{URI: uri}}}, std.Dictionary)
		for _, path := range []string{"json", "xml"} {
			got, err := interchangeRoundTrip(obj, transfer.ExplicitVRLittleEndian, path)
			if err != nil {
				t.Fatal(err)
			}
			e, ok := got.Get(core.TagPixelData)
			if !ok {
				t.Fatal("BulkDataURI attribute lost")
			}
			value, ok := e.Value.(core.BulkDataValue)
			if !ok || value.URI != uri {
				t.Fatalf("%s resolved or changed symbolic bulk value", path)
			}
			if err := got.Close(); err != nil {
				t.Fatal(err)
			}
		}
		var b bytes.Buffer
		if err := object.WriteDataSet(&b, obj, transfer.ExplicitVRLittleEndian); err == nil {
			t.Fatal("dataset writer resolved bulk without a resolver")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("implicit bulk HTTP request")
	}
}
