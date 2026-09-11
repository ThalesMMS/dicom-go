package encoding

import (
	"bytes"
	"errors"
	"testing"
)

func TestISO2022NormativePersonNameFixtures(t *testing.T) {
	tests := []struct {
		name  string
		codes []string
		text  string
		raw   []byte
	}{
		{
			name:  "DICOM PS3.5 H.3.1 Japanese",
			codes: []string{"", "ISO 2022 IR 87"},
			text:  "Yamada^Tarou=山田^太郎=やまだ^たろう",
			raw:   []byte("Yamada^Tarou=\x1b$B;3ED\x1b(B^\x1b$BB@O:\x1b(B=\x1b$B$d$^$@\x1b(B^\x1b$B$?$m$&\x1b(B"),
		},
		{
			name:  "DICOM PS3.5 H.3.2 Japanese with initial IR 13",
			codes: []string{"ISO 2022 IR 13", "ISO 2022 IR 87"},
			text:  "ﾔﾏﾀﾞ^ﾀﾛｳ=山田^太郎=やまだ^たろう",
			raw: []byte("\xd4\xcf\xc0\xde^\xc0\xdb\xb3=\x1b$B;3ED\x1b(J^\x1b$BB@O:\x1b(J=" +
				"\x1b$B$d$^$@\x1b(J^\x1b$B$?$m$&\x1b(J"),
		},
		{
			name:  "DICOM PS3.5 I.2 Korean",
			codes: []string{"", "ISO 2022 IR 149"},
			text:  "Hong^Gildong=洪^吉洞=홍^길동",
			raw: []byte("Hong^Gildong=\x1b$)C\xfb\xf3^\x1b$)C\xd1\xce\xd4\xd7=" +
				"\x1b$)C\xc8\xab^\x1b$)C\xb1\xe6\xb5\xbf"),
		},
		{
			name:  "DICOM PS3.5 K.2 Chinese",
			codes: []string{"", "ISO 2022 IR 58"},
			text:  "Zhang^XiaoDong=张^小东=",
			raw:   []byte("Zhang^XiaoDong=\x1b$)A\xd5\xc5^\x1b$)A\xd0\xa1\xb6\xab="),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			characterSet, err := ParseCharacterSet(test.codes...)
			if err != nil {
				t.Fatalf("ParseCharacterSet() error = %v", err)
			}
			decoded, err := characterSet.DecodePersonName(test.raw)
			if err != nil {
				t.Fatalf("DecodePersonName() error = %v", err)
			}
			if decoded != test.text {
				t.Fatalf("DecodePersonName() = %q, want %q", decoded, test.text)
			}
			encoded, err := characterSet.EncodePersonName(test.text)
			if err != nil {
				t.Fatalf("EncodePersonName() error = %v", err)
			}
			if !bytes.Equal(encoded, test.raw) {
				t.Fatalf("EncodePersonName() = % X, want % X", encoded, test.raw)
			}
		})
	}
}

