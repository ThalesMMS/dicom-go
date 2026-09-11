package encoding

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	textencoding "golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/japanese"
)

var (
	ErrUnsupportedCharset        = errors.New("dicom: unsupported specific character set")
	ErrUnrepresentableCharacter  = errors.New("dicom: character cannot be represented in the selected character set")
	ErrInvalidCharsetDeclaration = errors.New("dicom: invalid specific character set declaration")
)

// ErrUnsupportedCharacterSet is kept as a compatibility alias for older call sites.
var ErrUnsupportedCharacterSet = ErrUnsupportedCharset

// TextCodec decodes and encodes DICOM text according to a specific character set.
type TextCodec interface {
	Name() string
	Decode([]byte) (string, error)
	Encode(string) ([]byte, error)
}

// DecodeError describes a character-set decoding failure at a specific byte offset.
type DecodeError struct {
	Charset string
	Offset  int
	Byte    byte
}

func (e *DecodeError) Error() string {
	if e == nil {
		return "dicom: decode error"
	}
	return fmt.Sprintf("dicom: decode %s at byte %d: invalid byte 0x%02X", e.Charset, e.Offset, e.Byte)
}

// DefaultCodec implements the DICOM default repertoire (ASCII / ISO 646).
type DefaultCodec struct{}

func (DefaultCodec) Name() string {
	return "ISO_IR 6"
}

func (c DefaultCodec) Decode(text []byte) (string, error) {
	for i, b := range text {
		if b > 0x7F {
			return "", &DecodeError{
				Charset: c.Name(),
				Offset:  i,
				Byte:    b,
			}
		}
	}
	return string(text), nil
}

func (c DefaultCodec) Encode(text string) ([]byte, error) {
	out := make([]byte, 0, len(text))
	for _, r := range text {
		if r > 0x7F {
			return nil, fmt.Errorf("%w: encode %s rune %q outside the default repertoire", ErrUnrepresentableCharacter, c.Name(), r)
		}
		out = append(out, byte(r))
	}
	return out, nil
}

// Latin1Codec implements ISO-8859-1 / ISO_IR 100.
type Latin1Codec struct{}

func (Latin1Codec) Name() string {
	return "ISO_IR 100"
}

func (Latin1Codec) Decode(text []byte) (string, error) {
	highBytes := 0
	for _, b := range text {
		if b >= utf8.RuneSelf {
			highBytes++
		}
	}
	if highBytes == 0 {
		return string(text), nil
	}

	var builder strings.Builder
	builder.Grow(len(text) + highBytes)
	for _, b := range text {
		if b < utf8.RuneSelf {
			builder.WriteByte(b)
			continue
		}
		builder.WriteRune(rune(b))
	}
	return builder.String(), nil
}

func (c Latin1Codec) Encode(text string) ([]byte, error) {
	out := make([]byte, 0, len(text))
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("dicom: encode %s: invalid UTF-8 input", c.Name())
		}
		if r > 0xFF {
			return nil, fmt.Errorf("%w: encode %s rune %q", ErrUnrepresentableCharacter, c.Name(), r)
		}
		out = append(out, byte(r))
		text = text[size:]
	}
	return out, nil
}

// UTF8Codec implements ISO_IR 192.
type UTF8Codec struct{}

func (UTF8Codec) Name() string {
	return "ISO_IR 192"
}

func (c UTF8Codec) Decode(text []byte) (string, error) {
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRune(text[i:])
		if r == utf8.RuneError && size == 1 {
			return "", &DecodeError{
				Charset: c.Name(),
				Offset:  i,
				Byte:    text[i],
			}
		}
		i += size
	}
	return string(text), nil
}

func (c UTF8Codec) Encode(text string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("dicom: encode %s: invalid UTF-8 input", c.Name())
	}
	return []byte(text), nil
}

type xTextCodec struct {
	dicomName string
	encoding  textencoding.Encoding
}

// jisX0201Codec implements the exact DICOM ISO_IR 13 repertoire. Unlike a
// Shift-JIS codec, it cannot silently admit double-byte JIS X 0208 characters.
// DICOM assigns JIS Roman to G0 and half-width Katakana to G1 for this term.
type jisX0201Codec struct {
	dicomName string
}

func (c jisX0201Codec) Name() string { return c.dicomName }

func (c jisX0201Codec) Decode(raw []byte) (string, error) {
	return c.decode(raw, true)
}

func (c jisX0201Codec) DecodeSingleValue(raw []byte) (string, error) {
	return c.decode(raw, false)
}

