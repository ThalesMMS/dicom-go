package dicomjson

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestContextAPIsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(core.NewTag(0x0010, 0x0010), core.VRPN, "TEST^PATIENT"),
	}, std.Dictionary)
	if _, err := MarshalContext(ctx, obj, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("MarshalContext() error = %v, want context.Canceled", err)
	}
	if _, err := UnmarshalContext(ctx, []byte(`{}`), std.Dictionary, UnmarshalOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("UnmarshalContext() error = %v, want context.Canceled", err)
	}
}

func TestContextCancellationDuringTraversal(t *testing.T) {
	elements := make([]core.Element, 100)
	for i := range elements {
		elements[i] = dicomtest.NewOBElement(core.NewTag(0x0011, uint16(0x1000+i)), []byte{1, 2})
	}
	obj := object.FromElements(elements, std.Dictionary)
	if _, err := MarshalContext(newCancelAfterContext(8), obj, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("MarshalContext() error = %v, want cancellation during traversal", err)
	}

	encoded, err := Marshal(obj, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnmarshalContext(newCancelAfterContext(8), encoded, std.Dictionary, UnmarshalOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("UnmarshalContext() error = %v, want cancellation during traversal", err)
	}
}

type cancelAfterContext struct {
	mu        sync.Mutex
	remaining int
	done      chan struct{}
}

func newCancelAfterContext(checks int) *cancelAfterContext {
	return &cancelAfterContext{remaining: checks, done: make(chan struct{})}
}

func (ctx *cancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *cancelAfterContext) Value(any) any               { return nil }
func (ctx *cancelAfterContext) Err() error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.remaining > 0 {
		ctx.remaining--
		if ctx.remaining == 0 {
			close(ctx.done)
		}
	}
	if ctx.remaining == 0 {
		return context.Canceled
	}
	return nil
}

