package core

import "errors"

// ErrDiscardedValue means a payload was deliberately consumed without retention.
// It cannot be serialized or replayed; the caller must supply replacement data.
var ErrDiscardedValue = errors.New("dicom: payload was discarded during streaming")

// DiscardedValue preserves the distinction between a discarded payload and an
// empty or deferred value. In particular, copying its element to another dataset
// must not turn it into writable empty Pixel Data.
type DiscardedValue struct{}

func (DiscardedValue) Kind() ValueKind               { return ValueDiscarded }
func (DiscardedValue) EncodedLength() (Length, bool) { return UndefinedLength, false }
