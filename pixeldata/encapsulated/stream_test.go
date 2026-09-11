package encapsulated_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
)

func frameCount(obj *object.Object, n int) {
	obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 8), VR: core.VRIS}, Value: core.StringValue{fmt.Sprint(n)}})
}
func testWire(t testing.TB, c codecfixture.Case, count, cut int, table string) ([]byte, []byte, int) {
	t.Helper()
	pixel, err := c.PixelData()
	if err != nil {
		t.Fatal(err)
	}
	raw := pixel.Sequence.Fragments[0]
	if len(raw) > 0 && raw[len(raw)-1] == 0 {
		raw = raw[:len(raw)-1]
	}
	data := append([]byte(nil), raw...)
	if len(data)&1 != 0 {
		data = append(data, 0)
	}
	obj := c.Object()
	frameCount(obj, count)
	seq := core.FragmentSequence{}
	var offsets, lengths []uint64
	var offset uint64
	for i := 0; i < count; i++ {
		offsets = append(offsets, offset)
		lengths = append(lengths, uint64(len(raw)))
		if cut > 0 {
			seq.Fragments = append(seq.Fragments, data[:cut], data[cut:])
			offset += uint64(len(data) + 16)
		} else {
			seq.Fragments = append(seq.Fragments, data)
			offset += uint64(len(data) + 8)
		}
	}
	switch table {
	case "BOT":
		seq.OffsetTable = make([]byte, count*4)
		for i, o := range offsets {
			binary.LittleEndian.PutUint32(seq.OffsetTable[i*4:], uint32(o))
		}
	case "EOT":
		obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 1), VR: core.VROV}, Value: core.Uint64Value(offsets)})
		obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 2), VR: core.VROV}, Value: core.Uint64Value(lengths)})
	}
	obj.Put(core.Element{Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true}, Value: seq})
	var out bytes.Buffer
	if err := object.WriteFile(&out, &object.File{Dataset: obj, TransferSyntax: c.Syntax}); err != nil {
		t.Fatal(err)
	}
	wire := out.Bytes()
	// Locate the first complete frame using this test's known generated layout.
	first := bytes.Index(wire, data)
	if cut > 0 {
		first = bytes.Index(wire, data[cut:]) - cut - 8
	}
	if first < 0 {
		t.Fatal("fixture frame not located")
	}
	end := first + len(data)
	if cut > 0 {
		end += 8
	}
	return wire, data, end
}

type recordingSink struct {
	fn       func(parser.EncodedFrame) error
	closed   atomic.Int32
	closeErr error
}

