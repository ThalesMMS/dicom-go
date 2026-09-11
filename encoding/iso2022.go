package encoding

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	textencoding "golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
)

var ErrInvalidCodeExtension = errors.New("dicom: invalid ISO 2022 code extension")

// CodeExtensionError describes an invalid DICOM ISO 2022 sequence or encoded
// character at a byte offset in the original value.
type CodeExtensionError struct {
	Offset   int
	Sequence []byte
	Reason   string
}

func (e *CodeExtensionError) Error() string {
	if e == nil {
		return ErrInvalidCodeExtension.Error()
	}
	reason := e.Reason
	if reason == "" {
		reason = "invalid sequence"
	}
	return fmt.Sprintf("%s at byte %d (% X): %s", ErrInvalidCodeExtension, e.Offset, e.Sequence, reason)
}

func (e *CodeExtensionError) Unwrap() error {
	return ErrInvalidCodeExtension
}

type iso2022Area uint8

const (
	iso2022G0 iso2022Area = iota
	iso2022G1
)

type iso2022Kind uint8

const (
	iso2022ASCII iso2022Kind = iota
	iso2022JISRoman
	iso2022SingleByte
	iso2022JISX0201Katakana
	iso2022JISX0208
	iso2022JISX0212
	iso2022KSX1001
	iso2022GB2312
)

type iso2022Repertoire struct {
	code   string
	escape string
	area   iso2022Area
	kind   iso2022Kind
	codec  TextCodec
}

