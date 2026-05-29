package video

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	dicomtags "github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCopyFragmentableH264MP4(t *testing.T) {
	payload := append([]byte{0, 0, 0, 20}, []byte("ftypisom00000000")...)
	file := videoFile(t, transfer.MPEG4HP41F, payload[:8], payload[8:])
	var got bytes.Buffer
	info, err := Copy(context.Background(), file, &got, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("payload = % X, want % X", got.Bytes(), payload)
	}
	if info.Codec != CodecH264 || info.Container != ContainerMP4 || info.Extension != ".mp4" {
		t.Fatalf("info = %#v", info)
	}
	if info.FragmentCount != 2 || info.Size != int64(len(payload)) || info.NumberOfFrames != 30 {
		t.Fatalf("counts = %#v", info)
	}
	if info.FrameRate != 25 || info.FrameTimeMillis != 40 {
		t.Fatalf("timing = %.3f fps / %.3f ms", info.FrameRate, info.FrameTimeMillis)
	}
}

func TestCopyDeferredPixelDataStreamsFragments(t *testing.T) {
	payload := make([]byte, 188*2)
	payload[0], payload[188] = 0x47, 0x47
	encoded, err := dicomtest.Part10File(transfer.HEVCMP51, videoElements(payload)...)
	if err != nil {
		t.Fatal(err)
	}
	source := bytes.NewReader(encoded)
	file, err := object.ReadFileWithOptions(source, object.ReadFileOptions{DeferPixelData: true})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var got bytes.Buffer
	info, err := Copy(context.Background(), file, &got, Limits{MaxBytes: int64(len(payload))})
	if err != nil {
		t.Fatal(err)
	}
	if info.Container != ContainerMPEGTransport || info.Extension != ".ts" || !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("deferred copy info/payload = %#v / % X", info, got.Bytes())
	}
}

func TestCopyRejectsNonFragmentableMultipleFragments(t *testing.T) {
	file := videoFile(t, transfer.MPEG4HP41,
		[]byte{0, 0, 0, 12, 'f', 't', 'y', 'p'},
		[]byte("isom"),
	)
	_, err := Copy(context.Background(), file, &bytes.Buffer{}, Limits{})
	if !errors.Is(err, ErrMalformedFragments) {
		t.Fatalf("error = %v, want ErrMalformedFragments", err)
	}
}

func TestCopyRejectsNonEmptyBasicOffsetTable(t *testing.T) {
	file := videoFile(t, transfer.MPEG4HP41, []byte{0, 0, 0, 12, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	elem, _ := file.Dataset.Get(core.TagPixelData)
	seq := elem.Value.(core.FragmentSequence)
	seq.OffsetTable = []byte{0, 0, 0, 0}
	file.Dataset.Put(core.Element{Header: elem.Header, Value: seq})
	_, err := Inspect(context.Background(), file, Limits{})
	if !errors.Is(err, ErrMalformedFragments) {
		t.Fatalf("error = %v, want ErrMalformedFragments", err)
	}
}

func TestCopyRejectsOversizedAndCancelledMedia(t *testing.T) {
	file := videoFile(t, transfer.MPEG2MPML, []byte{0, 0, 1, 0xB3, 1, 2, 3, 4})
	if _, err := Copy(context.Background(), file, &bytes.Buffer{}, Limits{MaxBytes: 4}); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("oversized error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Copy(ctx, file, &bytes.Buffer{}, Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestCopyRejectsH264ElementaryStream(t *testing.T) {
	file := videoFile(t, transfer.MPEG4HP41, []byte{0, 0, 1, 0xB3, 1, 2, 3, 4})
	if _, err := Copy(context.Background(), file, &bytes.Buffer{}, Limits{}); !errors.Is(err, ErrUnsupportedContainer) {
		t.Fatalf("error = %v, want ErrUnsupportedContainer", err)
	}
}

func TestMetadataUsesDeterministicFrameTimeVectorAverage(t *testing.T) {
	file := videoFile(t, transfer.MPEG4HP41, mp4TestPayload())
	file.Dataset.Remove(dicomtags.FrameTime)
	file.Dataset.Put(core.Element{
		Header: core.ElementHeader{Tag: dicomtags.FrameTimeVector, VR: core.VRDS},
		Value:  core.StringValue{"0", "40", "60"},
	})
	info, err := Metadata(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameTimeMillis != 50 || info.FrameRate != 20 {
		t.Fatalf("timing = %.3f ms / %.3f fps, want 50 ms / 20 fps", info.FrameTimeMillis, info.FrameRate)
	}
}

func TestMetadataUsesActualFrameDurationMilliseconds(t *testing.T) {
	file := videoFile(t, transfer.MPEG4HP41, mp4TestPayload())
	file.Dataset.Remove(dicomtags.FrameTime)
	file.Dataset.Put(core.Element{
		Header: core.ElementHeader{Tag: dicomtags.ActualFrameDuration, VR: core.VRIS},
		Value:  core.StringValue{"40"},
	})
	info, err := Metadata(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameTimeMillis != 40 || info.FrameRate != 25 {
		t.Fatalf("timing = %.3f ms / %.3f fps, want 40 ms / 25 fps", info.FrameTimeMillis, info.FrameRate)
	}
}

func videoFile(t *testing.T, syntax transfer.Syntax, fragments ...[]byte) *object.File {
	t.Helper()
	return &object.File{
		TransferSyntax: syntax,
		Dataset:        object.FromElements(videoElements(fragments...), nil),
	}
}

func mp4TestPayload() []byte {
	return []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm'}
}

func videoElements(fragments ...[]byte) []core.Element {
	return []core.Element{
		dicomtest.NewUIElement(dicomtags.SOPClassUID, "1.2.840.10008.5.1.4.1.1.77.1.4.1"),
		dicomtest.NewUShortElement(dicomtags.Rows, 480),
		dicomtest.NewUShortElement(dicomtags.Columns, 640),
		dicomtest.NewStringElement(dicomtags.NumberOfFrames, core.VRIS, "30"),
		dicomtest.NewStringElement(dicomtags.FrameTime, core.VRDS, "40"),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...),
	}
}
