package encoding

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"golang.org/x/text/encoding/htmlindex"
)

func TestDefaultCodecDecodeASCII(t *testing.T) {
	codec := DefaultCodec{}

	got, err := codec.Decode([]byte("DOE^JOHN"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got != "DOE^JOHN" {
		t.Fatalf("Decode() = %q, want %q", got, "DOE^JOHN")
	}
}

func TestDefaultCodecDecodeErrorIncludesOffset(t *testing.T) {
	codec := DefaultCodec{}

	_, err := codec.Decode([]byte{'J', 'o', 0xE9})
	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("Decode() error = %v, want *DecodeError", err)
	}
	if decodeErr.Charset != "ISO_IR 6" || decodeErr.Offset != 2 || decodeErr.Byte != 0xE9 {
		t.Fatalf("DecodeError = %+v", decodeErr)
	}
}

func TestDecodeErrorNilMessage(t *testing.T) {
	var decodeErr *DecodeError
	if got := decodeErr.Error(); got != "dicom: decode error" {
		t.Fatalf("nil DecodeError.Error() = %q, want generic decode error", got)
	}
}

func TestLatin1CodecDecodeHighBytes(t *testing.T) {
	codec := Latin1Codec{}

	got, err := codec.Decode([]byte("Jos\xe9"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got != "José" {
		t.Fatalf("Decode() = %q, want %q", got, "José")
	}
}

func TestLatin1CodecDecodeBoundaryBytes(t *testing.T) {
	codec := Latin1Codec{}
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "empty", raw: nil, want: ""},
		{name: "ascii boundary", raw: []byte{0x7F}, want: "\x7f"},
		{name: "latin1 lower boundary", raw: []byte{0x80}, want: "\u0080"},
		{name: "latin1 upper boundary", raw: []byte{0xFF}, want: "ÿ"},
		{name: "mixed", raw: []byte{'A', 0x7F, 0x80, 0xFF}, want: "A\x7f\u0080ÿ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := codec.Decode(tt.raw)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Decode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLatin1CodecDecodeUsesSingleAllocation(t *testing.T) {
	codec := Latin1Codec{}
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "ascii",
			raw:  []byte(strings.Repeat("PATIENT^ASCII", 8)),
			want: strings.Repeat("PATIENT^ASCII", 8),
		},
		{
			name: "mixed high byte",
			raw:  []byte("Jos\xe9 Silva"),
			want: "José Silva",
		},
		{
			name: "all high bytes",
			raw:  []byte(strings.Repeat("\xe9", 16)),
			want: strings.Repeat("é", 16),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLatin1DecodeAllocs(t, 1, codec, tt.raw, tt.want)
		})
	}
}

