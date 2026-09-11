package dicomxml

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

type jsonElement struct {
	VR           string            `json:"vr"`
	Value        []json.RawMessage `json:"Value"`
	InlineBinary *string           `json:"InlineBinary"`
	BulkDataURI  *string           `json:"BulkDataURI"`
}

func Marshal(obj *object.Object, opts Options) ([]byte, error) {
	return MarshalContext(context.Background(), obj, opts)
}

func MarshalContext(ctx context.Context, obj *object.Object, opts Options) ([]byte, error) {
	var out bytes.Buffer
	if err := EncodeContext(ctx, &out, obj, opts); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func Encode(w io.Writer, obj *object.Object, opts Options) error {
	return EncodeContext(context.Background(), w, obj, opts)
}

func EncodeContext(ctx context.Context, w io.Writer, obj *object.Object, opts Options) error {
	if w == nil {
		return &Error{Op: "encode", Err: fmt.Errorf("%w: nil writer", ErrInvalidXML)}
	}
	b, err := newBudget(ctx, opts.Limits)
	if err != nil {
		return &Error{Op: "encode", Err: err}
	}
	if opts.Dictionary == nil {
		opts.Dictionary = std.Dictionary
	}
	jsonOptions := dicomjson.Options{
		Pretty:          false,
		OmitGroupLength: true,
		BulkDataURIFunc: opts.BulkDataURIFunc,
		ByteOrder:       opts.ByteOrder,
		Limits: dicomjson.Limits{
			MaxJSONBytes:         jsonLimit(b.limits.MaxXMLBytes),
			MaxInlineBinaryBytes: b.limits.MaxInlineBinaryBytes,
			MaxTotalBinaryBytes:  b.limits.MaxTotalBinaryBytes,
			MaxSequenceDepth:     b.limits.MaxSequenceDepth,
			MaxElements:          b.limits.MaxElements,
			MaxSequenceItems:     b.limits.MaxSequenceItems,
		},
	}
	encodedJSON, err := dicomjson.MarshalContext(ctx, obj, jsonOptions)
	if err != nil {
		return &Error{Op: "encode", Err: translateJSONLimit(err)}
	}
	var dataset map[string]jsonElement
	if err := json.Unmarshal(encodedJSON, &dataset); err != nil {
		return &Error{Op: "encode", Err: fmt.Errorf("bridge DICOM JSON: %w", err)}
	}

	lw := &limitedWriter{writer: w, remaining: b.limits.MaxXMLBytes}
	if _, err := io.WriteString(lw, xml.Header); err != nil {
		return &Error{Op: "encode", Err: err}
	}
	enc := xml.NewEncoder(lw)
	if opts.Pretty {
		enc.Indent("", "  ")
	}
	root := xml.StartElement{Name: xml.Name{Local: "NativeDicomModel"}, Attr: []xml.Attr{
		{Name: xml.Name{Local: "xmlns"}, Value: Namespace},
		{Name: xml.Name{Space: xmlNamespace, Local: "space"}, Value: "preserve"},
	}}
	if err := enc.EncodeToken(root); err != nil {
		return &Error{Op: "encode", Path: "/NativeDicomModel", Err: err}
	}
	if err := encodeJSONDataset(enc, dataset, opts, b, 0, "/NativeDicomModel"); err != nil {
		return err
	}
	if err := enc.EncodeToken(root.End()); err != nil {
		return &Error{Op: "encode", Path: "/NativeDicomModel", Err: err}
	}
	if err := enc.Flush(); err != nil {
		return &Error{Op: "encode", Err: err}
	}
	return nil
}

func translateJSONLimit(err error) error {
	switch {
	case errors.Is(err, dicomjson.ErrMaxJSONBytesExceeded):
		return fmt.Errorf("%w: DICOM JSON bridge: %v", ErrMaxXMLBytesExceeded, err)
	case errors.Is(err, dicomjson.ErrMaxInlineBinaryBytesExceeded):
		return fmt.Errorf("%w: %v", ErrMaxInlineBytesExceeded, err)
	case errors.Is(err, dicomjson.ErrMaxTotalBinaryBytesExceeded):
		return fmt.Errorf("%w: %v", ErrMaxBinaryBytesExceeded, err)
	case errors.Is(err, dicomjson.ErrMaxSequenceDepthExceeded):
		return fmt.Errorf("%w: %v", ErrMaxDepthExceeded, err)
	case errors.Is(err, dicomjson.ErrMaxElementsExceeded):
		return fmt.Errorf("%w: %v", ErrMaxElementsExceeded, err)
	case errors.Is(err, dicomjson.ErrMaxSequenceItemsExceeded):
		return fmt.Errorf("%w: %v", ErrMaxItemsExceeded, err)
	default:
		return err
	}
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("%w: limit reached", ErrMaxXMLBytesExceeded)
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func encodeJSONDataset(enc *xml.Encoder, dataset map[string]jsonElement, opts Options, b *budget, depth int, path string) error {
	if err := b.addElements(len(dataset), depth); err != nil {
		return &Error{Op: "encode", Path: path, Err: err}
	}
	tags := make([]core.Tag, 0, len(dataset))
	byTag := make(map[core.Tag]jsonElement, len(dataset))
	for text, elem := range dataset {
		tag, err := core.ParseTag(text)
		if err != nil {
			return &Error{Op: "encode", Path: path, Err: err}
		}
		if tag.IsGroupLength() {
			continue
		}
		tags = append(tags, tag)
		byTag[tag] = elem
	}
	sortTags(tags)
	for _, tag := range tags {
		if err := b.check(); err != nil {
			return &Error{Op: "encode", Path: path, Err: err}
		}
		if err := encodeJSONElement(enc, tag, byTag[tag], byTag, opts, b, depth, path); err != nil {
			return err
		}
	}
	return nil
}

func sortTags(tags []core.Tag) {
	sort.Slice(tags, func(i, j int) bool { return tags[i].Less(tags[j]) })
}

func encodeJSONElement(enc *xml.Encoder, tag core.Tag, elem jsonElement, dataset map[core.Tag]jsonElement, opts Options, b *budget, depth int, parentPath string) error {
	path := parentPath + "/DicomAttribute[@tag='" + tag.HexString() + "']"
	start := xml.StartElement{Name: xml.Name{Local: "DicomAttribute"}}
	xmlTag := tag.HexString()
	if tag.IsPrivate() && tag.Element >= 0x1000 {
		creatorTag := core.NewTag(tag.Group, tag.Element>>8)
		creator, ok := firstJSONText(dataset[creatorTag])
		if !ok || creator == "" {
			return &Error{Op: "encode", Path: path, Err: fmt.Errorf("%w: private data element has no private creator", ErrInvalidXML)}
		}
		xmlTag = fmt.Sprintf("%04X00%02X", tag.Group, tag.Element&0xff)
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "privateCreator"}, Value: creator})
	}
	start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "tag"}, Value: xmlTag})
	if elem.VR != "" {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "vr"}, Value: elem.VR})
	}
	if entry, ok := opts.Dictionary.ByTag(tag); ok && entry.Keyword != "" {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "keyword"}, Value: entry.Keyword})
	}
	if err := enc.EncodeToken(start); err != nil {
		return &Error{Op: "encode", Path: path, Err: err}
	}
	if elem.BulkDataURI != nil {
		attrs := []xml.Attr{{Name: xml.Name{Local: "uri"}, Value: *elem.BulkDataURI}}
		if strings.HasPrefix(strings.ToLower(*elem.BulkDataURI), "urn:uuid:") {
			attrs[0] = xml.Attr{Name: xml.Name{Local: "uuid"}, Value: (*elem.BulkDataURI)[9:]}
		}
		if err := emptyElement(enc, "BulkData", attrs); err != nil {
			return &Error{Op: "encode", Path: path, Err: err}
		}
	} else if elem.InlineBinary != nil {
		if err := textElement(enc, "InlineBinary", *elem.InlineBinary); err != nil {
			return &Error{Op: "encode", Path: path, Err: err}
		}
	} else {
		switch elem.VR {
		case core.VRSQ.String():
			for i, raw := range elem.Value {
				if err := b.addItem(depth + 1); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
				var item map[string]jsonElement
				if err := json.Unmarshal(raw, &item); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
				itemStart := xml.StartElement{Name: xml.Name{Local: "Item"}, Attr: []xml.Attr{{Name: xml.Name{Local: "number"}, Value: fmt.Sprint(i + 1)}}}
				if err := enc.EncodeToken(itemStart); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
				if err := encodeJSONDataset(enc, item, opts, b, depth+1, fmt.Sprintf("%s/Item[%d]", path, i+1)); err != nil {
					return err
				}
				if err := enc.EncodeToken(itemStart.End()); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
			}
		case core.VRPN.String():
			for i, raw := range elem.Value {
				pnStart := xml.StartElement{Name: xml.Name{Local: "PersonName"}, Attr: []xml.Attr{{Name: xml.Name{Local: "number"}, Value: fmt.Sprint(i + 1)}}}
				if err := enc.EncodeToken(pnStart); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
				if string(raw) != "null" {
					var pn dicomjson.PersonNameComponents
					if err := json.Unmarshal(raw, &pn); err != nil {
						return &Error{Op: "encode", Path: path, Err: err}
					}
					for _, group := range []struct{ name, value string }{{"Alphabetic", pn.Alphabetic}, {"Ideographic", pn.Ideographic}, {"Phonetic", pn.Phonetic}} {
						if group.value != "" {
							if err := encodePersonNameGroup(enc, group.name, group.value); err != nil {
								return &Error{Op: "encode", Path: path, Err: err}
							}
						}
					}
				}
				if err := enc.EncodeToken(pnStart.End()); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
			}
		default:
			for i, raw := range elem.Value {
				value := ""
				if string(raw) != "null" {
					if len(raw) > 0 && raw[0] == '"' {
						if err := json.Unmarshal(raw, &value); err != nil {
							return &Error{Op: "encode", Path: path, Err: err}
						}
					} else {
						value = string(raw)
					}
				}
				if err := b.checkValue(len(value)); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
				vStart := xml.StartElement{Name: xml.Name{Local: "Value"}, Attr: []xml.Attr{{Name: xml.Name{Local: "number"}, Value: fmt.Sprint(i + 1)}}}
				if err := enc.EncodeToken(vStart); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
				if value != "" {
					if err := enc.EncodeToken(xml.CharData(value)); err != nil {
						return &Error{Op: "encode", Path: path, Err: err}
					}
				}
				if err := enc.EncodeToken(vStart.End()); err != nil {
					return &Error{Op: "encode", Path: path, Err: err}
				}
			}
		}
	}
	if err := enc.EncodeToken(start.End()); err != nil {
		return &Error{Op: "encode", Path: path, Err: err}
	}
	return nil
}

func firstJSONText(elem jsonElement) (string, bool) {
	if len(elem.Value) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(elem.Value[0], &value); err != nil {
		return "", false
	}
	return value, true
}

func encodePersonNameGroup(enc *xml.Encoder, name, value string) error {
	start := xml.StartElement{Name: xml.Name{Local: name}}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	parts := strings.SplitN(value, "^", 5)
	names := []string{"FamilyName", "GivenName", "MiddleName", "NamePrefix", "NameSuffix"}
	for i, part := range parts {
		if part == "" {
			continue
		}
		if err := textElement(enc, names[i], part); err != nil {
			return err
		}
	}
	return enc.EncodeToken(start.End())
}

func textElement(enc *xml.Encoder, name, value string) error {
	start := xml.StartElement{Name: xml.Name{Local: name}}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	if value != "" {
		if err := enc.EncodeToken(xml.CharData(value)); err != nil {
			return err
		}
	}
	return enc.EncodeToken(start.End())
}

func emptyElement(enc *xml.Encoder, name string, attrs []xml.Attr) error {
	start := xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	return enc.EncodeToken(start.End())
}
