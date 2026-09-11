package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReaderLimitsRejectMaxSequenceDepth(t *testing.T) {
	nestedSeq := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x2222), core.VRSQ, [2]byte{}, 0xFFFFFFFF)
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		nestedSeq,
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:       std.Dictionary,
		MaxSequenceDepth: 2,
	})

	for i := 0; i < 2; i++ {
		if _, err := reader.Next(); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestReaderLimitsRejectMaxElements(t *testing.T) {
	stream := bytes.Join([][]byte{
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "ONE^TEST"), transfer.ExplicitVRLittleEndian),
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0020), "TWO^TEST"), transfer.ExplicitVRLittleEndian),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:  std.Dictionary,
		MaxElements: 1,
	})

	if _, err := reader.Next(); err != nil {
		t.Fatalf("first element: %v", err)
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxElementsExceeded) {
		t.Fatalf("expected ErrMaxElementsExceeded, got %v", err)
	}
}

func TestReaderLimitsRejectMaxFragments(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(encapsulatedPixelDataBytes(nil, []byte{0x01, 0x02}, []byte{0x03, 0x04})),
		transfer.JPEGBaseline,
		ReaderOptions{
			Dictionary:   std.Dictionary,
			MaxFragments: 1,
		},
	)

	_, err := reader.ReadDataSet()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxFragmentsExceeded) {
		t.Fatalf("expected ErrMaxFragmentsExceeded, got %v", err)
	}
}

func TestReaderLimitsRejectMaxFragmentsBeforeReadingRejectedPayload(t *testing.T) {
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.TagPixelData, core.VROB, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 2),
		{0x01, 0x02},
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFE),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.JPEGBaseline, ReaderOptions{
		Dictionary:   std.Dictionary,
		MaxFragments: 1,
	})

	_, err := reader.ReadDataSet()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxFragmentsExceeded) {
		t.Fatalf("expected ErrMaxFragmentsExceeded, got %v", err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected fragment-limit error before payload read, got %v", err)
	}
}

func TestReaderLimitsRejectMaxTotalBytes(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:    std.Dictionary,
		MaxTotalBytes: int64(len(buf)),
	})

	if _, err := reader.Next(); err != nil {
		t.Fatalf("first element: %v", err)
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxTotalBytesExceeded) {
		t.Fatalf("expected ErrMaxTotalBytesExceeded, got %v", err)
	}
}

func TestReaderLimitsAllowSyntheticDelimitersAtMaxTotalBytesBoundary(t *testing.T) {
	inner := definedElementBytes(
		transfer.ExplicitVRLittleEndian,
		core.NewTag(0x0010, 0x0010),
		core.VRPN,
		4,
		[]byte("TEST"),
	)
	item := dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner)))
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, uint32(len(item)+len(inner))),
		item,
		inner,
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:    std.Dictionary,
		MaxTotalBytes: int64(len(stream)),
	})

	wantKinds := []TokenKind{
		TokenStartSequence,
		TokenStartItem,
		TokenElement,
		TokenEndItem,
		TokenEndSequence,
	}
	for i, want := range wantKinds {
		tok, err := reader.Next()
		if err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if tok.Kind != want {
			t.Fatalf("token %d kind = %v, want %v", i, tok.Kind, want)
		}
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxTotalBytesExceeded) {
		t.Fatalf("expected ErrMaxTotalBytesExceeded, got %v", err)
	}
}

func TestReaderLimitsRejectMaxElementBytes(t *testing.T) {
	buf := definedElementBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("TEST"))
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:      std.Dictionary,
		MaxElementBytes: 2,
	})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxElementBytesExceeded) {
		t.Fatalf("expected ErrMaxElementBytesExceeded, got %v", err)
	}
}

func TestReaderLimitsRejectNativePixelDataBytesBeforePayloadRead(t *testing.T) {
	buf := definedElementBytes(transfer.ExplicitVRLittleEndian, core.TagPixelData, core.VROB, 4, []byte{1, 2})
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:        std.Dictionary,
		MaxElementBytes:   1,
		MaxPixelDataBytes: 3,
	})

	_, err := reader.Next()
	if !errors.Is(err, ErrMaxPixelDataBytesExceeded) {
		t.Fatalf("pixel limit error = %v, want ErrMaxPixelDataBytesExceeded", err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("pixel limit was checked after reading the rejected payload: %v", err)
	}
}

func TestReaderLimitsAllowPixelDataOverrideAboveGeneralElementLimit(t *testing.T) {
	buf := definedElementBytes(transfer.ExplicitVRLittleEndian, core.TagPixelData, core.VROB, 4, []byte{1, 2, 3, 4})
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:        std.Dictionary,
		MaxElementBytes:   2,
		MaxPixelDataBytes: 4,
	})
	if _, err := reader.Next(); err != nil {
		t.Fatalf("pixel override error = %v", err)
	}
}