func BenchmarkLatin1CodecDecode(b *testing.B) {
	codec := Latin1Codec{}
	cases := []struct {
		name string
		raw  []byte
	}{
		{name: "ascii", raw: []byte(strings.Repeat("PATIENT^ASCII", 8))},
		{name: "mixed", raw: []byte("Jos\xe9 Silva\\M\xfcller\\Gar\xe7on")},
		{name: "all_high", raw: []byte(strings.Repeat("\xe9", 64))},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := codec.Decode(tc.raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func assertLatin1DecodeAllocs(t *testing.T, maxAllocs float64, codec Latin1Codec, raw []byte, want string) {
	t.Helper()
	allocs := testing.AllocsPerRun(1000, func() {
		got, err := codec.Decode(raw)
		if err != nil {
			panic(err)
		}
		if got != want {
			panic(got)
		}
	})
	if allocs > maxAllocs {
		t.Fatalf("Decode() allocations/run = %.1f, want <= %.1f", allocs, maxAllocs)
	}
}

func TestParseCharacterSetUnsupportedCharset(t *testing.T) {
	_, err := ParseCharacterSet("UNKNOWN")
	if !errors.Is(err, ErrUnsupportedCharset) {
		t.Fatalf("ParseCharacterSet() error = %v, want ErrUnsupportedCharset", err)
	}
	if err == nil || !strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("ParseCharacterSet() error = %v, want unsupported code in message", err)
	}
}

func TestISOIR13UsesOnlyJISX0201(t *testing.T) {
	charset, err := ParseCharacterSet("ISO_IR 13")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := charset.Encode("表"); !errors.Is(err, ErrUnrepresentableCharacter) {
		t.Fatalf("Encode(Kanji) error = %v, want ErrUnrepresentableCharacter", err)
	}

	want := []byte{0x5c, 0x7e, 0xd4}
	raw, err := charset.EncodeSingleValue("¥‾ﾔ")
	if err != nil {
		t.Fatalf("EncodeSingleValue() error = %v", err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("EncodeSingleValue() = % X, want % X", raw, want)
	}
	decoded, err := charset.DecodeSingleValue(raw)
	if err != nil || decoded != "¥‾ﾔ" {
		t.Fatalf("DecodeSingleValue() = (%q, %v), want (%q, nil)", decoded, err, "¥‾ﾔ")
	}
	decoded, err = charset.Decode([]byte{0x5c})
	if err != nil || decoded != "\\" {
		t.Fatalf("Decode(delimiter) = (%q, %v), want (backslash, nil)", decoded, err)
	}
	if _, err := charset.EncodeSingleValue("\\"); !errors.Is(err, ErrUnrepresentableCharacter) {
		t.Fatalf("EncodeSingleValue(backslash) error = %v, want ErrUnrepresentableCharacter", err)
	}
}

func TestISO2022IR58RejectsGBKOnlyCharacters(t *testing.T) {
	charset, err := ParseCharacterSet("", "ISO 2022 IR 58")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := charset.Encode("ⅰ"); !errors.Is(err, ErrUnrepresentableCharacter) {
		t.Fatalf("Encode(GBK-only character) error = %v, want ErrUnrepresentableCharacter", err)
	}
	if _, err := charset.Decode([]byte("\x1b$)A\xa2\xa1\x1b(B")); !errors.Is(err, ErrInvalidCodeExtension) {
		t.Fatalf("Decode(GBK-only cell) error = %v, want ErrInvalidCodeExtension", err)
	}
}

func TestISO2022DeclarationRejectsDuplicateRepertoires(t *testing.T) {
	_, err := ParseCharacterSet("", "ISO 2022 IR 87", "ISO 2022 IR 87")
	if !errors.Is(err, ErrInvalidCharsetDeclaration) {
		t.Fatalf("ParseCharacterSet() error = %v, want ErrInvalidCharsetDeclaration", err)
	}
}

func TestParseCharacterSetRejectsNonConformantDeclarations(t *testing.T) {
	tests := [][]string{
		{"ISO 2022 IR 87"},
		{"ISO 2022 IR 87", "ISO 2022 IR 6"},
		{"", "ISO_IR 192"},
		{"ISO_IR 100", "ISO 2022 IR 87"},
		{"ISO 2022 IR 6", "GBK"},
		{"ISO 2022 IR 6", ""},
		{"ISO 2022 IR 6", "ISO 2022 IR 87", ""},
	}
	for _, declaration := range tests {
		if _, err := ParseCharacterSet(declaration...); !errors.Is(err, ErrInvalidCharsetDeclaration) {
			t.Errorf("ParseCharacterSet(%q) error = %v, want ErrInvalidCharsetDeclaration", declaration, err)
		}
	}
}

func TestParseCharacterSetEmptyLeading(t *testing.T) {
	_, err := ParseCharacterSet("", "ISO_IR 192")
	if !errors.Is(err, ErrInvalidCharsetDeclaration) {
		t.Fatalf("ParseCharacterSet() error = %v, want ErrInvalidCharsetDeclaration", err)
	}
}

func TestParseCharacterSetNormalization(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		want    string
		wantErr bool
	}{
		{name: "empty means default", code: "", want: "ISO_IR 6"},
		{name: "underscores", code: "ISO_IR_100", want: "ISO_IR 100"},
		{name: "spaces", code: " ISO IR 100 ", want: "ISO_IR 100"},
		{name: "lowercase", code: "gbk", want: "GBK"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCharacterSet(tt.code)
			if tt.wantErr {
				if !errors.Is(err, ErrUnsupportedCharset) {
					t.Fatalf("ParseCharacterSet() error = %v, want ErrUnsupportedCharset", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCharacterSet() error = %v", err)
			}
			if got.Name() != tt.want {
				t.Fatalf("ParseCharacterSet() = %q, want %q", got.Name(), tt.want)
			}
		})
	}
}

func TestSpecificCharacterSetCodecAndFromCode(t *testing.T) {
	charset, err := FromCode("ISO_IR 100")
	if err != nil {
		t.Fatalf("FromCode() error = %v", err)
	}
	if got := charset.Codec().Name(); got != "ISO_IR 100" {
		t.Fatalf("FromCode().Codec().Name() = %q, want ISO_IR 100", got)
	}

	var zero SpecificCharacterSet
	if got := zero.Codec().Name(); got != "ISO_IR 6" {
		t.Fatalf("zero SpecificCharacterSet codec = %q, want ISO_IR 6", got)
	}
	if got := zero.Name(); got != "ISO_IR 6" {
		t.Fatalf("zero SpecificCharacterSet name = %q, want ISO_IR 6", got)
	}
}

func TestParseCharacterSetSupportsStandardMappings(t *testing.T) {
	codes := []string{
		"ISO_IR 6",
		"ISO 2022 IR 6",
		"ISO_IR 13",
		"ISO 2022 IR 13",
		"ISO_IR 100",
		"ISO 2022 IR 100",
		"ISO_IR 101",
		"ISO 2022 IR 101",
		"ISO_IR 109",
		"ISO 2022 IR 109",
		"ISO_IR 110",
		"ISO 2022 IR 110",
		"ISO_IR 126",
		"ISO 2022 IR 126",
		"ISO_IR 127",
		"ISO 2022 IR 127",
		"ISO_IR 138",
		"ISO 2022 IR 138",
		"ISO_IR 144",
		"ISO 2022 IR 144",
		"ISO_IR 148",
		"ISO 2022 IR 148",
		"ISO 2022 IR 149",
		"ISO 2022 IR 159",
		"ISO_IR 166",
		"ISO 2022 IR 166",
		"ISO 2022 IR 87",
		"ISO 2022 IR 58",
		"ISO_IR 192",
		"GB18030",
		"GBK",
	}

	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			declaration := []string{code}
			if strings.HasPrefix(code, "ISO 2022 ") {
				declaration = []string{"", code}
				if code == "ISO 2022 IR 6" {
					declaration = []string{code, "ISO 2022 IR 87"}
				}
			}
			if _, err := ParseCharacterSet(declaration...); err != nil {
				t.Fatalf("ParseCharacterSet(%q) error = %v", code, err)
			}
		})
	}
}

