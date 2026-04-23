package parser

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type LengthPolicy uint8

const (
	LengthPolicyUndefined LengthPolicy = iota
	LengthPolicyPreserve
)

type WriterOptions struct {
	LengthPolicy LengthPolicy
}

func defaultWriterOptions() WriterOptions {
	return WriterOptions{LengthPolicy: LengthPolicyUndefined}
}

type Writer struct {
	w      io.Writer
	syntax transfer.Syntax
	enc    dicomenc.BasicEncoder
	opts   WriterOptions
}

func NewWriter(w io.Writer, syntax transfer.Syntax) *Writer {
	return NewWriterWithOptions(w, syntax, defaultWriterOptions())
}

func NewWriterWithOptions(w io.Writer, syntax transfer.Syntax, opts WriterOptions) *Writer {
	return &Writer{
		w:      w,
		syntax: syntax,
		enc:    dicomenc.NewBasicEncoder(syntax.ByteOrder),
		opts:   opts,
	}
}

func (w *Writer) WriteElement(el core.Element) error {
	if el.VR() == core.VRSQ {
		if err := w.validateElement(el); err != nil {
			return err
		}
		return w.writeSequenceValue(el)
	}
	if _, ok := el.Value.(core.FragmentSequence); ok {
		if err := w.validateElement(el); err != nil {
			return err
		}
		return w.writeFragmentSequence(el)
	}
	if err := w.validateElement(el); err != nil {
		return err
	}

	value, length, err := w.encodeValue(el)
	if err != nil {
		return err
	}
	if err := w.writeHeader(el.Tag(), el.VR(), length); err != nil {
		return err
	}
	if err := writeAll(w.w, value); err != nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), length, err)
	}
	return nil
}

func (w *Writer) encodeValue(el core.Element) ([]byte, core.Length, error) {
	if el.Value == nil {
		return nil, 0, nil
	}

	switch value := el.Value.(type) {
	case core.RawValue:
		if el.Header.HasLength() && el.Header.Length.IsUndefined() {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), el.Header.Length, fmt.Errorf("dicom: undefined length is only supported for sequence and fragment values"))
		}
		padded := el.VR().PadToEvenLength(value.Bytes())
		length, err := dicomenc.Uint32Length(len(padded))
		if err != nil {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), 0, err)
		}
		return padded, core.Length(length), nil
	case core.StringValue:
		if el.Header.HasLength() && el.Header.Length.IsUndefined() {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), el.Header.Length, fmt.Errorf("dicom: undefined length is only supported for sequence and fragment values"))
		}
		encoded := el.VR().PadToEvenLength([]byte(strings.Join([]string(value), "\\")))
		length, err := dicomenc.Uint32Length(len(encoded))
		if err != nil {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), 0, err)
		}
		return encoded, core.Length(length), nil
	default:
		return nil, 0, w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: unsupported value type %T", el.Value))
	}
}

func (w *Writer) validateElement(el core.Element) error {
	if el.Tag().IsSequenceDelimiting() {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), core.UndefinedLength, fmt.Errorf("dicom: items and delimiters cannot be written as standalone elements"))
	}

	switch el.Value.(type) {
	case nil, core.RawValue, core.StringValue:
	case core.SequenceValue:
		if el.VR() != core.VRSQ {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: core.SequenceValue requires SQ VR"))
		}
	case core.FragmentSequence:
		if el.Tag() != core.TagPixelData {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: core.FragmentSequence is only supported for Pixel Data"))
		}
		if el.VR() != core.VROB && el.VR() != core.VROW {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: encapsulated Pixel Data requires OB or OW VR"))
		}
	default:
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: unsupported value type %T", el.Value))
	}
	return nil
}

func (w *Writer) writeItem(ds core.DataSet) error {
	if err := w.writeItemHeader(core.UndefinedLength); err != nil {
		return err
	}
	for _, el := range ds.Elements {
		if err := w.WriteElement(el); err != nil {
			return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, core.UndefinedLength, err)
		}
	}
	if err := w.writeItemDelimiter(); err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, core.UndefinedLength, err)
	}
	return nil
}

