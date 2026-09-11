package object

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

const WalkPathNoItem = -1

const (
	defaultWalkMaxDepth    = 64
	defaultWalkMaxElements = 100_000
)

var (
	ErrWalkResourceLimit  = errors.New("dicom: Walk resource limit exceeded")
	ErrInvalidWalkOptions = errors.New("dicom: invalid Walk options")
)

type WalkLimit string

const (
	WalkLimitDepth    WalkLimit = "depth"
	WalkLimitElements WalkLimit = "elements"
)

type WalkLimitError struct {
	Limit   WalkLimit
	Maximum int
	Visited int
}

func (err *WalkLimitError) Error() string {
	if err == nil {
		return ErrWalkResourceLimit.Error()
	}
	return fmt.Sprintf("%s: %s maximum %d after %d visits", ErrWalkResourceLimit, err.Limit, err.Maximum, err.Visited)
}

func (err *WalkLimitError) Unwrap() error { return ErrWalkResourceLimit }

// WalkCanceledError reports how many callbacks completed before cancellation.
type WalkCanceledError struct {
	Visited int
	Err     error
}

func (err *WalkCanceledError) Error() string {
	if err == nil {
		return context.Canceled.Error()
	}
	return fmt.Sprintf("dicom: walk canceled after %d visits: %v", err.Visited, err.Err)
}

func (err *WalkCanceledError) Unwrap() error {
	if err == nil {
		return context.Canceled
	}
	return err.Err
}

type WalkOptions struct {
	MaxDepth    int
	MaxElements int
}

func DefaultWalkOptions() WalkOptions {
	return WalkOptions{MaxDepth: defaultWalkMaxDepth, MaxElements: defaultWalkMaxElements}
}

// WalkPath identifies a visited element during a recursive object traversal.
//
// The final step always identifies the visited element. Ancestor sequence item
// steps use the sequence tag with ItemIndex set to the zero-based item index.
// Sequence elements themselves are reported with ItemIndex set to WalkPathNoItem.
type WalkPath []WalkPathStep

// WalkPathStep identifies either a visited element or an ancestor sequence item.
type WalkPathStep struct {
	Tag       core.Tag
	ItemIndex int
}

// Clone returns an independent copy of p.
func (p WalkPath) Clone() WalkPath {
	if len(p) == 0 {
		return nil
	}
	clone := make(WalkPath, len(p))
	copy(clone, p)
	return clone
}

func (p WalkPath) Tags() []core.Tag {
	if len(p) == 0 {
		return nil
	}
	tags := make([]core.Tag, len(p))
	for i, step := range p {
		tags[i] = step.Tag
	}
	return tags
}

func (p WalkPath) String() string {
	if len(p) == 0 {
		return ""
	}
	parts := make([]string, len(p))
	for i, step := range p {
		if step.ItemIndex >= 0 {
			parts[i] = fmt.Sprintf("%s[%d]", step.Tag, step.ItemIndex)
			continue
		}
		parts[i] = step.Tag.String()
	}
	return strings.Join(parts, "/")
}

// Walk visits every element in the object in preorder, including sequence
// elements before their item children.
//
// The path contains ancestor sequence tags followed by the visited element tag.
// It intentionally omits sequence item indexes; use WalkPath when item indexes
// are needed. Returning a non-nil error stops traversal and returns that error.
func (o *Object) Walk(fn func(path []core.Tag, elem core.Element) error) error {
	return o.WalkContext(context.Background(), WalkOptions{}, fn)
}

// WalkContext is the cancellable, resource-bounded variant of Walk. Limits are
// checked before invoking the next callback or descending to the next level.
func (o *Object) WalkContext(ctx context.Context, opts WalkOptions, fn func(path []core.Tag, elem core.Element) error) error {
	normalized, err := normalizeWalkOptions(ctx, opts)
	if err != nil {
		return err
	}
	if o == nil || fn == nil {
		return nil
	}
	state := walkTagState{ctx: ctx, opts: normalized, fn: fn}
	return state.walk(o.Elements(), 0)
}

// WalkPath visits every element in the object in preorder with item-aware paths.
//
// The path for a sequence element uses the sequence tag with ItemIndex set to
// WalkPathNoItem. The path for an element inside a sequence item uses the
// ancestor sequence tag with ItemIndex set to the zero-based item index.
// Returning a non-nil error stops traversal and returns that error.
func (o *Object) WalkPath(fn func(path WalkPath, elem core.Element) error) error {
	return o.WalkPathContext(context.Background(), WalkOptions{}, fn)
}

