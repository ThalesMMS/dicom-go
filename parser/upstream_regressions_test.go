package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type regressionReadBudget struct {
	reader            io.Reader
	bytes, maxRequest int
}

func BenchmarkUpstreamUndefinedLengthRejection(b *testing.B) {
	header := dicomtest.ExplicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7777, 0x1010), core.VROB, 0xffffffff)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := NewReader(bytes.NewReader(header), transfer.ExplicitVRLittleEndian, ReaderOptions{MaxElementBytes: 16, MaxTotalBytes: 64}).ReadDataSet()
		if !errors.Is(err, ErrUnsupportedUndefinedLength) {
			b.Fatal(err)
		}
	}
}

func (r *regressionReadBudget) Read(p []byte) (int, error) {
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	if len(p) > 4096 {
		return 0, errors.New("regression attempted an oversized read")
	}
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func TestUpstreamUndefinedByteLengthIsBounded(t *testing.T) {
	for _, vr := range []core.VR{core.VROB, core.VROW} {
		for _, size := range []uint32{0xffffffff, 0xfffffffe} {
			t.Run(fmt.Sprintf("%s/%x", vr, size), func(t *testing.T) {
				header := dicomtest.ExplicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7777, 0x1010), vr, size)
				source := &regressionReadBudget{reader: bytes.NewReader(append(header, []byte("UNREAD_SYNTHETIC_PAYLOAD")...))}
				_, err := NewReader(source, transfer.ExplicitVRLittleEndian, ReaderOptions{MaxElementBytes: 16, MaxTotalBytes: 64}).ReadDataSet()
				want := ErrMaxElementBytesExceeded
				if size == 0xffffffff {
					want = ErrUnsupportedUndefinedLength
				}
				var parseErr *ParseError
				if !errors.Is(err, want) || !errors.As(err, &parseErr) {
					t.Fatalf("bounded rejection: %v", err)
				}
				if source.bytes != len(header) || source.maxRequest > len(header) {
					t.Fatalf("payload read or oversized request: %+v", source)
				}
			})
		}
	}
}

func TestUpstreamPrivateUNEOFAtEveryBoundary(t *testing.T) {
	data := dicomtest.PrivateUNNestedPixelRegression()
	for n := 1; n < len(data); n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			source := &regressionReadBudget{reader: bytes.NewReader(data[:n])}
			_, err := NewReader(source, transfer.ExplicitVRLittleEndian, ReaderOptions{MaxElementBytes: 64, MaxTotalBytes: 128, MaxSequenceDepth: 4}).ReadDataSet()
			var parseErr *ParseError
			if err == nil || !errors.As(err, &parseErr) {
				t.Fatalf("truncated private sequence accepted or untyped: %v", err)
			}
			if source.bytes > n || source.maxRequest > 64 {
				t.Fatal("unbounded truncated read")
			}
		})
	}
}

func TestUpstreamRequiredFieldEOFNeverEndsDataSet(t *testing.T) {
	for _, syntax := range []transfer.Syntax{transfer.ImplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRBigEndian} {
		for _, vr := range []core.VR{core.VROB, core.VRPN} {
			data := dicomtest.EncodeElements(syntax, core.NewRawElement(core.TagPixelData, vr, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
			for _, skip := range []bool{false, true} {
				for n := 1; n < len(data); n++ {
					t.Run(fmt.Sprintf("%s/%s/skip=%v/%d", syntax.UID, vr, skip, n), func(t *testing.T) {
						reader := NewReader(bytes.NewBuffer(data[:n]), syntax, ReaderOptions{SkipPixelData: skip, MaxElementBytes: 32, MaxTotalBytes: 64})
						_, err := reader.ReadAll()
						if err == nil || errors.Is(err, io.EOF) || !errors.Is(err, io.ErrUnexpectedEOF) {
							t.Fatalf("incomplete required field treated as boundary: %v", err)
						}
					})
				}
			}
		}
	}
}

func TestUpstreamFrameSinkMissingPayloadOrPaddingIsTruncated(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0002), 1),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "MONOCHROME2"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 1),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), 3),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0100), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0101), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0102), 7),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0103), 0),
		dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2, 3}),
	)
	for _, removed := range []int{1, 4} {
		sink := &testFrameSink{}
		_, err := NewReader(bytes.NewBuffer(data[:len(data)-removed]), transfer.ExplicitVRLittleEndian, ReaderOptions{FrameSink: sink}).ReadDataSet()
		if !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || sink.closeCount != 1 {
			t.Fatalf("removed=%d error=%v close=%d", removed, err, sink.closeCount)
		}
	}
}

type failingReplaySeeker struct {
	*bytes.Reader
	currentErr, restoreErr error
	restoreOffset          int64
	readCalls              int
}

func (s *failingReplaySeeker) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekCurrent && s.currentErr != nil {
		return 0, s.currentErr
	}
	if whence == io.SeekStart && offset == s.restoreOffset && s.restoreErr != nil {
		return 0, s.restoreErr
	}
	return s.Reader.Seek(offset, whence)
}
func (s *failingReplaySeeker) Read(p []byte) (int, error) { s.readCalls++; return s.Reader.Read(p) }

func TestDeferredReplayReportsSeekFailures(t *testing.T) {
	for _, mode := range []string{"current", "restore", "read-and-restore"} {
		t.Run(mode, func(t *testing.T) {
			data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
			source := &failingReplaySeeker{Reader: bytes.NewReader(data)}
			reader := NewReader(source, transfer.ExplicitVRLittleEndian, ReaderOptions{DeferPixelData: true})
			if _, err := reader.ReadDataSet(); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("synthetic seek failure")
			source.restoreOffset = int64(len(data))
			source.readCalls = 0
			if mode == "current" {
				source.currentErr = fault
			} else {
				source.restoreErr = fault
			}
			if mode == "read-and-restore" {
				source.Reader = bytes.NewReader(data[:len(data)-1])
				if _, err := source.Reader.Seek(source.restoreOffset, io.SeekStart); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			_, err := reader.CopyElementValueTo(core.TagPixelData, &out)
			if !errors.Is(err, fault) {
				t.Fatalf("seek failure reported as success/lost: %v", err)
			}
			if mode == "current" && (source.readCalls != 0 || out.Len() != 0) {
				t.Fatal("replay continued after failed position snapshot")
			}
			if mode == "read-and-restore" && !errors.Is(err, io.EOF) {
				t.Fatalf("read error lost when restoration also failed: %v", err)
			}
		})
	}
}