func TestReaderLimitsRejectDeformableVectorGridBeforeNestedPayloadRead(t *testing.T) {
	grid := dicomtest.NewSequenceElement(
		core.NewTag(0x0064, 0x0005),
		core.DataSet{Elements: []core.Element{
			dicomtest.BytesElement(tagVectorGridData, core.VROF, make([]byte, 8)),
		}},
	)
	stream := dicomtest.EncodeElement(grid, transfer.ExplicitVRLittleEndian)
	stream = stream[:len(stream)-2]
	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:                   std.Dictionary,
		MaxDeformableVectorGridBytes: 4,
	})

	_, err := reader.ReadDataSet()
	if !errors.Is(err, ErrMaxDeformableVectorGridBytesExceeded) {
		t.Fatalf("vector grid limit error = %v, want ErrMaxDeformableVectorGridBytesExceeded", err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("vector grid limit was checked after reading the rejected payload: %v", err)
	}
}

func TestReaderLimitsAllowVectorGridOverrideAboveGeneralElementLimit(t *testing.T) {
	grid := dicomtest.BytesElement(tagVectorGridData, core.VROF, make([]byte, 12))
	reader := NewReader(
		bytes.NewReader(dicomtest.EncodeElement(grid, transfer.ExplicitVRLittleEndian)),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{
			Dictionary:                   std.Dictionary,
			MaxElementBytes:              4,
			MaxDeformableVectorGridBytes: 12,
		},
	)
	if _, err := reader.Next(); err != nil {
		t.Fatalf("vector grid override error = %v", err)
	}
}

func TestReaderElementBudgetsDisableWholeDatasetSlurp(t *testing.T) {
	prefix := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "OK"),
		transfer.ExplicitVRLittleEndian,
	)
	tests := []struct {
		name    string
		element core.Element
		opts    ReaderOptions
		wantErr error
	}{
		{
			name:    "general element",
			element: dicomtest.BytesElement(core.NewTag(0x0011, 0x1010), core.VROB, make([]byte, 8)),
			opts:    ReaderOptions{MaxElementBytes: 4},
			wantErr: ErrMaxElementBytesExceeded,
		},
		{
			name:    "pixel data",
			element: dicomtest.BytesElement(core.TagPixelData, core.VROB, make([]byte, 8)),
			opts:    ReaderOptions{MaxPixelDataBytes: 4},
			wantErr: ErrMaxPixelDataBytesExceeded,
		},
		{
			name:    "deformable vector grid",
			element: dicomtest.BytesElement(tagVectorGridData, core.VROF, make([]byte, 12)),
			opts:    ReaderOptions{MaxDeformableVectorGridBytes: 8},
			wantErr: ErrMaxDeformableVectorGridBytesExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := append(append([]byte(nil), prefix...), dicomtest.EncodeElement(test.element, transfer.ExplicitVRLittleEndian)...)
			source := &countingSizedReadSeeker{Reader: *bytes.NewReader(stream)}
			test.opts.Dictionary = std.Dictionary
			reader := NewReader(source, transfer.ExplicitVRLittleEndian, test.opts)
			if _, err := reader.Next(); err != nil {
				t.Fatalf("prefix: %v", err)
			}
			if source.bytesRead >= len(stream) {
				t.Fatalf("first element read %d of %d bytes; remaining dataset was materialized before its budget check", source.bytesRead, len(stream))
			}
			if _, err := reader.Next(); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

type countingSizedReadSeeker struct {
	bytes.Reader
	bytesRead int
}

func (r *countingSizedReadSeeker) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += n
	return n, err
}

func TestReaderLimitsRejectCumulativeEncapsulatedPixelDataBytes(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(encapsulatedPixelDataBytes(nil, []byte{1, 2}, []byte{3, 4})),
		transfer.JPEGBaseline,
		ReaderOptions{Dictionary: std.Dictionary, MaxPixelDataBytes: 3},
	)
	_, err := reader.ReadDataSet()
	if !errors.Is(err, ErrMaxPixelDataBytesExceeded) {
		t.Fatalf("encapsulated pixel limit error = %v, want ErrMaxPixelDataBytesExceeded", err)
	}
}

func TestReaderLimitsRejectPartialElementEOF(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	truncated := buf[:len(buf)-2]
	reader := NewReader(bytes.NewReader(truncated), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected non-EOF error, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
}

func TestReaderLimitsReadsDefinedValueIncrementallyOnTruncatedLength(t *testing.T) {
	var stream bytes.Buffer
	_ = binary.Write(&stream, binary.LittleEndian, uint16(0x0010))
	_ = binary.Write(&stream, binary.LittleEndian, uint16(0x0020))
	_ = binary.Write(&stream, binary.LittleEndian, uint32(1<<20))
	source := &maxReadSizeReader{r: bytes.NewReader(stream.Bytes())}
	reader := NewReader(source, transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
	if source.maxReadSize > 64<<10 {
		t.Fatalf("largest value read buffer = %d bytes, want <= 64 KiB", source.maxReadSize)
	}
}

type maxReadSizeReader struct {
	r           *bytes.Reader
	maxReadSize int
}

func (r *maxReadSizeReader) Read(p []byte) (int, error) {
	if len(p) > r.maxReadSize {
		r.maxReadSize = len(p)
	}
	return r.r.Read(p)
}

func TestReaderLimitsRejectUnterminatedItem(t *testing.T) {
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "BROKEN^ITEM"), transfer.ExplicitVRLittleEndian),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.ReadDataSet()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected non-EOF error, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
}
