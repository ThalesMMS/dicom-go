package encoding

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strconv"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

type primitiveCase[T comparable] struct {
	name  string
	value T
}

func TestBasicUnsignedIntegersRoundTripByEndianness(t *testing.T) {
	t.Parallel()

	testPrimitiveRoundTrip(t, "u16", []primitiveCase[uint16]{
		{name: "zero", value: 0},
		{name: "max", value: 0xFFFF},
		{name: "byte_order_sensitive", value: 0x0102},
	}, func(enc BasicEncoder, w io.Writer, value uint16) error {
		return enc.WriteU16(w, value)
	}, func(dec BasicDecoder, r io.Reader) (uint16, error) {
		return dec.ReadU16(r)
	}, func(order binary.ByteOrder, value uint16) []byte {
		return u16Bytes(order, value)
	})

	testPrimitiveRoundTrip(t, "u32", []primitiveCase[uint32]{
		{name: "zero", value: 0},
		{name: "max", value: 0xFFFFFFFF},
		{name: "byte_order_sensitive", value: 0x01020304},
	}, func(enc BasicEncoder, w io.Writer, value uint32) error {
		return enc.WriteU32(w, value)
	}, func(dec BasicDecoder, r io.Reader) (uint32, error) {
		return dec.ReadU32(r)
	}, func(order binary.ByteOrder, value uint32) []byte {
		return u32Bytes(order, value)
	})

	testPrimitiveRoundTrip(t, "u64", []primitiveCase[uint64]{
		{name: "zero", value: 0},
		{name: "max", value: 0xFFFFFFFFFFFFFFFF},
		{name: "byte_order_sensitive", value: 0x0102030405060708},
	}, func(enc BasicEncoder, w io.Writer, value uint64) error {
		return enc.WriteU64(w, value)
	}, func(dec BasicDecoder, r io.Reader) (uint64, error) {
		return dec.ReadU64(r)
	}, func(order binary.ByteOrder, value uint64) []byte {
		return u64Bytes(order, value)
	})
}

func TestBasicSignedIntegersRoundTripByEndianness(t *testing.T) {
	t.Parallel()

	testPrimitiveRoundTrip(t, "i16", []primitiveCase[int16]{
		{name: "zero", value: 0},
		{name: "positive", value: 0x0102},
		{name: "negative_one", value: -1},
		{name: "max", value: 32767},
		{name: "min", value: -32768},
	}, func(enc BasicEncoder, w io.Writer, value int16) error {
		return enc.WriteI16(w, value)
	}, func(dec BasicDecoder, r io.Reader) (int16, error) {
		return dec.ReadI16(r)
	}, func(order binary.ByteOrder, value int16) []byte {
		return u16Bytes(order, uint16(value))
	})

	testPrimitiveRoundTrip(t, "i32", []primitiveCase[int32]{
		{name: "zero", value: 0},
		{name: "positive", value: 0x01020304},
		{name: "negative_one", value: -1},
		{name: "max", value: 2147483647},
		{name: "min", value: -2147483648},
	}, func(enc BasicEncoder, w io.Writer, value int32) error {
		return enc.WriteI32(w, value)
	}, func(dec BasicDecoder, r io.Reader) (int32, error) {
		return dec.ReadI32(r)
	}, func(order binary.ByteOrder, value int32) []byte {
		return u32Bytes(order, uint32(value))
	})

	testPrimitiveRoundTrip(t, "i64", []primitiveCase[int64]{
		{name: "zero", value: 0},
		{name: "positive", value: 0x0102030405060708},
		{name: "negative_one", value: -1},
		{name: "max", value: 9223372036854775807},
		{name: "min", value: -9223372036854775808},
	}, func(enc BasicEncoder, w io.Writer, value int64) error {
		return enc.WriteI64(w, value)
	}, func(dec BasicDecoder, r io.Reader) (int64, error) {
		return dec.ReadI64(r)
	}, func(order binary.ByteOrder, value int64) []byte {
		return u64Bytes(order, uint64(value))
	})
}

