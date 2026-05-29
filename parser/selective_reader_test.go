package parser

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestSelectiveReaderMaterializeSkipAndStop(t *testing.T) {
	patientName := core.NewTag(0x0010, 0x0010)
	patientID := core.NewTag(0x0010, 0x0020)
	studyDescription := core.NewTag(0x0008, 0x1030)
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewStringElement(patientName, core.VRPN, "AB"),
		dicomtest.NewStringElement(patientID, core.VRLO, "CDEF"),
		dicomtest.NewStringElement(studyDescription, core.VRLO, "EF"),
	)

	var calls []struct {
		path   validation.Path
		header core.ElementHeader
		offset int64
	}
	reader, err := NewSelectiveReader(
		context.Background(),
		bytes.NewReader(data),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary, MaxElementBytes: 2},
		SelectiveReaderOptions{Select: func(_ context.Context, path validation.Path, header core.ElementHeader, offset int64) (SelectiveDisposition, error) {
			calls = append(calls, struct {
				path   validation.Path
				header core.ElementHeader
				offset int64
			}{path: path.Clone(), header: header, offset: offset})
			switch header.Tag {
			case patientName:
				return SelectiveMaterialize, nil
			case patientID:
				return SelectiveSkip, nil
			case studyDescription:
				return SelectiveStop, nil
			default:
				return SelectiveMaterialize, nil
			}
		}},
	)
	if err != nil {
		t.Fatalf("NewSelectiveReader() error = %v", err)
	}

	first, err := reader.Next()
	if err != nil {
		t.Fatalf("first Next() error = %v", err)
	}
	if first.Kind != TokenElement || first.Header.Tag != patientName || first.Element.Value == nil {
		t.Fatalf("first token = %#v, want materialized Patient Name", first)
	}

	second, err := reader.Next()
	if err != nil {
		t.Fatalf("second Next() error = %v", err)
	}
	if second.Kind != TokenElement || second.Header.Tag != patientID || second.Element.Value != nil {
		t.Fatalf("second token = %#v, want skipped Patient ID", second)
	}

	stop, err := reader.Next()
	if err != nil {
		t.Fatalf("third Next() error = %v", err)
	}
	if stop.Kind != TokenStop || stop.Header.Tag != studyDescription {
		t.Fatalf("third token = %#v, want terminal Study Description", stop)
	}
	if got := stop.Kind.String(); got != "stop" {
		t.Fatalf("TokenStop.String() = %q, want stop", got)
	}
	if got, want := reader.Position(), stop.Offset+definedHeaderLength(transfer.ExplicitVRLittleEndian, core.VRLO); got != want {
		t.Fatalf("position after stop = %d, want header end %d", got, want)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after stop error = %v, want io.EOF", err)
	}

	if len(calls) != 3 {
		t.Fatalf("selector calls = %d, want 3", len(calls))
	}
	for i, call := range calls {
		if len(call.path) != 1 || call.path[0].Tag != call.header.Tag || call.path[0].ItemIndex != validation.NoItem {
			t.Fatalf("call %d path = %#v, want top-level header path", i, call.path)
		}
		if call.offset < 0 {
			t.Fatalf("call %d offset = %d, want non-negative", i, call.offset)
		}
	}
}

type selectiveCountingReadSeeker struct {
	*bytes.Reader
	bytesRead int64
}

func (r *selectiveCountingReadSeeker) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

type selectiveCountingReader struct {
	r         io.Reader
	bytesRead int64
}

type selectiveOpaqueReadSeeker struct {
	reader    *bytes.Reader
	bytesRead int64
}

func (r *selectiveOpaqueReadSeeker) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func (r *selectiveOpaqueReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return r.reader.Seek(offset, whence)
}