var (
	iso2022IR6 = &iso2022Repertoire{
		code: "ISO 2022 IR 6", escape: "\x1b(B", area: iso2022G0, kind: iso2022ASCII, codec: DefaultCodec{},
	}
	iso2022IR14 = &iso2022Repertoire{
		code: "ISO 2022 IR 14", escape: "\x1b(J", area: iso2022G0, kind: iso2022JISRoman,
	}
	iso2022Repertoires = map[string]*iso2022Repertoire{
		"ISO 2022 IR 6":   iso2022IR6,
		"ISO 2022 IR 13":  {code: "ISO 2022 IR 13", escape: "\x1b)I", area: iso2022G1, kind: iso2022JISX0201Katakana},
		"ISO 2022 IR 87":  {code: "ISO 2022 IR 87", escape: "\x1b$B", area: iso2022G0, kind: iso2022JISX0208},
		"ISO 2022 IR 100": {code: "ISO 2022 IR 100", escape: "\x1b-A", area: iso2022G1, kind: iso2022SingleByte, codec: Latin1Codec{}},
		"ISO 2022 IR 101": {code: "ISO 2022 IR 101", escape: "\x1b-B", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 109": {code: "ISO 2022 IR 109", escape: "\x1b-C", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 110": {code: "ISO 2022 IR 110", escape: "\x1b-D", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 126": {code: "ISO 2022 IR 126", escape: "\x1b-F", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 127": {code: "ISO 2022 IR 127", escape: "\x1b-G", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 138": {code: "ISO 2022 IR 138", escape: "\x1b-H", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 144": {code: "ISO 2022 IR 144", escape: "\x1b-L", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 148": {code: "ISO 2022 IR 148", escape: "\x1b-M", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 149": {code: "ISO 2022 IR 149", escape: "\x1b$)C", area: iso2022G1, kind: iso2022KSX1001},
		"ISO 2022 IR 159": {code: "ISO 2022 IR 159", escape: "\x1b$(D", area: iso2022G0, kind: iso2022JISX0212},
		"ISO 2022 IR 166": {code: "ISO 2022 IR 166", escape: "\x1b-T", area: iso2022G1, kind: iso2022SingleByte},
		"ISO 2022 IR 58":  {code: "ISO 2022 IR 58", escape: "\x1b$)A", area: iso2022G1, kind: iso2022GB2312},
	}
)

type iso2022CharacterSet struct {
	initialG0 *iso2022Repertoire
	initialG1 *iso2022Repertoire
	allowed   []*iso2022Repertoire
	escapes   map[string]*iso2022Repertoire
}

func buildISO2022CharacterSet(codes []string, codecs []TextCodec) (*iso2022CharacterSet, error) {
	usesCodeExtensions := false
	for _, code := range codes {
		if strings.HasPrefix(code, "ISO 2022 ") {
			usesCodeExtensions = true
			break
		}
	}
	if !usesCodeExtensions {
		return nil, nil
	}

	state := &iso2022CharacterSet{initialG0: iso2022IR6, escapes: make(map[string]*iso2022Repertoire)}
	resolved := make([]*iso2022Repertoire, len(codes))
	seenCodes := make(map[string]int, len(codes))
	for index, code := range codes {
		extensionCode := iso2022Code(code)
		if previous, duplicate := seenCodes[extensionCode]; duplicate {
			return nil, fmt.Errorf("%w: value %d duplicates value %d (%s)", ErrInvalidCharsetDeclaration, index+1, previous+1, displayCharacterSetCode(code))
		}
		seenCodes[extensionCode] = index
		repertoire, ok := iso2022Repertoires[extensionCode]
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a supported DICOM ISO 2022 repertoire", ErrUnsupportedCharset, displayCharacterSetCode(code))
		}
		if repertoire.kind == iso2022SingleByte && repertoire.codec == nil {
			repertoire = cloneISO2022Repertoire(repertoire)
			repertoire.codec = codecs[index]
		}
		resolved[index] = repertoire
	}
	if len(codes) > 0 {
		switch iso2022Code(codes[0]) {
		case "ISO 2022 IR 13":
			state.initialG0 = iso2022IR14
			state.initialG1 = resolved[0]
		case "ISO 2022 IR 6":
			state.initialG0 = iso2022IR6
		default:
			if resolved[0].area == iso2022G1 && resolved[0].kind == iso2022SingleByte {
				state.initialG1 = resolved[0]
			}
		}
	}

	seen := make(map[*iso2022Repertoire]bool)
	add := func(repertoire *iso2022Repertoire) {
		if repertoire == nil || seen[repertoire] {
			return
		}
		seen[repertoire] = true
		state.allowed = append(state.allowed, repertoire)
		state.escapes[repertoire.escape] = repertoire
	}
	add(state.initialG0)
	add(state.initialG1)
	for _, repertoire := range resolved {
		add(repertoire)
	}

	return state, nil
}

func cloneISO2022Repertoire(source *iso2022Repertoire) *iso2022Repertoire {
	clone := *source
	return &clone
}

func iso2022Code(code string) string {
	if strings.HasPrefix(code, "ISO IR ") {
		return strings.Replace(code, "ISO IR ", "ISO 2022 IR ", 1)
	}
	return code
}

func (s *iso2022CharacterSet) decode(text []byte, personName, valueDelimiter bool) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w: missing state machine", ErrInvalidCodeExtension)
	}
	g0, g1 := s.initialG0, s.initialG1
	pnGroup := 0
	var output strings.Builder
	output.Grow(len(text))

	for offset := 0; offset < len(text); {
		if text[offset] == 0x1b {
			if personName && pnGroup == 0 {
				return "", newCodeExtensionError(offset, boundedSequence(text[offset:], 4), "code extension is not permitted in the alphabetic PN component group")
			}
			repertoire, size := s.escapeAt(text[offset:])
			if repertoire == nil {
				return "", newCodeExtensionError(offset, boundedSequence(text[offset:], 4), "unsupported, undeclared, or truncated escape sequence")
			}
			if repertoire.area == iso2022G0 {
				g0 = repertoire
			} else {
				g1 = repertoire
			}
			offset += size
			continue
		}

		value := text[offset]
		if isISO2022ResetByte(value, personName, valueDelimiter) {
			if !s.isInitialState(g0, g1) {
				return "", newCodeExtensionError(offset, []byte{value}, "initial repertoire was not restored before delimiter or control character")
			}
			output.WriteByte(value)
			g0, g1 = s.initialG0, s.initialG1
			if personName && value == '=' {
				pnGroup++
			}
			offset++
			continue
		}
		if value < 0x20 || value == 0x7f || value >= 0x80 && value < 0xa0 {
			return "", newCodeExtensionError(offset, []byte{value}, "control character is not permitted by DICOM text encoding")
		}
		if value == ' ' {
			output.WriteByte(value)
			offset++
			continue
		}

		repertoire := g0
		if value >= 0x80 {
			repertoire = g1
			if repertoire == nil {
				return "", newCodeExtensionError(offset, []byte{value}, "no G1 repertoire is designated")
			}
		}
		encodedSize, err := repertoire.encodedSize(text[offset:])
		if err != nil {
			return "", newCodeExtensionError(offset, boundedSequence(text[offset:], 2), err.Error())
		}
		decoded, err := repertoire.decode(text[offset : offset+encodedSize])
		if err != nil {
			return "", newCodeExtensionError(offset, text[offset:offset+encodedSize], err.Error())
		}
		output.WriteString(decoded)
		offset += encodedSize
	}
	if !s.isInitialState(g0, g1) {
		return "", newCodeExtensionError(len(text), nil, "initial repertoire was not restored before the end of the value")
	}
	return output.String(), nil
}

func (s *iso2022CharacterSet) encode(text string, personName, valueDelimiter bool) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: missing state machine", ErrInvalidCodeExtension)
	}
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("dicom: encode ISO 2022: invalid UTF-8 input")
	}
	g0, g1 := s.initialG0, s.initialG1
	pnGroup := 0
	encoded := make([]byte, 0, len(text))

	for offset, value := range text {
		if value <= 0x7f && isISO2022ResetByte(byte(value), personName, valueDelimiter) {
			encoded = s.appendInitialDesignations(encoded, g0, g1)
			encoded = append(encoded, byte(value))
			g0, g1 = s.initialG0, s.initialG1
			if personName && value == '=' {
				pnGroup++
			}
			continue
		}
		if value == ' ' {
			encoded = append(encoded, ' ')
			continue
		}

		if raw, ok := g0.encodeRune(value); ok {
			encoded = append(encoded, raw...)
			continue
		}
		if g1 != nil {
			if raw, ok := g1.encodeRune(value); ok {
				encoded = append(encoded, raw...)
				continue
			}
		}

		repertoire, raw := s.findRepertoire(value)
		if repertoire == nil {
			return nil, fmt.Errorf("%w: encode ISO 2022 rune %q at UTF-8 byte %d", ErrUnrepresentableCharacter, value, offset)
		}
		if personName && pnGroup == 0 {
			return nil, fmt.Errorf("%w: code extension for rune %q at UTF-8 byte %d is not permitted in the alphabetic PN component group", ErrUnrepresentableCharacter, value, offset)
		}
		if repertoire.area == iso2022G0 {
			if g0 != repertoire {
				encoded = append(encoded, repertoire.escape...)
				g0 = repertoire
			}
		} else if g1 != repertoire {
			encoded = append(encoded, repertoire.escape...)
			g1 = repertoire
		}
		encoded = append(encoded, raw...)
	}
	encoded = s.appendInitialDesignations(encoded, g0, g1)
	return encoded, nil
}

