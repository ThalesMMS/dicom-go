package parser

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/internal/valueencode"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

type LengthPolicy uint8

const (
	LengthPolicyUndefined LengthPolicy = iota
	LengthPolicyPreserve
)

var tagSpecificCharacterSet = core.NewTag(0x0008, 0x0005)

type WriterOptions struct {
	LengthPolicy LengthPolicy
	// CharacterSet encodes Unicode StringValue content for VRs governed by
	// Specific Character Set. Its zero value uses the DICOM default repertoire.
	CharacterSet dicomenc.SpecificCharacterSet
	// BulkDataResolver opens the byte source for a core.BulkDataValue. The
	// writer takes ownership of every non-nil source Reader returned by the
	// resolver and closes it exactly once, including on failures.
	BulkDataResolver BulkDataResolver
}

func defaultWriterOptions() WriterOptions {
	return WriterOptions{LengthPolicy: LengthPolicyUndefined}
}

type Writer struct {
	w            io.Writer
	syntax       transfer.Syntax
	enc          dicomenc.BasicEncoder
	opts         WriterOptions
	characterSet dicomenc.SpecificCharacterSet

	validationWrite      func(*Writer, core.Element, validation.Path) error
	validationPath       validation.Path
	validationCount      *committedWriter
	validationPostWrites *[]pendingValidationPostWrite
	validationPostWrite  func([]pendingValidationPostWrite) error
}

func NewWriter(w io.Writer, syntax transfer.Syntax) *Writer {
	return NewWriterWithOptions(w, syntax, defaultWriterOptions())
}

func NewWriterWithOptions(w io.Writer, syntax transfer.Syntax, opts WriterOptions) *Writer {
	return &Writer{
		w:            w,
		syntax:       syntax,
		enc:          dicomenc.NewBasicEncoder(syntax.ByteOrder),
		opts:         opts,
		characterSet: opts.CharacterSet,
	}
}

func (w *Writer) WriteElement(el core.Element) error {
	if el.Tag() == tagSpecificCharacterSet {
		if err := w.useDeclaredCharacterSet(el); err != nil {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), err)
		}
	}
	if el.VR() == core.VRSQ || isSequenceValue(el.Value) {
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
	if value, ok := el.Value.(core.BulkDataValue); ok {
		if err := w.validateElement(el); err != nil {
			return err
		}
		return w.writeBulkDataValue(el, value)
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

func (w *Writer) writeValidationElement(el core.Element) error {
	path := append(validation.Path(nil), w.validationPath...)
	path = append(path, validation.PathStep{Tag: el.Tag(), ItemIndex: validation.NoItem})
	return w.validationWrite(w, el, path)
}

func (w *Writer) writeElementValidated(el core.Element, path validation.Path) error {
	if el.Tag() == tagSpecificCharacterSet {
		if err := w.useDeclaredCharacterSet(el); err != nil {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), err)
		}
	}
	if el.VR() == core.VRSQ || isSequenceValue(el.Value) {
		if err := w.validateElement(el); err != nil {
			return err
		}
		return w.writeSequenceValueValidated(el, path)
	}
	if _, ok := el.Value.(core.FragmentSequence); ok {
		if err := w.validateElement(el); err != nil {
			return err
		}
		return w.writeFragmentSequence(el)
	}
	if value, ok := el.Value.(core.BulkDataValue); ok {
		if err := w.validateElement(el); err != nil {
			return err
		}
		return w.writeBulkDataValue(el, value)
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

// WriteDeferredElement writes an element whose value bytes are supplied by
// copyValueTo instead of an in-memory core.Value. It is used by higher-level
// object APIs to preserve skipped values from seekable sources.
func (w *Writer) WriteDeferredElement(el core.Element, copyValueTo func(io.Writer) (int64, error)) error {
	if el.Value != nil {
		return w.WriteElement(el)
	}
	if copyValueTo == nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: deferred element requires value provider"))
	}
	if el.Tag() == core.TagPixelData && (el.VR() == core.VROB || el.VR() == core.VROW) && el.Header.Length.IsUndefined() {
		if err := w.writeHeader(core.TagPixelData, el.VR(), core.UndefinedLength); err != nil {
			return err
		}
		if _, err := copyValueTo(w.w); err != nil {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), core.UndefinedLength, err)
		}
		return nil
	}
	if !el.Header.HasLength() || el.Header.Length.IsUndefined() {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: deferred element requires a defined length or encapsulated Pixel Data"))
	}
	if err := w.writeHeader(el.Tag(), el.VR(), el.Header.Length); err != nil {
		return err
	}
	copied, err := copyValueTo(w.w)
	if err != nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Header.Length, err)
	}
	if copied != int64(el.Header.Length) {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Header.Length, fmt.Errorf("dicom: deferred element copied %d bytes, want %d", copied, el.Header.Length))
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
		padded := padRawValueToEvenLength(el.VR(), value.Bytes())
		length, err := dicomenc.Uint32Length(len(padded))
		if err != nil {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), 0, err)
		}
		return padded, core.Length(length), nil
	case core.StringValue:
		if el.Header.HasLength() && el.Header.Length.IsUndefined() {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), el.Header.Length, fmt.Errorf("dicom: undefined length is only supported for sequence and fragment values"))
		}
		characterSet := w.characterSet
		if dictionary.IsPrivateCreatorTag(el.Tag()) {
			characterSet = dicomenc.DefaultCharacterSet
		}
		encoded, length, err := encodeStringValueWithCharacterSet(el.VR(), value, characterSet)
		if err != nil {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), 0, err)
		}
		return encoded, length, nil
	case core.Uint16Value, core.Int16Value, core.Uint32Value, core.Int32Value,
		core.Uint64Value, core.Int64Value, core.Float32Value, core.Float64Value,
		core.TagValue:
		if el.Header.HasLength() && el.Header.Length.IsUndefined() {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), el.Header.Length, fmt.Errorf("dicom: undefined length is only supported for sequence and fragment values"))
		}
		encoded, length, err := valueencode.Numeric(value, w.enc.Endianness().ByteOrder())
		if err != nil {
			return nil, 0, w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), 0, err)
		}
		return encoded, length, nil
	default:
		return nil, 0, w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: unsupported value type %T", el.Value))
	}
}