func (r *selectiveCountingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func TestSelectiveReaderSkipSeeksWithoutReadingPayload(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	value := []byte("0123456789ABCDEF")
	data := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, uint32(len(value)), value)
	source := &selectiveCountingReadSeeker{Reader: bytes.NewReader(data)}
	reader, err := NewSelectiveReader(context.Background(), source, transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenElement || tok.Element.Value != nil {
		t.Fatalf("token = %#v, want skipped element", tok)
	}
	if got, want := source.bytesRead, definedHeaderLength(transfer.ExplicitVRLittleEndian, core.VRLO); got != want {
		t.Fatalf("source bytes read = %d, want header-only %d", got, want)
	}
	if got, want := reader.Position(), int64(len(data)); got != want {
		t.Fatalf("position = %d, want %d", got, want)
	}
}

func TestSelectiveReaderSkipUsesSeekWhenSourceSizeIsNotExposed(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	value := []byte("0123456789ABCDEF")
	data := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, uint32(len(value)), value)
	source := &selectiveOpaqueReadSeeker{reader: bytes.NewReader(data)}
	reader, err := NewSelectiveReader(context.Background(), source, transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	if got, want := source.bytesRead, definedHeaderLength(transfer.ExplicitVRLittleEndian, core.VRLO); got != want {
		t.Fatalf("source bytes read = %d, want header-only %d", got, want)
	}
}

func TestSelectiveReaderStreamingSkipDiscardsPayload(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	value := []byte("01234567")
	data := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, uint32(len(value)), value)
	source := &selectiveCountingReader{r: bytes.NewBuffer(data)}
	reader, err := NewSelectiveReader(context.Background(), source, transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	if got, want := source.bytesRead, int64(len(data)); got != want {
		t.Fatalf("source bytes read = %d, want full stream %d", got, want)
	}
}

func TestSelectiveReaderSkipHonorsTruncationAndTotalLimit(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	header := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, 8, nil)
	headerLength := definedHeaderLength(transfer.ExplicitVRLittleEndian, core.VRLO)

	for _, tc := range []struct {
		name       string
		source     func([]byte) io.Reader
		opts       ReaderOptions
		wantErr    error
		wantOffset int64
	}{
		{
			name:       "seekable truncation",
			source:     func(data []byte) io.Reader { return bytes.NewReader(data) },
			wantErr:    io.ErrUnexpectedEOF,
			wantOffset: headerLength + 2,
		},
		{
			name:       "streaming truncation",
			source:     func(data []byte) io.Reader { return bytes.NewBuffer(data) },
			wantErr:    io.ErrUnexpectedEOF,
			wantOffset: headerLength + 2,
		},
		{
			name:       "seekable total limit",
			source:     func(data []byte) io.Reader { return bytes.NewReader(data) },
			opts:       ReaderOptions{MaxTotalBytes: headerLength + 3},
			wantErr:    ErrMaxTotalBytesExceeded,
			wantOffset: headerLength + 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(append([]byte(nil), header...), 0x01, 0x02)
			if errors.Is(tc.wantErr, ErrMaxTotalBytesExceeded) {
				data = append(data, make([]byte, 6)...)
			}
			reader, err := NewSelectiveReader(context.Background(), tc.source(data), transfer.ExplicitVRLittleEndian, tc.opts, SelectiveReaderOptions{
				Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
					return SelectiveSkip, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Next(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Next() error = %v, want %v", err, tc.wantErr)
			}
			if got := reader.Position(); got != tc.wantOffset {
				t.Fatalf("position = %d, want %d", got, tc.wantOffset)
			}
		})
	}
}

func TestSelectiveReaderSkipHonorsOddLengthAndElementCount(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	odd := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, 3, []byte("odd"))
	reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(odd), transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, ErrOddElementLength) {
		t.Fatalf("odd skip error = %v, want ErrOddElementLength", err)
	}

	even := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, 2, []byte("ok"))
	twoElements := append(append([]byte(nil), even...), even...)
	reader, err = NewSelectiveReader(context.Background(), bytes.NewReader(twoElements), transfer.ExplicitVRLittleEndian, ReaderOptions{MaxElements: 1}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, ErrMaxElementsExceeded) {
		t.Fatalf("second skip error = %v, want ErrMaxElementsExceeded", err)
	}
}

func TestSelectiveReaderReportsNestedItemPaths(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	leafTag := core.NewTag(0x0010, 0x0020)
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewSequenceElement(sequenceTag,
			core.DataSet{Elements: []core.Element{dicomtest.NewStringElement(leafTag, core.VRLO, "A0")}},
			core.DataSet{Elements: []core.Element{dicomtest.NewStringElement(leafTag, core.VRLO, "A1")}},
		),
	)

	var paths []validation.Path
	reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary}, SelectiveReaderOptions{
		Select: func(_ context.Context, path validation.Path, _ core.ElementHeader, _ int64) (SelectiveDisposition, error) {
			paths = append(paths, path.Clone())
			return SelectiveMaterialize, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAllTokens(reader); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("selector paths = %d, want SQ plus two leaves", len(paths))
	}
	for i, itemIndex := range []int{0, 1} {
		path := paths[i+1]
		want := validation.Path{
			{Tag: sequenceTag, ItemIndex: itemIndex},
			{Tag: leafTag, ItemIndex: validation.NoItem},
		}
		if !reflect.DeepEqual(path, want) {
			t.Fatalf("leaf path %d = %#v, want %#v", i, path, want)
		}
	}
}