func (s *recordingSink) HandleEncodedFrame(f parser.EncodedFrame) error {
	if s.fn != nil {
		return s.fn(f)
	}
	return nil
}
func (s *recordingSink) Close() error { s.closed.Add(1); return s.closeErr }
func newStream(t testing.TB, ctx context.Context, sink parser.EncodedFrameSink, limits encapsulated.Limits) *encapsulated.Stream {
	t.Helper()
	s, err := encapsulated.NewStream(ctx, sink, limits)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEncodedStreamFirstFrameBeforeRemainderAndBackpressure(t *testing.T) {
	for _, table := range []string{"empty", "BOT", "EOT"} {
		t.Run(table, func(t *testing.T) {
			cut := 2
			if table == "EOT" {
				cut = 0
			}
			wire, want, end := testWire(t, codecfixture.JPEGBaselineSmall(), 3, cut, table)
			input, producer := io.Pipe()
			defer input.Close()
			defer producer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frames := make(chan parser.EncodedFrame)
			stream := newStream(t, ctx, parser.NewEncodedFrameChannelSinkContext(ctx, frames), encapsulated.Limits{})
			type result struct {
				file *object.File
				err  error
			}
			done := make(chan result, 1)
			go func() {
				f, err := object.ReadFileWithOptions(input, object.ReadFileOptions{EncapsulatedSink: stream})
				done <- result{f, err}
			}()
			prefixDone := make(chan error, 1)
			go func() { _, err := producer.Write(wire[:end]); prefixDone <- err }()
			select {
			case err := <-prefixDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("prefix blocked")
			}
			// The producer has made none of the remaining frames or delimiter available.
			var first parser.EncodedFrame
			select {
			case first = <-frames:
			case <-time.After(5 * time.Second):
				t.Fatal("first frame waited for rest of file")
			}
			if first.Index != 0 || !bytes.Equal(first.Data, want) || first.Metadata.NumberOfFrames != 3 {
				t.Fatal("first frame/metadata")
			}
			secondDone := make(chan error, 1)
			go func() { _, err := producer.Write(wire[end:]); producer.Close(); secondDone <- err }()
			// An unbuffered sink cannot complete the read while the consumer is idle.
			select {
			case r := <-done:
				t.Fatalf("backpressure lost: %v", r.err)
			default:
			}
			for i := 1; i < 3; i++ {
				select {
				case f := <-frames:
					if f.Index != i || !bytes.Equal(f.Data, want) {
						t.Fatal("frame content/order")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("frame timeout")
				}
			}
			select {
			case r := <-done:
				if r.err != nil || r.file == nil || !r.file.Dataset.HasDiscardedValues() {
					t.Fatalf("read state: %v", r.err)
				}
				var output bytes.Buffer
				if err := object.WriteFile(&output, r.file); !errors.Is(err, core.ErrDiscardedValue) || output.Len() != 0 {
					t.Fatalf("discarded write: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("read timeout")
			}
			if _, ok := <-frames; ok {
				t.Fatal("channel not closed")
			}
			if err := <-secondDone; err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Data, want) {
				t.Fatal("owned frame overwritten")
			}
		})
	}
}

func TestEncodedStreamDeferredRoundTripAndNoImplicitRetention(t *testing.T) {
	wire, _, _ := testWire(t, codecfixture.JPEGLosslessSV1RGB8Interleaved(), 3, 2, "BOT")
	for _, retain := range []bool{false, true} {
		t.Run(fmt.Sprint(retain), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "synthetic.dcm")
			if err := os.WriteFile(path, wire, 0600); err != nil {
				t.Fatal(err)
			}
			sink := &recordingSink{}
			stream := newStream(t, context.Background(), sink, encapsulated.Limits{})
			file, err := object.OpenFileWithOptions(path, object.ReadFileOptions{EncapsulatedSink: stream, DeferPixelData: retain})
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if sink.closed.Load() != 1 || file.Dataset.HasDiscardedValues() == retain {
				t.Fatal("retention/finalization")
			}
			var out bytes.Buffer
			err = object.WriteFile(&out, file)
			if !retain {
				if !errors.Is(err, core.ErrDiscardedValue) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || !bytes.Equal(out.Bytes(), wire) {
				t.Fatalf("deferred roundtrip: %v", err)
			}
			file.Close()
			out.Reset()
			if err := object.WriteFile(&out, file); err == nil {
				t.Fatal("closed source silently serialized")
			}
		})
	}
	stream := newStream(t, context.Background(), &recordingSink{}, encapsulated.Limits{})
	if _, err := object.ReadFileWithOptions(struct{ io.Reader }{bytes.NewReader(wire)}, object.ReadFileOptions{EncapsulatedSink: stream, DeferPixelData: true}); !errors.Is(err, object.ErrDeferredValueRequiresSeekable) {
		t.Fatal(err)
	}
}

func TestEncodedStreamCancellationLateErrorsAndCloseOnce(t *testing.T) {
	wire, _, end := testWire(t, codecfixture.JPEGBaselineSmall(), 3, 2, "BOT")
	consumerErr := errors.New("consumer failed")
	closeErr := errors.New("sink close failed")
	for _, tc := range []struct {
		name     string
		data     []byte
		callback bool
		want     error
	}{
		{"truncated after first", wire[:end], false, io.ErrUnexpectedEOF},
		{"truncated final delimiter", wire[:len(wire)-1], false, io.ErrUnexpectedEOF},
		{"early meta failure", wire[:20], false, io.ErrUnexpectedEOF},
		{"consumer", wire, true, consumerErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			sink := &recordingSink{closeErr: closeErr, fn: func(f parser.EncodedFrame) error {
				calls++
				if tc.callback {
					return consumerErr
				}
				return nil
			}}
			stream := newStream(t, context.Background(), sink, encapsulated.Limits{})
			file, err := object.ReadFileWithOptions(bytes.NewReader(tc.data), object.ReadFileOptions{EncapsulatedSink: stream})
			if file != nil || !errors.Is(err, tc.want) || !errors.Is(err, closeErr) || sink.closed.Load() != 1 {
				t.Fatalf("error/close: %v, %d", err, sink.closed.Load())
			}
			if tc.name == "truncated after first" && calls != 1 {
				t.Fatalf("delivered=%d", calls)
			}
			stream.Close()
			if sink.closed.Load() != 1 {
				t.Fatal("double close")
			}
		})
	}
	for _, closeSink := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked-send-close=%t", closeSink), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := make(chan parser.EncodedFrame)
			entered := make(chan struct{})
			downstream := parser.NewEncodedFrameChannelSinkContext(ctx, ch)
			sink := &notifyingSink{EncodedFrameSink: downstream, entered: entered}
			stream := newStream(t, ctx, sink, encapsulated.Limits{})
			done := make(chan error, 1)
			go func() {
				file, err := object.ReadFileWithOptions(bytes.NewReader(wire), object.ReadFileOptions{EncapsulatedSink: stream})
				if file != nil {
					err = errors.New("partial file returned")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("send not reached")
			}
			if closeSink {
				stream.Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || !closeSink && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("blocked send leaked")
			}
			if _, ok := <-ch; ok {
				t.Fatal("channel remains open")
			}
		})
	}
}