func TestParseCharacterSetDecodesExpandedCharsets(t *testing.T) {
	tests := []struct {
		code  string
		label string
		text  string
		raw   []byte
	}{
		{code: "ISO_IR 192", text: "山田", raw: []byte("山田")},
		{code: "GB18030", label: "gb18030", text: "\U0002000B"},
		{code: "GBK", label: "gbk", text: "中文"},
		{code: "ISO 2022 IR 87", label: "iso-2022-jp", text: "日本"},
		{code: "ISO 2022 IR 159", text: "齩", raw: []byte("\x1b$(DmN\x1b(B")},
		{code: "ISO 2022 IR 149", text: "홍길동", raw: append([]byte("\x1b$)C"), mustEncodeHTMLLabel(t, "euc-kr", "홍길동")...)},
		{code: "ISO_IR 101", label: "iso-8859-2", text: "Łódź"},
		{code: "ISO_IR 148", label: "iso-ir-148", text: "İstanbul"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			raw := tt.raw
			if raw == nil {
				raw = mustEncodeHTMLLabel(t, tt.label, tt.text)
			}
			declaration := []string{tt.code}
			if strings.HasPrefix(tt.code, "ISO 2022 ") {
				declaration = []string{"", tt.code}
			}
			charset, err := ParseCharacterSet(declaration...)
			if err != nil {
				t.Fatalf("ParseCharacterSet(%q) error = %v", tt.code, err)
			}
			got, err := charset.Decode(raw)
			if err != nil {
				t.Fatalf("Decode(%q) error = %v", tt.code, err)
			}
			if got != tt.text {
				t.Fatalf("Decode(%q) = %q, want %q", tt.code, got, tt.text)
			}
		})
	}
}