// WalkPathContext is the cancellable, resource-bounded variant of WalkPath.
func (o *Object) WalkPathContext(ctx context.Context, opts WalkOptions, fn func(path WalkPath, elem core.Element) error) error {
	normalized, err := normalizeWalkOptions(ctx, opts)
	if err != nil {
		return err
	}
	if o == nil || fn == nil {
		return nil
	}
	state := walkPathState{ctx: ctx, opts: normalized, fn: fn}
	return state.walk(o.Elements(), 0)
}

func normalizeWalkOptions(ctx context.Context, opts WalkOptions) (WalkOptions, error) {
	if ctx == nil {
		return WalkOptions{}, fmt.Errorf("%w: nil context", ErrInvalidWalkOptions)
	}
	if opts.MaxDepth < 0 || opts.MaxElements < 0 {
		return WalkOptions{}, fmt.Errorf("%w: limits must not be negative", ErrInvalidWalkOptions)
	}
	defaults := DefaultWalkOptions()
	if opts.MaxDepth == 0 {
		opts.MaxDepth = defaults.MaxDepth
	}
	if opts.MaxElements == 0 {
		opts.MaxElements = defaults.MaxElements
	}
	if err := ctx.Err(); err != nil {
		return WalkOptions{}, &WalkCanceledError{Err: err}
	}
	return opts, nil
}

type walkTagState struct {
	ctx     context.Context
	opts    WalkOptions
	fn      func([]core.Tag, core.Element) error
	path    []core.Tag
	visited int
}

func (state *walkTagState) walk(elements []core.Element, depth int) error {
	for _, elem := range elements {
		if err := state.check(); err != nil {
			return err
		}
		tag := elem.Tag()
		state.path = append(state.path, tag)
		err := state.fn(append([]core.Tag(nil), state.path...), elem)
		state.path = state.path[:len(state.path)-1]
		if err != nil {
			return err
		}
		state.visited++

		seq, ok := elem.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		if len(seq.Items) > 0 && depth >= state.opts.MaxDepth {
			return &WalkLimitError{Limit: WalkLimitDepth, Maximum: state.opts.MaxDepth, Visited: state.visited}
		}
		for _, item := range seq.Items {
			state.path = append(state.path, tag)
			err := state.walk(item.Elements, depth+1)
			state.path = state.path[:len(state.path)-1]
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *walkTagState) check() error {
	if err := state.ctx.Err(); err != nil {
		return &WalkCanceledError{Visited: state.visited, Err: err}
	}
	if state.visited >= state.opts.MaxElements {
		return &WalkLimitError{Limit: WalkLimitElements, Maximum: state.opts.MaxElements, Visited: state.visited}
	}
	return nil
}

type walkPathState struct {
	ctx     context.Context
	opts    WalkOptions
	fn      func(WalkPath, core.Element) error
	path    WalkPath
	visited int
}

func (state *walkPathState) walk(elements []core.Element, depth int) error {
	for _, elem := range elements {
		if err := state.check(); err != nil {
			return err
		}
		tag := elem.Tag()
		state.path = append(state.path, WalkPathStep{Tag: tag, ItemIndex: WalkPathNoItem})
		err := state.fn(state.path.Clone(), elem)
		state.path = state.path[:len(state.path)-1]
		if err != nil {
			return err
		}
		state.visited++

		seq, ok := elem.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		if len(seq.Items) > 0 && depth >= state.opts.MaxDepth {
			return &WalkLimitError{Limit: WalkLimitDepth, Maximum: state.opts.MaxDepth, Visited: state.visited}
		}
		for i, item := range seq.Items {
			state.path = append(state.path, WalkPathStep{Tag: tag, ItemIndex: i})
			err := state.walk(item.Elements, depth+1)
			state.path = state.path[:len(state.path)-1]
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *walkPathState) check() error {
	if err := state.ctx.Err(); err != nil {
		return &WalkCanceledError{Visited: state.visited, Err: err}
	}
	if state.visited >= state.opts.MaxElements {
		return &WalkLimitError{Limit: WalkLimitElements, Maximum: state.opts.MaxElements, Visited: state.visited}
	}
	return nil
}
