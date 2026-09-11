package multiframe

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ThalesMMS/dicom-go/core"
)

type conversionBudget struct {
	ctx         context.Context
	maxElements int
	maxDepth    int
	elements    int
}

func (b *conversionBudget) cloneElement(element core.Element, depth int) (core.Element, error) {
	if err := b.ctx.Err(); err != nil {
		return core.Element{}, err
	}
	if depth > b.maxDepth || b.elements >= b.maxElements {
		return core.Element{}, fmt.Errorf("%w: elements or sequence depth", ErrResourceLimit)
	}
	b.elements++
	clone := element
	value, err := b.cloneValue(element.Value, depth)
	if err != nil {
		return core.Element{}, err
	}
	clone.Value = value
	return clone, nil
}

func (b *conversionBudget) cloneValue(value core.Value, depth int) (core.Value, error) {
	switch value := value.(type) {
	case nil:
		return nil, fmt.Errorf("%w: deferred element value", ErrInvalidSource)
	case core.RawValue:
		return core.RawValue(core.CloneBytes(value)), nil
	case core.StringValue:
		return append(core.StringValue(nil), value...), nil
	case core.Uint16Value:
		return append(core.Uint16Value(nil), value...), nil
	case core.Int16Value:
		return append(core.Int16Value(nil), value...), nil
	case core.Uint32Value:
		return append(core.Uint32Value(nil), value...), nil
	case core.Int32Value:
		return append(core.Int32Value(nil), value...), nil
	case core.Uint64Value:
		return append(core.Uint64Value(nil), value...), nil
	case core.Int64Value:
		return append(core.Int64Value(nil), value...), nil
	case core.Float32Value:
		return append(core.Float32Value(nil), value...), nil
	case core.Float64Value:
		return append(core.Float64Value(nil), value...), nil
	case core.TagValue:
		return append(core.TagValue(nil), value...), nil
	case core.BulkDataValue:
		return value, nil
	case core.FragmentSequence:
		out := core.FragmentSequence{OffsetTable: core.CloneBytes(value.OffsetTable), Fragments: make([][]byte, len(value.Fragments))}
		for i := range value.Fragments {
			out.Fragments[i] = core.CloneBytes(value.Fragments[i])
		}
		return out, nil
	case core.SequenceValue:
		if depth >= b.maxDepth {
			return nil, fmt.Errorf("%w: sequence depth", ErrResourceLimit)
		}
		items := make([]core.DataSet, len(value.Items))
		for i, item := range value.Items {
			items[i] = item
			items[i].Elements = make([]core.Element, len(item.Elements))
			for j, element := range item.Elements {
				clone, err := b.cloneElement(element, depth+1)
				if err != nil {
					return nil, err
				}
				items[i].Elements[j] = clone
			}
		}
		return core.SequenceValue{Items: items}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported element value type", ErrInvalidSource)
	}
}

func elementsEqual(a, b core.Element) bool {
	return a.Tag() == b.Tag() && a.VR() == b.VR() && reflect.DeepEqual(a.Value, b.Value)
}