func TestSelectiveReaderStopsBeforePixelPayloads(t *testing.T) {
	tests := []struct {
		name   string
		tag    core.Tag
		vr     core.VR
		syntax transfer.Syntax
		data   func() []byte
	}{
		{
			name: "integer pixel data", tag: core.TagPixelData, vr: core.VROB, syntax: transfer.ExplicitVRLittleEndian,
			data: func() []byte {
				return definedElementBytes(transfer.ExplicitVRLittleEndian, core.TagPixelData, core.VROB, 4, []byte{1, 2, 3, 4})
			},
		},
		{
			name: "float pixel data", tag: tagFloatPixelData, vr: core.VROF, syntax: transfer.ExplicitVRLittleEndian,
			data: func() []byte {
				return definedElementBytes(transfer.ExplicitVRLittleEndian, tagFloatPixelData, core.VROF, 4, []byte{1, 2, 3, 4})
			},
		},
		{
			name: "double float pixel data", tag: tagDoubleFloatPixelData, vr: core.VROD, syntax: transfer.ExplicitVRLittleEndian,
			data: func() []byte {
				return definedElementBytes(transfer.ExplicitVRLittleEndian, tagDoubleFloatPixelData, core.VROD, 8, make([]byte, 8))
			},
		},
		{
			name: "encapsulated pixel data", tag: core.TagPixelData, vr: core.VROB, syntax: transfer.JPEGBaseline,
			data: func() []byte { return encapsulatedPixelDataBytes(nil, []byte{1, 2}) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.data()
			source := &selectiveCountingReadSeeker{Reader: bytes.NewReader(data)}
			reader, err := NewSelectiveReader(context.Background(), source, tc.syntax, ReaderOptions{MaxElementBytes: 1}, SelectiveReaderOptions{
				Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
					return SelectiveStop, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			if tok.Kind != TokenStop || tok.Header.Tag != tc.tag || tok.Header.VR != tc.vr {
				t.Fatalf("stop token = %#v", tok)
			}
			wantHeaderBytes := definedHeaderLength(tc.syntax, tc.vr)
			if got := source.bytesRead; got != wantHeaderBytes {
				t.Fatalf("bytes read = %d, want header-only %d", got, wantHeaderBytes)
			}
		})
	}
}

func TestSelectiveReaderSkipsNestedEncapsulatedPixelDataAsOneToken(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	metadataTag := core.NewTag(0x0020, 0x000D)
	fragment := bytes.Repeat([]byte{0x5a}, 64<<10)
	nestedPixel := dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragment, fragment)
	data := dicomtest.EncodeElements(transfer.JPEGBaseline,
		dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: []core.Element{nestedPixel}}),
		dicomtest.NewStringElement(metadataTag, core.VRUI, "1.2.3.4"),
	)
	source := &selectiveCountingReadSeeker{Reader: bytes.NewReader(data)}
	var paths []validation.Path
	reader, err := NewSelectiveReader(context.Background(), source, transfer.JPEGBaseline, ReaderOptions{Dictionary: std.Dictionary}, SelectiveReaderOptions{
		Select: func(_ context.Context, path validation.Path, header core.ElementHeader, _ int64) (SelectiveDisposition, error) {
			paths = append(paths, path.Clone())
			if header.Tag == core.TagPixelData {
				return SelectiveSkip, nil
			}
			return SelectiveMaterialize, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := readAllTokens(reader)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenKinds(t, tokens, []TokenKind{
		TokenStartSequence, TokenStartItem, TokenElement, TokenEndItem, TokenEndSequence, TokenElement,
	})
	if tokens[2].Header.Tag != core.TagPixelData || tokens[2].Element.Value != nil {
		t.Fatalf("nested pixel token = %#v, want one skipped Pixel Data element", tokens[2])
	}
	if tokens[5].Header.Tag != metadataTag || tokens[5].Element.StringValue() != "1.2.3.4" {
		t.Fatalf("metadata after nested Pixel Data = %#v", tokens[5])
	}
	if len(paths) != 3 {
		t.Fatalf("selector calls = %d, want outer SQ, nested Pixel Data, and top-level metadata", len(paths))
	}
	wantPixelPath := validation.Path{
		{Tag: sequenceTag, ItemIndex: 0},
		{Tag: core.TagPixelData, ItemIndex: validation.NoItem},
	}
	if !reflect.DeepEqual(paths[1], wantPixelPath) {
		t.Fatalf("nested Pixel Data path = %#v, want %#v", paths[1], wantPixelPath)
	}
	if source.bytesRead >= 1024 {
		t.Fatalf("seekable nested skip read %d bytes, want headers only", source.bytesRead)
	}
}

func TestSelectiveReaderEncapsulatedSkipHonorsFragmentAndSourceLimits(t *testing.T) {
	data := encapsulatedPixelDataBytes(nil, []byte{0x01, 0x02}, []byte{0x03, 0x04})
	reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data), transfer.JPEGBaseline, ReaderOptions{MaxFragments: 1}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, ErrMaxFragmentsExceeded) {
		t.Fatalf("fragment-limit error = %v, want ErrMaxFragmentsExceeded", err)
	}

	truncated := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.TagPixelData, core.VROB, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 8),
		{0x01, 0x02},
	}, nil)
	reader, err = NewSelectiveReader(context.Background(), bytes.NewReader(truncated), transfer.JPEGBaseline, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated fragment error = %v, want io.ErrUnexpectedEOF", err)
	}

	oddFragment := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.TagPixelData, core.VROB, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 3),
		{0x01, 0x02, 0x03},
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)
	reader, err = NewSelectiveReader(context.Background(), bytes.NewReader(oddFragment), transfer.JPEGBaseline, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, ErrOddElementLength) {
		t.Fatalf("odd fragment error = %v, want ErrOddElementLength", err)
	}

	limited := encapsulatedPixelDataBytes(nil, bytes.Repeat([]byte{0x09}, 32))
	reader, err = NewSelectiveReader(context.Background(), bytes.NewReader(limited), transfer.JPEGBaseline, ReaderOptions{MaxTotalBytes: 32}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, ErrMaxTotalBytesExceeded) {
		t.Fatalf("encapsulated MaxTotal error = %v, want ErrMaxTotalBytesExceeded", err)
	}
}