type notifyingSink struct {
	parser.EncodedFrameSink
	entered chan struct{}
}

func (s *notifyingSink) HandleEncodedFrame(f parser.EncodedFrame) error {
	close(s.entered)
	return s.EncodedFrameSink.HandleEncodedFrame(f)
}

type repeatingReader struct {
	data          []byte
	offset, count int
}

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.count == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	if r.offset == len(r.data) {
		r.count--
		r.offset = 0
	}
	return n, nil
}

func TestEncodedStreamPayloadRetentionDoesNotGrowWithFrameCount(t *testing.T) {
	// A generated 64 KiB opaque APP segment keeps the source itself constant
	// while 1024 frames (64 MiB of payload) are consumed without retention.
	frame := []byte{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xfe}
	frame = append(frame, make([]byte, 65532)...)
	frame = append(frame, 0xff, 0xda, 0, 2, 1, 2, 0xff, 0xd9)
	if len(frame)&1 != 0 {
		frame = append(frame, 0)
	}
	c := codecfixture.JPEGBaselineSmall()
	obj := c.Object()
	frameCount(obj, 1024)
	obj.Remove(core.TagPixelData)
	var prefix bytes.Buffer
	if err := object.WriteDataSet(&prefix, obj, c.Syntax); err != nil {
		t.Fatal(err)
	}
	// Pixel Data header and BOT are emitted by the existing writer, then the
	// known Item template is repeated by a constant-memory io.Reader.
	var pixel bytes.Buffer
	if err := parser.NewWriter(&pixel, c.Syntax).WriteElement(core.Element{Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true}, Value: core.FragmentSequence{Fragments: [][]byte{frame}}}); err != nil {
		t.Fatal(err)
	}
	encoded := pixel.Bytes()
	start := bytes.Index(encoded, frame) - 8
	input := io.MultiReader(bytes.NewReader(prefix.Bytes()), bytes.NewReader(encoded[:start]), &repeatingReader{data: encoded[start : len(encoded)-8], count: 1024}, bytes.NewReader(encoded[len(encoded)-8:]))
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak uint64
	calls := 0
	sink := &recordingSink{fn: func(f parser.EncodedFrame) error {
		calls++
		if len(f.Data) != len(frame) {
			return errors.New("frame length")
		}
		if calls%128 == 0 {
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapAlloc)
		}
		return nil
	}}
	stream := newStream(t, context.Background(), sink, encapsulated.Limits{})
	got, err := object.ReadDataSetWithOptions(input, c.Syntax, object.ReadFileOptions{EncapsulatedSink: stream})
	if err != nil || calls != 1024 || !got.HasDiscardedValues() {
		t.Fatalf("count/state: %d %v", calls, err)
	}
	if peak > before.HeapAlloc+(8<<20) {
		t.Fatalf("live payload grew with frame count: base=%d peak=%d", before.HeapAlloc, peak)
	}
}

func TestEncodedStreamFullIndependentJPEGReconstructions(t *testing.T) {
	for _, c := range []codecfixture.Case{codecfixture.JPEGBaselineSmall(), codecfixture.JPEGExtendedSmall(), codecfixture.JPEGExtendedProcess4Mono12(), codecfixture.JPEGLosslessSV1RGB8Interleaved()} {
		t.Run(c.Name, func(t *testing.T) {
			want := c.ExpectedFrames
			policy := codecfixture.SamplePolicy{}
			if c.Name == "jpeg-baseline-small" || c.Name == "jpeg-extended-small" {
				_, v, err := codecfixture.ReadFullReconstruction("../..", "jpeg-assembly-"+c.Name)
				if err != nil {
					t.Fatal(err)
				}
				want = v
			}
			if c.SamplePolicy != nil {
				policy = *c.SamplePolicy
			}
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"empty", "BOT", "EOT"} {
				cut := 2
				if table == "EOT" {
					cut = 0
				}
				wire, _, _ := testWire(t, c, 3, cut, table)
				calls := 0
				sink := &recordingSink{fn: func(f parser.EncodedFrame) error {
					calls++
					o := c.Object()
					frameCount(o, 1)
					got, err := registry.DecodeFrames(f.TransferSyntax.UID, pixeldata.PixelData{Encapsulated: true, Sequence: core.FragmentSequence{Fragments: [][]byte{f.Data}}}, o)
					if err != nil {
						return err
					}
					m := f.Metadata
					layout := codecfixture.SampleLayout{Rows: int(m.Rows), Columns: int(m.Columns), Components: int(m.SamplesPerPixel), BitsAllocated: int(m.BitsAllocated), BitsStored: int(m.BitsStored), HighBit: int(m.HighBit)}
					r, err := codecfixture.CompareSamples(got.Data, layout, want, layout, policy)
					if err != nil {
						return err
					}
					if !r.Qualified {
						return errors.New("independent sample mismatch")
					}
					return nil
				}}
				stream := newStream(t, context.Background(), sink, encapsulated.Limits{})
				if _, err := object.ReadFileWithOptions(bytes.NewReader(wire), object.ReadFileOptions{EncapsulatedSink: stream}); err != nil || calls != 3 {
					t.Fatalf("%s: %v count=%d", table, err, calls)
				}
			}
		})
	}
}