func (w *Writer) writeDefinedItem(ds core.DataSet) error {
	itemValue, err := w.encodeDataSet(ds)
	if err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, 0, err)
	}
	length, err := dicomenc.Uint32Length(len(itemValue))
	if err != nil {
		return w.wrapWriteError(OpWriteLength, core.TagItem, core.VRUN, 0, err)
	}
	itemLength := core.Length(length)
	if err := w.writeItemHeader(itemLength); err != nil {
		return err
	}
	if err := writeAll(w.w, itemValue); err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, itemLength, err)
	}
	return nil
}

func (w *Writer) writeSequenceValue(el core.Element) error {
	value, ok := el.Value.(core.SequenceValue)
	if !ok {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: sequence VR requires core.SequenceValue"))
	}

	if !w.sequenceUsesUndefinedLength(el) {
		return w.writeDefinedLengthSequenceValue(el, value)
	}
	if err := w.writeHeader(el.Tag(), core.VRSQ, core.UndefinedLength); err != nil {
		return err
	}
	for _, item := range value.Items {
		if err := w.writeItem(item); err != nil {
			return err
		}
	}
	if err := w.writeSequenceDelimiter(); err != nil {
		return err
	}
	return nil
}

func (w *Writer) writeDefinedLengthSequenceValue(el core.Element, value core.SequenceValue) error {
	var buf bytes.Buffer
	child := w.child(&buf)
	for _, item := range value.Items {
		if err := child.writeDefinedItem(item); err != nil {
			return err
		}
	}
	length, err := dicomenc.Uint32Length(buf.Len())
	if err != nil {
		return w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), 0, err)
	}
	sequenceLength := core.Length(length)
	if err := w.writeHeader(el.Tag(), core.VRSQ, sequenceLength); err != nil {
		return err
	}
	if err := writeAll(w.w, buf.Bytes()); err != nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), sequenceLength, err)
	}
	return nil
}

func (w *Writer) writeBasicOffsetTable(table []byte) error {
	if len(table)%4 != 0 {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, 0, fmt.Errorf("dicom: Basic Offset Table length %d is not a multiple of 4", len(table)))
	}
	length, err := dicomenc.Uint32Length(len(table))
	if err != nil {
		return w.wrapWriteError(OpWriteLength, core.TagItem, core.VRUN, 0, err)
	}
	itemLength := core.Length(length)
	if err := w.writeItemHeader(itemLength); err != nil {
		return err
	}
	if err := writeAll(w.w, table); err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, itemLength, err)
	}
	return nil
}

func (w *Writer) writeFragment(data []byte) error {
	paddedLength, err := dicomenc.Uint32Length(dicomenc.EvenLength(len(data)))
	if err != nil {
		return w.wrapWriteError(OpWriteLength, core.TagItem, core.VRUN, 0, err)
	}
	itemLength := core.Length(paddedLength)
	if err := w.writeItemHeader(itemLength); err != nil {
		return err
	}
	if err := writeAll(w.w, data); err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, itemLength, err)
	}
	if len(data)%2 == 1 {
		if err := writeAll(w.w, []byte{0x00}); err != nil {
			return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, itemLength, err)
		}
	}
	return nil
}

func (w *Writer) writeFragmentSequence(el core.Element) error {
	value, ok := el.Value.(core.FragmentSequence)
	if !ok {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: Pixel Data requires core.FragmentSequence"))
	}
	// Encapsulated Pixel Data must use OB in explicit VR encodings, so the
	// writer preserves DICOM compliance here even if the caller provided OW.
	if err := w.writeHeader(core.TagPixelData, core.VROB, core.UndefinedLength); err != nil {
		return err
	}
	if err := w.writeBasicOffsetTable(value.OffsetTable); err != nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), core.VROB, core.UndefinedLength, err)
	}
	for _, fragment := range value.Fragments {
		if err := w.writeFragment(fragment); err != nil {
			return w.wrapWriteError(OpWriteValue, el.Tag(), core.VROB, core.UndefinedLength, err)
		}
	}
	if err := w.writeSequenceDelimiter(); err != nil {
		return err
	}
	return nil
}