func TestBasicFloatsRoundTripByEndianness(t *testing.T) {
	t.Parallel()

	testPrimitiveRoundTrip(t, "f32", []primitiveCase[float32]{
		{name: "zero", value: 0},
		{name: "positive_decimal", value: 1.5},
		{name: "negative_decimal", value: -123.25},
		{name: "positive_infinity", value: float32(math.Inf(1))},
		{name: "negative_infinity", value: float32(math.Inf(-1))},
	}, func(enc BasicEncoder, w io.Writer, value float32) error {
		return enc.WriteF32(w, value)
	}, func(dec BasicDecoder, r io.Reader) (float32, error) {
		return dec.ReadF32(r)
	}, func(order binary.ByteOrder, value float32) []byte {
		return u32Bytes(order, math.Float32bits(value))
	})

	testPrimitiveRoundTrip(t, "f64", []primitiveCase[float64]{
		{name: "zero", value: 0},
		{name: "positive_decimal", value: 1.5},
		{name: "negative_decimal", value: -123.25},
		{name: "pi", value: math.Pi},
		{name: "positive_infinity", value: math.Inf(1)},
		{name: "negative_infinity", value: math.Inf(-1)},
	}, func(enc BasicEncoder, w io.Writer, value float64) error {
		return enc.WriteF64(w, value)
	}, func(dec BasicDecoder, r io.Reader) (float64, error) {
		return dec.ReadF64(r)
	}, func(order binary.ByteOrder, value float64) []byte {
		return u64Bytes(order, math.Float64bits(value))
	})
}

func TestBasicTagsRoundTripByEndianness(t *testing.T) {
	t.Parallel()

	testPrimitiveRoundTrip(t, "tag", []primitiveCase[core.Tag]{
		{name: "standard", value: core.NewTag(0x0010, 0x0010)},
		{name: "private", value: core.NewTag(0x0011, 0x1001)},
		{name: "group_length", value: core.NewTag(0x0008, 0x0000)},
		{name: "max", value: core.NewTag(0xFFFF, 0xFFFF)},
	}, func(enc BasicEncoder, w io.Writer, value core.Tag) error {
		return enc.WriteTag(w, value)
	}, func(dec BasicDecoder, r io.Reader) (core.Tag, error) {
		return dec.ReadTag(r)
	}, func(order binary.ByteOrder, value core.Tag) []byte {
		buf := make([]byte, 4)
		order.PutUint16(buf[:2], value.Group)
		order.PutUint16(buf[2:], value.Element)
		return buf
	})
}

func TestBasicCoderDefaultsToLittleEndian(t *testing.T) {
	t.Parallel()

	if got := NewBasicDecoder(nil).Endianness(); got != LittleEndian {
		t.Fatalf("NewBasicDecoder(nil).Endianness() = %v, want %v", got, LittleEndian)
	}
	if got := NewBasicEncoder(nil).Endianness(); got != LittleEndian {
		t.Fatalf("NewBasicEncoder(nil).Endianness() = %v, want %v", got, LittleEndian)
	}
	if got := (BasicDecoder{}).Endianness(); got != LittleEndian {
		t.Fatalf("(BasicDecoder{}).Endianness() = %v, want %v", got, LittleEndian)
	}
	if got := (BasicEncoder{}).Endianness(); got != LittleEndian {
		t.Fatalf("(BasicEncoder{}).Endianness() = %v, want %v", got, LittleEndian)
	}
}

func TestBasicDecoderPreservesIOError(t *testing.T) {
	t.Parallel()

	t.Run("read_u32_short_buffer", func(t *testing.T) {
		t.Parallel()

		_, err := NewBasicDecoder(binary.LittleEndian).ReadU32(bytes.NewReader([]byte{0x01, 0x02, 0x03}))
		if err == nil {
			t.Fatal("ReadU32() error = nil, want wrapped error")
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("ReadU32() error = %v, want wrapped io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("read_tag_element_error", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		reader := io.MultiReader(bytes.NewReader([]byte{0x10, 0x00}), errReader{err: wantErr})

		_, err := NewBasicDecoder(binary.LittleEndian).ReadTag(reader)
		if err == nil {
			t.Fatal("ReadTag() error = nil, want wrapped error")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("ReadTag() error = %v, want wrapped %v", err, wantErr)
		}
	})
}

func TestBasicEncoderPreservesIOError(t *testing.T) {
	t.Parallel()

	t.Run("write_u16_error", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		err := NewBasicEncoder(binary.BigEndian).WriteU16(errWriter{err: wantErr}, 0x1234)
		if err == nil {
			t.Fatal("WriteU16() error = nil, want wrapped error")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("WriteU16() error = %v, want wrapped %v", err, wantErr)
		}
	})

	t.Run("write_tag_element_error", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		err := NewBasicEncoder(binary.BigEndian).WriteTag(&failAfterWriter{limit: 2, err: wantErr}, core.NewTag(0x1234, 0x5678))
		if err == nil {
			t.Fatal("WriteTag() error = nil, want wrapped error")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("WriteTag() error = %v, want wrapped %v", err, wantErr)
		}
	})
}

