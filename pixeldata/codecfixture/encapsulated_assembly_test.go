package codecfixture

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestSharedJPEGAssemblyIndependentFullSamples(t *testing.T) {
	cases := []Case{JPEGBaselineSmall(), JPEGExtendedSmall(), JPEGExtendedProcess4Mono12(), JPEGLosslessSV1RGB8Interleaved()}
	process14 := JPEGLosslessSV1RGB8Interleaved()
	process14.Name += "-process14"
	process14.Syntax = transfer.JPEGLosslessNonHierarchical
	cases = append(cases, process14)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			obj := c.Object()
			pixel, err := c.PixelData()
			if err != nil {
				t.Fatal(err)
			}
			if len(pixel.Sequence.Fragments) != 1 {
				t.Fatal("source corpus layout drift")
			}
			stream := pixel.Sequence.Fragments[0]
			want := c.ExpectedFrames
			policy := SamplePolicy{}
			if c.Name == "jpeg-baseline-small" || c.Name == "jpeg-extended-small" {
				_, want, err = ReadFullReconstruction("../..", "jpeg-assembly-"+c.Name)
				if err != nil {
					t.Fatal(err)
				}
				m, err := readCodecFullCorpusManifest(filepath.Join("testdata", "codecfull"))
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range m.Fixtures {
					if f.ID == "jpeg-assembly-"+c.Name {
						b, err := readCodecFullCorpusFile(filepath.Join("testdata", "codecfull"), f.Path, f.SHA256)
						if err != nil || !bytes.Equal(b, stream) {
							t.Fatal("independent oracle input differs from case")
						}
					}
				}
			} else if c.Name == "jpeg-extended-process4-mono12" {
				policy = *c.SamplePolicy
			}
			meta, err := pixeldata.ExtractMetadata(obj)
			if err != nil {
				t.Fatal(err)
			}
			layout := SampleLayout{Rows: int(meta.Rows), Columns: int(meta.Columns), Components: int(meta.SamplesPerPixel), BitsAllocated: int(meta.BitsAllocated), BitsStored: int(meta.BitsStored), HighBit: int(meta.HighBit)}
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			original, err := registry.DecodeFrames(c.Syntax.UID, pixel, obj)
			if err != nil {
				t.Fatal(err)
			}
			check := func(got pixeldata.Frames) {
				t.Helper()
				if len(got.Data) != 2 || got.Rows != layout.Rows || got.Columns != layout.Columns {
					t.Fatal("frame geometry")
				}
				for _, frame := range got.Data {
					r, err := CompareSamples([][]byte{frame}, layout, want, layout, policy)
					if err != nil || !r.Qualified || r.Samples != uint64(layout.Rows*layout.Columns*layout.Components) {
						t.Fatalf("independent full samples: %+v %v", r, err)
					}
					if !bytes.Equal(frame, original.Data[0]) {
						t.Fatal("fragmentation changed decoder samples")
					}
				}
			}
			obj.Put(stringElement(tagNumberOfFrames, core.VRIS, "2"))
			padded := append([]byte(nil), stream...)
			if len(padded)&1 != 0 {
				padded = append(padded, 0)
			}
			for split := 2; split < len(padded); split += 2 {
				for _, bot := range []bool{false, true} {
					seq := core.FragmentSequence{Fragments: [][]byte{padded[:split], padded[split:], padded}}
					if bot {
						seq.OffsetTable = offsetTable32(0, uint32(len(padded)+16))
					}
					got, err := registry.DecodeFrames(c.Syntax.UID, pixeldata.PixelData{Encapsulated: true, Sequence: seq}, obj)
					if err != nil {
						t.Fatalf("split=%d BOT=%t: %v", split, bot, err)
					}
					check(got)
				}
			}
			// EOT is legal only with a single Item per frame. Its lengths exclude pad.
			seq := core.FragmentSequence{Fragments: [][]byte{padded, padded}}
			obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 1), VR: core.VROV}, Value: core.Uint64Value{0, uint64(len(padded) + 8)}})
			obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 2), VR: core.VROV}, Value: core.Uint64Value{uint64(len(stream)), uint64(len(stream))}})
			obj.Put(core.Element{Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true}, Value: seq})
			var wire bytes.Buffer
			if err := object.WriteFile(&wire, &object.File{Dataset: obj, TransferSyntax: c.Syntax}); err != nil {
				t.Fatal(err)
			}
			file, err := object.ReadFile(bytes.NewReader(wire.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			pixel, err = pixeldata.Extract(file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			got, err := registry.DecodeFrames(c.Syntax.UID, pixel, file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			check(got)
		})
	}
}

