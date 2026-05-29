package object

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

const WalkPathNoItem = -1

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
	if o == nil || fn == nil {
		return nil
	}
	var path []core.Tag
	return walkTagElements(o.Elements(), &path, fn)
}

// WalkPath visits every element in the object in preorder with item-aware paths.
//
// The path for a sequence element uses the sequence tag with ItemIndex set to
// WalkPathNoItem. The path for an element inside a sequence item uses the
// ancestor sequence tag with ItemIndex set to the zero-based item index.
// Returning a non-nil error stops traversal and returns that error.
func (o *Object) WalkPath(fn func(path WalkPath, elem core.Element) error) error {
	if o == nil || fn == nil {
		return nil
	}
	var path WalkPath
	return walkElements(o.Elements(), &path, fn)
}

func walkTagElements(elements []core.Element, path *[]core.Tag, fn func(path []core.Tag, elem core.Element) error) error {
	for _, elem := range elements {
		tag := elem.Tag()
		*path = append(*path, tag)
		err := fn(append([]core.Tag(nil), (*path)...), elem)
		*path = (*path)[:len(*path)-1]
		if err != nil {
			return err
		}

		seq, ok := elem.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		for _, item := range seq.Items {
			*path = append(*path, tag)
			err := walkTagElements(item.Elements, path, fn)
			*path = (*path)[:len(*path)-1]
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func walkElements(elements []core.Element, path *WalkPath, fn func(path WalkPath, elem core.Element) error) error {
	for _, elem := range elements {
		tag := elem.Tag()
		*path = append(*path, WalkPathStep{Tag: tag, ItemIndex: WalkPathNoItem})
		err := fn((*path).Clone(), elem)
		*path = (*path)[:len(*path)-1]
		if err != nil {
			return err
		}

		seq, ok := elem.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		for i, item := range seq.Items {
			*path = append(*path, WalkPathStep{Tag: tag, ItemIndex: i})
			err := walkElements(item.Elements, path, fn)
			*path = (*path)[:len(*path)-1]
			if err != nil {
				return err
			}
		}
	}
	return nil
}