func (c jisX0201Codec) decode(raw []byte, valueDelimiter bool) (string, error) {
	var output strings.Builder
	output.Grow(len(raw))
	for offset, value := range raw {
		switch {
		case value >= 0x20 && value <= 0x7e:
			switch value {
			case 0x5c:
				if valueDelimiter {
					output.WriteByte(value)
				} else {
					output.WriteRune('¥')
				}
			case 0x7e:
				output.WriteRune('‾')
			default:
				output.WriteByte(value)
			}
		case value >= 0xa1 && value <= 0xdf:
			decoded, err := japanese.ShiftJIS.NewDecoder().Bytes([]byte{value})
			if err != nil || strings.ContainsRune(string(decoded), utf8.RuneError) {
				return "", &DecodeError{Charset: c.Name(), Offset: offset, Byte: value}
			}
			output.Write(decoded)
		default:
			return "", &DecodeError{Charset: c.Name(), Offset: offset, Byte: value}
		}
	}
	return output.String(), nil
}

func (c jisX0201Codec) Encode(text string) ([]byte, error) {
	return c.encode(text, true)
}

func (c jisX0201Codec) EncodeSingleValue(text string) ([]byte, error) {
	return c.encode(text, false)
}

func (c jisX0201Codec) encode(text string, valueDelimiter bool) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("dicom: encode %s: invalid UTF-8 input", c.Name())
	}
	out := make([]byte, 0, len(text))
	for offset, value := range text {
		switch {
		case value == '¥' && !valueDelimiter:
			out = append(out, 0x5c)
		case value == '‾':
			out = append(out, 0x7e)
		case value >= 0x20 && value <= 0x7d && value != '\\' && value != '¥':
			out = append(out, byte(value))
		default:
			raw, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(string(value)))
			if err != nil || len(raw) != 1 || raw[0] < 0xa1 || raw[0] > 0xdf {
				return nil, fmt.Errorf("%w: encode %s rune %q at UTF-8 byte %d", ErrUnrepresentableCharacter, c.Name(), value, offset)
			}
			out = append(out, raw[0])
		}
	}
	return out, nil
}

func (c xTextCodec) Name() string {
	return c.dicomName
}

func (c xTextCodec) Decode(text []byte) (string, error) {
	if c.encoding == nil {
		return string(text), nil
	}
	out, err := c.encoding.NewDecoder().Bytes(text)
	if err != nil {
		return "", fmt.Errorf("dicom: decode %s: %w", c.Name(), err)
	}
	return string(out), nil
}