func (w *Writer) encodeDataSet(dataSet core.DataSet) ([]byte, error) {
	var buf bytes.Buffer
	child := w.child(&buf)
	for _, el := range dataSet.Elements {
		if err := child.WriteElement(el); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (w *Writer) child(out io.Writer) *Writer {
	return &Writer{
		w:      out,
		syntax: w.syntax,
		enc:    w.enc,
		opts:   w.opts,
	}
}

func (w *Writer) sequenceUsesUndefinedLength(el core.Element) bool {
	if w.opts.LengthPolicy == LengthPolicyUndefined {
		return true
	}
	return el.Header.HasLength() && el.Header.Length.IsUndefined()
}

func (w *Writer) writeItemDelimiter() error {
	if err := w.enc.WriteTag(w.w, core.TagItemDelimitationItem); err != nil {
		return w.wrapWriteError(OpWriteTag, core.TagItemDelimitationItem, core.VRUN, 0, err)
	}
	if err := w.enc.WriteU32(w.w, 0); err != nil {
		return w.wrapWriteError(OpWriteLength, core.TagItemDelimitationItem, core.VRUN, 0, err)
	}
	return nil
}

func (w *Writer) writeSequenceDelimiter() error {
	if err := w.enc.WriteTag(w.w, core.TagSequenceDelimitationItem); err != nil {
		return w.wrapWriteError(OpWriteTag, core.TagSequenceDelimitationItem, core.VRUN, 0, err)
	}
	if err := w.enc.WriteU32(w.w, 0); err != nil {
		return w.wrapWriteError(OpWriteLength, core.TagSequenceDelimitationItem, core.VRUN, 0, err)
	}
	return nil
}

func (w *Writer) writeItemHeader(length core.Length) error {
	if err := w.enc.WriteTag(w.w, core.TagItem); err != nil {
		return w.wrapWriteError(OpWriteTag, core.TagItem, core.VRUN, length, err)
	}
	if err := w.enc.WriteU32(w.w, uint32(length)); err != nil {
		return w.wrapWriteError(OpWriteLength, core.TagItem, core.VRUN, length, err)
	}
	return nil
}

func (w *Writer) writeHeader(tag core.Tag, vr core.VR, length core.Length) error {
	if w.syntax.ExplicitVR {
		return w.writeExplicitHeader(tag, vr, length)
	}
	return w.writeImplicitHeader(tag, vr, length)
}

func (w *Writer) writeExplicitHeader(tag core.Tag, vr core.VR, length core.Length) error {
	if err := w.enc.WriteTag(w.w, tag); err != nil {
		return w.wrapWriteError(OpWriteTag, tag, vr, length, err)
	}

	vr = normalizeExplicitHeaderVR(vr)
	vrBytes := []byte(vr.String())
	if len(vrBytes) != 2 {
		return w.wrapWriteError(OpWriteVR, tag, vr, length, fmt.Errorf("dicom: invalid VR %q", vr))
	}
	if err := writeAll(w.w, vrBytes); err != nil {
		return w.wrapWriteError(OpWriteVR, tag, vr, length, err)
	}

	if vr.UsesLongExplicitLength() {
		if err := writeAll(w.w, []byte{0x00, 0x00}); err != nil {
			return w.wrapWriteError(OpWriteReserved, tag, vr, length, err)
		}
		if err := w.enc.WriteU32(w.w, uint32(length)); err != nil {
			return w.wrapWriteError(OpWriteLength, tag, vr, length, err)
		}
		return nil
	}

	if uint32(length) > 0xFFFF {
		return w.wrapWriteError(OpWriteLength, tag, vr, length, fmt.Errorf("dicom: explicit VR %s length %d exceeds uint16", vr, length))
	}
	if err := w.enc.WriteU16(w.w, uint16(length)); err != nil {
		return w.wrapWriteError(OpWriteLength, tag, vr, length, err)
	}
	return nil
}

func normalizeExplicitHeaderVR(vr core.VR) core.VR {
	if vr == "" {
		return core.VRUN
	}
	return vr
}

func (w *Writer) writeImplicitHeader(tag core.Tag, vr core.VR, length core.Length) error {
	if err := w.enc.WriteTag(w.w, tag); err != nil {
		return w.wrapWriteError(OpWriteTag, tag, vr, length, err)
	}
	if err := w.enc.WriteU32(w.w, uint32(length)); err != nil {
		return w.wrapWriteError(OpWriteLength, tag, vr, length, err)
	}
	return nil
}

func (w *Writer) wrapWriteError(op Op, tag core.Tag, vr core.VR, length core.Length, err error) error {
	if err == nil {
		return nil
	}
	return &WriteError{
		Op:     op,
		Tag:    tag,
		VR:     vr,
		Length: length,
		Err:    err,
	}
}

func writeAll(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		buf = buf[n:]
	}
	return nil
}