func TestSelectiveReaderStreamingEncapsulatedSkipConsumesAndContinues(t *testing.T) {
	metadataTag := core.NewTag(0x0020, 0x000D)
	data := append(
		encapsulatedPixelDataBytes(nil, bytes.Repeat([]byte{0x7c}, 4096)),
		definedElementBytes(transfer.JPEGBaseline, metadataTag, core.VRUI, 8, []byte("1.2.3.4\x00"))...,
	)
	source := &selectiveCountingReader{r: bytes.NewBuffer(data)}
	reader, err := NewSelectiveReader(context.Background(), source, transfer.JPEGBaseline, ReaderOptions{Dictionary: std.Dictionary}, SelectiveReaderOptions{
		Select: func(_ context.Context, _ validation.Path, header core.ElementHeader, _ int64) (SelectiveDisposition, error) {
			if header.Tag == core.TagPixelData {
				return SelectiveSkip, nil
			}
			return SelectiveMaterialize, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := readAllTokens(reader)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenKinds(t, tokens, []TokenKind{TokenElement, TokenElement})
	if tokens[0].Header.Tag != core.TagPixelData || tokens[0].Element.Value != nil || tokens[1].Header.Tag != metadataTag {
		t.Fatalf("tokens = %#v", tokens)
	}
	if got, want := source.bytesRead, int64(len(data)); got != want {
		t.Fatalf("streaming bytes read = %d, want %d", got, want)
	}
}

func TestSelectiveReaderSupportsImplicitAndBigEndian(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	for _, syntax := range []transfer.Syntax{transfer.ImplicitVRLittleEndian, transfer.ExplicitVRBigEndian} {
		t.Run(syntax.Name, func(t *testing.T) {
			data := definedElementBytes(syntax, tag, core.VRLO, 4, []byte("ABCD"))
			var gotHeader core.ElementHeader
			reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data), syntax, ReaderOptions{Dictionary: std.Dictionary}, SelectiveReaderOptions{
				Select: func(_ context.Context, _ validation.Path, header core.ElementHeader, _ int64) (SelectiveDisposition, error) {
					gotHeader = header
					return SelectiveSkip, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Next(); err != nil {
				t.Fatal(err)
			}
			if gotHeader.Tag != tag || gotHeader.VR != core.VRLO || gotHeader.Length != 4 {
				t.Fatalf("selector header = %#v", gotHeader)
			}
		})
	}
}

func TestSelectiveReaderContextAndCallbackFailuresAreRedacted(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	data := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, 2, []byte("OK"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSelectiveReader(ctx, bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveMaterialize, nil
		},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled constructor error = %v", err)
	}

	for _, tc := range []struct {
		name    string
		selectf SelectiveReaderSelector
		want    error
	}{
		{
			name: "callback error",
			selectf: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
				return SelectiveMaterialize, errors.New("PHI-CANARY-patient-name")
			},
			want: ErrSelectiveReaderCallback,
		},
		{
			name: "callback panic",
			selectf: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
				panic("PHI-CANARY-patient-name")
			},
			want: ErrSelectiveReaderCallback,
		},
		{
			name: "invalid disposition",
			selectf: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
				return SelectiveDisposition(255), nil
			},
			want: ErrSelectiveDisposition,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{Select: tc.selectf})
			if err != nil {
				t.Fatal(err)
			}
			_, err = reader.Next()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Next() error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "PHI-CANARY") {
				t.Fatalf("error leaked callback content: %v", err)
			}
		})
	}

	ctx, cancel = context.WithCancel(context.Background())
	reader, err := NewSelectiveReader(ctx, bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			cancel()
			return SelectiveMaterialize, errors.New("PHI-CANARY-after-cancel")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("callback cancellation error = %v, want context.Canceled", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	reader, err = NewSelectiveReader(ctx, bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			cancel()
			panic("PHI-CANARY-after-cancel-panic")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("callback panic after cancellation error = %v, want context.Canceled", err)
	}
}

type selectiveCancelingReader struct {
	reader      *bytes.Reader
	cancel      context.CancelFunc
	cancelAfter int64
	read        int64
}

func (r *selectiveCancelingReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	n, err := r.reader.Read(p)
	r.read += int64(n)
	if r.read >= r.cancelAfter {
		r.cancel()
	}
	return n, err
}

func TestSelectiveReaderCancellationInterruptsMaterialization(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	data := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VRLO, 8, []byte("12345678"))
	headerLength := definedHeaderLength(transfer.ExplicitVRLittleEndian, core.VRLO)
	ctx, cancel := context.WithCancel(context.Background())
	source := &selectiveCancelingReader{reader: bytes.NewReader(data), cancel: cancel, cancelAfter: headerLength + 1}
	reader, err := NewSelectiveReader(ctx, source, transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveMaterialize, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next() error = %v, want context.Canceled", err)
	}
	if got, want := reader.Position(), headerLength+1; got != want {
		t.Fatalf("position after cancellation = %d, want %d", got, want)
	}
}

