package jpegls

import (
	"fmt"
	"io"
)

type bitWriter struct {
	buf   []byte
	val   uint32
	count int
}

func (w *bitWriter) writeBit(bit uint32) {
	w.val = (w.val << 1) | (bit & 1)
	w.count++
	if w.count < 8 {
		return
	}
	b := byte(w.val)
	w.buf = append(w.buf, b)
	w.val = 0
	w.count = 0
	if b == 0xff {
		w.val = 0
		w.count = 1
	}
}

func (w *bitWriter) writeBits(value uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.writeBit((value >> uint(i)) & 1)
	}
}

func (w *bitWriter) writeOnes(n int) {
	for i := 0; i < n; i++ {
		w.writeBit(1)
	}
}

func (w *bitWriter) writeZeros(n int) {
	for i := 0; i < n; i++ {
		w.writeBit(0)
	}
}

func (w *bitWriter) padToByte() {
	if w.count == 0 {
		return
	}
	w.writeZeros(8 - w.count)
}

type bitReader struct {
	buf       []byte
	pos       int
	val       uint32
	count     int
	lastWasFF bool
}

func (r *bitReader) readBit() (uint32, error) {
	if r.count == 0 {
		if r.pos >= len(r.buf) {
			return 0, io.ErrUnexpectedEOF
		}
		b := r.buf[r.pos]
		r.pos++
		if r.lastWasFF {
			if b&0x80 != 0 {
				return 0, fmt.Errorf("%w: unexpected marker in entropy data", ErrInvalidCodestream)
			}
			r.val = uint32(b) & 0x7f
			r.count = 7
			r.lastWasFF = false
		} else {
			r.val = uint32(b)
			r.count = 8
			r.lastWasFF = b == 0xff
		}
	}
	r.count--
	return (r.val >> r.count) & 1, nil
}

func (r *bitReader) readBits(n int) (uint32, error) {
	var value uint32
	for n > 0 {
		if r.count == 0 {
			// Keep byte refill, JPEG-LS stuffing and marker/error handling in
			// readBit. Only already-buffered bits are consumed in a batch.
			bit, err := r.readBit()
			if err != nil {
				return 0, err
			}
			value = (value << 1) | bit
			n--
			continue
		}
		take := n
		if take > r.count {
			take = r.count
		}
		r.count -= take
		value = (value << uint(take)) | ((r.val >> uint(r.count)) & ((1 << uint(take)) - 1))
		n -= take
	}
	return value, nil
}
