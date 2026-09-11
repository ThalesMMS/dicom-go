package object

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCloneFileCopiesFileWithoutSharingDataset(t *testing.T) {
	src := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	clone, err := CloneFile(src)
	if err != nil {
		t.Fatalf("CloneFile() error = %v", err)
	}
	clone.Dataset.Put(core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("CLONE^PATIENT")))

	if got, _ := src.Dataset.GetString(core.NewTag(0x0010, 0x0010)); got == "CLONE^PATIENT" {
		t.Fatal("CloneFile() shared dataset storage with source")
	}
}

func TestCloneFileRejectsNilFile(t *testing.T) {
	_, err := CloneFile(nil)
	if !errors.Is(err, ErrNilFile) {
		t.Fatalf("CloneFile(nil) error = %v, want ErrNilFile", err)
	}
}

func TestCloneFileDoesNotShareMutableValueBuffers(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1115)
	nestedTag := core.NewTag(0x0010, 0x0010)
	elements := append(dicomtest.MinimalDataset(),
		core.Element{
			Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				core.NewRawElement(nestedTag, core.VRPN, []byte("SOURCE^PATIENT")),
			}}}},
		},
		core.Element{
			Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
			Value: core.FragmentSequence{
				OffsetTable: []byte{0, 0, 0, 0},
				Fragments:   [][]byte{{1, 2, 3, 4}, {5, 6}},
			},
		},
	)
	src := cloneTestFile(t, elements, transfer.JPEGBaseline)
	src.Preamble = bytes.Repeat([]byte{0xA5}, part10PreambleLength)

	clone, err := CloneFile(src)
	if err != nil {
		t.Fatalf("CloneFile() error = %v", err)
	}
	clone.Preamble[0] = 0
	clonePixel, _ := clone.Dataset.Get(core.TagPixelData)
	cloneFragments := clonePixel.Value.(core.FragmentSequence)
	cloneFragments.OffsetTable[0] = 9
	cloneFragments.Fragments[0][0] = 9
	cloneItems, _ := clone.Dataset.GetSequence(sequenceTag)
	cloneName, _ := cloneItems[0].Get(nestedTag)
	cloneNameRaw, _ := cloneName.RawBytes()
	cloneNameRaw[0] = 'X'

	if src.Preamble[0] != 0xA5 {
		t.Fatal("clone preamble aliases source")
	}
	sourcePixel, _ := src.Dataset.Get(core.TagPixelData)
	sourceFragments := sourcePixel.Value.(core.FragmentSequence)
	if sourceFragments.OffsetTable[0] != 0 || sourceFragments.Fragments[0][0] != 1 {
		t.Fatal("clone fragment buffers alias source")
	}
	sourceItems, _ := src.Dataset.GetSequence(sequenceTag)
	if got, _ := sourceItems[0].GetString(nestedTag); got != "SOURCE^PATIENT" {
		t.Fatalf("source nested value = %q after clone mutation", got)
	}
}