func padRawValueToEvenLength(vr core.VR, data []byte) []byte {
	if len(data)%2 == 0 {
		return data
	}
	padded := make([]byte, len(data)+1)
	copy(padded, data)
	padded[len(data)] = vr.PaddingByte()
	return padded
}

func encodeStringValue(vr core.VR, value core.StringValue) ([]byte, core.Length, error) {
	return encodeStringValueWithCharacterSet(vr, value, dicomenc.DefaultCharacterSet)
}

func encodeStringValueWithCharacterSet(vr core.VR, value core.StringValue, characterSet dicomenc.SpecificCharacterSet) ([]byte, core.Length, error) {
	components := make([][]byte, len(value))
	total := uint64(0)
	for i, component := range value {
		var (
			encoded []byte
			err     error
		)
		switch {
		case !vr.UsesSpecificCharacterSet():
			encoded = []byte(component)
		case vr == core.VRPN:
			encoded, err = characterSet.EncodePersonName(component)
		case !vr.UsesTextValueDelimiter():
			encoded, err = characterSet.EncodeSingleValue(component)
		default:
			encoded, err = characterSet.Encode(component)
		}
		if err != nil {
			return nil, 0, fmt.Errorf("dicom: encode %s string component %d: %w", vr, i, err)
		}
		components[i] = encoded
		total += uint64(len(encoded))
	}
	if len(components) > 0 {
		total += uint64(len(components) - 1)
	}
	if total%2 == 1 {
		total++
	}
	if total >= uint64(core.UndefinedLength) {
		return nil, 0, fmt.Errorf("dicom: string value length %d exceeds maximum defined DICOM length: %w", total, dicomenc.ErrLengthOverflow)
	}
	encodedLength := core.Length(total)
	length, err := definedUint32Length(encodedLength)
	if err != nil {
		return nil, 0, err
	}
	encodedSize, err := intLength(encodedLength)
	if err != nil {
		return nil, 0, err
	}

	encoded := make([]byte, encodedSize)
	offset := 0
	for i, component := range components {
		if i > 0 {
			encoded[offset] = '\\'
			offset++
		}
		offset += copy(encoded[offset:], component)
	}
	if offset < len(encoded) {
		encoded[offset] = vr.PaddingByte()
	}
	return encoded, core.Length(length), nil
}

func (w *Writer) useDeclaredCharacterSet(el core.Element) error {
	characterSet, err := dicomenc.ParseCharacterSet(el.StringValues()...)
	if err != nil {
		return err
	}
	w.characterSet = characterSet
	return nil
}

func definedUint32Length(length core.Length) (uint32, error) {
	if length.IsUndefined() {
		return 0, fmt.Errorf("dicom: defined value length %d uses the undefined-length sentinel: %w", length, dicomenc.ErrLengthOverflow)
	}
	return uint32(length), nil
}

func intLength(length core.Length) (int, error) {
	return intLengthWithMax(length, int(^uint(0)>>1))
}

func intLengthWithMax(length core.Length, maxInt int) (int, error) {
	if uint64(length) > uint64(maxInt) {
		return 0, fmt.Errorf("dicom: length %d exceeds platform int max %d: %w", length, maxInt, dicomenc.ErrLengthOverflow)
	}
	return int(length), nil
}

