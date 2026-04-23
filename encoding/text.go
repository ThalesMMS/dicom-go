package encoding

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var ErrUnsupportedCharset = errors.New("dicom: unsupported specific character set")

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
			return nil, fmt.Errorf("dicom: encode %s: rune %q is outside the default repertoire", c.Name(), r)
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
	runes := make([]rune, len(text))
	for i, b := range text {
		runes[i] = rune(b)
	}
	return string(runes), nil
}

func (c Latin1Codec) Encode(text string) ([]byte, error) {
	out := make([]byte, 0, len(text))
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("dicom: encode %s: invalid UTF-8 input", c.Name())
		}
		if r > 0xFF {
			return nil, fmt.Errorf("dicom: encode %s: rune %q cannot be represented", c.Name(), r)
		}
		out = append(out, byte(r))
		text = text[size:]
	}
	return out, nil
}

// SpecificCharacterSet wraps a text codec selected from DICOM SpecificCharacterSet codes.
type SpecificCharacterSet struct {
	codec TextCodec
}

func (s SpecificCharacterSet) Name() string {
	return s.effectiveCodec().Name()
}

func (s SpecificCharacterSet) Decode(text []byte) (string, error) {
	return s.effectiveCodec().Decode(text)
}

func (s SpecificCharacterSet) Encode(text string) ([]byte, error) {
	return s.effectiveCodec().Encode(text)
}

func (s SpecificCharacterSet) Codec() TextCodec {
	return s.effectiveCodec()
}

var (
	DefaultCharacterSet = SpecificCharacterSet{codec: DefaultCodec{}}
	ISOIR100            = SpecificCharacterSet{codec: Latin1Codec{}}
)

// ParseCharacterSet resolves a DICOM Specific Character Set code list to a supported codec.
// This safe subset supports only the default repertoire and ISO_IR 100.
func ParseCharacterSet(codes ...string) (SpecificCharacterSet, error) {
	normalized := make([]string, 0, len(codes))
	for _, code := range codes {
		norm := normalizeCharacterSetCode(code)
		if norm == "" {
			continue
		}
		normalized = append(normalized, norm)
	}

	if len(normalized) == 0 {
		return DefaultCharacterSet, nil
	}
	if len(normalized) > 1 {
		return SpecificCharacterSet{}, fmt.Errorf("%w: multiple character sets %q", ErrUnsupportedCharset, strings.Join(normalized, ", "))
	}

	switch normalized[0] {
	case "ISO IR 6":
		return DefaultCharacterSet, nil
	case "ISO IR 100":
		return ISOIR100, nil
	default:
		return SpecificCharacterSet{}, fmt.Errorf("%w: %q", ErrUnsupportedCharset, displayCharacterSetCode(normalized[0]))
	}
}

// FromCode is kept as a compatibility helper for earlier call sites.
func FromCode(code string) (SpecificCharacterSet, error) {
	return ParseCharacterSet(code)
}

func (s SpecificCharacterSet) effectiveCodec() TextCodec {
	if s.codec == nil {
		return DefaultCodec{}
	}
	return s.codec
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
