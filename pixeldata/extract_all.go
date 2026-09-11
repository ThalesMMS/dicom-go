package pixeldata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

const PixelDataPathNoItem = -1

const (
	defaultExtractAllMaxMatches         = 10_000
	defaultExtractAllMaxBytes     int64 = 2 << 30
	defaultExtractAllMaxDepth           = 64
	defaultExtractAllMaxFragments       = 100_000
)

var (
	// ErrExtractAllResourceLimit reports that a configured traversal limit was reached.
	ErrExtractAllResourceLimit = errors.New("dicom: ExtractAll resource limit exceeded")
	// ErrInvalidExtractAllOptions reports negative traversal limits.
	ErrInvalidExtractAllOptions = errors.New("dicom: invalid ExtractAll options")
)

type ExtractAllLimit string

const (
	ExtractAllLimitMatches   ExtractAllLimit = "matches"
	ExtractAllLimitBytes     ExtractAllLimit = "bytes"
	ExtractAllLimitDepth     ExtractAllLimit = "depth"
	ExtractAllLimitFragments ExtractAllLimit = "fragments"
)

// ExtractAllLimitError identifies the resource limit that stopped extraction.
type ExtractAllLimitError struct {
	Limit   ExtractAllLimit
	Maximum int64
}

func (err *ExtractAllLimitError) Error() string {
	if err == nil {
		return ErrExtractAllResourceLimit.Error()
	}
	return fmt.Sprintf("%s: %s maximum %d", ErrExtractAllResourceLimit, err.Limit, err.Maximum)
}

func (err *ExtractAllLimitError) Unwrap() error {
	return ErrExtractAllResourceLimit
}

// ExtractAllOptions bounds recursive Pixel Data extraction. Zero-valued fields
// select the conservative defaults returned by DefaultExtractAllOptions;
// negative fields are invalid.
type ExtractAllOptions struct {
	MaxMatches       int
	MaxBytes         int64
	MaxSequenceDepth int
	MaxFragments     int
}

// DefaultExtractAllOptions returns the limits used by ExtractAll.
func DefaultExtractAllOptions() ExtractAllOptions {
	return ExtractAllOptions{
		MaxMatches:       defaultExtractAllMaxMatches,
		MaxBytes:         defaultExtractAllMaxBytes,
		MaxSequenceDepth: defaultExtractAllMaxDepth,
		MaxFragments:     defaultExtractAllMaxFragments,
	}
}

type PixelDataContext string

const (
	PixelDataContextTopLevel          PixelDataContext = "top-level"
	PixelDataContextIconImageSequence PixelDataContext = "icon-image-sequence"
	PixelDataContextSequence          PixelDataContext = "sequence"
)

type PixelDataPath []PixelDataPathStep

type PixelDataPathStep struct {
	Tag       core.Tag
	ItemIndex int
}

// Clone returns an independent copy of p.
func (p PixelDataPath) Clone() PixelDataPath {
	if len(p) == 0 {
		return nil
	}
	clone := make(PixelDataPath, len(p))
	copy(clone, p)
	return clone
}

func (p PixelDataPath) String() string {
	if len(p) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(p) * 16)
	for i, step := range p {
		if i > 0 {
			builder.WriteByte('/')
		}
		builder.WriteString(step.Tag.String())
		if step.ItemIndex >= 0 {
			builder.WriteByte('[')
			var indexBuffer [20]byte
			builder.Write(strconv.AppendInt(indexBuffer[:0], int64(step.ItemIndex), 10))
			builder.WriteByte(']')
		}
	}
	return builder.String()
}

type ExtractedPixelData struct {
	Context PixelDataContext
	Path    PixelDataPath
	Data    PixelData
}

var tagIconImageSequence = core.NewTag(0x0088, 0x0200)

func ExtractAll(obj *object.Object) ([]ExtractedPixelData, error) {
	return ExtractAllWithOptions(context.Background(), obj, ExtractAllOptions{})
}

// ExtractAllWithOptions returns independent copies of every Pixel Data value in
// source order. It stops before cloning or appending a match that would exceed
// opts, and returns context cancellation errors through errors.Is.
func ExtractAllWithOptions(ctx context.Context, obj *object.Object, opts ExtractAllOptions) ([]ExtractedPixelData, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidExtractAllOptions)
	}
	normalized, err := normalizeExtractAllOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dicom: extract all Pixel Data: %w", err)
	}
	if obj == nil {
		return nil, nil
	}
	state := extractAllState{ctx: ctx, opts: normalized}
	if err := state.extractElements(obj.Elements(), 0); err != nil {
		return nil, err
	}
	return state.out, nil
}

func normalizeExtractAllOptions(opts ExtractAllOptions) (ExtractAllOptions, error) {
	if opts.MaxMatches < 0 || opts.MaxBytes < 0 || opts.MaxSequenceDepth < 0 || opts.MaxFragments < 0 {
		return ExtractAllOptions{}, fmt.Errorf("%w: limits must not be negative", ErrInvalidExtractAllOptions)
	}
	defaults := DefaultExtractAllOptions()
	if opts.MaxMatches == 0 {
		opts.MaxMatches = defaults.MaxMatches
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = defaults.MaxBytes
	}
	if opts.MaxSequenceDepth == 0 {
		opts.MaxSequenceDepth = defaults.MaxSequenceDepth
	}
	if opts.MaxFragments == 0 {
		opts.MaxFragments = defaults.MaxFragments
	}
	return opts, nil
}

type extractAllState struct {
	ctx       context.Context
	opts      ExtractAllOptions
	path      PixelDataPath
	out       []ExtractedPixelData
	matches   int
	bytes     int64
	fragments int
}

