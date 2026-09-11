// Package codify generates reviewable Go source using the public object/core
// APIs. Its default replaces payloads; faithful generation is an explicit local
// opt-in and can contain PHI. Generate performs no file or network operations.
package codify

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"go/types"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	ErrOptions = errors.New("codify: invalid options")
	ErrLimit   = errors.New("codify: resource limit")
	ErrValue   = errors.New("codify: unsupported or unavailable value")
)

type Mode string

const (
	Structural Mode = "structural"
	Faithful   Mode = "faithful"
)

// Limits are finite in both modes. Zero fields select defaults; values above
// the documented ceilings or negative values are invalid.
type Limits struct {
	MaxElements, MaxDepth, MaxItems, MaxValues    int
	MaxValueBytes, MaxBinaryBytes, MaxOutputBytes int
}

func DefaultLimits() Limits { return Limits{10000, 32, 10000, 100000, 4 << 20, 64 << 10, 1 << 20} }

type Options struct {
	Package, Function string
	Mode              Mode
	// InlineBinary retains bounded raw bulk/unknown values and fragments only in
	// faithful mode. Otherwise explicit DiscardedValue placeholders are emitted.
	InlineBinary bool
	Limits       Limits
}

// Change contains only structural coordinates and a fixed reason, never values.
type Change struct {
	Path   object.WalkPath `json:"path"`
	Reason string          `json:"reason"`
}
type Report struct {
	Schema       string   `json:"schema"`
	Mode         Mode     `json:"mode"`
	InlineBinary bool     `json:"inline_binary"`
	Elements     int      `json:"elements"`
	Changes      []Change `json:"changes,omitempty"`
}
type Result struct {
	Source []byte
	Report Report
}

