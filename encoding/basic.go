package encoding

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/ThalesMMS/dicom-go/core"
)

type Endianness uint8

const (
	LittleEndian Endianness = iota
	BigEndian
)

var ErrLengthOverflow = errors.New("dicom: value length exceeds uint32")

func (e Endianness) ByteOrder() binary.ByteOrder {
	if e == BigEndian {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

type BasicDecoder struct {
	Order binary.ByteOrder
}

func NewBasicDecoder(order binary.ByteOrder) BasicDecoder {
	if order == nil {
		order = binary.LittleEndian
	}
	return BasicDecoder{Order: order}
}

func (d BasicDecoder) byteOrder() binary.ByteOrder {
	if d.Order == nil {
		return binary.LittleEndian
	}
	return d.Order
}

func (d BasicDecoder) Endianness() Endianness {
	if d.byteOrder() == binary.BigEndian {
		return BigEndian
	}
	return LittleEndian
}

type BasicEncoder struct {
	Order binary.ByteOrder
}

func NewBasicEncoder(order binary.ByteOrder) BasicEncoder {
	if order == nil {
		order = binary.LittleEndian
	}
	return BasicEncoder{Order: order}
}

func (e BasicEncoder) byteOrder() binary.ByteOrder {
	if e.Order == nil {
		return binary.LittleEndian
	}
	return e.Order
}

func (e BasicEncoder) Endianness() Endianness {
	if e.byteOrder() == binary.BigEndian {
		return BigEndian
	}
	return LittleEndian
}

func readExact(r io.Reader, buf []byte, what string) error {
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("dicom: read %s: %w", what, err)
	}
	return nil
}

func writeExact(w io.Writer, buf []byte, what string) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return fmt.Errorf("dicom: write %s: %w", what, err)
		}
		if n == 0 {
			return fmt.Errorf("dicom: write %s: %w", what, io.ErrShortWrite)
		}
		buf = buf[n:]
	}
	return nil
}

func (d BasicDecoder) ReadU16(r io.Reader) (uint16, error) {
	var b [2]byte
	if err := readExact(r, b[:], "uint16"); err != nil {
		return 0, err
	}
	return d.byteOrder().Uint16(b[:]), nil
}

func (d BasicDecoder) ReadU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if err := readExact(r, b[:], "uint32"); err != nil {
		return 0, err
	}
	return d.byteOrder().Uint32(b[:]), nil
}

func (d BasicDecoder) ReadU64(r io.Reader) (uint64, error) {
	var b [8]byte
	if err := readExact(r, b[:], "uint64"); err != nil {
		return 0, err
	}
	return d.byteOrder().Uint64(b[:]), nil
}

func (d BasicDecoder) ReadI16(r io.Reader) (int16, error) {
	v, err := d.ReadU16(r)
	if err != nil {
		return 0, err
	}
	return int16(v), nil
}

func (d BasicDecoder) ReadI32(r io.Reader) (int32, error) {
	v, err := d.ReadU32(r)
	if err != nil {
		return 0, err
	}
	return int32(v), nil
}

func (d BasicDecoder) ReadI64(r io.Reader) (int64, error) {
	v, err := d.ReadU64(r)
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

func (d BasicDecoder) ReadF32(r io.Reader) (float32, error) {
	v, err := d.ReadU32(r)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}

func (d BasicDecoder) ReadF64(r io.Reader) (float64, error) {
	v, err := d.ReadU64(r)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

func (d BasicDecoder) ReadTag(r io.Reader) (core.Tag, error) {
	group, err := d.ReadU16(r)
	if err != nil {
		return core.Tag{}, fmt.Errorf("dicom: read tag group: %w", err)
	}
	element, err := d.ReadU16(r)
	if err != nil {
		return core.Tag{}, fmt.Errorf("dicom: read tag element: %w", err)
	}
	return core.NewTag(group, element), nil
}

func (e BasicEncoder) WriteU16(w io.Writer, v uint16) error {
	var b [2]byte
	e.byteOrder().PutUint16(b[:], v)
	return writeExact(w, b[:], "uint16")
}

func (e BasicEncoder) WriteU32(w io.Writer, v uint32) error {
	var b [4]byte
	e.byteOrder().PutUint32(b[:], v)
	return writeExact(w, b[:], "uint32")
}

func (e BasicEncoder) WriteU64(w io.Writer, v uint64) error {
	var b [8]byte
	e.byteOrder().PutUint64(b[:], v)
	return writeExact(w, b[:], "uint64")
}

func (e BasicEncoder) WriteI16(w io.Writer, v int16) error {
	return e.WriteU16(w, uint16(v))
}

func (e BasicEncoder) WriteI32(w io.Writer, v int32) error {
	return e.WriteU32(w, uint32(v))
}

func (e BasicEncoder) WriteI64(w io.Writer, v int64) error {
	return e.WriteU64(w, uint64(v))
}

func (e BasicEncoder) WriteF32(w io.Writer, v float32) error {
	return e.WriteU32(w, math.Float32bits(v))
}

func (e BasicEncoder) WriteF64(w io.Writer, v float64) error {
	return e.WriteU64(w, math.Float64bits(v))
}

func (e BasicEncoder) WriteTag(w io.Writer, t core.Tag) error {
	if err := e.WriteU16(w, t.Group); err != nil {
		return fmt.Errorf("dicom: write tag group: %w", err)
	}
	if err := e.WriteU16(w, t.Element); err != nil {
		return fmt.Errorf("dicom: write tag element: %w", err)
	}
	return nil
}

func EvenLength(n int) int {
	if n%2 == 0 {
		return n
	}
	return n + 1
}

func PadBytes(data []byte, pad byte) []byte {
	padded := core.CloneBytes(data)
	if len(padded) == EvenLength(len(padded)) {
		return padded
	}
	return append(padded, pad)
}

func Uint32Length(n int) (uint32, error) {
	if n < 0 || uint64(n) >= math.MaxUint32 {
		return 0, fmt.Errorf("dicom: length %d overflows uint32: %w", n, ErrLengthOverflow)
	}
	return uint32(n), nil
}