func (w *Writer) validateElement(el core.Element) error {
	if _, ok := el.Value.(core.DiscardedValue); ok {
		return core.ErrDiscardedValue
	}
	if el.Tag().IsSequenceDelimiting() {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), core.UndefinedLength, fmt.Errorf("dicom: items and delimiters cannot be written as standalone elements"))
	}

	switch el.Value.(type) {
	case nil, core.RawValue, core.StringValue:
	case core.BulkDataValue:
		if w.opts.BulkDataResolver == nil {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: core.BulkDataValue requires a BulkDataResolver"))
		}
		if el.VR() == core.VRSQ {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: core.BulkDataValue cannot encode an SQ value"))
		}
		if el.Header.HasLength() && el.Header.Length.IsUndefined() {
			return w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), el.Header.Length, fmt.Errorf("dicom: core.BulkDataValue requires a defined length"))
		}
		if el.Tag() == core.TagPixelData && w.syntax.Encapsulated {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: core.BulkDataValue cannot synthesize encapsulated Pixel Data"))
		}
	case core.Uint16Value, core.Int16Value, core.Uint32Value, core.Int32Value, core.Uint64Value, core.Int64Value, core.Float32Value, core.Float64Value, core.TagValue:
		if err := valueencode.ValidateNumeric(el.VR(), el.Value); err != nil {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), err)
		}
	case core.SequenceValue:
		if el.VR() != core.VRSQ && (el.VR() != core.VRUN || w.syntax.ExplicitVR) {
			return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: core.SequenceValue requires SQ VR, or UN in Implicit VR"))
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

func isSequenceValue(value core.Value) bool {
	_, ok := value.(core.SequenceValue)
	return ok
}

func (w *Writer) writeItem(ds core.DataSet) error {
	child, err := w.childForDataSet(w.w, ds)
	if err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, core.UndefinedLength, err)
	}
	if err := w.writeItemHeader(core.UndefinedLength); err != nil {
		return err
	}
	for _, el := range ds.Elements {
		if err := child.WriteElement(el); err != nil {
			return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, core.UndefinedLength, err)
		}
	}
	if err := w.writeItemDelimiter(); err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, core.UndefinedLength, err)
	}
	return nil
}