func TestBasicEncoderReportsShortWrite(t *testing.T) {
	t.Parallel()

	err := NewBasicEncoder(binary.LittleEndian).WriteU32(zeroWriter{}, 0x12345678)
	if err == nil {
		t.Fatal("WriteU32() error = nil, want wrapped io.ErrShortWrite")
	}
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WriteU32() error = %v, want wrapped io.ErrShortWrite", err)
	}
}

func TestPadBytes(t *testing.T) {
	t.Parallel()

	odd := []byte{0x01, 0x02, 0x03}
	padded := PadBytes(odd, 0x00)
	if !bytes.Equal(padded, []byte{0x01, 0x02, 0x03, 0x00}) {
		t.Fatalf("PadBytes(%v, 0x00) = %v", odd, padded)
	}
	odd[0] = 0xFF
	if padded[0] != 0x01 {
		t.Fatal("PadBytes should clone the input slice")
	}

	even := []byte{0xAA, 0xBB}
	cloned := PadBytes(even, 0x00)
	if !bytes.Equal(cloned, even) {
		t.Fatalf("PadBytes(%v, 0x00) = %v", even, cloned)
	}
	even[0] = 0x00
	if cloned[0] != 0xAA {
		t.Fatal("PadBytes should not alias even-length inputs")
	}
}

func TestUint32Length(t *testing.T) {
	t.Parallel()

	if got, err := Uint32Length(42); err != nil || got != 42 {
		t.Fatalf("Uint32Length(42) = (%d, %v), want (42, nil)", got, err)
	}

	if strconv.IntSize <= 32 {
		t.Skip("overflow case requires 64-bit int")
	}

	_, err := Uint32Length(int(math.MaxUint32))
	if err == nil {
		t.Fatal("Uint32Length(math.MaxUint32) error = nil, want overflow error")
	}
	if !errors.Is(err, ErrLengthOverflow) {
		t.Fatalf("Uint32Length(math.MaxUint32) error = %v, want wrapped ErrLengthOverflow", err)
	}

	_, err = Uint32Length(int(uint64(math.MaxUint32) + 1))
	if err == nil {
		t.Fatal("Uint32Length(overflow) error = nil, want overflow error")
	}
	if !errors.Is(err, ErrLengthOverflow) {
		t.Fatalf("Uint32Length(overflow) error = %v, want wrapped ErrLengthOverflow", err)
	}
}

func testPrimitiveRoundTrip[T comparable](
	t *testing.T,
	kind string,
	cases []primitiveCase[T],
	write func(BasicEncoder, io.Writer, T) error,
	read func(BasicDecoder, io.Reader) (T, error),
	bytesFor func(binary.ByteOrder, T) []byte,
) {
	t.Helper()

	for _, endianCase := range []struct {
		name  string
		order binary.ByteOrder
	}{
		{name: "little", order: binary.LittleEndian},
		{name: "big", order: binary.BigEndian},
	} {
		endianCase := endianCase
		t.Run(kind+"/"+endianCase.name, func(t *testing.T) {
			t.Parallel()

			enc := NewBasicEncoder(endianCase.order)
			dec := NewBasicDecoder(endianCase.order)

			for _, tc := range cases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					wantBytes := bytesFor(endianCase.order, tc.value)

					var buf bytes.Buffer
					if err := write(enc, &buf, tc.value); err != nil {
						t.Fatalf("write failed: %v", err)
					}
					if got := buf.Bytes(); !bytes.Equal(got, wantBytes) {
						t.Fatalf("encoded bytes = % X, want % X", got, wantBytes)
					}

					got, err := read(dec, bytes.NewReader(wantBytes))
					if err != nil {
						t.Fatalf("read failed: %v", err)
					}
					if got != tc.value {
						t.Fatalf("decoded value = %#v, want %#v", got, tc.value)
					}
				})
			}
		})
	}
}

func u16Bytes(order binary.ByteOrder, value uint16) []byte {
	buf := make([]byte, 2)
	order.PutUint16(buf, value)
	return buf
}

func u32Bytes(order binary.ByteOrder, value uint32) []byte {
	buf := make([]byte, 4)
	order.PutUint32(buf, value)
	return buf
}

func u64Bytes(order binary.ByteOrder, value uint64) []byte {
	buf := make([]byte, 8)
	order.PutUint64(buf, value)
	return buf
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type failAfterWriter struct {
	limit int
	wrote int
	err   error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.wrote >= w.limit {
		return 0, w.err
	}

	remaining := w.limit - w.wrote
	if len(p) > remaining {
		w.wrote += remaining
		return remaining, nil
	}
	w.wrote += len(p)
	return len(p), nil
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}
