package pixeldata

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

const PixelDataPathNoItem = -1

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
	if obj == nil {
		return nil, nil
	}
	var out []ExtractedPixelData
	var path PixelDataPath
	if err := extractAllElements(obj.Elements(), &path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func extractAllElements(elements []core.Element, path *PixelDataPath, out *[]ExtractedPixelData) error {
	for _, elem := range elements {
		tag := elem.Tag()
		if tag == core.TagPixelData {
			*path = append(*path, PixelDataPathStep{Tag: tag, ItemIndex: PixelDataPathNoItem})
			pixelPath := *path
			pixel, err := pixelDataFromElement(elem)
			if err != nil {
				pathText := pixelPath.String()
				*path = (*path)[:len(*path)-1]
				return fmt.Errorf("dicom: extract Pixel Data at %s: %w", pathText, err)
			}
			pathCopy := pixelPath.Clone()
			*out = append(*out, ExtractedPixelData{
				Context: classifyPixelDataPath(pathCopy),
				Path:    pathCopy,
				Data:    pixel,
			})
			*path = (*path)[:len(*path)-1]
			continue
		}

		seq, ok := elem.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		for i, item := range seq.Items {
			*path = append(*path, PixelDataPathStep{Tag: tag, ItemIndex: i})
			err := extractAllElements(item.Elements, path, out)
			*path = (*path)[:len(*path)-1]
			if err != nil {
				return err
			}
		}
	}
	return nil
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