func (s *iso2022CharacterSet) isInitialState(g0, g1 *iso2022Repertoire) bool {
	return g0 == s.initialG0 && (s.initialG1 == nil || g1 == s.initialG1)
}

func (s *iso2022CharacterSet) appendInitialDesignations(encoded []byte, g0, g1 *iso2022Repertoire) []byte {
	if g0 != s.initialG0 {
		encoded = append(encoded, s.initialG0.escape...)
	}
	if s.initialG1 != nil && g1 != s.initialG1 {
		encoded = append(encoded, s.initialG1.escape...)
	}
	return encoded
}

func (s *iso2022CharacterSet) findRepertoire(value rune) (*iso2022Repertoire, []byte) {
	for _, repertoire := range s.allowed {
		if raw, ok := repertoire.encodeRune(value); ok {
			return repertoire, raw
		}
	}
	return nil, nil
}

func (s *iso2022CharacterSet) escapeAt(text []byte) (*iso2022Repertoire, int) {
	for sequence, repertoire := range s.escapes {
		if bytes.HasPrefix(text, []byte(sequence)) {
			return repertoire, len(sequence)
		}
	}
	return nil, 0
}

func (r *iso2022Repertoire) encodedSize(text []byte) (int, error) {
	if len(text) == 0 {
		return 0, errors.New("truncated encoded character")
	}
	value := text[0]
	switch r.kind {
	case iso2022ASCII, iso2022JISRoman:
		if value < 0x20 || value > 0x7e {
			return 0, errors.New("byte is outside the G0 94-character area")
		}
		return 1, nil
	case iso2022SingleByte:
		if value < 0xa0 {
			return 0, errors.New("byte is outside the G1 96-character area")
		}
		return 1, nil
	case iso2022JISX0201Katakana:
		if value < 0xa1 || value > 0xdf {
			return 0, errors.New("byte is outside the JIS X 0201 Katakana area")
		}
		return 1, nil
	case iso2022JISX0208, iso2022JISX0212:
		if len(text) < 2 {
			return 0, errors.New("truncated two-byte G0 character")
		}
		if text[0] < 0x21 || text[0] > 0x7e || text[1] < 0x21 || text[1] > 0x7e {
			return 0, errors.New("bytes are outside the two-byte G0 94-character area")
		}
		return 2, nil
	case iso2022KSX1001, iso2022GB2312:
		if len(text) < 2 {
			return 0, errors.New("truncated two-byte G1 character")
		}
		if text[0] < 0xa1 || text[0] > 0xfe || text[1] < 0xa1 || text[1] > 0xfe {
			return 0, errors.New("bytes are outside the two-byte G1 94-character area")
		}
		if r.kind == iso2022GB2312 && text[0] > 0xf7 {
			return 0, errors.New("bytes are outside the GB 2312 repertoire")
		}
		return 2, nil
	default:
		return 0, errors.New("unknown repertoire")
	}
}

