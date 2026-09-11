package jpegls

import (
	"errors"
	"io"
	"testing"
)

// The bit-at-a-time reference retains the former readBits semantics, including
// state after stuffed bytes, an invalid marker and truncated input.
func referenceReadBits(reader *bitReader, n int) (uint32, error) {
	var value uint32
	for i := 0; i < n; i++ {
		bit, err := reader.readBit()
		if err != nil {
			return 0, err
		}
		value = (value << 1) | bit
	}
	return value, nil
}

func checkBatchedReadBits(t testing.TB, raw []byte, widths []byte) {
	t.Helper()
	actual := bitReader{buf: raw}
	reference := bitReader{buf: raw}
	for _, width := range widths {
		n := int(width % 41)
		got, gotErr := actual.readBits(n)
		want, wantErr := referenceReadBits(&reference, n)
		if got != want || (gotErr == nil) != (wantErr == nil) || errors.Is(gotErr, io.ErrUnexpectedEOF) != errors.Is(wantErr, io.ErrUnexpectedEOF) || errors.Is(gotErr, ErrInvalidCodestream) != errors.Is(wantErr, ErrInvalidCodestream) {
			t.Fatalf("readBits(%d): result/error differs", n)
		}
		if actual.pos != reference.pos || actual.count != reference.count || actual.val != reference.val || actual.lastWasFF != reference.lastWasFF {
			t.Fatalf("readBits(%d): reader state differs", n)
		}
	}
}

func TestReadBitsBatchPreservesStuffingAndFailureState(t *testing.T) {
	for _, raw := range [][]byte{nil, {0}, {0xff}, {0xff, 0x7f, 0xff, 0, 0x12, 0x34}, {0xff, 0x80}, {0x55, 0xaa, 0x81, 0x00, 0xfe, 0xff, 0x7f}} {
		for prefix := byte(0); prefix < 9; prefix++ {
			for width := byte(0); width <= 40; width++ {
				checkBatchedReadBits(t, raw, []byte{prefix, width, 7, 1, 8, 32, 0})
			}
		}
	}
}

func FuzzReadBitsBatchEquivalent(f *testing.F) {
	f.Add([]byte{0xff, 0x7f, 0x55, 0xaa}, []byte{3, 8, 1, 16})
	f.Add([]byte{0xff, 0x80}, []byte{7, 2, 8})
	f.Fuzz(func(t *testing.T, raw, widths []byte) {
		if len(raw) > 4096 || len(widths) > 256 {
			return
		}
		checkBatchedReadBits(t, raw, widths)
	})
}
