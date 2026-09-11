package dicomjson

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrInvalidLimits                = errors.New("dicomjson: invalid resource limits")
	ErrMaxJSONBytesExceeded         = errors.New("dicomjson: maximum JSON bytes exceeded")
	ErrMaxInlineBinaryBytesExceeded = errors.New("dicomjson: maximum InlineBinary bytes exceeded")
	ErrMaxTotalBinaryBytesExceeded  = errors.New("dicomjson: maximum total binary bytes exceeded")
	ErrMaxSequenceDepthExceeded     = errors.New("dicomjson: maximum sequence depth exceeded")
	ErrMaxElementsExceeded          = errors.New("dicomjson: maximum element count exceeded")
	ErrMaxSequenceItemsExceeded     = errors.New("dicomjson: maximum sequence item count exceeded")
)

// Limits bounds DICOM JSON conversion work. Zero-valued fields preserve the
// historical unlimited behavior. Negative values are invalid.
type Limits struct {
	MaxJSONBytes         int64
	MaxInlineBinaryBytes int64
	MaxTotalBinaryBytes  int64
	MaxSequenceDepth     int
	MaxElements          int
	// MaxSequenceItems bounds cumulative SQ items and encapsulated Pixel Data
	// fragments. The Basic Offset Table is not counted as a fragment.
	MaxSequenceItems int
}

func (limits Limits) validate() error {
	if limits.MaxJSONBytes < 0 || limits.MaxInlineBinaryBytes < 0 || limits.MaxTotalBinaryBytes < 0 ||
		limits.MaxSequenceDepth < 0 || limits.MaxElements < 0 || limits.MaxSequenceItems < 0 {
		return fmt.Errorf("%w: limits must not be negative", ErrInvalidLimits)
	}
	return nil
}

type conversionBudget struct {
	ctx           context.Context
	limits        Limits
	elements      int
	sequenceItems int
	binaryBytes   int64
}

func newConversionBudget(ctx context.Context, limits Limits) (*conversionBudget, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &conversionBudget{ctx: ctx, limits: limits}, nil
}

func (budget *conversionBudget) checkContext() error {
	if budget == nil || budget.ctx == nil {
		return nil
	}
	return budget.ctx.Err()
}

func (budget *conversionBudget) checkJSONBytes(size int) error {
	if err := budget.checkContext(); err != nil {
		return err
	}
	if budget.limits.MaxJSONBytes > 0 && int64(size) > budget.limits.MaxJSONBytes {
		return fmt.Errorf("%w: got %d, limit %d", ErrMaxJSONBytesExceeded, size, budget.limits.MaxJSONBytes)
	}
	return nil
}

func (budget *conversionBudget) enterDataSet(depth, elements int) error {
	if err := budget.checkContext(); err != nil {
		return err
	}
	if budget.limits.MaxSequenceDepth > 0 && depth > budget.limits.MaxSequenceDepth {
		return fmt.Errorf("%w: got %d, limit %d", ErrMaxSequenceDepthExceeded, depth, budget.limits.MaxSequenceDepth)
	}
	if budget.limits.MaxElements > 0 && elements > budget.limits.MaxElements-budget.elements {
		return fmt.Errorf("%w: got more than %d", ErrMaxElementsExceeded, budget.limits.MaxElements)
	}
	budget.elements += elements
	return nil
}

func (budget *conversionBudget) addSequenceItems(items int) error {
	if err := budget.checkContext(); err != nil {
		return err
	}
	if budget.limits.MaxSequenceItems > 0 && items > budget.limits.MaxSequenceItems-budget.sequenceItems {
		return fmt.Errorf("%w: got more than %d", ErrMaxSequenceItemsExceeded, budget.limits.MaxSequenceItems)
	}
	budget.sequenceItems += items
	return nil
}

func (budget *conversionBudget) parserFragmentLimit() int {
	if budget.limits.MaxSequenceItems == 0 {
		return 0
	}
	remaining := budget.limits.MaxSequenceItems - budget.sequenceItems
	if remaining < 1 {
		// parser.Reader uses zero to mean unlimited. Allow it to materialize at
		// most one fragment so addSequenceItems can report the cumulative limit.
		return 1
	}
	return remaining
}

func (budget *conversionBudget) addInlineBinary(size int64) error {
	if err := budget.checkContext(); err != nil {
		return err
	}
	if budget.limits.MaxInlineBinaryBytes > 0 && size > budget.limits.MaxInlineBinaryBytes {
		return fmt.Errorf("%w: got %d, limit %d", ErrMaxInlineBinaryBytesExceeded, size, budget.limits.MaxInlineBinaryBytes)
	}
	if budget.limits.MaxTotalBinaryBytes > 0 && size > budget.limits.MaxTotalBinaryBytes-budget.binaryBytes {
		return fmt.Errorf("%w: got more than %d", ErrMaxTotalBinaryBytesExceeded, budget.limits.MaxTotalBinaryBytes)
	}
	budget.binaryBytes += size
	return nil
}
