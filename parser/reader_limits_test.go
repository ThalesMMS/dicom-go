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
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
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
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 2),
		{0x01, 0x02},
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFE),
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
	item := sequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner)))
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

func TestReaderLimitsRejectUnterminatedItem(t *testing.T) {
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
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
