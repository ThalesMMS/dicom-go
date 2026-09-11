package dicomtest

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

// SemanticElement is a test-only, lossless comparison form. Numbers are decimal
// strings rather than float64; sequence item order and empty attributes survive.
// It deliberately does not use any serializer under comparison as its oracle.
type SemanticElement struct {
	Tag     string              `json:"tag"`
	VR      string              `json:"vr"`
	Values  []string            `json:"values,omitempty"`
	Items   [][]SemanticElement `json:"items,omitempty"`
	Opaque  string              `json:"opaque,omitempty"`
	BulkURI string              `json:"bulk_uri,omitempty"`
}

func SemanticDataSet(ds core.DataSet, order binary.ByteOrder) ([]SemanticElement, error) {
	return semanticDataSet(ds, order, 0)
}

func semanticDataSet(ds core.DataSet, order binary.ByteOrder, depth int) ([]SemanticElement, error) {
	if depth > 32 || len(ds.Elements) > 10000 {
		return nil, fmt.Errorf("semantic fixture budget exceeded")
	}
	if order == nil {
		order = binary.LittleEndian
	}
	result := make([]SemanticElement, 0, len(ds.Elements))
	seen := map[core.Tag]bool{}
	for _, e := range ds.Elements {
		if seen[e.Tag()] {
			return nil, fmt.Errorf("%s: duplicate attribute", e.Tag())
		}
		seen[e.Tag()] = true
		n := SemanticElement{Tag: e.Tag().HexString(), VR: e.VR().String()}
		switch v := e.Value.(type) {
		case core.SequenceValue:
			for i, item := range v.Items {
				child, err := semanticDataSet(item, order, depth+1)
				if err != nil {
					return nil, fmt.Errorf("%s[%d]/%w", n.Tag, i, err)
				}
				n.Items = append(n.Items, child)
			}
		case core.BulkDataValue:
			n.BulkURI = v.URI
		case core.StringValue:
			n.Values = core.SplitTextMultiplicity(e.VR(), strings.Join(v, "\\"))
		case core.RawValue:
			if e.VR().IsStringLike() {
				n.Values = core.SplitTextMultiplicity(e.VR(), string(v))
			} else {
				var err error
				n.Values, n.Opaque, err = semanticRaw(e.VR(), v, order)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", n.Tag, err)
				}
			}
		case core.TagValue:
			for _, tag := range v {
				n.Values = append(n.Values, tag.HexString())
			}
		case core.Uint16Value, core.Uint32Value, core.Uint64Value:
			values := reflect.ValueOf(v)
			for i := 0; i < values.Len(); i++ {
				n.Values = append(n.Values, strconv.FormatUint(values.Index(i).Uint(), 10))
			}
		case core.Int16Value, core.Int32Value, core.Int64Value:
			values := reflect.ValueOf(v)
			for i := 0; i < values.Len(); i++ {
				n.Values = append(n.Values, strconv.FormatInt(values.Index(i).Int(), 10))
			}
		case core.Float32Value:
			for _, f := range v {
				n.Values = append(n.Values, strconv.FormatFloat(float64(f), 'g', -1, 32))
			}
		case core.Float64Value:
			for _, f := range v {
				n.Values = append(n.Values, strconv.FormatFloat(f, 'g', -1, 64))
			}
		case nil:
			if !e.Header.HasLength() || e.Header.Length != 0 {
				return nil, fmt.Errorf("%s: unavailable value is not empty", n.Tag)
			}
		default:
			return nil, fmt.Errorf("%s: unsupported fixture value %T", n.Tag, v)
		}
		if e.VR() == core.VRDS || e.VR() == core.VRIS {
			for i, value := range n.Values {
				if value == "" {
					continue
				}
				number, ok := new(big.Rat).SetString(value)
				if !ok {
					return nil, fmt.Errorf("%s/value[%d]: invalid decimal fixture value", n.Tag, i)
				}
				n.Values[i] = number.RatString()
			}
		}
		result = append(result, n)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tag < result[j].Tag })
	return result, nil
}

func semanticRaw(vr core.VR, raw []byte, order binary.ByteOrder) ([]string, string, error) {
	width := 0
	switch vr {
	case core.VRSS, core.VRUS:
		width = 2
	case core.VRSL, core.VRUL, core.VRFL, core.VRAT:
		width = 4
	case core.VRSV, core.VRUV, core.VRFD:
		width = 8
	}
	if width == 0 {
		return nil, hex.EncodeToString(raw), nil
	}
	if len(raw)%width != 0 {
		return nil, "", fmt.Errorf("invalid %s byte length", vr)
	}
	var values []string
	for len(raw) > 0 {
		var value string
		switch vr {
		case core.VRUS:
			value = strconv.FormatUint(uint64(order.Uint16(raw)), 10)
		case core.VRSS:
			value = strconv.FormatInt(int64(int16(order.Uint16(raw))), 10)
		case core.VRUL:
			value = strconv.FormatUint(uint64(order.Uint32(raw)), 10)
		case core.VRSL:
			value = strconv.FormatInt(int64(int32(order.Uint32(raw))), 10)
		case core.VRUV:
			value = strconv.FormatUint(order.Uint64(raw), 10)
		case core.VRSV:
			value = strconv.FormatInt(int64(order.Uint64(raw)), 10)
		case core.VRFL:
			value = strconv.FormatFloat(float64(math.Float32frombits(order.Uint32(raw))), 'g', -1, 32)
		case core.VRFD:
			value = strconv.FormatFloat(math.Float64frombits(order.Uint64(raw)), 'g', -1, 64)
		case core.VRAT:
			value = core.NewTag(order.Uint16(raw), order.Uint16(raw[2:])).HexString()
		}
		values = append(values, value)
		raw = raw[width:]
	}
	return values, "", nil
}

// DiffSemantic reports the first tag/item/value path without dumping values.
func DiffSemantic(got, want []SemanticElement) string {
	return diffSemantic("dataset", got, want)
}

func diffSemantic(path string, got, want []SemanticElement) string {
	for i, w := range want {
		if i >= len(got) {
			return path + "/" + w.Tag + ": attribute missing"
		}
		g := got[i]
		p := path + "/" + w.Tag
		if g.Tag != w.Tag {
			return p + ": attribute missing or unexpected"
		}
		if g.VR != w.VR {
			return p + ": VR differs"
		}
		if len(g.Values) != len(w.Values) {
			return p + ": VM differs"
		}
		for j, v := range w.Values {
			if g.Values[j] != v {
				return fmt.Sprintf("%s/value[%d]: differs", p, j)
			}
		}
		if g.Opaque != w.Opaque {
			return p + ": opaque bytes differ"
		}
		if g.BulkURI != w.BulkURI {
			return p + ": BulkDataURI differs"
		}
		if len(g.Items) != len(w.Items) {
			return p + ": sequence item count differs"
		}
		for j, item := range w.Items {
			if diff := diffSemantic(fmt.Sprintf("%s/item[%d]", p, j), g.Items[j], item); diff != "" {
				return diff
			}
		}
	}
	if len(got) > len(want) {
		return path + "/" + got[len(want)].Tag + ": unexpected attribute"
	}
	return ""
}