func (r *iso2022Repertoire) decode(raw []byte) (string, error) {
	switch r.kind {
	case iso2022ASCII:
		return string(raw), nil
	case iso2022JISRoman:
		if len(raw) != 1 {
			return "", errors.New("invalid JIS Roman character width")
		}
		switch raw[0] {
		case 0x5c:
			return "¥", nil
		case 0x7e:
			return "‾", nil
		default:
			return string(raw), nil
		}
	case iso2022JISX0201Katakana:
		return decodeISO2022Bytes(japanese.ShiftJIS, raw)
	case iso2022JISX0208:
		return decodeISO2022Bytes(japanese.EUCJP, []byte{raw[0] | 0x80, raw[1] | 0x80})
	case iso2022JISX0212:
		return decodeISO2022Bytes(japanese.EUCJP, []byte{0x8f, raw[0] | 0x80, raw[1] | 0x80})
	case iso2022KSX1001:
		return decodeISO2022Bytes(korean.EUCKR, raw)
	case iso2022GB2312:
		return decodeGB2312(raw)
	default:
		if r.codec == nil {
			return "", errors.New("missing repertoire codec")
		}
		decoded, err := r.codec.Decode(raw)
		if err != nil {
			return "", err
		}
		if strings.ContainsRune(decoded, utf8.RuneError) {
			return "", errors.New("undefined encoded character")
		}
		return decoded, nil
	}
}

func (r *iso2022Repertoire) encodeRune(value rune) ([]byte, bool) {
	switch r.kind {
	case iso2022ASCII:
		if value >= 0x20 && value <= 0x7e {
			return []byte{byte(value)}, true
		}
		return nil, false
	case iso2022JISRoman:
		switch value {
		case '¥':
			return []byte{0x5c}, true
		case '‾':
			return []byte{0x7e}, true
		}
		if value >= 0x20 && value <= 0x7d && value != 0x5c {
			return []byte{byte(value)}, true
		}
		return nil, false
	case iso2022JISX0201Katakana:
		raw, err := encodeISO2022Rune(japanese.ShiftJIS, value)
		if err == nil && len(raw) == 1 && raw[0] >= 0xa1 && raw[0] <= 0xdf {
			return raw, true
		}
		return nil, false
	case iso2022JISX0208, iso2022JISX0212:
		raw, err := encodeISO2022Rune(japanese.EUCJP, value)
		if err != nil {
			return nil, false
		}
		if r.kind == iso2022JISX0208 && len(raw) == 2 && in94ByteArea(raw[0]) && in94ByteArea(raw[1]) {
			return []byte{raw[0] & 0x7f, raw[1] & 0x7f}, true
		}
		if r.kind == iso2022JISX0212 && len(raw) == 3 && raw[0] == 0x8f && in94ByteArea(raw[1]) && in94ByteArea(raw[2]) {
			return []byte{raw[1] & 0x7f, raw[2] & 0x7f}, true
		}
		return nil, false
	case iso2022KSX1001:
		raw, err := encodeISO2022Rune(korean.EUCKR, value)
		if err == nil && len(raw) == 2 && in94ByteArea(raw[0]) && in94ByteArea(raw[1]) {
			return raw, true
		}
		return nil, false
	case iso2022GB2312:
		return encodeGB2312Rune(value)
	default:
		if r.codec == nil {
			return nil, false
		}
		raw, err := r.codec.Encode(string(value))
		if err == nil && len(raw) == 1 && raw[0] >= 0xa0 {
			return raw, true
		}
		return nil, false
	}
}