// Generate preserves supported in-memory values, not original Part 10 bytes,
// file meta, preamble, dictionary overlays or parser offsets. The caller owns
// obj and must keep it unchanged until Generate returns. Deferred/custom values
// are never resolved and BulkDataURI is never fetched.
func Generate(ctx context.Context, obj *object.Object, opts Options) (Result, error) {
	o, err := normalize(opts)
	if err != nil {
		return Result{}, err
	}
	if ctx == nil || obj == nil {
		return Result{}, ErrOptions
	}
	g := generator{ctx: ctx, opts: o, report: Report{Schema: "dicom-go-codify/v1", Mode: o.Mode, InlineBinary: o.InlineBinary}}
	// Reuse the existing bounded, item-aware object traversal for preflight.
	err = obj.WalkPathContext(ctx, object.WalkOptions{MaxDepth: o.Limits.MaxDepth, MaxElements: o.Limits.MaxElements}, func(path object.WalkPath, e core.Element) error {
		g.report.Elements++
		if _, err := core.ParseVR(string(e.VR())); err != nil {
			return ErrValue
		}
		if len(e.VR()) != 2 {
			return ErrValue
		}
		switch v := e.Value.(type) {
		case core.SequenceValue:
			if len(v.Items) > o.Limits.MaxItems-g.items {
				return ErrLimit
			}
			g.items += len(v.Items)
		case core.FragmentSequence:
			if len(v.Fragments) > o.Limits.MaxValues-g.values {
				return ErrLimit
			}
			g.values += len(v.Fragments)
			if o.Mode == Faithful && o.InlineBinary {
				if err := g.addBytes(len(v.OffsetTable), true); err != nil {
					return err
				}
				for _, f := range v.Fragments {
					if err := g.addBytes(len(f), true); err != nil {
						return err
					}
				}
			}
		case core.RawValue:
			if o.Mode == Faithful && (!bulk(e) || o.InlineBinary) {
				if err := g.addBytes(len(v), bulk(e)); err != nil {
					return err
				}
			}
		case core.StringValue:
			if len(v) > o.Limits.MaxValues-g.values {
				return ErrLimit
			}
			g.values += len(v)
			if o.Mode == Faithful {
				for _, s := range v {
					if err := g.addBytes(len(s), false); err != nil {
						return err
					}
				}
			}
		case core.Uint16Value, core.Int16Value, core.Uint32Value, core.Int32Value, core.Uint64Value, core.Int64Value, core.Float32Value, core.Float64Value, core.TagValue:
			n := reflect.ValueOf(v).Len()
			if n > o.Limits.MaxValues-g.values {
				return ErrLimit
			}
			g.values += n
			if o.Mode == Faithful && bulk(e) && o.InlineBinary {
				width := int(reflect.TypeOf(v).Elem().Size())
				if n > o.Limits.MaxBinaryBytes/width {
					return ErrLimit
				}
				if err := g.addBytes(n*width, true); err != nil {
					return err
				}
			}
		case core.BulkDataValue:
			if o.Mode == Faithful {
				if err := g.addBytes(len(v.URI), false); err != nil {
					return err
				}
			}
		case nil:
			if o.Mode == Faithful && !(bulk(e) && !o.InlineBinary) && (!e.Header.LengthSet || e.Header.Length != 0) {
				return ErrValue
			}
		case core.DiscardedValue:
		default:
			if o.Mode == Faithful {
				return ErrValue
			}
		}
		reason := ""
		if o.Mode == Structural {
			reason = "payload replaced; parser offsets and encoded lengths omitted"
		} else if (bulk(e) && !o.InlineBinary) || isDiscarded(e.Value) {
			reason = "payload omitted; explicit discarded placeholder"
		}
		if reason != "" {
			g.report.Changes = append(g.report.Changes, Change{path.Clone(), reason})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, object.ErrWalkResourceLimit) {
			err = ErrLimit
		}
		return Result{}, err
	}
	g.limit = o.Limits.MaxOutputBytes
	g.write("func %s() *object.Object {\nds := object.FromElements([]core.Element{\n", o.Function)
	g.elements(obj.Elements())
	g.write("}, std.Dictionary)\n")
	order := "LittleEndian"
	if obj.ValueByteOrder() == binary.BigEndian {
		order = "BigEndian"
	}
	g.write("ds.SetValueByteOrder(binary.%s)\nreturn ds\n}\n", order)
	if o.Package == "main" {
		g.write("func main() { ds := %s(); fmt.Printf(\"constructed %%d elements; review generation report before use\\n\", len(ds.Elements())) }\n", o.Function)
	}
	if g.err != nil {
		return Result{}, g.err
	}
	header := "// Generated by github.com/ThalesMMS/dicom-go/codify; schema dicom-go-codify/v1.\n"
	header += fmt.Sprintf("// Mode: %s; inline binary: %t; elements: %d; substitutions/omissions: %d.\n", o.Mode, o.InlineBinary, g.report.Elements, len(g.report.Changes))
	header += "// Reconstructs an object, not a byte-exact Part 10 file. No automatic execution.\n"
	if o.Mode == Faithful {
		header += "// LOCAL FAITHFUL OUTPUT MAY CONTAIN PHI, INCLUDING PRIVATE/NESTED DATA AND URIS.\n"
	} else {
		header += "// Payloads replaced with synthetic/empty/discarded values; not an anonymization guarantee.\n"
	}
	header += fmt.Sprintf("package %s\nimport (\n\"encoding/binary\"\n\"github.com/ThalesMMS/dicom-go/core\"\n\"github.com/ThalesMMS/dicom-go/object\"\n\"github.com/ThalesMMS/dicom-go/dictionary/std\"\n", o.Package)
	if g.useMath {
		header += "\"math\"\n"
	}
	if o.Package == "main" {
		header += "\"fmt\"\n"
	}
	header += ")\n"
	if len(header) > g.limit-g.buf.Len() {
		return Result{}, ErrLimit
	}
	src, err := format.Source(append([]byte(header), g.buf.Bytes()...))
	if err != nil {
		return Result{}, ErrValue
	}
	if len(src) > g.limit {
		return Result{}, ErrLimit
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{Source: src, Report: g.report}, nil
}

// NormalizeOptions validates options and resolves zero fields to finite defaults.
func NormalizeOptions(o Options) (Options, error) { return normalize(o) }

func normalize(o Options) (Options, error) {
	if o.Package == "" {
		o.Package = "main"
	}
	if o.Function == "" {
		o.Function = "BuildObject"
	}
	if o.Mode == "" {
		o.Mode = Structural
	}
	if !token.IsIdentifier(o.Package) || o.Package == "_" || !token.IsIdentifier(o.Function) || types.Universe.Lookup(o.Function) != nil || strings.Contains("|_|main|init|core|object|std|binary|math|fmt|", "|"+o.Function+"|") {
		return Options{}, ErrOptions
	}
	if o.Mode != Structural && o.Mode != Faithful || o.InlineBinary && o.Mode != Faithful {
		return Options{}, ErrOptions
	}
	d := DefaultLimits()
	fields := []*int{&o.Limits.MaxElements, &o.Limits.MaxDepth, &o.Limits.MaxItems, &o.Limits.MaxValues, &o.Limits.MaxValueBytes, &o.Limits.MaxBinaryBytes, &o.Limits.MaxOutputBytes}
	defaults := []int{d.MaxElements, d.MaxDepth, d.MaxItems, d.MaxValues, d.MaxValueBytes, d.MaxBinaryBytes, d.MaxOutputBytes}
	caps := []int{100000, 64, 100000, 1000000, 64 << 20, 1 << 20, 16 << 20}
	for i, p := range fields {
		if *p == 0 {
			*p = defaults[i]
		}
		if *p < 0 || *p > caps[i] {
			return Options{}, ErrOptions
		}
	}
	return o, nil
}

type generator struct {
	ctx                                           context.Context
	opts                                          Options
	report                                        Report
	buf                                           bytes.Buffer
	limit, items, values, valueBytes, binaryBytes int
	err                                           error
	useMath                                       bool
}

func (g *generator) addBytes(n int, binaryValue bool) error {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if n > g.opts.Limits.MaxValueBytes-g.valueBytes {
		return ErrLimit
	}
	g.valueBytes += n
	if binaryValue {
		if n > g.opts.Limits.MaxBinaryBytes-g.binaryBytes {
			return ErrLimit
		}
		g.binaryBytes += n
	}
	return nil
}
func (g *generator) write(format string, args ...any) {
	if g.err != nil {
		return
	}
	if err := g.ctx.Err(); err != nil {
		g.err = err
		return
	}
	s := fmt.Sprintf(format, args...)
	if len(s) > g.limit-g.buf.Len() {
		g.err = ErrLimit
		return
	}
	g.buf.WriteString(s)
}
func isDiscarded(v core.Value) bool { _, ok := v.(core.DiscardedValue); return ok }
func bulk(e core.Element) bool {
	if e.Tag() == core.NewTag(0x7fe0, 0x0010) || e.Tag() == core.NewTag(0x7fe0, 0x0008) || e.Tag() == core.NewTag(0x7fe0, 0x0009) {
		return true
	}
	if _, ok := e.Value.(core.FragmentSequence); ok {
		return true
	}
	switch e.VR() {
	case core.VROB, core.VROW, core.VROF, core.VROD, core.VROL, core.VROV, core.VRUN:
		return true
	}
	return false
}
func (g *generator) elements(elements []core.Element) {
	for _, e := range elements {
		if g.err != nil {
			return
		}
		if g.opts.Mode == Structural {
			g.write("// Payload replaced; original lengths and offsets omitted.\n")
		} else if (bulk(e) && !g.opts.InlineBinary) || isDiscarded(e.Value) {
			g.write("// Payload omitted; explicit discarded placeholder.\n")
		}
		g.write("{Header:core.ElementHeader{Tag:core.NewTag(0x%04X,0x%04X),VR:core.VR(%s)", e.Tag().Group, e.Tag().Element, strconv.Quote(string(e.VR())))
		if g.opts.Mode == Faithful && !(bulk(e) && !g.opts.InlineBinary) && !isDiscarded(e.Value) {
			g.write(",Length:core.Length(%d),LengthSet:%t", e.Header.Length, e.Header.LengthSet)
		}
		g.write("},Value:")
		g.value(e)
		g.write("},\n")
	}
}
func (g *generator) value(e core.Element) {
	structural := g.opts.Mode == Structural
	if (bulk(e) && (structural || !g.opts.InlineBinary)) || isDiscarded(e.Value) {
		g.write("core.DiscardedValue{}")
		return
	}
	switch v := e.Value.(type) {
	case nil:
		g.write("nil")
	case core.SequenceValue:
		g.write("core.SequenceValue{Items:[]core.DataSet{")
		for _, item := range v.Items {
			g.write("{Elements:[]core.Element{\n")
			g.elements(item.Elements)
			g.write("}},\n")
			if g.err != nil {
				return
			}
		}
		g.write("}}")
	case core.FragmentSequence:
		g.write("core.FragmentSequence{OffsetTable:[]byte(%s),Fragments:[][]byte{", strconv.Quote(string(v.OffsetTable)))
		for _, fragment := range v.Fragments {
			g.write("[]byte(%s),", strconv.Quote(string(fragment)))
		}
		g.write("}}")
	case core.RawValue:
		if structural {
			g.write("core.RawValue{}")
			return
		}
		g.write("core.RawValue(%s)", strconv.Quote(string(v)))
	case core.StringValue:
		g.write("core.StringValue{")
		for _, s := range v {
			if structural {
				s = ""
			}
			g.write("%s,", strconv.Quote(s))
			if g.err != nil {
				return
			}
		}
		g.write("}")
	case core.BulkDataValue:
		if structural {
			g.write("core.DiscardedValue{}")
			return
		}
		g.write("core.BulkDataValue{URI:%s}", strconv.Quote(v.URI))
	case core.TagValue:
		g.write("core.TagValue{")
		for _, tag := range v {
			if structural {
				tag = core.Tag{}
			}
			g.write("core.NewTag(0x%04X,0x%04X),", tag.Group, tag.Element)
			if g.err != nil {
				return
			}
		}
		g.write("}")
	case core.Uint16Value, core.Int16Value, core.Uint32Value, core.Int32Value, core.Uint64Value, core.Int64Value, core.Float32Value, core.Float64Value:
		g.write("core.%s{", reflect.TypeOf(v).Name())
		values := reflect.ValueOf(v)
		for i := 0; i < values.Len(); i++ {
			if structural {
				g.write("0,")
				continue
			}
			n := values.Index(i)
			switch n.Kind() {
			case reflect.Uint16, reflect.Uint32, reflect.Uint64:
				g.write("%s,", strconv.FormatUint(n.Uint(), 10))
			case reflect.Int16, reflect.Int32, reflect.Int64:
				g.write("%s,", strconv.FormatInt(n.Int(), 10))
			case reflect.Float32:
				g.useMath = true
				g.write("math.Float32frombits(0x%08X),", math.Float32bits(v.(core.Float32Value)[i]))
			case reflect.Float64:
				g.useMath = true
				g.write("math.Float64frombits(0x%016X),", math.Float64bits(n.Float()))
			}
			if g.err != nil {
				return
			}
		}
		g.write("}")
	default:
		g.write("core.DiscardedValue{}")
	}
}
