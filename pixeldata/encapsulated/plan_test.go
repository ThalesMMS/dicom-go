package encapsulated

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func boundaryStream(entropy []byte) []byte {
	// Delimiter-looking bytes occur in APP1 and must be skipped by its length.
	out := []byte{0xff, 0xd8, 0xff, 0xe1, 0, 14, 0xff, 0xd9, 0xff, 0xd8, 0xfe, 0xff, 0xdd, 0xe0, 0, 0, 0, 0, 0xff, 0xda, 0, 2}
	out = append(out, entropy...)
	out = append(out, 0xff, 0xd9)
	if len(out)&1 != 0 {
		out = append(out, 0)
	}
	return out
}

func TestPlanMarkerBoundariesAndOpaqueSegments(t *testing.T) {
	for _, format := range []Format{JPEG, JPEGLS} {
		stream := boundaryStream([]byte{1, 0xff, 0, 2, 0xff, 0xd0, 3})
		if format == JPEGLS {
			stream = boundaryStream([]byte{1, 0xff, 0x7f, 2, 3})
		}
		for split := 2; split < len(stream); split += 2 {
			fragments := [][]byte{stream[:split], stream[split:], stream}
			p, err := New(context.Background(), Memory(fragments), Tables{}, 2, format, Limits{})
			if err != nil {
				t.Fatalf("format=%d split=%d: %v", format, split, err)
			}
			first, err := p.Frame(context.Background(), 0)
			if err != nil || first.Borrowed || !bytes.Equal(first.Data, stream) {
				t.Fatalf("split=%d: %+v %v", split, first, err)
			}
			second, err := p.Frame(context.Background(), 1)
			if err != nil || !second.Borrowed || !bytes.Equal(second.Data, stream) {
				t.Fatalf("second: %v", err)
			}
			first.Data[0] = 0
			if stream[0] != 0xff || second.Data[0] != 0xff {
				t.Fatal("owned frame aliases source")
			}
		}
	}
}