func TestSharedJPEGLSMultiframeOrderAgainstIndependentReference(t *testing.T) {
	root := filepath.Join("testdata", "codecfull", "pydicom")
	file, err := object.OpenFile(filepath.Join(root, "emri_small_jpeg_ls_lossless.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ref, err := object.OpenFile(filepath.Join(root, "emri_small.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	registry := pixeldata.NewMemoryRegistry()
	if err := RegisterBuiltinCodecs(registry); err != nil {
		t.Fatal(err)
	}
	refPixel, err := pixeldata.Extract(ref.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.DecodeFrames(ref.TransferSyntax.UID, refPixel, ref.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := encapsulated.FromFragments(context.Background(), pixel.Sequence, file.Dataset, meta.NumberOfFrames, encapsulated.JPEGLS, encapsulated.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var seq core.FragmentSequence
	var offsets []uint32
	var offset uint32
	for i := 0; i < plan.Len(); i++ {
		view, err := plan.Frame(context.Background(), i)
		if err != nil {
			t.Fatal(err)
		}
		stream := append([]byte(nil), view.Data...)
		if len(stream)&1 != 0 {
			stream = append(stream, 0)
		}
		cut := 2 + 2*(i%7)
		if cut >= len(stream) {
			t.Fatal("unexpected small fixture")
		}
		offsets = append(offsets, offset)
		seq.Fragments = append(seq.Fragments, stream[:cut], stream[cut:])
		offset += uint32(len(stream) + 16)
	}
	for _, bot := range []bool{false, true} {
		t.Run(fmt.Sprint(bot), func(t *testing.T) {
			seq.OffsetTable = nil
			if bot {
				seq.OffsetTable = offsetTable32(offsets...)
			}
			got, err := registry.DecodeFrames(file.TransferSyntax.UID, pixeldata.PixelData{Encapsulated: true, Sequence: seq}, file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Data) != len(want.Data) {
				t.Fatal("frame count")
			}
			for i := range want.Data {
				if !bytes.Equal(got.Data[i], want.Data[i]) {
					t.Fatalf("frame %d differs from full independent reference", i)
				}
			}
		})
	}
}

func offsetTable32(offsets ...uint32) []byte {
	data := make([]byte, len(offsets)*4)
	for i, v := range offsets {
		binary.LittleEndian.PutUint32(data[i*4:], v)
	}
	return data
}

func TestSharedJPEGAssemblyRejectsExtraImagesUnderBOT(t *testing.T) {
	for _, c := range []Case{JPEGBaselineSmall(), JPEGExtendedSmall()} {
		t.Run(c.Name, func(t *testing.T) {
			pixel, err := c.PixelData()
			if err != nil {
				t.Fatal(err)
			}
			stream := append([]byte(nil), pixel.Sequence.Fragments[0]...)
			if len(stream)&1 != 0 {
				stream = append(stream, 0)
			}
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			for _, fragments := range [][][]byte{{append(append([]byte(nil), stream...), stream...)}, {stream, stream}} {
				pixel.Sequence = core.FragmentSequence{OffsetTable: offsetTable32(0), Fragments: fragments}
				got, err := registry.DecodeFrames(c.Syntax.UID, pixel, c.Object())
				if !errors.Is(err, encapsulated.ErrLayout) || len(got.Data) != 0 {
					t.Fatalf("extra image under BOT: %v", err)
				}
			}
		})
	}
}

func TestSharedAssemblyPreservesFrameCountError(t *testing.T) {
	for _, c := range []Case{JPEGBaselineSmall(), JPEGExtendedSmall(), JPEGLSLosslessSmall([]byte{0xff, 0xd8, 0xff, 0xd9}, []byte{0, 0})} {
		t.Run(c.Name, func(t *testing.T) {
			pixel, err := c.PixelData()
			if err != nil {
				t.Fatal(err)
			}
			obj := c.Object()
			obj.Put(stringElement(tagNumberOfFrames, core.VRIS, "2"))
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			if c.Syntax.UID == transfer.JPEGLSLossless.UID {
				if err := jpegls.Register(registry); err != nil {
					t.Fatal(err)
				}
			}
			got, err := registry.DecodeFrames(c.Syntax.UID, pixel, obj)
			if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) || !errors.Is(err, encapsulated.ErrFrameCount) || len(got.Data) != 0 {
				t.Fatalf("frame count compatibility: %v", err)
			}
		})
	}
}