func (state *extractAllState) extractElements(elements []core.Element, depth int) error {
	for _, elem := range elements {
		if err := state.contextError(); err != nil {
			return err
		}
		tag := elem.Tag()
		if tag == core.TagPixelData {
			if state.matches >= state.opts.MaxMatches {
				return &ExtractAllLimitError{Limit: ExtractAllLimitMatches, Maximum: int64(state.opts.MaxMatches)}
			}
			payloadBytes, fragmentCount, err := state.pixelDataBytes(elem)
			if err != nil {
				return err
			}
			if payloadBytes > state.opts.MaxBytes-state.bytes {
				return &ExtractAllLimitError{Limit: ExtractAllLimitBytes, Maximum: state.opts.MaxBytes}
			}
			state.path = append(state.path, PixelDataPathStep{Tag: tag, ItemIndex: PixelDataPathNoItem})
			pixelPath := state.path
			pixel, err := state.clonePixelData(elem)
			if err != nil {
				pathText := pixelPath.String()
				state.path = state.path[:len(state.path)-1]
				return fmt.Errorf("dicom: extract Pixel Data at %s: %w", pathText, err)
			}
			pathCopy := pixelPath.Clone()
			state.out = append(state.out, ExtractedPixelData{
				Context: classifyPixelDataPath(pathCopy),
				Path:    pathCopy,
				Data:    pixel,
			})
			state.matches++
			state.bytes += payloadBytes
			state.fragments += fragmentCount
			state.path = state.path[:len(state.path)-1]
			continue
		}

		seq, ok := elem.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		if len(seq.Items) > 0 && depth >= state.opts.MaxSequenceDepth {
			return &ExtractAllLimitError{Limit: ExtractAllLimitDepth, Maximum: int64(state.opts.MaxSequenceDepth)}
		}
		for i, item := range seq.Items {
			if err := state.contextError(); err != nil {
				return err
			}
			state.path = append(state.path, PixelDataPathStep{Tag: tag, ItemIndex: i})
			err := state.extractElements(item.Elements, depth+1)
			state.path = state.path[:len(state.path)-1]
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *extractAllState) contextError() error {
	if err := state.ctx.Err(); err != nil {
		return fmt.Errorf("dicom: extract all Pixel Data: %w", err)
	}
	return nil
}

func (state *extractAllState) pixelDataBytes(element core.Element) (int64, int, error) {
	if raw, ok := element.RawBytes(); ok {
		return int64(len(raw)), 0, nil
	}
	fragments, ok := element.Value.(core.FragmentSequence)
	if !ok {
		state.path = append(state.path, PixelDataPathStep{Tag: element.Tag(), ItemIndex: PixelDataPathNoItem})
		pathText := state.path.String()
		state.path = state.path[:len(state.path)-1]
		return 0, 0, fmt.Errorf("dicom: extract Pixel Data at %s: dicom: pixel data is not a supported value type", pathText)
	}
	if len(fragments.Fragments) > state.opts.MaxFragments-state.fragments {
		return 0, 0, &ExtractAllLimitError{Limit: ExtractAllLimitFragments, Maximum: int64(state.opts.MaxFragments)}
	}
	total := int64(len(fragments.OffsetTable))
	const maxInt64 = int64(^uint64(0) >> 1)
	for index, fragment := range fragments.Fragments {
		if index&255 == 0 {
			if err := state.contextError(); err != nil {
				return 0, 0, err
			}
		}
		fragmentBytes := int64(len(fragment))
		if fragmentBytes > maxInt64-total {
			return 0, 0, &ExtractAllLimitError{Limit: ExtractAllLimitBytes, Maximum: state.opts.MaxBytes}
		}
		total += fragmentBytes
	}
	return total, len(fragments.Fragments), nil
}

func (state *extractAllState) clonePixelData(element core.Element) (PixelData, error) {
	if raw, ok := element.RawBytes(); ok {
		clone, err := state.cloneBytes(raw)
		return PixelData{Raw: clone}, err
	}
	fragments, ok := element.Value.(core.FragmentSequence)
	if !ok {
		return PixelData{}, errors.New("dicom: pixel data is not a supported value type")
	}
	offsetTable, err := state.cloneBytes(fragments.OffsetTable)
	if err != nil {
		return PixelData{}, err
	}
	cloned := core.FragmentSequence{
		OffsetTable: offsetTable,
		Fragments:   make([][]byte, len(fragments.Fragments)),
	}
	for index := range fragments.Fragments {
		cloned.Fragments[index], err = state.cloneBytes(fragments.Fragments[index])
		if err != nil {
			return PixelData{}, err
		}
	}
	return PixelData{Sequence: cloned, Encapsulated: true}, nil
}

func (state *extractAllState) cloneBytes(source []byte) ([]byte, error) {
	if len(source) == 0 {
		if err := state.contextError(); err != nil {
			return nil, err
		}
		if source == nil {
			return nil, nil
		}
		return make([]byte, 0), nil
	}
	clone := make([]byte, len(source))
	const copyChunkBytes = 1 << 20
	for offset := 0; offset < len(source); offset += copyChunkBytes {
		if err := state.contextError(); err != nil {
			return nil, err
		}
		end := offset + copyChunkBytes
		if end > len(source) {
			end = len(source)
		}
		copy(clone[offset:end], source[offset:end])
	}
	return clone, nil
}

func classifyPixelDataPath(path PixelDataPath) PixelDataContext {
	if len(path) <= 1 {
		return PixelDataContextTopLevel
	}
	for _, step := range path[:len(path)-1] {
		if step.Tag == tagIconImageSequence {
			return PixelDataContextIconImageSequence
		}
	}
	return PixelDataContextSequence
}