func TestISO2022IndependentPydicomJapaneseIR159Fixture(t *testing.T) {
	// Golden bytes are from pydicom 3.0.1's independently implemented
	// test_japanese_multi_byte_personname fixture:
	// https://github.com/pydicom/pydicom/blob/v3.0.1/tests/test_charset.py
	// The character 鷗 is from JIS X 0212 (ISO-IR 159), while the surrounding
	// characters use IR 87.
	characterSet, err := ParseCharacterSet("ISO 2022 IR 6", "ISO 2022 IR 87", "ISO 2022 IR 159")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	text := "Mori^Ogai=森^鷗外=もり^おうがい"
	raw := []byte(
		"Mori^Ogai=\x1b$B?9\x1b(B^\x1b$(Dl?\x1b$B30\x1b(B=" +
			"\x1b$B$b$j\x1b(B^\x1b$B$*$&$,$$\x1b(B",
	)

	decoded, err := characterSet.DecodePersonName(raw)
	if err != nil {
		t.Fatalf("DecodePersonName() error = %v", err)
	}
	if decoded != text {
		t.Fatalf("DecodePersonName() = %q, want %q", decoded, text)
	}
	encoded, err := characterSet.EncodePersonName(text)
	if err != nil {
		t.Fatalf("EncodePersonName() error = %v", err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("EncodePersonName() = % X, want % X", encoded, raw)
	}
}

func TestISO2022IndependentPydicomMixedRepertoireFixture(t *testing.T) {
	characterSet, err := ParseCharacterSet("ISO 2022 IR 13", "ISO 2022 IR 87", "ISO 2022 IR 159")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	text := "あaｱア齩"
	// pydicom 3.0.1 emits an explicit IR 13 designation before the Katakana.
	// It is redundant because IR 13 is initially designated in G1, but valid.
	pydicomRaw := []byte("\x1b$B$\"\x1b(Ja\x1b)I\xb1\x1b$B%\"\x1b$(DmN\x1b(J")
	canonicalRaw := []byte("\x1b$B$\"\x1b(Ja\xb1\x1b$B%\"\x1b$(DmN\x1b(J")

	decoded, err := characterSet.Decode(pydicomRaw)
	if err != nil {
		t.Fatalf("Decode(pydicom fixture) error = %v", err)
	}
	if decoded != text {
		t.Fatalf("Decode(pydicom fixture) = %q, want %q", decoded, text)
	}
	encoded, err := characterSet.Encode(text)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if !bytes.Equal(encoded, canonicalRaw) {
		t.Fatalf("Encode() = % X, want canonical % X", encoded, canonicalRaw)
	}
}

func TestISO2022IndependentPydicomSingleByteExtensionFixture(t *testing.T) {
	characterSet, err := ParseCharacterSet("", "ISO 2022 IR 13")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	text := "==ﾔﾏﾀﾞ^ﾀﾛｳ"
	raw := []byte("==\x1b)I\xd4\xcf\xc0\xde^\x1b)I\xc0\xdb\xb3")

	decoded, err := characterSet.DecodePersonName(raw)
	if err != nil {
		t.Fatalf("DecodePersonName() error = %v", err)
	}
	if decoded != text {
		t.Fatalf("DecodePersonName() = %q, want %q", decoded, text)
	}
	encoded, err := characterSet.EncodePersonName(text)
	if err != nil {
		t.Fatalf("EncodePersonName() error = %v", err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("EncodePersonName() = % X, want % X", encoded, raw)
	}
}

func TestISO2022ResetsAtValueAndLineBoundaries(t *testing.T) {
	characterSet, err := ParseCharacterSet("", "ISO 2022 IR 149")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	raw := []byte("\x1b$)C\xc8\xab\\\x1b$)C\xb1\xe6\r\n\x1b$)C\xb5\xbf")

	decoded, err := characterSet.Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded != "홍\\길\r\n동" {
		t.Fatalf("Decode() = %q", decoded)
	}
	encoded, err := characterSet.Encode(decoded)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("Encode() = % X, want % X", encoded, raw)
	}
}

func TestISO2022RestoresNonDefaultInitialG1(t *testing.T) {
	characterSet, err := ParseCharacterSet("ISO 2022 IR 100", "ISO 2022 IR 126")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	text := "Buc=Δ"
	raw := []byte("Buc=\x1b-F\xc4\x1b-A")

	encoded, err := characterSet.EncodePersonName(text)
	if err != nil {
		t.Fatalf("EncodePersonName() error = %v", err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("EncodePersonName() = % X, want % X", encoded, raw)
	}
	decoded, err := characterSet.DecodePersonName(raw)
	if err != nil {
		t.Fatalf("DecodePersonName() error = %v", err)
	}
	if decoded != text {
		t.Fatalf("DecodePersonName() = %q, want %q", decoded, text)
	}
	if _, err := characterSet.DecodePersonName(raw[:len(raw)-3]); !errors.Is(err, ErrInvalidCodeExtension) {
		t.Fatalf("DecodePersonName(missing restore) error = %v, want ErrInvalidCodeExtension", err)
	}
}

func TestISO2022RestoresNonDefaultInitialG1AtEveryBoundary(t *testing.T) {
	characterSet, err := ParseCharacterSet("ISO 2022 IR 100", "ISO 2022 IR 126")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	tests := []struct {
		name       string
		personName bool
		text       string
	}{
		{name: "value and controls", text: "Δ\\Δ\tΔ\rΔ\nΔ\fΔ"},
		{name: "PN delimiters", personName: true, text: "=Δ^Δ=Δ"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				raw []byte
				err error
			)
			if test.personName {
				raw, err = characterSet.EncodePersonName(test.text)
			} else {
				raw, err = characterSet.Encode(test.text)
			}
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			for offset := 0; offset < len(raw); offset++ {
				if !isISO2022ResetByte(raw[offset], test.personName, true) {
					continue
				}
				if offset == 0 {
					continue
				}
				if offset < 3 || !bytes.Equal(raw[offset-3:offset], []byte("\x1b-A")) {
					t.Fatalf("boundary at %d lacks initial G1 restore in % X", offset, raw)
				}
			}
			var decoded string
			if test.personName {
				decoded, err = characterSet.DecodePersonName(raw)
			} else {
				decoded, err = characterSet.Decode(raw)
			}
			if err != nil || decoded != test.text {
				t.Fatalf("Decode() = (%q, %v), want (%q, nil)", decoded, err, test.text)
			}
		})
	}
}