func TestPlanRejectsFramingAndOffsetConflicts(t *testing.T) {
	stream := boundaryStream([]byte{1, 2, 3})
	for _, tc := range []struct {
		name      string
		fragments [][]byte
		frames    int
		tables    Tables
	}{
		{"two frames inside Item", [][]byte{append(append([]byte(nil), stream...), stream...), stream[:2], stream[2:]}, 2, Tables{}},
		{"truncated segment", [][]byte{{0xff, 0xd8, 0xff, 0xe1, 0, 32}, {0xff, 0xd9}, stream}, 2, Tables{}},
		{"split truncated marker", [][]byte{stream[:len(stream)-2], {0xff, 0xff}}, 1, Tables{}},
		{"odd non-final Item", [][]byte{stream[:3], stream[3:]}, 1, Tables{Basic: basicOffsets(0)}},
		{"empty EOT", [][]byte{stream}, 1, Tables{ExtendedPresent: true, LengthsPresent: true}},
		{"EOT missing lengths", [][]byte{stream}, 1, Tables{ExtendedPresent: true, Extended: []uint64{0}}},
		{"EOT stale presence", [][]byte{stream}, 1, Tables{Extended: []uint64{0}}},
		{"EOT with BOT", [][]byte{stream}, 1, Tables{Basic: basicOffsets(0), ExtendedPresent: true, LengthsPresent: true, Extended: []uint64{0}, Lengths: []uint64{uint64(len(stream))}}},
		{"EOT with multiple Items", [][]byte{stream[:2], stream[2:]}, 1, Tables{ExtendedPresent: true, LengthsPresent: true, Extended: []uint64{0}, Lengths: []uint64{uint64(len(stream))}}},
		{"BOT payload-relative", [][]byte{stream, stream}, 2, Tables{Basic: basicOffsets(0, uint32(len(stream)))}},
		{"EOT payload-relative", [][]byte{stream, stream}, 2, Tables{ExtendedPresent: true, LengthsPresent: true, Extended: []uint64{0, uint64(len(stream))}, Lengths: []uint64{uint64(len(stream)), uint64(len(stream))}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(context.Background(), Memory(tc.fragments), tc.tables, tc.frames, JPEG, Limits{}); !errors.Is(err, ErrLayout) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

type observedReader struct {
	io.ReaderAt
	mu      sync.Mutex
	bytes   int
	offsets []int64
}

func (r *observedReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bytes += len(p)
	r.offsets = append(r.offsets, off)
	return r.ReaderAt.ReadAt(p, off)
}

func TestPlanDeferredFrameOwnershipAndActualFileConsumption(t *testing.T) {
	stream := boundaryStream([]byte{1, 2, 3, 4})
	// Item headers are owned by the parser/index, and are never exposed as payload.
	encoded := append(make([]byte, 8), stream...)
	encoded = append(encoded, make([]byte, 8)...)
	encoded = append(encoded, stream...)
	path := filepath.Join(t.TempDir(), "fragments.bin")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := &observedReader{ReaderAt: file}
	source, err := ReaderAt(reader, []ItemRange{{8, uint64(len(stream))}, {int64(16 + len(stream)), uint64(len(stream))}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(context.Background(), source, Tables{Basic: basicOffsets(0, uint32(8+len(stream)))}, 2, JPEG, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if reader.bytes != 0 {
		t.Fatal("planning read deferred payload")
	}
	v, err := p.Frame(context.Background(), 1)
	if err != nil || v.Borrowed || !bytes.Equal(v.Data, stream) {
		t.Fatalf("frame=%+v %v", v, err)
	}
	if reader.bytes != len(stream) || len(reader.offsets) != 1 || reader.offsets[0] != int64(16+len(stream)) {
		t.Fatal("read outside requested frame")
	}
	v.Data[0] = 0
	again, err := p.Frame(context.Background(), 1)
	if err != nil || again.Data[0] != 0xff {
		t.Fatal("deferred buffers reused")
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			v, err := p.Frame(context.Background(), 0)
			if err != nil || !bytes.Equal(v.Data, stream) {
				t.Errorf("concurrent frame: %v", err)
			}
		}()
	}
	group.Wait()
	if err := file.Truncate(int64(len(encoded) - 3)); err != nil {
		t.Fatal(err)
	}
	if v, err := p.Frame(context.Background(), 1); v.Data != nil || !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, ErrSource) {
		t.Fatalf("truncation: %+v %v", v, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Frame(context.Background(), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed source: %v", err)
	}
}

type virtualSource struct {
	count  int
	size   uint64
	reads  int
	cancel context.CancelFunc
}

func (s *virtualSource) FragmentCount() int               { return s.count }
func (s *virtualSource) FragmentSize(int) (uint64, error) { return s.size, nil }
func (s *virtualSource) ReadFragmentAt(ctx context.Context, _ int, p []byte, _ int64) (int, error) {
	s.reads++
	if s.cancel != nil {
		s.cancel()
	}
	clear(p)
	return len(p), nil
}

func TestPlanLimitsAndCancellationBeforeReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source virtualSource
		frames int
		limits Limits
	}{
		{"frame count", virtualSource{count: 2, size: 2}, 2, Limits{MaxFrames: 1}},
		{"fragment count", virtualSource{count: 100001, size: 2}, 1, Limits{}},
		{"aggregate bytes", virtualSource{count: 2, size: 4}, 2, Limits{MaxBytes: 7}},
		{"frame bytes", virtualSource{count: 1, size: 8}, 1, Limits{MaxFrameBytes: 7}},
		{"per-frame fragment count", virtualSource{count: 3, size: 2}, 1, Limits{MaxFragmentsPerFrame: 2}},
		{"overflow", virtualSource{count: 1, size: math.MaxUint64}, 1, Limits{}},
		{"negative limits", virtualSource{count: 1, size: 2}, 1, Limits{MaxFrames: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(context.Background(), &tc.source, Tables{Basic: basicOffsets(0)}, tc.frames, JPEG, tc.limits)
			if !errors.Is(err, ErrResourceLimit) || tc.source.reads != 0 {
				t.Fatalf("reads=%d error=%v", tc.source.reads, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &virtualSource{count: 1, size: 128 << 10}
	if _, err := New(ctx, source, Tables{}, 1, JPEG, Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p, err := New(context.Background(), source, Tables{}, 1, JPEG, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Frame(ctx, 0); !errors.Is(err, context.Canceled) || source.reads != 0 {
		t.Fatal("pre-cancelled frame read source")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	source.cancel = cancel
	if v, err := p.Frame(ctx, 0); v.Data != nil || !errors.Is(err, context.Canceled) || source.reads != 1 {
		t.Fatalf("mid-read cancellation: %+v %v", v, err)
	}
	if _, err := New(context.Background(), source, Tables{}, 1, Format(99), Limits{}); !errors.Is(err, ErrLayout) {
		t.Fatal("accepted non-JPEG format")
	}
}

func TestReaderAtRejectsInvalidRanges(t *testing.T) {
	for _, items := range [][]ItemRange{{{-1, 2}}, {{0, 1}}, {{0, 3}}, {{math.MaxInt64, 2}}, {{0, 4}, {2, 2}}} {
		if _, err := ReaderAt(bytes.NewReader(nil), items); !errors.Is(err, ErrLayout) {
			t.Fatalf("ranges=%v error=%v", items, err)
		}
	}
}

func TestEOTRejectsWrongVR(t *testing.T) {
	obj := object.New(nil)
	obj.Put(core.NewRawElement(tagExtendedOffsetTable, core.VRUN, uint64Table(0)))
	obj.Put(core.NewRawElement(tagExtendedOffsetTableLengths, core.VROV, uint64Table(4)))
	if _, err := ReadTables(obj, nil, 1); !errors.Is(err, ErrLayout) {
		t.Fatalf("wrong EOT VR: %v", err)
	}
}

func TestPlanConsumesExistingParserDeferredItemLocations(t *testing.T) {
	stream := boundaryStream([]byte{1, 2, 3})
	sequence := core.FragmentSequence{Fragments: [][]byte{stream[:4], stream[4:], stream}}
	var wire bytes.Buffer
	element := core.Element{Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true}, Value: sequence}
	if err := parser.NewWriter(&wire, transfer.JPEGBaseline).WriteElement(element); err != nil {
		t.Fatal(err)
	}
	reader := parser.NewReader(bytes.NewReader(wire.Bytes()), transfer.JPEGBaseline, parser.ReaderOptions{DeferPixelData: true, MaxFragments: 8, MaxPixelDataBytes: 4096})
	var ranges []ItemRange
	seenBOT := false
	for {
		token, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if token.Kind != parser.TokenElement || !token.Header.Tag.IsItem() {
			continue
		}
		if token.Element.Value != nil {
			t.Fatal("parser materialized deferred Item")
		}
		if !seenBOT {
			if token.Header.Length != 0 {
				t.Fatal("fixture BOT")
			}
			seenBOT = true
			continue
		}
		ranges = append(ranges, ItemRange{Offset: token.Offset + 8, Length: uint64(token.Header.Length)})
	}
	if len(ranges) != 3 {
		t.Fatalf("parser ranges=%v", ranges)
	}
	source, err := ReaderAt(bytes.NewReader(wire.Bytes()), ranges)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(context.Background(), source, Tables{}, 2, JPEG, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < p.Len(); i++ {
		v, err := p.Frame(context.Background(), i)
		if err != nil || v.Borrowed || !bytes.Equal(v.Data, stream) {
			t.Fatalf("parser-derived frame=%+v %v", v, err)
		}
	}
}

func FuzzPlanMarkerAndOffsetBoundaries(f *testing.F) {
	f.Add(boundaryStream([]byte{1, 2, 3}), uint16(2), byte(0))
	f.Fuzz(func(t *testing.T, data []byte, cut uint16, mode byte) {
		if len(data) < 4 || len(data) > 64<<10 {
			return
		}
		split := 2 + int(cut)%(len(data)-2)
		split &^= 1
		fragments := [][]byte{data[:split], data[split:], {0xff, 0xd8, 0xff, 0xd9}}
		tables := Tables{}
		if mode&2 != 0 {
			tables.Basic = basicOffsets(0, uint32(cut))
		}
		format := JPEG
		if mode&1 != 0 {
			format = JPEGLS
		}
		p, err := New(context.Background(), Memory(fragments), tables, 2, format, Limits{MaxBytes: 128 << 10, MaxFrameBytes: 128 << 10})
		if err != nil {
			return
		}
		for i := 0; i < p.Len(); i++ {
			if _, err := p.Frame(context.Background(), i); err != nil {
				t.Fatal(err)
			}
		}
	})
}