func TestParseCharacterSetEncodesExpandedCharsets(t *testing.T) {
	tests := []struct {
		code  string
		label string
		text  string
		want  []byte
	}{
		{code: "ISO_IR 192", text: "山田", want: []byte("山田")},
		{code: "GB18030", label: "gb18030", text: "\U0002000B"},
		{code: "GBK", label: "gbk", text: "中文"},
		{code: "ISO 2022 IR 87", label: "iso-2022-jp", text: "日本"},
		{code: "ISO 2022 IR 159", text: "齩", want: []byte("\x1b$(DmN\x1b(B")},
		{code: "ISO 2022 IR 149", text: "홍길동", want: append([]byte("\x1b$)C"), mustEncodeHTMLLabel(t, "euc-kr", "홍길동")...)},
		{code: "ISO 2022 IR 58", text: "中文", want: append([]byte("\x1b$)A"), mustEncodeHTMLLabel(t, "iso-ir-58", "中文")...)},
		{code: "ISO_IR 13", label: "shift_jis", text: "ｶﾀｶﾅ"},
		{code: "ISO_IR 101", label: "iso-8859-2", text: "Łódź"},
		{code: "ISO_IR 109", label: "iso-8859-3", text: "Ħello"},
		{code: "ISO_IR 110", label: "iso-8859-4", text: "Ārsts"},
		{code: "ISO_IR 126", label: "iso-ir-126", text: "Δοκιμή"},
		{code: "ISO_IR 127", label: "iso-ir-127", text: "سلام"},
		{code: "ISO_IR 138", label: "iso-ir-138", text: "שלום"},
		{code: "ISO_IR 144", label: "iso-ir-144", text: "Привет"},
		{code: "ISO_IR 148", label: "iso-ir-148", text: "İstanbul"},
		{code: "ISO_IR 166", label: "iso-8859-11", text: "ไทย"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			want := tt.want
			if want == nil {
				want = mustEncodeHTMLLabel(t, tt.label, tt.text)
			}
			declaration := []string{tt.code}
			if strings.HasPrefix(tt.code, "ISO 2022 ") {
				declaration = []string{"", tt.code}
			}
			charset, err := ParseCharacterSet(declaration...)
			if err != nil {
				t.Fatalf("ParseCharacterSet(%q) error = %v", tt.code, err)
			}
			got, err := charset.Encode(tt.text)
			if err != nil {
				t.Fatalf("Encode(%q) error = %v", tt.code, err)
			}
			if string(got) != string(want) {
				t.Fatalf("Encode(%q) = % X, want % X", tt.code, got, want)
			}
			roundTrip, err := charset.Decode(got)
			if err != nil {
				t.Fatalf("Decode(%q) round-trip error = %v", tt.code, err)
			}
			if roundTrip != tt.text {
				t.Fatalf("Decode(Encode(%q)) = %q, want %q", tt.code, roundTrip, tt.text)
			}
		})
	}
}

func TestSpecificCharacterSetDecodePersonNameUsesISO2022StateMachine(t *testing.T) {
	charset, err := ParseCharacterSet("ISO 2022 IR 6", "ISO 2022 IR 87", "ISO 2022 IR 149")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	raw := []byte("Jose^Silva=\x1b$B;3ED\x1b(B^\x1b$BB@O:\x1b(B=\x1b$)C\xc8\xab^\x1b$)C\xb1\xe6\xb5\xbf")

	got, err := charset.DecodePersonName(raw)
	if err != nil {
		t.Fatalf("DecodePersonName() error = %v", err)
	}
	if got != "Jose^Silva=山田^太郎=홍^길동" {
		t.Fatalf("DecodePersonName() = %q", got)
	}
}