func TestISO2022MalformedInputReportsBoundedOffset(t *testing.T) {
	characterSet, err := ParseCharacterSet("", "ISO 2022 IR 87")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	tests := []struct {
		name   string
		raw    []byte
		offset int
		pn     bool
	}{
		{name: "lone escape", raw: []byte("AB\x1b"), offset: 2},
		{name: "truncated escape", raw: []byte("AB\x1b$("), offset: 2},
		{name: "unknown escape", raw: []byte("AB\x1b%G"), offset: 2},
		{name: "undeclared escape", raw: []byte("AB\x1b$)C"), offset: 2},
		{name: "forbidden locking shift", raw: []byte("AB\x0e"), offset: 2},
		{name: "forbidden C1", raw: []byte("AB\x80"), offset: 2},
		{name: "truncated pair", raw: []byte("\x1b$B!"), offset: 3},
		{name: "undefined JIS X 0208 pair", raw: []byte("\x1b$B~~\x1b(B"), offset: 3},
		{name: "truncated escape before delimiter", raw: []byte("AB\x1b$^"), offset: 2, pn: true},
		{name: "forbidden shift in", raw: []byte("AB\x0f"), offset: 2},
		{name: "forbidden single shift 2", raw: []byte("AB\x8e"), offset: 2},
		{name: "forbidden single shift 3", raw: []byte("AB\x8f"), offset: 2},
		{name: "forbidden G2 designation", raw: []byte("AB\x1b*B"), offset: 2},
		{name: "forbidden G3 designation", raw: []byte("AB\x1b+B"), offset: 2},
		{name: "missing restore before PN delimiter", raw: []byte("=\x1b$B;3^"), offset: 6, pn: true},
		{name: "missing restore at end", raw: []byte("\x1b$B;3"), offset: 5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var decodeErr error
			if test.pn {
				_, decodeErr = characterSet.DecodePersonName(test.raw)
			} else {
				_, decodeErr = characterSet.Decode(test.raw)
			}
			if !errors.Is(decodeErr, ErrInvalidCodeExtension) {
				t.Fatalf("error = %v, want ErrInvalidCodeExtension", decodeErr)
			}
			var detail *CodeExtensionError
			if !errors.As(decodeErr, &detail) {
				t.Fatalf("error = %v, want *CodeExtensionError", decodeErr)
			}
			if detail.Offset != test.offset {
				t.Fatalf("offset = %d, want %d", detail.Offset, test.offset)
			}
			if len(detail.Sequence) > 4 {
				t.Fatalf("sequence length = %d, want <= 4", len(detail.Sequence))
			}
		})
	}
}

func TestISO2022RequiresG1RedesignationAfterPNDelimiter(t *testing.T) {
	characterSet, err := ParseCharacterSet("", "ISO 2022 IR 149")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	_, err = characterSet.DecodePersonName([]byte("=\x1b$)C\xc8\xab^\xb1\xe6"))
	var detail *CodeExtensionError
	if !errors.As(err, &detail) || detail.Offset != 8 {
		t.Fatalf("DecodePersonName() error = %v, want CodeExtensionError at byte 8", err)
	}
}

func TestCodeExtensionErrorNilMessage(t *testing.T) {
	var decodeErr *CodeExtensionError
	if got := decodeErr.Error(); got != ErrInvalidCodeExtension.Error() {
		t.Fatalf("nil CodeExtensionError.Error() = %q", got)
	}
}
