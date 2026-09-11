package pixeldata

import (
	"context"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func equivalentCloneLimits(limits TranscodeLimits) TranscodeLimits {
	if limits.MaxOutputBytes < limits.MaxInputBytes {
		limits.MaxInputBytes = limits.MaxOutputBytes
	}
	return limits
}

func outputCloneLimits(limits TranscodeLimits) TranscodeLimits {
	limits.MaxInputBytes = limits.MaxOutputBytes
	return limits
}

func cloneDetachedObject(ctx context.Context, source *object.Object, limits TranscodeLimits) (*object.Object, error) {
	if source == nil {
		return nil, nil
	}
	state := cloneState{ctx: ctx, limits: limits}
	elements, err := state.cloneElements(source.Elements(), 1)
	if err != nil {
		return nil, err
	}
	clone := object.FromElements(elements, nil)
	clone.SetValueByteOrder(source.ValueByteOrder())
	return clone, nil
}

type cloneState struct {
	ctx       context.Context
	limits    TranscodeLimits
	elements  int
	fragments int
	bytes     int64
}

func (s *cloneState) cloneElements(elements []core.Element, depth int) ([]core.Element, error) {
	if depth > s.limits.MaxDepth || len(elements) > s.limits.MaxElements-s.elements {
		return nil, transcodeLimitError("elements")
	}
	s.elements += len(elements)
	out := make([]core.Element, len(elements))
	for i, element := range elements {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		out[i] = element
		value, err := s.cloneValue(element.Value, depth)
		if err != nil {
			return nil, err
		}
		out[i].Value = value
	}
	return out, nil
}

func (s *cloneState) cloneValue(value core.Value, depth int) (core.Value, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: deferred value is unavailable", ErrTranscodeUnsupported)
	}
	addBytes := func(n int) error {
		if n < 0 || int64(n) > s.limits.MaxInputBytes-s.bytes {
			return transcodeLimitError("input_bytes")
		}
		s.bytes += int64(n)
		return nil
	}
	switch v := value.(type) {
	case core.RawValue:
		if err := addBytes(len(v)); err != nil {
			return nil, err
		}
		return core.RawValue(core.CloneBytes(v)), nil
	case core.StringValue:
		if len(v) > s.limits.MaxElements-s.elements {
			return nil, transcodeLimitError("elements")
		}
		s.elements += len(v)
		length, ok := v.EncodedLength()
		if !ok || uint64(length) > math.MaxInt {
			return nil, transcodeLimitError("input_bytes")
		}
		if err := addBytes(int(length)); err != nil {
			return nil, err
		}
		return append(core.StringValue(nil), v...), nil
	case core.Uint16Value:
		if err := addBytes(len(v) * 2); err != nil {
			return nil, err
		}
		return append(core.Uint16Value(nil), v...), nil
	case core.Int16Value:
		if err := addBytes(len(v) * 2); err != nil {
			return nil, err
		}
		return append(core.Int16Value(nil), v...), nil
	case core.Uint32Value:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.Uint32Value(nil), v...), nil
	case core.Int32Value:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.Int32Value(nil), v...), nil
	case core.Uint64Value:
		if err := addBytes(len(v) * 8); err != nil {
			return nil, err
		}
		return append(core.Uint64Value(nil), v...), nil
	case core.Int64Value:
		if err := addBytes(len(v) * 8); err != nil {
			return nil, err
		}
		return append(core.Int64Value(nil), v...), nil
	case core.Float32Value:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.Float32Value(nil), v...), nil
	case core.Float64Value:
		if err := addBytes(len(v) * 8); err != nil {
			return nil, err
		}
		return append(core.Float64Value(nil), v...), nil
	case core.TagValue:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.TagValue(nil), v...), nil
	case core.SequenceValue:
		if len(v.Items) > s.limits.MaxElements-s.elements {
			return nil, transcodeLimitError("elements")
		}
		s.elements += len(v.Items)
		items := make([]core.DataSet, len(v.Items))
		for i, item := range v.Items {
			elements, err := s.cloneElements(item.Elements, depth+1)
			if err != nil {
				return nil, err
			}
			items[i] = item
			items[i].Elements = elements
		}
		return core.SequenceValue{Items: items}, nil
	case core.FragmentSequence:
		if len(v.Fragments) > s.limits.MaxFragments-s.fragments {
			return nil, transcodeLimitError("fragments")
		}
		s.fragments += len(v.Fragments)
		if err := addBytes(len(v.OffsetTable)); err != nil {
			return nil, err
		}
		out := core.FragmentSequence{OffsetTable: core.CloneBytes(v.OffsetTable), Fragments: make([][]byte, len(v.Fragments))}
		for i, fragment := range v.Fragments {
			if err := addBytes(len(fragment)); err != nil {
				return nil, err
			}
			out.Fragments[i] = core.CloneBytes(fragment)
		}
		return out, nil
	case core.BulkDataValue:
		if err := addBytes(len(v.URI)); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, fmt.Errorf("%w: unsupported element value", ErrTranscodeUnsupported)
	}
}
