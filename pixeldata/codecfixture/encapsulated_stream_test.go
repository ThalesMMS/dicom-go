package codecfixture

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestEncodedStreamJPEGLSIndependentFullCorpus(t *testing.T) {
	root := filepath.Join("testdata", "codecfull")
	manifest, err := readCodecFullCorpusManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, f := range manifest.Fixtures {
		if !strings.HasPrefix(f.Path, "jpegls/") && !strings.HasPrefix(f.Path, "jpegls-near/") {
			continue
		}
		count++
		t.Run(f.ID, func(t *testing.T) {
			e, want, err := ReadFullReconstruction("../..", f.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := readCodecFullCorpusFile(root, f.Path, f.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			length := len(raw)
			stream := append([]byte(nil), raw...)
			if len(stream)&1 != 0 {
				stream = append(stream, 0)
			}
			mid := (len(stream) / 2) &^ 1
			elements := metadataElements(f.ID, f.Input.Rows, f.Input.Columns, 2, fragmentElement(stream))
			for _, el := range []core.Element{
				stringElement(tagPhotometricInterpretation, core.VRCS, f.Input.Photometric),
				uint16Element(tagSamplesPerPixel, uint16(f.Input.Components)), uint16Element(tagPlanarConfiguration, 0),
				uint16Element(tagBitsAllocated, uint16(f.Input.BitsAllocated)), uint16Element(tagBitsStored, uint16(f.Input.BitsStored)), uint16Element(tagHighBit, uint16(f.Input.HighBit)),
			} {
				elements = replaceElement(elements, el)
			}
			syntax, ok := transfer.DefaultRegistry.Get(f.Input.TransferSyntax)
			if !ok {
				t.Fatal("syntax")
			}
			c := Case{Name: f.ID, Syntax: syntax, Elements: elements, RegisterCodecs: RegisterBuiltinCodecs}
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"empty", "BOT", "EOT"} {
				obj := c.Object()
				seq := core.FragmentSequence{Fragments: [][]byte{stream[:mid], stream[mid:], stream}}
				if table == "BOT" {
					seq.OffsetTable = offsetTable32(0, uint32(len(stream)+16))
				}
				if table == "EOT" {
					seq.Fragments = [][]byte{stream, stream}
					obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 1), VR: core.VROV}, Value: core.Uint64Value{0, uint64(len(stream) + 8)}})
					obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 2), VR: core.VROV}, Value: core.Uint64Value{uint64(length), uint64(length)}})
				}
				obj.Put(core.Element{Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true}, Value: seq})
				var wire bytes.Buffer
				if err := object.WriteFile(&wire, &object.File{Dataset: obj, TransferSyntax: syntax}); err != nil {
					t.Fatal(err)
				}
				calls := 0
				sink := parser.EncodedFrameSinkFunc(func(frame parser.EncodedFrame) error {
					if frame.Index != calls || frame.Metadata.NumberOfFrames != 2 {
						return fmt.Errorf("frame metadata/order")
					}
					calls++
					decodeObj := c.Object()
					decodeObj.Put(stringElement(tagNumberOfFrames, core.VRIS, "1"))
					got, err := registry.DecodeFrames(syntax.UID, pixeldata.PixelData{Encapsulated: true, Sequence: core.FragmentSequence{Fragments: [][]byte{frame.Data}}}, decodeObj)
					if err != nil {
						return err
					}
					r, err := CompareSamples(got.Data, e.Layout, want, e.Layout, SamplePolicy{})
					if err != nil {
						return err
					}
					if !r.Qualified || r.Mismatches != 0 || r.Samples != uint64(f.Input.Rows*f.Input.Columns*f.Input.Components) {
						return fmt.Errorf("independent reconstruction mismatch")
					}
					return nil
				})
				assembler, err := encapsulated.NewStream(context.Background(), sink, encapsulated.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				file, err := object.ReadFileWithOptions(bytes.NewReader(wire.Bytes()), object.ReadFileOptions{EncapsulatedSink: assembler})
				if err != nil || calls != 2 || file == nil || !file.Dataset.HasDiscardedValues() {
					t.Fatalf("%s: %v calls=%d", table, err, calls)
				}
			}
		})
	}
	if count != 173 {
		t.Fatalf("corpus count %d", count)
	}
}
