package jpeglossless

import "fmt"

// huffTable is a JPEG Huffman decode table built from the DHT BITS/HUFFVAL
// arrays using the canonical generation in ITU-T T.81 Annex C/F.
type huffTable struct {
	mincode [17]int
	maxcode [17]int // -1 when no codes of that length
	valptr  [17]int
	values  []byte
}

func buildHuffTable(counts [17]int, values []byte) *huffTable {
	// HUFFSIZE: code length per value, in order.
	var sizes []int
	for l := 1; l <= 16; l++ {
		for i := 0; i < counts[l]; i++ {
			sizes = append(sizes, l)
		}
	}
	// HUFFCODE: canonical codes.
	codes := make([]int, len(sizes))
	if len(sizes) > 0 {
		code := 0
		si := sizes[0]
		k := 0
		for k < len(sizes) {
			for k < len(sizes) && sizes[k] == si {
				codes[k] = code
				code++
				k++
			}
			code <<= 1
			si++
		}
	}
	t := &huffTable{values: values}
	k := 0
	for l := 1; l <= 16; l++ {
		if counts[l] == 0 {
			t.maxcode[l] = -1
			continue
		}
		t.valptr[l] = k
		t.mincode[l] = codes[k]
		k += counts[l]
		t.maxcode[l] = codes[k-1]
	}
	return t
}

// decodeSymbol reads bits until a code matches (T.81 Annex F.2.2.3 DECODE).
func (t *huffTable) decodeSymbol(br *bitReader) (byte, error) {
	code := 0
	for l := 1; l <= 16; l++ {
		b, err := br.readBit()
		if err != nil {
			return 0, err
		}
		code = (code << 1) | b
		if t.maxcode[l] >= 0 && code <= t.maxcode[l] {
			idx := t.valptr[l] + (code - t.mincode[l])
			if idx < 0 || idx >= len(t.values) {
				return 0, fmt.Errorf("%w: Huffman value index out of range", ErrInvalidStream)
			}
			return t.values[idx], nil
		}
	}
	return 0, fmt.Errorf("%w: undecodable Huffman code", ErrInvalidStream)
}

// bitReader reads MSB-first bits from a JPEG entropy segment, undoing 0xFF byte
// stuffing. It stops at a marker (returns an error if more bits are requested).
type bitReader struct {
	data  []byte
	pos   int
	cur   byte
	nbits int
}

func (br *bitReader) readBit() (int, error) {
	if br.nbits == 0 {
		if br.pos >= len(br.data) {
			return 0, fmt.Errorf("%w: unexpected end of entropy data", ErrInvalidStream)
		}
		b := br.data[br.pos]
		br.pos++
		if b == 0xFF {
			if br.pos >= len(br.data) {
				return 0, fmt.Errorf("%w: dangling 0xFF in entropy data", ErrInvalidStream)
			}
			next := br.data[br.pos]
			switch {
			case next == 0x00:
				br.pos++ // stuffed zero → literal 0xFF
			default:
				return 0, fmt.Errorf("%w: marker 0xFF%02X inside entropy data", ErrInvalidStream, next)
			}
		}
		br.cur = b
		br.nbits = 8
	}
	br.nbits--
	return int((br.cur >> uint(br.nbits)) & 1), nil
}

func (br *bitReader) readBits(n int) (int, error) {
	v := 0
	for i := 0; i < n; i++ {
		b, err := br.readBit()
		if err != nil {
			return 0, err
		}
		v = (v << 1) | b
	}
	return v, nil
}

// alignToRestart discards the rest of the current byte and skips the next
// restart marker (FF D0–D7).
func (br *bitReader) alignToRestart() error {
	br.nbits = 0
	if br.pos+1 >= len(br.data) || br.data[br.pos] != 0xFF {
		return fmt.Errorf("%w: expected restart marker", ErrInvalidStream)
	}
	m := br.data[br.pos+1]
	if m < 0xD0 || m > 0xD7 {
		return fmt.Errorf("%w: 0xFF%02X is not a restart marker", ErrInvalidStream, m)
	}
	br.pos += 2
	return nil
}
