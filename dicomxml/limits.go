package dicomxml

import (
	"context"
	"fmt"
)

type budget struct {
	ctx      context.Context
	limits   Limits
	elements int
	items    int
	binary   int64
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxXMLBytes < 0 || limits.MaxValueBytes < 0 || limits.MaxInlineBinaryBytes < 0 ||
		limits.MaxTotalBinaryBytes < 0 || limits.MaxSequenceDepth < 0 || limits.MaxElements < 0 ||
		limits.MaxSequenceItems < 0 {
		return Limits{}, ErrInvalidLimits
	}
	defaults := DefaultLimits()
	if limits.MaxXMLBytes == 0 {
		limits.MaxXMLBytes = defaults.MaxXMLBytes
	}
	if limits.MaxValueBytes == 0 {
		limits.MaxValueBytes = defaults.MaxValueBytes
	}
	if limits.MaxInlineBinaryBytes == 0 {
		limits.MaxInlineBinaryBytes = defaults.MaxInlineBinaryBytes
	}
	if limits.MaxTotalBinaryBytes == 0 {
		limits.MaxTotalBinaryBytes = defaults.MaxTotalBinaryBytes
	}
	if limits.MaxSequenceDepth == 0 {
		limits.MaxSequenceDepth = defaults.MaxSequenceDepth
	}
	if limits.MaxElements == 0 {
		limits.MaxElements = defaults.MaxElements
	}
	if limits.MaxSequenceItems == 0 {
		limits.MaxSequenceItems = defaults.MaxSequenceItems
	}
	return limits, nil
}

func newBudget(ctx context.Context, limits Limits) (*budget, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &budget{ctx: ctx, limits: limits}, nil
}

func (b *budget) check() error { return b.ctx.Err() }

func (b *budget) addElements(n, depth int) error {
	if err := b.check(); err != nil {
		return err
	}
	if depth > b.limits.MaxSequenceDepth {
		return fmt.Errorf("%w: got %d, limit %d", ErrMaxDepthExceeded, depth, b.limits.MaxSequenceDepth)
	}
	if n > b.limits.MaxElements-b.elements {
		return fmt.Errorf("%w: limit %d", ErrMaxElementsExceeded, b.limits.MaxElements)
	}
	b.elements += n
	return nil
}

func (b *budget) addItem(depth int) error {
	if err := b.addElements(0, depth); err != nil {
		return err
	}
	if b.items >= b.limits.MaxSequenceItems {
		return fmt.Errorf("%w: limit %d", ErrMaxItemsExceeded, b.limits.MaxSequenceItems)
	}
	b.items++
	return nil
}

func (b *budget) checkValue(n int) error {
	if err := b.check(); err != nil {
		return err
	}
	if n > b.limits.MaxValueBytes {
		return fmt.Errorf("%w: got %d, limit %d", ErrMaxValueBytesExceeded, n, b.limits.MaxValueBytes)
	}
	return nil
}

func (b *budget) addBinary(n int64, inline bool) error {
	if err := b.check(); err != nil {
		return err
	}
	if inline && n > b.limits.MaxInlineBinaryBytes {
		return fmt.Errorf("%w: got %d, limit %d", ErrMaxInlineBytesExceeded, n, b.limits.MaxInlineBinaryBytes)
	}
	if n > b.limits.MaxTotalBinaryBytes-b.binary {
		return fmt.Errorf("%w: limit %d", ErrMaxBinaryBytesExceeded, b.limits.MaxTotalBinaryBytes)
	}
	b.binary += n
	return nil
}

func jsonLimit(xmlLimit int64) int64 {
	const max = int64(^uint64(0) >> 1)
	if xmlLimit > max/4 {
		return max
	}
	return xmlLimit * 4
}