func TestSelectiveReaderRejectsInvalidOptionsAndSequenceSkip(t *testing.T) {
	if _, err := NewSelectiveReader(context.Background(), nil, transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{}); !errors.Is(err, ErrSelectiveReaderOptions) {
		t.Fatalf("nil source error = %v", err)
	}
	if _, err := NewSelectiveReader(context.Background(), bytes.NewReader(nil), transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{}); !errors.Is(err, ErrSelectiveReaderOptions) {
		t.Fatalf("nil selector error = %v", err)
	}

	sequenceTag := core.NewTag(0x0008, 0x1111)
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.NewSequenceElement(sequenceTag))
	reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); !errors.Is(err, ErrSelectiveDisposition) {
		t.Fatalf("sequence skip error = %v, want ErrSelectiveDisposition", err)
	}
}

func TestSelectiveReaderStopDoesNotValidateDeclaredValueLength(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	data := definedElementBytes(transfer.ExplicitVRLittleEndian, tag, core.VROB, uint32(^uint32(0)-1), nil)
	reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{MaxElementBytes: 1, MaxTotalBytes: int64(len(data))}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveStop, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenStop || tok.Header.Length != core.Length(^uint32(0)-1) {
		t.Fatalf("stop token = %#v", tok)
	}
}

func TestSelectiveReaderBigEndianOffsetUsesAbsoluteBase(t *testing.T) {
	const baseOffset int64 = 4096
	tag := core.NewTag(0x0010, 0x0020)
	data := definedElementBytes(transfer.ExplicitVRBigEndian, tag, core.VRLO, 2, []byte("OK"))
	var gotOffset int64
	reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data), transfer.ExplicitVRBigEndian, ReaderOptions{BaseOffset: baseOffset}, SelectiveReaderOptions{
		Select: func(_ context.Context, _ validation.Path, _ core.ElementHeader, offset int64) (SelectiveDisposition, error) {
			gotOffset = offset
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	if gotOffset != baseOffset {
		t.Fatalf("selector offset = %d, want %d", gotOffset, baseOffset)
	}
}

func TestSelectiveReaderReadsBigEndianLength(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0020)
	var data bytes.Buffer
	writeTag(&data, binary.BigEndian, tag)
	data.WriteString(core.VRLO.String())
	writeUint16(&data, binary.BigEndian, 2)
	data.WriteString("OK")
	reader, err := NewSelectiveReader(context.Background(), bytes.NewReader(data.Bytes()), transfer.ExplicitVRBigEndian, ReaderOptions{}, SelectiveReaderOptions{
		Select: func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error) {
			return SelectiveSkip, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
}