func (w *Writer) writeItemValidated(ds core.DataSet, path validation.Path) error {
	child, err := w.childForDataSetValidated(w.w, ds)
	if err != nil {
		return w.wrapWriteError(OpWriteValue, core.TagItem, core.VRUN, core.UndefinedLength, err)
	}
	if err := w.writeItemHeader(core.UndefinedLength); err != nil {
		return err
	}
	child.validationPath = path.Clone()
	for _, el := range ds.Elements {
		if err := child.writeValidationElement(el); err != nil {
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

func (w *Writer) writeDefinedItemValidated(ds core.DataSet, path validation.Path) error {
	itemValue, postWrites, err := w.encodeDataSetValidated(ds, path)
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
	if err := w.commitValidationPostWrites(postWrites); err != nil {
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
	if err := w.writeHeader(el.Tag(), el.VR(), core.UndefinedLength); err != nil {
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

func (w *Writer) writeSequenceValueValidated(el core.Element, path validation.Path) error {
	value, ok := el.Value.(core.SequenceValue)
	if !ok {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), el.Length(), fmt.Errorf("dicom: sequence VR requires core.SequenceValue"))
	}

	if !w.sequenceUsesUndefinedLength(el) {
		return w.writeDefinedLengthSequenceValueValidated(el, value, path)
	}
	if err := w.writeHeader(el.Tag(), el.VR(), core.UndefinedLength); err != nil {
		return err
	}
	for itemIndex, item := range value.Items {
		var itemPath validation.Path
		if len(path) > 0 {
			itemPath = path.Clone()
			itemPath[len(itemPath)-1].ItemIndex = itemIndex
		}
		if err := w.writeItemValidated(item, itemPath); err != nil {
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
	if err := w.writeHeader(el.Tag(), el.VR(), sequenceLength); err != nil {
		return err
	}
	if err := writeAll(w.w, buf.Bytes()); err != nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), sequenceLength, err)
	}
	return nil
}

func (w *Writer) writeDefinedLengthSequenceValueValidated(el core.Element, value core.SequenceValue, path validation.Path) error {
	var buf bytes.Buffer
	child := w.childValidatedAt(&buf, validationWriterPosition(w)+encodedHeaderLength(w.syntax, el.VR()))
	for itemIndex, item := range value.Items {
		var itemPath validation.Path
		if len(path) > 0 {
			itemPath = path.Clone()
			itemPath[len(itemPath)-1].ItemIndex = itemIndex
		}
		if err := child.writeDefinedItemValidated(item, itemPath); err != nil {
			return err
		}
	}
	length, err := dicomenc.Uint32Length(buf.Len())
	if err != nil {
		return w.wrapWriteError(OpWriteLength, el.Tag(), el.VR(), 0, err)
	}
	sequenceLength := core.Length(length)
	if err := w.writeHeader(el.Tag(), el.VR(), sequenceLength); err != nil {
		return err
	}
	if err := writeAll(w.w, buf.Bytes()); err != nil {
		return w.wrapWriteError(OpWriteValue, el.Tag(), el.VR(), sequenceLength, err)
	}
	if err := w.commitValidationPostWrites(child.pendingValidationPostWrites()); err != nil {
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
	child, err := w.childForDataSet(&buf, dataSet)
	if err != nil {
		return nil, err
	}
	for _, el := range dataSet.Elements {
		if err := child.WriteElement(el); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (w *Writer) encodeDataSetValidated(dataSet core.DataSet, path validation.Path) ([]byte, []pendingValidationPostWrite, error) {
	var buf bytes.Buffer
	child, err := w.childForDataSetValidatedAt(&buf, dataSet, validationWriterPosition(w)+8)
	if err != nil {
		return nil, nil, err
	}
	child.validationPath = path.Clone()
	for _, el := range dataSet.Elements {
		if err := child.writeValidationElement(el); err != nil {
			return nil, child.pendingValidationPostWrites(), err
		}
	}
	return buf.Bytes(), child.pendingValidationPostWrites(), nil
}

func (w *Writer) child(out io.Writer) *Writer {
	return &Writer{
		w:            out,
		syntax:       w.syntax,
		enc:          w.enc,
		opts:         w.opts,
		characterSet: w.characterSet,
	}
}

func (w *Writer) childForDataSet(out io.Writer, dataSet core.DataSet) (*Writer, error) {
	child := w.child(out)
	for _, el := range dataSet.Elements {
		if el.Tag() != tagSpecificCharacterSet {
			continue
		}
		if err := child.useDeclaredCharacterSet(el); err != nil {
			return nil, err
		}
		break
	}
	return child, nil
}

func (w *Writer) childValidated(out io.Writer) *Writer {
	if validationCount, ok := out.(*committedWriter); ok && validationCount == w.validationCount {
		return w.childValidatedWithCounter(out, validationCount)
	}
	return w.childValidatedAt(out, validationWriterPosition(w))
}

func (w *Writer) childValidatedAt(out io.Writer, base int64) *Writer {
	child := w.childValidatedWithCounter(out, &committedWriter{destination: out, base: base})
	postWrites := make([]pendingValidationPostWrite, 0)
	child.validationPostWrites = &postWrites
	return child
}

func (w *Writer) childValidatedWithCounter(out io.Writer, validationCount *committedWriter) *Writer {
	return &Writer{
		w:                    validationCount,
		syntax:               w.syntax,
		enc:                  w.enc,
		opts:                 w.opts,
		characterSet:         w.characterSet,
		validationWrite:      w.validationWrite,
		validationPath:       w.validationPath.Clone(),
		validationCount:      validationCount,
		validationPostWrites: w.validationPostWrites,
		validationPostWrite:  w.validationPostWrite,
	}
}

func (w *Writer) pendingValidationPostWrites() []pendingValidationPostWrite {
	if w == nil || w.validationPostWrites == nil {
		return nil
	}
	return *w.validationPostWrites
}

func (w *Writer) commitValidationPostWrites(postWrites []pendingValidationPostWrite) error {
	if len(postWrites) == 0 {
		return nil
	}
	if w.validationPostWrites != nil {
		*w.validationPostWrites = append(*w.validationPostWrites, postWrites...)
		return nil
	}
	if w.validationPostWrite != nil {
		return w.validationPostWrite(postWrites)
	}
	return nil
}

func (w *Writer) childForDataSetValidated(out io.Writer, dataSet core.DataSet) (*Writer, error) {
	child := w.childValidated(out)
	return child.useDataSetCharacterSet(dataSet)
}

func (w *Writer) childForDataSetValidatedAt(out io.Writer, dataSet core.DataSet, base int64) (*Writer, error) {
	child := w.childValidatedAt(out, base)
	return child.useDataSetCharacterSet(dataSet)
}

func (w *Writer) useDataSetCharacterSet(dataSet core.DataSet) (*Writer, error) {
	child := w
	for _, el := range dataSet.Elements {
		if el.Tag() != tagSpecificCharacterSet {
			continue
		}
		if err := child.useDeclaredCharacterSet(el); err != nil {
			return nil, err
		}
		break
	}
	return child, nil
}

func encodedHeaderLength(syntax transfer.Syntax, vr core.VR) int64 {
	if syntax.ExplicitVR && vr.UsesLongExplicitLength() {
		return 12
	}
	return 8
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