func (c xTextCodec) Encode(text string) ([]byte, error) {
	if c.encoding == nil {
		return []byte(text), nil
	}
	out, err := c.encoding.NewEncoder().Bytes([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("%w: encode %s: %v", ErrUnrepresentableCharacter, c.Name(), err)
	}
	return out, nil
}

// SpecificCharacterSet wraps one or more codecs selected from DICOM
// SpecificCharacterSet codes. For PN values, the codecs are applied to the
// alphabetic, ideographic, and phonetic component groups using a deterministic
// 1/2/3-code fallback rule.
type SpecificCharacterSet struct {
	codecs  []TextCodec
	iso2022 *iso2022CharacterSet
}

func (s SpecificCharacterSet) Name() string {
	return strings.Join(s.Names(), "\\")
}

func (s SpecificCharacterSet) Names() []string {
	codecs := s.configuredCodecs()
	names := make([]string, len(codecs))
	for i := range codecs {
		names[i] = codecs[i].Name()
	}
	return names
}

func (s SpecificCharacterSet) Decode(text []byte) (string, error) {
	if s.iso2022 != nil {
		return s.iso2022.decode(text, false, true)
	}
	return s.valueCodec().Decode(text)
}

func (s SpecificCharacterSet) Encode(text string) ([]byte, error) {
	if s.iso2022 != nil {
		return s.iso2022.encode(text, false, true)
	}
	return s.valueCodec().Encode(text)
}

// DecodeSingleValue decodes a VR whose backslash byte is text rather than a
// value-multiplicity delimiter (ST, LT, UT, and UR).
func (s SpecificCharacterSet) DecodeSingleValue(text []byte) (string, error) {
	if s.iso2022 != nil {
		return s.iso2022.decode(text, false, false)
	}
	if codec, ok := s.valueCodec().(interface{ DecodeSingleValue([]byte) (string, error) }); ok {
		return codec.DecodeSingleValue(text)
	}
	return s.valueCodec().Decode(text)
}

// EncodeSingleValue encodes a VR whose backslash byte is text rather than a
// value-multiplicity delimiter (ST, LT, UT, and UR).
func (s SpecificCharacterSet) EncodeSingleValue(text string) ([]byte, error) {
	if s.iso2022 != nil {
		return s.iso2022.encode(text, false, false)
	}
	if codec, ok := s.valueCodec().(interface{ EncodeSingleValue(string) ([]byte, error) }); ok {
		return codec.EncodeSingleValue(text)
	}
	return s.valueCodec().Encode(text)
}

func (s SpecificCharacterSet) Codec() TextCodec {
	if s.iso2022 != nil {
		return specificCharacterSetCodec{characterSet: s}
	}
	return s.valueCodec()
}

type specificCharacterSetCodec struct {
	characterSet SpecificCharacterSet
}

func (c specificCharacterSetCodec) Name() string {
	return c.characterSet.Name()
}

func (c specificCharacterSetCodec) Decode(text []byte) (string, error) {
	return c.characterSet.Decode(text)
}

func (c specificCharacterSetCodec) Encode(text string) ([]byte, error) {
	return c.characterSet.Encode(text)
}

func (s SpecificCharacterSet) DecodePersonName(text []byte) (string, error) {
	if s.iso2022 != nil {
		return s.iso2022.decode(text, true, true)
	}
	groups := bytes.Split(text, []byte{'='})
	decoded := make([]string, len(groups))
	for i := range groups {
		group, err := s.personNameCodec(i).Decode(groups[i])
		if err != nil {
			return "", fmt.Errorf("dicom: decode PN component group %d with %s: %w", i, s.personNameCodec(i).Name(), err)
		}
		decoded[i] = group
	}
	return strings.Join(decoded, "="), nil
}

// EncodePersonName encodes alphabetic, ideographic, and phonetic component
// groups with their configured Specific Character Set codecs.
func (s SpecificCharacterSet) EncodePersonName(text string) ([]byte, error) {
	if s.iso2022 != nil {
		return s.iso2022.encode(text, true, true)
	}
	groups := strings.Split(text, "=")
	encoded := make([][]byte, len(groups))
	for i := range groups {
		group, err := s.personNameCodec(i).Encode(groups[i])
		if err != nil {
			return nil, fmt.Errorf("dicom: encode PN component group %d with %s: %w", i, s.personNameCodec(i).Name(), err)
		}
		encoded[i] = group
	}
	return bytes.Join(encoded, []byte{'='}), nil
}

var (
	DefaultCharacterSet = SpecificCharacterSet{codecs: []TextCodec{DefaultCodec{}}}
	ISOIR100            = SpecificCharacterSet{codecs: []TextCodec{Latin1Codec{}}}
)

// ParseCharacterSet resolves a DICOM Specific Character Set code list to a supported codec.
func ParseCharacterSet(codes ...string) (SpecificCharacterSet, error) {
	normalized, err := normalizeCharacterSetDeclaration(codes)
	if err != nil {
		return SpecificCharacterSet{}, err
	}
	if len(normalized) == 0 {
		return DefaultCharacterSet, nil
	}
	codecs := make([]TextCodec, 0, len(normalized))
	for _, norm := range normalized {
		codec, err := codecForCharacterSet(norm)
		if err != nil {
			return SpecificCharacterSet{}, err
		}
		codecs = append(codecs, codec)
	}
	iso2022, err := buildISO2022CharacterSet(normalized, codecs)
	if err != nil {
		return SpecificCharacterSet{}, err
	}
	return SpecificCharacterSet{codecs: codecs, iso2022: iso2022}, nil
}

func normalizeCharacterSetDeclaration(codes []string) ([]string, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	normalized := make([]string, len(codes))
	for index, code := range codes {
		normalized[index] = normalizeCharacterSetCode(code)
		if index > 0 && normalized[index] == "" {
			return nil, fmt.Errorf("%w: value %d is empty; only Value 1 may be empty", ErrInvalidCharsetDeclaration, index+1)
		}
	}
	if len(normalized) == 1 {
		if normalized[0] == "" {
			return nil, nil
		}
		if strings.HasPrefix(normalized[0], "ISO 2022 ") {
			return nil, fmt.Errorf("%w: a Code Extension term requires a multi-valued declaration", ErrInvalidCharsetDeclaration)
		}
		return normalized, nil
	}

	if normalized[0] == "" {
		normalized[0] = "ISO 2022 IR 6"
	}
	for index, code := range normalized {
		if !strings.HasPrefix(code, "ISO 2022 ") {
			return nil, fmt.Errorf("%w: value %d %q is not a Code Extension term", ErrInvalidCharsetDeclaration, index+1, displayCharacterSetCode(code))
		}
	}
	if !isISO2022InitialCharacterSet(normalized[0]) {
		return nil, fmt.Errorf("%w: value 1 %q is not an allowed initial repertoire", ErrInvalidCharsetDeclaration, displayCharacterSetCode(normalized[0]))
	}
	return normalized, nil
}

func isISO2022InitialCharacterSet(code string) bool {
	switch code {
	case "ISO 2022 IR 6", "ISO 2022 IR 13", "ISO 2022 IR 100", "ISO 2022 IR 101",
		"ISO 2022 IR 109", "ISO 2022 IR 110", "ISO 2022 IR 126", "ISO 2022 IR 127",
		"ISO 2022 IR 138", "ISO 2022 IR 144", "ISO 2022 IR 148", "ISO 2022 IR 166":
		return true
	default:
		return false
	}
}

// FromCode is kept as a compatibility helper for earlier call sites.
func FromCode(code string) (SpecificCharacterSet, error) {
	return ParseCharacterSet(code)
}

func (s SpecificCharacterSet) configuredCodecs() []TextCodec {
	if len(s.codecs) == 0 {
		return []TextCodec{DefaultCodec{}}
	}
	return s.codecs
}

func (s SpecificCharacterSet) alphabeticCodec() TextCodec {
	return s.configuredCodecs()[0]
}

func (s SpecificCharacterSet) ideographicCodec() TextCodec {
	codecs := s.configuredCodecs()
	if len(codecs) > 1 {
		return codecs[1]
	}
	return codecs[0]
}

func (s SpecificCharacterSet) phoneticCodec() TextCodec {
	codecs := s.configuredCodecs()
	if len(codecs) > 2 {
		return codecs[2]
	}
	if len(codecs) > 1 {
		return codecs[1]
	}
	return codecs[0]
}

func (s SpecificCharacterSet) personNameCodec(group int) TextCodec {
	switch group {
	case 0:
		return s.alphabeticCodec()
	case 1:
		return s.ideographicCodec()
	default:
		return s.phoneticCodec()
	}
}

func (s SpecificCharacterSet) valueCodec() TextCodec {
	return s.alphabeticCodec()
}

var htmlEncodingNames = map[string]string{
	"ISO IR 101":      "iso-8859-2",
	"ISO 2022 IR 101": "iso-8859-2",
	"ISO IR 109":      "iso-8859-3",
	"ISO 2022 IR 109": "iso-8859-3",
	"ISO IR 110":      "iso-8859-4",
	"ISO 2022 IR 110": "iso-8859-4",
	"ISO IR 126":      "iso-ir-126",
	"ISO 2022 IR 126": "iso-ir-126",
	"ISO IR 127":      "iso-ir-127",
	"ISO 2022 IR 127": "iso-ir-127",
	"ISO IR 138":      "iso-ir-138",
	"ISO 2022 IR 138": "iso-ir-138",
	"ISO IR 144":      "iso-ir-144",
	"ISO 2022 IR 144": "iso-ir-144",
	"ISO IR 148":      "iso-ir-148",
	"ISO 2022 IR 148": "iso-ir-148",
	"ISO 2022 IR 149": "euc-kr",
	"ISO 2022 IR 159": "iso-2022-jp",
	"ISO IR 166":      "iso-8859-11",
	"ISO 2022 IR 166": "iso-8859-11",
	"ISO 2022 IR 87":  "iso-2022-jp",
	"ISO 2022 IR 58":  "iso-ir-58",
	"GB18030":         "gb18030",
	"GBK":             "gbk",
}

func codecForCharacterSet(code string) (TextCodec, error) {
	switch code {
	case "ISO IR 6", "ISO 2022 IR 6":
		return DefaultCodec{}, nil
	case "ISO IR 100", "ISO 2022 IR 100":
		return Latin1Codec{}, nil
	case "ISO IR 13", "ISO 2022 IR 13":
		return jisX0201Codec{dicomName: displayCharacterSetCode(code)}, nil
	case "ISO IR 192":
		return UTF8Codec{}, nil
	}

	htmlName, ok := htmlEncodingNames[code]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedCharset, displayCharacterSetCode(code))
	}
	enc, err := htmlindex.Get(htmlName)
	if err != nil {
		return nil, fmt.Errorf("dicom: resolve %s as %s: %w", displayCharacterSetCode(code), htmlName, err)
	}
	return xTextCodec{dicomName: displayCharacterSetCode(code), encoding: enc}, nil
}

func normalizeCharacterSetCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	code = strings.ReplaceAll(code, "_", " ")
	code = strings.ToUpper(code)
	return strings.Join(strings.Fields(code), " ")
}

func displayCharacterSetCode(code string) string {
	if strings.HasPrefix(code, "ISO IR ") {
		return strings.Replace(code, "ISO IR ", "ISO_IR ", 1)
	}
	return code
}
