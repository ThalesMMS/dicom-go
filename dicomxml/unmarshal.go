package dicomxml

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagPattern  = regexp.MustCompile(`^[0-9A-F]{8}$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type nativeAttribute struct {
	tag, vr, keyword, privateCreator string
	values                           []string
	persons                          []nativePersonName
	items                            [][]nativeAttribute
	inline                           *string
	bulk                             *BulkDataReference
}

type nativePersonName struct {
	groups [3]*[5]string
}

func Unmarshal(data []byte, dict dictionary.DataDictionary) (*object.Object, error) {
	return UnmarshalWithOptions(data, dict, DefaultUnmarshalOptions())
}

func UnmarshalWithOptions(data []byte, dict dictionary.DataDictionary, opts UnmarshalOptions) (*object.Object, error) {
	return UnmarshalContext(context.Background(), data, dict, opts)
}

func UnmarshalContext(ctx context.Context, data []byte, dict dictionary.DataDictionary, opts UnmarshalOptions) (*object.Object, error) {
	return DecodeContext(ctx, bytes.NewReader(data), dict, opts)
}

func Decode(r io.Reader, dict dictionary.DataDictionary, opts UnmarshalOptions) (*object.Object, error) {
	return DecodeContext(context.Background(), r, dict, opts)
}

func DecodeContext(ctx context.Context, r io.Reader, dict dictionary.DataDictionary, opts UnmarshalOptions) (*object.Object, error) {
	if r == nil {
		return nil, &Error{Op: "decode", Err: fmt.Errorf("%w: nil reader", ErrInvalidXML)}
	}
	b, err := newBudget(ctx, opts.Limits)
	if err != nil {
		return nil, &Error{Op: "decode", Err: err}
	}
	if dict == nil {
		dict = std.Dictionary
	}
	if opts.ByteOrder == nil && opts.TransferSyntax.ByteOrder != nil {
		opts.ByteOrder = opts.TransferSyntax.ByteOrder
	}
	if opts.ByteOrder == nil {
		opts.ByteOrder = binary.LittleEndian
	}
	if opts.BulkDataPolicy > ResolveBulkData {
		return nil, &Error{Op: "decode", Err: fmt.Errorf("%w: unknown BulkData policy %d", ErrInvalidXML, opts.BulkDataPolicy)}
	}
	if opts.BulkDataPolicy == ResolveBulkData && opts.BulkDataResolver == nil {
		return nil, &Error{Op: "decode", Err: fmt.Errorf("%w: ResolveBulkData requires a resolver", ErrInvalidXML)}
	}

	limited := &maxBytesReader{reader: &contextReader{ctx: b.ctx, reader: r}, remaining: b.limits.MaxXMLBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, &Error{Op: "decode", Err: err}
	}
	if int64(len(data)) > b.limits.MaxXMLBytes {
		return nil, &Error{Op: "decode", Err: fmt.Errorf("%w: got more than %d", ErrMaxXMLBytesExceeded, b.limits.MaxXMLBytes)}
	}
	attrs, err := parseDocument(data, b, dict, opts.AllowMissingKeyword)
	if err != nil {
		return nil, err
	}
	dataset, err := nativeDatasetToJSON(b.ctx, attrs, dict, opts, b, "/NativeDicomModel")
	if err != nil {
		return nil, err
	}
	encodedJSON, err := json.Marshal(dataset)
	if err != nil {
		return nil, &Error{Op: "decode", Err: fmt.Errorf("bridge DICOM JSON: %w", err)}
	}
	obj, err := dicomjson.UnmarshalContext(b.ctx, encodedJSON, dict, dicomjson.UnmarshalOptions{
		TextOptions:    opts.TextOptions,
		ByteOrder:      opts.ByteOrder,
		TransferSyntax: opts.TransferSyntax,
		Limits: dicomjson.Limits{
			MaxJSONBytes:         jsonLimit(b.limits.MaxXMLBytes),
			MaxInlineBinaryBytes: b.limits.MaxTotalBinaryBytes,
			MaxTotalBinaryBytes:  b.limits.MaxTotalBinaryBytes,
			MaxSequenceDepth:     b.limits.MaxSequenceDepth,
			MaxElements:          b.limits.MaxElements,
			MaxSequenceItems:     b.limits.MaxSequenceItems,
		},
	})
	if err != nil {
		translated := translateJSONLimit(err)
		if translated == err && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			translated = fmt.Errorf("%w: %v", ErrInvalidXML, err)
		}
		return nil, &Error{Op: "decode", Err: translated}
	}
	if opts.BulkDataPolicy == ResolveBulkData {
		if err := resolveObjectBulkData(b.ctx, obj, dict, opts, b); err != nil {
			return nil, err
		}
	}
	return obj, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		err = r.ctx.Err()
	}
	return n, err
}

type maxBytesReader struct {
	reader    io.Reader
	remaining int64
}

func (r *maxBytesReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func parseDocument(data []byte, b *budget, dict dictionary.DataDictionary, allowMissingKeyword bool) ([]nativeAttribute, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	for {
		token, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, &Error{Op: "decode", Err: fmt.Errorf("%w: missing root element", ErrInvalidXML)}
			}
			return nil, &Error{Op: "decode", Err: fmt.Errorf("%w: %v", ErrInvalidXML, err)}
		}
		switch token := token.(type) {
		case xml.ProcInst:
			if !strings.EqualFold(token.Target, "xml") {
				return nil, invalidXML("/", "processing instructions are not allowed")
			}
		case xml.Directive:
			return nil, invalidXML("/", "DTD and directives are not allowed")
		case xml.Comment:
			continue
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return nil, invalidXML("/", "text is not allowed before the root element")
			}
		case xml.StartElement:
			if token.Name.Space != Namespace || token.Name.Local != "NativeDicomModel" {
				return nil, invalidXML("/", "root must be NativeDicomModel in namespace %q", Namespace)
			}
			if err := validateRootAttributes(token.Attr); err != nil {
				return nil, err
			}
			attrs, err := parseDataSet(dec, token, b, dict, allowMissingKeyword, 0, "/NativeDicomModel")
			if err != nil {
				return nil, err
			}
			if err := ensureDocumentEOF(dec); err != nil {
				return nil, err
			}
			return attrs, nil
		}
	}
}

func validateRootAttributes(attrs []xml.Attr) error {
	space := false
	defaultNamespace := false
	for _, attr := range attrs {
		// encoding/xml may expose an explicit default namespace declaration as
		// an attribute in addition to resolving StartElement.Name.Space.
		if attr.Name.Space == "" && attr.Name.Local == "xmlns" && attr.Value == Namespace {
			if defaultNamespace {
				return invalidXML("/NativeDicomModel", "duplicate default namespace declaration")
			}
			defaultNamespace = true
			continue
		}
		if attr.Name.Space == xmlNamespace && attr.Name.Local == "space" {
			if space || attr.Value != "preserve" {
				return invalidXML("/NativeDicomModel", `xml:space must occur once with value "preserve"`)
			}
			space = true
			continue
		}
		return invalidXML("/NativeDicomModel", "unexpected root attribute %s", attr.Name.Local)
	}
	if !space {
		return invalidXML("/NativeDicomModel", `missing xml:space="preserve"`)
	}
	return nil
}

func ensureDocumentEOF(dec *xml.Decoder) error {
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return invalidXML("/", "%v", err)
		}
		switch token := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return invalidXML("/", "trailing text is not allowed")
			}
		case xml.Comment:
			continue
		default:
			return invalidXML("/", "trailing XML content is not allowed")
		}
	}
}

func parseDataSet(dec *xml.Decoder, parent xml.StartElement, b *budget, dict dictionary.DataDictionary, allowMissingKeyword bool, depth int, path string) ([]nativeAttribute, error) {
	var attrs []nativeAttribute
	for {
		if err := b.check(); err != nil {
			return nil, &Error{Op: "decode", Path: path, Err: err}
		}
		token, err := dec.Token()
		if err != nil {
			return nil, invalidXML(path, "%v", err)
		}
		switch token := token.(type) {
		case xml.Directive:
			return nil, invalidXML(path, "DTD and directives are not allowed")
		case xml.ProcInst:
			return nil, invalidXML(path, "processing instructions are not allowed")
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return nil, invalidXML(path, "text is not allowed between attributes")
			}
		case xml.Comment:
			continue
		case xml.StartElement:
			if token.Name.Space != Namespace || token.Name.Local != "DicomAttribute" {
				return nil, invalidXML(path, "unexpected element %s", token.Name.Local)
			}
			attr, err := parseAttribute(dec, token, b, dict, allowMissingKeyword, depth, path)
			if err != nil {
				return nil, err
			}
			attrs = append(attrs, attr)
			if err := b.addElements(1, depth); err != nil {
				return nil, &Error{Op: "decode", Path: path, Err: err}
			}
		case xml.EndElement:
			if token.Name != parent.Name {
				return nil, invalidXML(path, "unexpected closing element %s", token.Name.Local)
			}
			return attrs, nil
		}
	}
}

func parseAttribute(dec *xml.Decoder, start xml.StartElement, b *budget, dict dictionary.DataDictionary, allowMissingKeyword bool, depth int, parentPath string) (nativeAttribute, error) {
	var out nativeAttribute
	seen := map[string]bool{}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			return out, invalidXML(parentPath, "namespaced DicomAttribute attribute %s is not allowed", attr.Name.Local)
		}
		if seen[attr.Name.Local] {
			return out, invalidXML(parentPath, "duplicate attribute %s", attr.Name.Local)
		}
		seen[attr.Name.Local] = true
		switch attr.Name.Local {
		case "tag":
			out.tag = attr.Value
		case "vr":
			out.vr = attr.Value
		case "keyword":
			out.keyword = attr.Value
		case "privateCreator":
			out.privateCreator = attr.Value
		default:
			return out, invalidXML(parentPath, "unexpected DicomAttribute attribute %s", attr.Name.Local)
		}
	}
	if !tagPattern.MatchString(out.tag) {
		return out, invalidXML(parentPath, "tag %q must be eight uppercase hexadecimal digits", out.tag)
	}
	tag, _ := core.ParseTag(out.tag)
	if tag.IsGroupLength() {
		return out, invalidXML(parentPath, "group length attributes are prohibited")
	}
	path := parentPath + "/DicomAttribute[@tag='" + out.tag + "']"
	if out.vr == "" {
		entry, ok := dict.ByTag(tag)
		if !ok || entry.VR == "" {
			return out, invalidXML(path, "VR is required when the tag is unknown")
		}
		out.vr = entry.VR.String()
	}
	vr, err := core.ParseVR(out.vr)
	if err != nil {
		return out, invalidXML(path, "%v", err)
	}
	if out.keyword != "" {
		if entry, ok := dict.ByTag(tag); ok && entry.Keyword != "" && out.keyword != entry.Keyword {
			return out, invalidXML(path, "keyword %q does not match %q", out.keyword, entry.Keyword)
		}
	} else if entry, ok := dict.ByTag(tag); ok && entry.Keyword != "" && !allowMissingKeyword {
		return out, invalidXML(path, "keyword %q is required for known tag %s", entry.Keyword, out.tag)
	}
	if out.privateCreator != "" {
		if !tag.IsPrivate() || tag.Element > 0x00ff {
			return out, invalidXML(path, "privateCreator requires a normalized private tag gggg00ee")
		}
	} else if tag.IsPrivate() && tag.Element >= 0x1000 {
		return out, invalidXML(path, "private data tag must be normalized and include privateCreator")
	}

	kind := ""
	expected := 1
	for {
		token, err := dec.Token()
		if err != nil {
			return out, invalidXML(path, "%v", err)
		}
		switch token := token.(type) {
		case xml.Directive:
			return out, invalidXML(path, "DTD and directives are not allowed")
		case xml.ProcInst:
			return out, invalidXML(path, "processing instructions are not allowed")
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return out, invalidXML(path, "text is not allowed outside a value")
			}
		case xml.Comment:
			continue
		case xml.StartElement:
			if token.Name.Space != Namespace {
				return out, invalidXML(path, "foreign namespace on %s", token.Name.Local)
			}
			childKind := token.Name.Local
			if kind != "" && kind != childKind {
				return out, invalidXML(path, "mixed value representations %s and %s", kind, childKind)
			}
			kind = childKind
			switch childKind {
			case "Value":
				if vr == core.VRSQ || vr == core.VRPN {
					return out, invalidXML(path, "Value is prohibited for VR %s", vr)
				}
				if err := validateNumber(token.Attr, expected, path+"/Value"); err != nil {
					return out, err
				}
				value, err := parseTextElement(dec, token, b, path+"/Value")
				if err != nil {
					return out, err
				}
				if vr == core.VRAT && !tagPattern.MatchString(value) {
					return out, invalidXML(path+"/Value", "AT value %q must be eight uppercase hexadecimal digits", value)
				}
				out.values = append(out.values, value)
				expected++
			case "Item":
				if vr != core.VRSQ {
					return out, invalidXML(path, "Item requires VR SQ")
				}
				if err := validateNumber(token.Attr, expected, path+"/Item"); err != nil {
					return out, err
				}
				if err := b.addItem(depth + 1); err != nil {
					return out, &Error{Op: "decode", Path: path, Err: err}
				}
				item, err := parseDataSet(dec, token, b, dict, allowMissingKeyword, depth+1, fmt.Sprintf("%s/Item[%d]", path, expected))
				if err != nil {
					return out, err
				}
				out.items = append(out.items, item)
				expected++
			case "PersonName":
				if vr != core.VRPN {
					return out, invalidXML(path, "PersonName requires VR PN")
				}
				if err := validateNumber(token.Attr, expected, path+"/PersonName"); err != nil {
					return out, err
				}
				pn, err := parsePersonName(dec, token, b, path+"/PersonName")
				if err != nil {
					return out, err
				}
				out.persons = append(out.persons, pn)
				expected++
			case "InlineBinary":
				if expected != 1 || !inlineVR(vr) {
					return out, invalidXML(path, "InlineBinary is not valid for VR %s or multiplicity", vr)
				}
				if len(token.Attr) != 0 {
					return out, invalidXML(path, "InlineBinary has unexpected attributes")
				}
				encoded, err := parseTextElementLimit(dec, token, b, path+"/InlineBinary", b.limits.MaxXMLBytes, ErrMaxXMLBytesExceeded)
				if err != nil {
					return out, err
				}
				encoded = stripXMLWhitespace(encoded)
				decodedLength, err := exactBase64DecodedLength(encoded)
				if err != nil {
					return out, invalidXML(path+"/InlineBinary", "invalid base64: %v", err)
				}
				if err := b.addBinary(decodedLength, true); err != nil {
					return out, &Error{Op: "decode", Path: path + "/InlineBinary", Err: err}
				}
				if decodedLength == 0 {
					return out, invalidXML(path+"/InlineBinary", "InlineBinary must not represent a zero-length Value Field")
				}
				decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
				if err != nil {
					return out, invalidXML(path+"/InlineBinary", "invalid base64: %v", err)
				}
				if int64(len(decoded)) != decodedLength {
					return out, invalidXML(path+"/InlineBinary", "decoded length mismatch")
				}
				out.inline = &encoded
				expected++
			case "BulkData":
				if expected != 1 || vr == core.VRSQ {
					return out, invalidXML(path, "BulkData is not valid for VR %s or multiplicity", vr)
				}
				ref, err := parseBulkData(dec, token, tag, vr, path+"/BulkData")
				if err != nil {
					return out, err
				}
				out.bulk = &ref
				expected++
			default:
				return out, invalidXML(path, "unexpected value element %s", childKind)
			}
		case xml.EndElement:
			if token.Name != start.Name {
				return out, invalidXML(path, "unexpected closing element %s", token.Name.Local)
			}
			return out, nil
		}
	}
}

func validateNumber(attrs []xml.Attr, expected int, path string) error {
	if len(attrs) != 1 || attrs[0].Name.Space != "" || attrs[0].Name.Local != "number" {
		return invalidXML(path, "requires exactly one number attribute")
	}
	n, err := strconv.Atoi(attrs[0].Value)
	if err != nil || n != expected {
		return invalidXML(path, "number must be monotonically increasing; got %q, want %d", attrs[0].Value, expected)
	}
	return nil
}

func parseTextElement(dec *xml.Decoder, start xml.StartElement, b *budget, path string) (string, error) {
	return parseTextElementLimit(dec, start, b, path, int64(b.limits.MaxValueBytes), ErrMaxValueBytesExceeded)
}

func parseTextElementLimit(dec *xml.Decoder, start xml.StartElement, b *budget, path string, limit int64, limitErr error) (string, error) {
	var text strings.Builder
	for {
		token, err := dec.Token()
		if err != nil {
			return "", invalidXML(path, "%v", err)
		}
		switch token := token.(type) {
		case xml.CharData:
			if int64(text.Len()) > limit-int64(len(token)) {
				return "", &Error{Op: "decode", Path: path, Err: limitErr}
			}
			text.Write(token)
		case xml.Comment:
			continue
		case xml.EndElement:
			if token.Name != start.Name {
				return "", invalidXML(path, "unexpected closing element")
			}
			return text.String(), nil
		default:
			return "", invalidXML(path, "markup is not allowed inside text values")
		}
	}
}

func exactBase64DecodedLength(encoded string) (int64, error) {
	if len(encoded)%4 != 0 {
		return 0, fmt.Errorf("length is not a multiple of four")
	}
	padding := 0
	if len(encoded) > 0 && encoded[len(encoded)-1] == '=' {
		padding++
	}
	if len(encoded) > 1 && encoded[len(encoded)-2] == '=' {
		padding++
	}
	if strings.Contains(encoded[:len(encoded)-padding], "=") {
		return 0, fmt.Errorf("padding occurs before the end")
	}
	return int64(base64.StdEncoding.DecodedLen(len(encoded)) - padding), nil
}

func parsePersonName(dec *xml.Decoder, start xml.StartElement, b *budget, path string) (nativePersonName, error) {
	var pn nativePersonName
	groupIndex := map[string]int{"Alphabetic": 0, "Ideographic": 1, "Phonetic": 2}
	last := -1
	for {
		token, err := dec.Token()
		if err != nil {
			return pn, invalidXML(path, "%v", err)
		}
		switch token := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return pn, invalidXML(path, "text outside name components is not allowed")
			}
		case xml.Comment:
			continue
		case xml.StartElement:
			idx, ok := groupIndex[token.Name.Local]
			if token.Name.Space != Namespace || !ok || idx <= last || len(token.Attr) != 0 {
				return pn, invalidXML(path, "invalid or out-of-order person name group %s", token.Name.Local)
			}
			components, err := parseNameGroup(dec, token, b, path+"/"+token.Name.Local)
			if err != nil {
				return pn, err
			}
			pn.groups[idx] = &components
			last = idx
		case xml.EndElement:
			if token.Name != start.Name {
				return pn, invalidXML(path, "unexpected closing element")
			}
			return pn, nil
		default:
			return pn, invalidXML(path, "unsupported XML token")
		}
	}
}

func parseNameGroup(dec *xml.Decoder, start xml.StartElement, b *budget, path string) ([5]string, error) {
	var values [5]string
	indices := map[string]int{"FamilyName": 0, "GivenName": 1, "MiddleName": 2, "NamePrefix": 3, "NameSuffix": 4}
	last := -1
	for {
		token, err := dec.Token()
		if err != nil {
			return values, invalidXML(path, "%v", err)
		}
		switch token := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return values, invalidXML(path, "text outside name components is not allowed")
			}
		case xml.Comment:
			continue
		case xml.StartElement:
			idx, ok := indices[token.Name.Local]
			if token.Name.Space != Namespace || !ok || idx <= last || len(token.Attr) != 0 {
				return values, invalidXML(path, "invalid or out-of-order name component %s", token.Name.Local)
			}
			value, err := parseTextElement(dec, token, b, path+"/"+token.Name.Local)
			if err != nil {
				return values, err
			}
			values[idx] = value
			last = idx
		case xml.EndElement:
			if token.Name != start.Name {
				return values, invalidXML(path, "unexpected closing element")
			}
			return values, nil
		default:
			return values, invalidXML(path, "unsupported XML token")
		}
	}
}

func parseBulkData(dec *xml.Decoder, start xml.StartElement, tag core.Tag, vr core.VR, path string) (BulkDataReference, error) {
	ref := BulkDataReference{Tag: tag, VR: vr}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			return ref, invalidXML(path, "namespaced BulkData attribute is not allowed")
		}
		switch attr.Name.Local {
		case "uri":
			if ref.URI != "" {
				return ref, invalidXML(path, "duplicate uri")
			}
			ref.URI = attr.Value
		case "uuid":
			if ref.UUID != "" {
				return ref, invalidXML(path, "duplicate uuid")
			}
			ref.UUID = attr.Value
		default:
			return ref, invalidXML(path, "unexpected BulkData attribute %s", attr.Name.Local)
		}
	}
	if (ref.URI == "") == (ref.UUID == "") {
		return ref, invalidXML(path, "exactly one of uri or uuid is required")
	}
	if ref.UUID != "" && !uuidPattern.MatchString(ref.UUID) {
		return ref, invalidXML(path, "invalid UUID")
	}
	if ref.URI != "" {
		if strings.IndexFunc(ref.URI, unicode.IsControl) >= 0 {
			return ref, invalidXML(path, "URI contains control characters")
		}
		if _, err := url.Parse(ref.URI); err != nil {
			return ref, invalidXML(path, "invalid URI: %v", err)
		}
	}
	for {
		token, err := dec.Token()
		if err != nil {
			return ref, invalidXML(path, "%v", err)
		}
		switch token := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return ref, invalidXML(path, "BulkData must be empty")
			}
		case xml.Comment:
			continue
		case xml.EndElement:
			if token.Name != start.Name {
				return ref, invalidXML(path, "unexpected closing element")
			}
			return ref, nil
		default:
			return ref, invalidXML(path, "BulkData must be empty")
		}
	}
}

func nativeDatasetToJSON(ctx context.Context, attrs []nativeAttribute, dict dictionary.DataDictionary, opts UnmarshalOptions, b *budget, path string) (map[string]dicomjson.Element, error) {
	out := make(map[string]dicomjson.Element, len(attrs))
	alloc := newPrivateAllocator(attrs)
	for _, attr := range attrs {
		if err := ctx.Err(); err != nil {
			return nil, &Error{Op: "decode", Path: path, Err: err}
		}
		tag, _ := core.ParseTag(attr.tag)
		if attr.privateCreator != "" {
			var err error
			tag, err = alloc.allocate(tag, attr.privateCreator)
			if err != nil {
				return nil, &Error{Op: "decode", Path: path, Err: err}
			}
			creatorTag := core.NewTag(tag.Group, tag.Element>>8)
			key := creatorTag.HexString()
			if _, exists := out[key]; !exists {
				out[key] = dicomjson.Element{VR: core.VRLO.String(), Value: []any{attr.privateCreator}}
			}
		}
		key := tag.HexString()
		if existing, exists := out[key]; exists {
			if isMatchingPrivateCreator(tag, attr, existing) {
				continue
			}
			return nil, invalidXML(path, "duplicate resolved tag %s", key)
		}
		entry := dicomjson.Element{VR: attr.vr}
		switch {
		case attr.bulk != nil:
			ref := *attr.bulk
			ref.Tag = tag
			switch opts.BulkDataPolicy {
			case PreserveBulkData, ResolveBulkData:
				if ref.URI != "" {
					entry.BulkDataURI = ref.URI
				} else {
					entry.BulkDataURI = "urn:uuid:" + ref.UUID
				}
			case RejectBulkData:
				return nil, &Error{Op: "decode", Path: path + "/DicomAttribute[@tag='" + attr.tag + "']/BulkData", Err: ErrBulkDataRejected}
			}
		case attr.inline != nil:
			if tag == core.TagPixelData && !opts.TransferSyntax.Encapsulated && looksEncapsulatedValueField(*attr.inline) {
				return nil, &Error{Op: "decode", Path: path + "/DicomAttribute[@tag='" + attr.tag + "']/InlineBinary", Err: ErrTransferSyntaxRequired}
			}
			entry.InlineBinary = *attr.inline
		case attr.vr == core.VRSQ.String():
			entry.Value = make([]any, 0, len(attr.items))
			for i, item := range attr.items {
				nested, err := nativeDatasetToJSON(ctx, item, dict, opts, b, fmt.Sprintf("%s/DicomAttribute[@tag='%s']/Item[%d]", path, attr.tag, i+1))
				if err != nil {
					return nil, err
				}
				entry.Value = append(entry.Value, nested)
			}
		case attr.vr == core.VRPN.String():
			entry.Value = make([]any, 0, len(attr.persons))
			for _, pn := range attr.persons {
				value := dicomjson.PersonNameComponents{}
				if pn.groups[0] != nil {
					value.Alphabetic = joinNameComponents(*pn.groups[0])
				}
				if pn.groups[1] != nil {
					value.Ideographic = joinNameComponents(*pn.groups[1])
				}
				if pn.groups[2] != nil {
					value.Phonetic = joinNameComponents(*pn.groups[2])
				}
				entry.Value = append(entry.Value, value)
			}
		default:
			entry.Value = make([]any, len(attr.values))
			for i, value := range attr.values {
				entry.Value[i] = value
			}
		}
		out[key] = entry
	}
	return out, nil
}

func isMatchingPrivateCreator(tag core.Tag, attr nativeAttribute, existing dicomjson.Element) bool {
	if !tag.IsPrivate() || tag.Element < 0x0010 || tag.Element > 0x00ff || attr.vr != core.VRLO.String() || len(attr.values) != 1 {
		return false
	}
	return existing.VR == core.VRLO.String() && len(existing.Value) == 1 && existing.Value[0] == attr.values[0]
}

func resolveObjectBulkData(ctx context.Context, obj *object.Object, dict dictionary.DataDictionary, opts UnmarshalOptions, b *budget) error {
	if obj == nil {
		return nil
	}
	for _, elem := range obj.SortedElements() {
		resolved, err := resolveElementBulkData(ctx, elem, dict, opts, b, "/NativeDicomModel/DicomAttribute[@tag='"+elem.Tag().HexString()+"']")
		if err != nil {
			return err
		}
		obj.Put(resolved)
	}
	return nil
}

func resolveElementBulkData(ctx context.Context, elem core.Element, dict dictionary.DataDictionary, opts UnmarshalOptions, b *budget, path string) (core.Element, error) {
	if err := ctx.Err(); err != nil {
		return core.Element{}, &Error{Op: "decode", Path: path, Err: err}
	}
	switch value := elem.Value.(type) {
	case core.BulkDataValue:
		ref := BulkDataReference{Tag: elem.Tag(), VR: elem.VR(), URI: value.URI}
		if strings.HasPrefix(strings.ToLower(value.URI), "urn:uuid:") {
			ref.UUID = value.URI[9:]
			ref.URI = ""
		}
		reader, err := opts.BulkDataResolver(ctx, ref)
		if err != nil {
			return core.Element{}, &Error{Op: "decode", Path: path, Err: fmt.Errorf("%w: %v", ErrBulkDataResolver, err)}
		}
		if reader == nil {
			return core.Element{}, &Error{Op: "decode", Path: path, Err: fmt.Errorf("%w: resolver returned nil reader", ErrBulkDataResolver)}
		}
		data, readErr := readResolvedBulk(reader, b)
		closeErr := reader.Close()
		if readErr != nil {
			return core.Element{}, &Error{Op: "decode", Path: path, Err: readErr}
		}
		if closeErr != nil {
			return core.Element{}, &Error{Op: "decode", Path: path, Err: fmt.Errorf("%w: close: %v", ErrBulkDataResolver, closeErr)}
		}
		if elem.Tag() == core.TagPixelData && looksEncapsulatedBytes(data) {
			if !opts.TransferSyntax.Encapsulated {
				return core.Element{}, &Error{Op: "decode", Path: path, Err: ErrTransferSyntaxRequired}
			}
			parsed, err := decodeEncapsulatedBulkData(ctx, elem, data, dict, opts, b)
			if err != nil {
				return core.Element{}, &Error{Op: "decode", Path: path, Err: err}
			}
			return parsed, nil
		}
		if err := validateResolvedValueLength(elem.VR(), len(data)); err != nil {
			return core.Element{}, &Error{Op: "decode", Path: path, Err: err}
		}
		elem.Value = core.RawValue(core.CloneBytes(data))
		elem.Header.Length = core.Length(len(data))
		elem.Header.LengthSet = true
		return elem, nil
	case core.SequenceValue:
		for itemIndex := range value.Items {
			for elementIndex := range value.Items[itemIndex].Elements {
				nested := value.Items[itemIndex].Elements[elementIndex]
				resolved, err := resolveElementBulkData(ctx, nested, dict, opts, b, fmt.Sprintf("%s/Item[%d]/DicomAttribute[@tag='%s']", path, itemIndex+1, nested.Tag().HexString()))
				if err != nil {
					return core.Element{}, err
				}
				value.Items[itemIndex].Elements[elementIndex] = resolved
			}
		}
		elem.Value = value
	}
	return elem, nil
}

func decodeEncapsulatedBulkData(ctx context.Context, elem core.Element, data []byte, dict dictionary.DataDictionary, opts UnmarshalOptions, b *budget) (core.Element, error) {
	dataset := map[string]dicomjson.Element{elem.Tag().HexString(): {VR: elem.VR().String(), InlineBinary: base64.StdEncoding.EncodeToString(data)}}
	encoded, err := json.Marshal(dataset)
	if err != nil {
		return core.Element{}, err
	}
	obj, err := dicomjson.UnmarshalContext(ctx, encoded, dict, dicomjson.UnmarshalOptions{
		TextOptions: opts.TextOptions, ByteOrder: opts.ByteOrder, TransferSyntax: opts.TransferSyntax,
		Limits: dicomjson.Limits{MaxJSONBytes: jsonLimit(b.limits.MaxXMLBytes), MaxInlineBinaryBytes: int64(len(data)), MaxTotalBinaryBytes: int64(len(data)), MaxSequenceItems: b.limits.MaxSequenceItems},
	})
	if err != nil {
		return core.Element{}, err
	}
	resolved, ok := obj.Get(elem.Tag())
	if !ok {
		return core.Element{}, fmt.Errorf("resolved Pixel Data is missing")
	}
	return resolved, nil
}

func validateResolvedValueLength(vr core.VR, size int) error {
	width := 1
	switch vr {
	case core.VRAT, core.VRFL, core.VROF, core.VRSL, core.VRUL, core.VROL:
		width = 4
	case core.VRFD, core.VROD, core.VRSV, core.VRUV, core.VROV:
		width = 8
	case core.VRSS, core.VRUS, core.VROW:
		width = 2
	}
	if size%width != 0 {
		return fmt.Errorf("%w: resolved VR %s length %d is not a multiple of %d", ErrBulkDataResolver, vr, size, width)
	}
	return nil
}

func readResolvedBulk(reader io.Reader, b *budget) ([]byte, error) {
	remaining := b.limits.MaxTotalBinaryBytes - b.binary
	if remaining < 0 {
		remaining = 0
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: b.ctx, reader: reader}, remaining+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBulkDataResolver, err)
	}
	if int64(len(data)) > remaining {
		return nil, ErrMaxBinaryBytesExceeded
	}
	if err := b.addBinary(int64(len(data)), false); err != nil {
		return nil, err
	}
	return data, nil
}

func joinNameComponents(parts [5]string) string {
	last := len(parts)
	for last > 0 && parts[last-1] == "" {
		last--
	}
	return strings.Join(parts[:last], "^")
}

type privateAllocator struct {
	byGroup map[uint16]map[string]uint16
	used    map[uint16]map[uint16]bool
}

func newPrivateAllocator(attrs []nativeAttribute) *privateAllocator {
	a := &privateAllocator{byGroup: map[uint16]map[string]uint16{}, used: map[uint16]map[uint16]bool{}}
	for _, attr := range attrs {
		tag, err := core.ParseTag(attr.tag)
		if err != nil || !tag.IsPrivate() || tag.Element < 0x0010 || tag.Element > 0x00ff || len(attr.values) == 0 {
			continue
		}
		if a.byGroup[tag.Group] == nil {
			a.byGroup[tag.Group] = map[string]uint16{}
			a.used[tag.Group] = map[uint16]bool{}
		}
		a.byGroup[tag.Group][attr.values[0]] = tag.Element
		a.used[tag.Group][tag.Element] = true
	}
	return a
}
func (a *privateAllocator) allocate(normalized core.Tag, creator string) (core.Tag, error) {
	if a.byGroup[normalized.Group] == nil {
		a.byGroup[normalized.Group] = map[string]uint16{}
		a.used[normalized.Group] = map[uint16]bool{}
	}
	block, ok := a.byGroup[normalized.Group][creator]
	if !ok {
		for candidate := uint16(0x10); candidate <= 0xff; candidate++ {
			if !a.used[normalized.Group][candidate] {
				block = candidate
				ok = true
				break
			}
		}
		if !ok {
			return core.Tag{}, fmt.Errorf("%w: no private creator block available in group %04X", ErrInvalidXML, normalized.Group)
		}
		a.byGroup[normalized.Group][creator] = block
		a.used[normalized.Group][block] = true
	}
	return core.NewTag(normalized.Group, block<<8|(normalized.Element&0xff)), nil
}

func inlineVR(vr core.VR) bool {
	switch vr {
	case core.VROB, core.VROD, core.VROF, core.VROL, core.VROV, core.VROW, core.VRUN:
		return true
	default:
		return false
	}
}

func stripXMLWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, value)
}

func looksEncapsulatedValueField(encoded string) bool {
	if len(encoded) < 8 {
		return false
	}
	prefix, err := base64.StdEncoding.DecodeString(encoded[:8])
	if err != nil || len(prefix) < 4 {
		return false
	}
	return looksEncapsulatedBytes(prefix)
}

func looksEncapsulatedBytes(data []byte) bool {
	return len(data) >= 4 && (bytes.Equal(data[:4], []byte{0xfe, 0xff, 0x00, 0xe0}) ||
		bytes.Equal(data[:4], []byte{0xff, 0xfe, 0xe0, 0x00}))
}

func invalidXML(path, format string, args ...any) error {
	return &Error{Op: "decode", Path: path, Err: fmt.Errorf("%w: %s", ErrInvalidXML, fmt.Sprintf(format, args...))}
}