func TestEncodedStreamRejectsLayoutLimitsAndScope(t *testing.T) {
	c := codecfixture.JPEGBaselineSmall()
	wire, _, _ := testWire(t, c, 2, 2, "BOT")
	for _, tc := range []struct {
		name   string
		limits encapsulated.Limits
	}{
		{"frame count", encapsulated.Limits{MaxFrames: 1}},
		{"fragments", encapsulated.Limits{MaxFragments: 1}},
		{"frame fragments", encapsulated.Limits{MaxFragmentsPerFrame: 1}},
		{"frame bytes", encapsulated.Limits{MaxFrameBytes: 2}},
		{"object bytes", encapsulated.Limits{MaxBytes: 8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			stream := newStream(t, context.Background(), sink, tc.limits)
			got, err := object.ReadFileWithOptions(bytes.NewReader(wire), object.ReadFileOptions{EncapsulatedSink: stream})
			if got != nil || !errors.Is(err, encapsulated.ErrResourceLimit) || sink.closed.Load() != 1 {
				t.Fatalf("limit: %v", err)
			}
		})
	}
	for _, name := range []string{"bad BOT", "fragmented EOT", "nested pixel", "missing metadata", "late metadata", "late EOT", "skip conflict", "missing BOT", "empty EOT", "native syntax"} {
		t.Run(name, func(t *testing.T) {
			data := wire
			f, err := object.ReadFile(bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			p, err := pixeldata.Extract(f.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "bad BOT":
				binary.LittleEndian.PutUint32(p.Sequence.OffsetTable[4:], 2)
				el, _ := f.Dataset.Get(core.TagPixelData)
				el.Value = p.Sequence
				f.Dataset.Put(el)
			case "fragmented EOT":
				data, _, _ = testWire(t, c, 2, 2, "EOT")
			case "nested pixel":
				pixel, _ := f.Dataset.Get(core.TagPixelData)
				f.Dataset.Remove(core.TagPixelData)
				f.Dataset.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{pixel}}}}})
			case "missing metadata":
				f.Dataset.Remove(core.NewTag(0x0028, 0x0010))
			case "native syntax":
				f.TransferSyntax = codecfixture.NativeSmall().Syntax
			case "missing BOT":
				// No Item at all before Sequence Delimitation.
				idx := bytes.Index(data, []byte{0xe0, 0x7f, 0x10, 0, 'O', 'B'})
				data = append(append([]byte(nil), data[:idx+12]...), data[len(data)-8:]...)
			case "empty EOT":
				f.Dataset.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 1), VR: core.VROV}, Value: core.Uint64Value{}})
			}
			if name != "fragmented EOT" && name != "missing BOT" {
				var out bytes.Buffer
				if err := object.WriteFile(&out, f); err != nil {
					t.Fatal(err)
				}
				data = out.Bytes()
			}
			if name == "late metadata" || name == "late EOT" {
				var out bytes.Buffer
				el := core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS}, Value: core.Uint16Value{99}}
				if name == "late EOT" {
					el = core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 1), VR: core.VROV}, Value: core.Uint64Value{0, 100}}
				}
				if err := parser.NewWriter(&out, c.Syntax).WriteElement(el); err != nil {
					t.Fatal(err)
				}
				data = append(data, out.Bytes()...)
			}
			sink := &recordingSink{}
			stream := newStream(t, context.Background(), sink, encapsulated.Limits{})
			got, err := object.ReadFileWithOptions(bytes.NewReader(data), object.ReadFileOptions{EncapsulatedSink: stream, SkipPixelData: name == "skip conflict"})
			if err == nil || got != nil || sink.closed.Load() != 1 {
				t.Fatalf("invalid layout accepted: %v", err)
			}
		})
	}
}