func TestUnmarshalLimits(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		limits Limits
		want   error
	}{
		{
			name:   "JSON bytes",
			data:   `{}`,
			limits: Limits{MaxJSONBytes: 1},
			want:   ErrMaxJSONBytesExceeded,
		},
		{
			name:   "InlineBinary bytes",
			data:   `{"7FE00010":{"vr":"OB","InlineBinary":"AQIDBA=="}}`,
			limits: Limits{MaxInlineBinaryBytes: 3},
			want:   ErrMaxInlineBinaryBytesExceeded,
		},
		{
			name: "cumulative binary bytes",
			data: `{"00111001":{"vr":"OB","InlineBinary":"AQI="},` +
				`"00111002":{"vr":"OB","InlineBinary":"AwQ="}}`,
			limits: Limits{MaxTotalBinaryBytes: 3},
			want:   ErrMaxTotalBinaryBytesExceeded,
		},
		{
			name: "sequence depth",
			data: `{"00081111":{"vr":"SQ","Value":[{` +
				`"00081140":{"vr":"SQ","Value":[{` +
				`"00100010":{"vr":"PN","Value":[{"Alphabetic":"TEST"}]}` +
				`}]}}]}}`,
			limits: Limits{MaxSequenceDepth: 1},
			want:   ErrMaxSequenceDepthExceeded,
		},
		{
			name: "cumulative elements",
			data: `{"00100010":{"vr":"PN","Value":[{"Alphabetic":"TEST"}]},` +
				`"00100020":{"vr":"LO","Value":["ID"]}}`,
			limits: Limits{MaxElements: 1},
			want:   ErrMaxElementsExceeded,
		},
		{
			name:   "cumulative sequence items",
			data:   `{"00081111":{"vr":"SQ","Value":[{},{}]}}`,
			limits: Limits{MaxSequenceItems: 1},
			want:   ErrMaxSequenceItemsExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := UnmarshalContext(context.Background(), []byte(test.data), std.Dictionary, UnmarshalOptions{Limits: test.limits})
			if !errors.Is(err, test.want) {
				t.Fatalf("UnmarshalContext() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestMarshalLimits(t *testing.T) {
	firstBinary := dicomtest.NewOBElement(core.NewTag(0x0011, 0x1001), []byte{1, 2})
	secondBinary := dicomtest.NewOBElement(core.NewTag(0x0011, 0x1002), []byte{3, 4})
	twoBinary := object.FromElements([]core.Element{firstBinary, secondBinary}, std.Dictionary)

	leaf := core.DataSet{Elements: []core.Element{
		dicomtest.NewStringElement(core.NewTag(0x0010, 0x0010), core.VRPN, "TEST"),
	}}
	inner := dicomtest.NewSequenceElement(core.NewTag(0x0008, 0x1140), leaf)
	outer := dicomtest.NewSequenceElement(core.NewTag(0x0008, 0x1111), core.DataSet{Elements: []core.Element{inner}})
	deep := object.FromElements([]core.Element{outer}, std.Dictionary)
	twoItems := object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(core.NewTag(0x0008, 0x1111), core.DataSet{}, core.DataSet{}),
	}, std.Dictionary)

	unlimitedJSON, err := Marshal(twoBinary, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		obj    *object.Object
		limits Limits
		want   error
	}{
		{"JSON bytes", twoBinary, Limits{MaxJSONBytes: int64(len(unlimitedJSON) - 1)}, ErrMaxJSONBytesExceeded},
		{"InlineBinary bytes", twoBinary, Limits{MaxInlineBinaryBytes: 1}, ErrMaxInlineBinaryBytesExceeded},
		{"cumulative binary bytes", twoBinary, Limits{MaxTotalBinaryBytes: 3}, ErrMaxTotalBinaryBytesExceeded},
		{"sequence depth", deep, Limits{MaxSequenceDepth: 1}, ErrMaxSequenceDepthExceeded},
		{"cumulative elements", twoBinary, Limits{MaxElements: 1}, ErrMaxElementsExceeded},
		{"cumulative sequence items", twoItems, Limits{MaxSequenceItems: 1}, ErrMaxSequenceItemsExceeded},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := MarshalContext(context.Background(), test.obj, Options{Limits: test.limits})
			if !errors.Is(err, test.want) {
				t.Fatalf("MarshalContext() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLimitsRejectNegativeValues(t *testing.T) {
	_, err := UnmarshalContext(context.Background(), []byte(`{}`), std.Dictionary, UnmarshalOptions{
		Limits: Limits{MaxElements: -1},
	})
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("UnmarshalContext() error = %v, want ErrInvalidLimits", err)
	}
}

func TestBinaryAndJSONLimitsAcceptExactBoundary(t *testing.T) {
	const source = `{"7FE00010":{"vr":"OB","InlineBinary":"AQIDBA=="}}`
	for name, limits := range map[string]Limits{
		"JSON":               {MaxJSONBytes: int64(len(source))},
		"InlineBinary":       {MaxInlineBinaryBytes: 4},
		"total binary bytes": {MaxTotalBinaryBytes: 4},
		"elements":           {MaxElements: 1},
	} {
		t.Run("unmarshal "+name, func(t *testing.T) {
			if _, err := UnmarshalContext(context.Background(), []byte(source), std.Dictionary, UnmarshalOptions{Limits: limits}); err != nil {
				t.Fatalf("UnmarshalContext() exact boundary error = %v", err)
			}
		})
	}

	obj := object.FromElements([]core.Element{
		dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2, 3, 4}),
	}, std.Dictionary)
	encoded, err := Marshal(obj, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, limits := range map[string]Limits{
		"JSON":               {MaxJSONBytes: int64(len(encoded))},
		"InlineBinary":       {MaxInlineBinaryBytes: 4},
		"total binary bytes": {MaxTotalBinaryBytes: 4},
		"elements":           {MaxElements: 1},
	} {
		t.Run("marshal "+name, func(t *testing.T) {
			if _, err := MarshalContext(context.Background(), obj, Options{Limits: limits}); err != nil {
				t.Fatalf("MarshalContext() exact boundary error = %v", err)
			}
		})
	}
}

func TestMarshalJSONLimitPreservesEncoding(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(core.NewTag(0x0010, 0x0010), core.VRPN, "TEST^PATIENT"),
	}, std.Dictionary)
	for _, pretty := range []bool{false, true} {
		unlimited, err := Marshal(obj, Options{Pretty: pretty})
		if err != nil {
			t.Fatal(err)
		}
		limited, err := Marshal(obj, Options{Pretty: pretty, Limits: Limits{MaxJSONBytes: int64(len(unlimited))}})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(limited, unlimited) {
			t.Fatalf("limited Marshal() = %q, unlimited Marshal() = %q", limited, unlimited)
		}
	}
}