func TestSpecificCharacterSetDecodeValueUsesPrimaryCodec(t *testing.T) {
	charset, err := ParseCharacterSet("", "ISO 2022 IR 149")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}

	got, err := charset.Decode([]byte("ASCII VALUE"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got != "ASCII VALUE" {
		t.Fatalf("Decode() = %q, want ASCII VALUE", got)
	}
}

func TestSpecificCharacterSetEncode(t *testing.T) {
	got, err := DefaultCharacterSet.Encode("DOE^JOHN")
	if err != nil || string(got) != "DOE^JOHN" {
		t.Fatalf("DefaultCharacterSet.Encode() = (%v, %v)", got, err)
	}

	got, err = ISOIR100.Encode("José")
	if err != nil || string(got) != "Jos\xe9" {
		t.Fatalf("ISOIR100.Encode() = (%v, %v)", got, err)
	}
}

func TestSpecificCharacterSetEncodePersonNameUsesComponentGroupCodecs(t *testing.T) {
	tests := []struct {
		name    string
		codes   []string
		input   string
		want    []byte
		wantErr bool
	}{
		{name: "one component group", codes: []string{"ISO_IR 100"}, input: "José^Silva", want: []byte("Jos\xe9^Silva")},
		{name: "encoding error", codes: []string{"ISO_IR 6"}, input: "José^Silva", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			charset, err := ParseCharacterSet(tt.codes...)
			if err != nil {
				t.Fatalf("ParseCharacterSet() error = %v", err)
			}
			encoded, err := charset.EncodePersonName(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("EncodePersonName(%q) error = nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("EncodePersonName() error = %v", err)
			}
			if !bytes.Equal(encoded, tt.want) {
				t.Fatalf("encoded PN = % X, want % X", encoded, tt.want)
			}
			decoded, err := charset.DecodePersonName(encoded)
			if err != nil {
				t.Fatalf("DecodePersonName() error = %v", err)
			}
			if decoded != tt.input {
				t.Fatalf("PN round-trip = %q, want %q", decoded, tt.input)
			}
		})
	}
}

func TestSpecificCharacterSetEncodePersonNamePreservesDecodedComponentGroupBytes(t *testing.T) {
	iso2022 := []byte("Jose^Silva=\x1b$B;3ED\x1b(B^\x1b$BB@O:\x1b(B=\x1b$)C\xc8\xab^\x1b$)C\xb1\xe6\xb5\xbf")

	tests := []struct {
		name  string
		codes []string
		raw   []byte
		want  string
	}{
		{name: "ISO 2022 state machine", codes: []string{"ISO 2022 IR 6", "ISO 2022 IR 87", "ISO 2022 IR 149"}, raw: iso2022, want: "Jose^Silva=山田^太郎=홍^길동"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			charset, err := ParseCharacterSet(tt.codes...)
			if err != nil {
				t.Fatalf("ParseCharacterSet() error = %v", err)
			}
			decoded, err := charset.DecodePersonName(tt.raw)
			if err != nil {
				t.Fatalf("DecodePersonName() error = %v", err)
			}
			if decoded != tt.want {
				t.Fatalf("DecodePersonName() = %q, want %q", decoded, tt.want)
			}
			encoded, err := charset.EncodePersonName(decoded)
			if err != nil {
				t.Fatalf("EncodePersonName() error = %v", err)
			}
			if !bytes.Equal(encoded, tt.raw) {
				t.Fatalf("EncodePersonName(DecodePersonName()) = % X, want % X", encoded, tt.raw)
			}
		})
	}
}

func mustEncodeHTMLLabel(t *testing.T, label string, text string) []byte {
	t.Helper()
	enc, err := htmlindex.Get(label)
	if err != nil {
		t.Fatalf("htmlindex.Get(%q) error = %v", label, err)
	}
	out, err := enc.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatalf("Encode(%q, %q) error = %v", label, text, err)
	}
	return out
}