func TestCloneFileWithOptionsEnforcesResourceLimits(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1115)
	privateTag := core.NewTag(0x0011, 0x1010)
	nested := core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{{
			Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				core.NewRawElement(privateTag, core.VRLO, []byte("nested")),
			}}}},
		}}}}},
	}
	fragments := core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: [][]byte{{1, 2}, {3, 4}}},
	}
	elements := append(dicomtest.MinimalDataset(),
		core.NewRawElement(privateTag, core.VRLO, bytes.Repeat([]byte{'A'}, 32)),
		nested,
		fragments,
	)
	src := cloneTestFile(t, elements, transfer.JPEGBaseline)

	tests := []struct {
		name string
		opts CloneFileOptions
		want error
	}{
		{name: "element bytes", opts: CloneFileOptions{MaxElementBytes: 16}, want: parser.ErrMaxElementBytesExceeded},
		{name: "pixel bytes", opts: CloneFileOptions{MaxPixelDataBytes: 3}, want: parser.ErrMaxPixelDataBytesExceeded},
		{name: "total bytes", opts: CloneFileOptions{MaxTotalBytes: 64}, want: parser.ErrMaxTotalBytesExceeded},
		{name: "sequence depth", opts: CloneFileOptions{MaxSequenceDepth: 1}, want: parser.ErrMaxDepthExceeded},
		{name: "elements", opts: CloneFileOptions{MaxElements: 4}, want: parser.ErrMaxElementsExceeded},
		{name: "items", opts: CloneFileOptions{MaxItems: 1}, want: parser.ErrMaxElementsExceeded},
		{name: "fragments", opts: CloneFileOptions{MaxFragments: 1}, want: parser.ErrMaxFragmentsExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CloneFileWithOptions(src, tt.opts)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CloneFileWithOptions() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCloneFileWithOptionsRejectsNegativeLimits(t *testing.T) {
	src := cloneTestFile(t, dicomtest.MinimalDataset(), transfer.ExplicitVRLittleEndian)
	if _, err := CloneFileWithOptions(src, CloneFileOptions{MaxElements: -1}); !errors.Is(err, ErrInvalidCloneOptions) {
		t.Fatalf("CloneFileWithOptions() error = %v, want ErrInvalidCloneOptions", err)
	}
}

func TestCloneFileStreamsDeferredPixelDataIntoIndependentClone(t *testing.T) {
	pixels := bytes.Repeat([]byte{1, 2, 3, 4}, 1024)
	elements := append(dicomtest.MinimalDataset(), core.NewRawElement(core.TagPixelData, core.VROB, pixels))
	materialized := cloneTestFile(t, elements, transfer.ExplicitVRLittleEndian)
	var encoded bytes.Buffer
	if err := WriteFile(&encoded, materialized); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	deferred, err := ReadFileWithOptions(bytes.NewReader(encoded.Bytes()), ReadFileOptions{DeferPixelData: true})
	if err != nil {
		t.Fatalf("ReadFileWithOptions: %v", err)
	}
	deferredPixel, _ := deferred.Dataset.Get(core.TagPixelData)
	if deferredPixel.Value != nil {
		t.Fatalf("deferred Pixel Data value type = %T, want nil", deferredPixel.Value)
	}
	if _, err := CloneFileWithOptions(deferred, CloneFileOptions{MaxPixelDataBytes: int64(len(pixels) - 1)}); !errors.Is(err, parser.ErrMaxPixelDataBytesExceeded) {
		t.Fatalf("bounded deferred clone error = %v, want ErrMaxPixelDataBytesExceeded", err)
	}

	clone, err := CloneFile(deferred)
	if err != nil {
		t.Fatalf("CloneFile: %v", err)
	}
	got, ok := clone.Dataset.GetRaw(core.TagPixelData)
	if !ok || !bytes.Equal(got, pixels) {
		t.Fatalf("cloned Pixel Data length = %d, want %d", len(got), len(pixels))
	}
	got[0] = 99
	again, _ := clone.Dataset.GetRaw(core.TagPixelData)
	if again[0] != pixels[0] {
		t.Fatal("GetRaw result unexpectedly aliases cloned Pixel Data")
	}
	var clonedEncoding bytes.Buffer
	if err := WriteFile(&clonedEncoding, clone); err != nil {
		t.Fatalf("WriteFile(clone): %v", err)
	}
	if !bytes.Equal(clonedEncoding.Bytes(), encoded.Bytes()) {
		t.Fatal("streaming clone changed the encoded file or element order")
	}
}

func cloneTestFile(t *testing.T, elements []core.Element, syntax transfer.Syntax) *File {
	t.Helper()
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: elements}, std.Dictionary),
		TransferSyntax: syntax,
	}
	if err := file.RebuildFileMeta(); err != nil {
		t.Fatalf("RebuildFileMeta: %v", err)
	}
	return file
}