func TestNonFiniteFloatValuesRoundTrip(t *testing.T) {
	flTag := core.NewTag(0x0018, 0x602C)
	fdTag := core.NewTag(0x0018, 0x602E)
	fl := make([]byte, 3*4)
	binary.LittleEndian.PutUint32(fl[0:4], math.Float32bits(float32(math.NaN())))
	binary.LittleEndian.PutUint32(fl[4:8], math.Float32bits(float32(math.Inf(1))))
	binary.LittleEndian.PutUint32(fl[8:12], math.Float32bits(float32(math.Inf(-1))))
	fd := make([]byte, 3*8)
	binary.LittleEndian.PutUint64(fd[0:8], math.Float64bits(math.NaN()))
	binary.LittleEndian.PutUint64(fd[8:16], math.Float64bits(math.Inf(1)))
	binary.LittleEndian.PutUint64(fd[16:24], math.Float64bits(math.Inf(-1)))
	obj := object.FromElements([]core.Element{
		core.NewRawElement(flTag, core.VRFL, fl),
		core.NewRawElement(fdTag, core.VRFD, fd),
	}, std.Dictionary)

	encoded, err := Marshal(obj, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"NaN"`, `"Infinity"`, `"-Infinity"`} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Fatalf("Marshal() = %s, want token %s", encoded, want)
		}
	}

	decoded, err := Unmarshal(encoded, std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}
	assertNonFiniteRawValues(t, decoded, flTag, 4)
	assertNonFiniteRawValues(t, decoded, fdTag, 8)
}

func assertNonFiniteRawValues(t *testing.T, obj *object.Object, tag core.Tag, width int) {
	t.Helper()
	raw, ok := obj.GetRaw(tag)
	if !ok {
		t.Fatalf("missing raw value for %s", tag)
	}
	if width == 4 {
		values := []float32{
			math.Float32frombits(binary.LittleEndian.Uint32(raw[0:4])),
			math.Float32frombits(binary.LittleEndian.Uint32(raw[4:8])),
			math.Float32frombits(binary.LittleEndian.Uint32(raw[8:12])),
		}
		if !math.IsNaN(float64(values[0])) || !math.IsInf(float64(values[1]), 1) || !math.IsInf(float64(values[2]), -1) {
			t.Fatalf("decoded FL values = %v, want NaN, +Inf, -Inf", values)
		}
		return
	}
	values := []float64{
		math.Float64frombits(binary.LittleEndian.Uint64(raw[0:8])),
		math.Float64frombits(binary.LittleEndian.Uint64(raw[8:16])),
		math.Float64frombits(binary.LittleEndian.Uint64(raw[16:24])),
	}
	if !math.IsNaN(values[0]) || !math.IsInf(values[1], 1) || !math.IsInf(values[2], -1) {
		t.Fatalf("decoded FD values = %v, want NaN, +Inf, -Inf", values)
	}
}

func TestEncapsulatedPixelDataJSONRoundTripWritesPart10(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0, 0, 0, 0},
		[]byte{0xFF, 0xD8, 0xFF, 0xD9},
		[]byte{1, 2, 3, 4},
	)
	elements := append([]core.Element{}, dicomtest.MinimalDataset()...)
	elements = append(elements, pixel)
	original := object.FromElements(elements, std.Dictionary)

	encoded, err := Marshal(original, Options{ByteOrder: transfer.JPEGBaseline.ByteOrder})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalContext(context.Background(), encoded, std.Dictionary, UnmarshalOptions{
		TransferSyntax: transfer.JPEGBaseline,
	})
	if err != nil {
		t.Fatal(err)
	}
	decodedPixel, ok := decoded.Get(core.TagPixelData)
	if !ok {
		t.Fatal("decoded object is missing Pixel Data")
	}
	fragments, ok := decodedPixel.Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("decoded Pixel Data value = %T, want core.FragmentSequence", decodedPixel.Value)
	}
	if len(fragments.Fragments) != 2 {
		t.Fatalf("decoded fragment count = %d, want 2", len(fragments.Fragments))
	}

	var part10 bytes.Buffer
	if err := object.WriteFile(&part10, &object.File{Dataset: decoded, TransferSyntax: transfer.JPEGBaseline}); err != nil {
		t.Fatalf("WriteFile() after JSON round trip: %v", err)
	}
	readBack, err := object.ReadFile(bytes.NewReader(part10.Bytes()))
	if err != nil {
		t.Fatalf("ReadFile() after JSON round trip: %v", err)
	}
	readPixel, ok := readBack.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("Part 10 file is missing Pixel Data")
	}
	if _, ok := readPixel.Value.(core.FragmentSequence); !ok {
		t.Fatalf("Part 10 Pixel Data value = %T, want core.FragmentSequence", readPixel.Value)
	}
}

func TestEncapsulatedFragmentCountUsesSequenceItemLimit(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		nil,
		[]byte{1, 2},
		[]byte{3, 4},
	)
	obj := object.FromElements([]core.Element{pixel}, std.Dictionary)
	if _, err := MarshalContext(context.Background(), obj, Options{
		Limits: Limits{MaxSequenceItems: 2},
	}); err != nil {
		t.Fatalf("MarshalContext() exact fragment boundary error = %v", err)
	}
	if _, err := MarshalContext(context.Background(), obj, Options{
		Limits: Limits{MaxSequenceItems: 1},
	}); !errors.Is(err, ErrMaxSequenceItemsExceeded) {
		t.Fatalf("MarshalContext() fragment limit error = %v, want ErrMaxSequenceItemsExceeded", err)
	}

	encoded, err := Marshal(obj, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnmarshalContext(context.Background(), encoded, std.Dictionary, UnmarshalOptions{
		TransferSyntax: transfer.JPEGBaseline,
		Limits:         Limits{MaxSequenceItems: 2},
	}); err != nil {
		t.Fatalf("UnmarshalContext() exact fragment boundary error = %v", err)
	}
	if _, err := UnmarshalContext(context.Background(), encoded, std.Dictionary, UnmarshalOptions{
		TransferSyntax: transfer.JPEGBaseline,
		Limits:         Limits{MaxSequenceItems: 1},
	}); !errors.Is(err, ErrMaxSequenceItemsExceeded) {
		t.Fatalf("UnmarshalContext() fragment limit error = %v, want ErrMaxSequenceItemsExceeded", err)
	}
}

func TestEncapsulatedInlineBinaryLimitUsesCompleteValueField(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, []byte{1, 2, 3, 4})
	obj := object.FromElements([]core.Element{pixel}, std.Dictionary)
	valueField, err := fragmentSequenceValueField(pixel.Value.(core.FragmentSequence), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarshalContext(context.Background(), obj, Options{
		Limits: Limits{MaxInlineBinaryBytes: int64(len(valueField))},
	}); err != nil {
		t.Fatalf("MarshalContext() exact encapsulated boundary error = %v", err)
	}
	if _, err := MarshalContext(context.Background(), obj, Options{
		Limits: Limits{MaxInlineBinaryBytes: int64(len(valueField) - 1)},
	}); !errors.Is(err, ErrMaxInlineBinaryBytesExceeded) {
		t.Fatalf("MarshalContext() encapsulated boundary+1 error = %v, want ErrMaxInlineBinaryBytesExceeded", err)
	}
}

func TestEncapsulatedPixelDataRejectsMalformedValueField(t *testing.T) {
	_, err := UnmarshalContext(context.Background(), []byte(
		`{"7FE00010":{"vr":"OB","InlineBinary":"AQIDBA=="}}`,
	), std.Dictionary, UnmarshalOptions{TransferSyntax: transfer.JPEGBaseline})
	if err == nil {
		t.Fatal("UnmarshalContext() error = nil, want malformed encapsulated Value Field error")
	}
}