func decodeISO2022Bytes(codec textencoding.Encoding, raw []byte) (string, error) {
	decoded, err := codec.NewDecoder().Bytes(raw)
	if err != nil {
		return "", err
	}
	text := string(decoded)
	if strings.ContainsRune(text, utf8.RuneError) {
		return "", errors.New("undefined encoded character")
	}
	return text, nil
}

func encodeISO2022Rune(codec textencoding.Encoding, value rune) ([]byte, error) {
	return codec.NewEncoder().Bytes([]byte(string(value)))
}

func decodeGB2312(raw []byte) (string, error) {
	if len(raw) != 2 {
		return "", errors.New("invalid GB 2312 character width")
	}
	if !isGB2312Cell(raw[0], raw[1]) {
		return "", errors.New("undefined GB 2312 character")
	}
	hz := []byte{'~', '{', raw[0] & 0x7f, raw[1] & 0x7f, '~', '}'}
	return decodeISO2022Bytes(simplifiedchinese.HZGB2312, hz)
}

func encodeGB2312Rune(value rune) ([]byte, bool) {
	hz, err := encodeISO2022Rune(simplifiedchinese.HZGB2312, value)
	if err != nil || len(hz) < 4 || hz[0] != '~' || hz[1] != '{' {
		return nil, false
	}
	if len(hz) != 4 && (len(hz) != 6 || hz[4] != '~' || hz[5] != '}') {
		return nil, false
	}
	if hz[2] < 0x21 || hz[2] > 0x77 || hz[3] < 0x21 || hz[3] > 0x7e {
		return nil, false
	}
	first, second := hz[2]|0x80, hz[3]|0x80
	if !isGB2312Cell(first, second) {
		return nil, false
	}
	return []byte{first, second}, true
}

func isGB2312Cell(first, second byte) bool {
	if first < 0xa1 || first > 0xf7 || second < 0xa1 || second > 0xfe {
		return false
	}
	row, column := first-0xa0, second-0xa0
	switch row {
	case 1, 3:
		return true
	case 2:
		return column >= 17 && column <= 66 || column >= 69 && column <= 78 || column >= 81 && column <= 92
	case 4:
		return column <= 83
	case 5:
		return column <= 86
	case 6:
		return column <= 24 || column >= 33 && column <= 56
	case 7:
		return column <= 33 || column >= 49 && column <= 81
	case 8:
		return column <= 26 || column >= 37 && column <= 73
	case 9:
		return column >= 4 && column <= 79
	case 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54:
		return true
	case 55:
		return column <= 89
	default:
		return row >= 56 && row <= 87
	}
}

func in94ByteArea(value byte) bool {
	return value >= 0xa1 && value <= 0xfe
}

func isISO2022ResetByte(value byte, personName, valueDelimiter bool) bool {
	switch value {
	case '\\':
		return valueDelimiter
	case '\t', '\n', '\f', '\r':
		return true
	case '^', '=':
		return personName
	default:
		return false
	}
}

func newCodeExtensionError(offset int, sequence []byte, reason string) error {
	return &CodeExtensionError{Offset: offset, Sequence: append([]byte(nil), sequence...), Reason: reason}
}

func boundedSequence(sequence []byte, maximum int) []byte {
	if len(sequence) > maximum {
		sequence = sequence[:maximum]
	}
	return sequence
}
