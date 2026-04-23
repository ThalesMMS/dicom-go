package encoding

import (
	"errors"
	"strings"
	"testing"
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

func TestParseCharacterSetUnsupportedCharset(t *testing.T) {
	_, err := ParseCharacterSet("ISO_IR 192")
	if !errors.Is(err, ErrUnsupportedCharset) {
		t.Fatalf("ParseCharacterSet() error = %v, want ErrUnsupportedCharset", err)
	}
	if err == nil || !strings.Contains(err.Error(), "ISO_IR 192") {
		t.Fatalf("ParseCharacterSet() error = %v, want unsupported code in message", err)
	}
}

func TestParseCharacterSetEmptyLeading(t *testing.T) {
	_, err := ParseCharacterSet("", "ISO_IR 192")
	if !errors.Is(err, ErrUnsupportedCharset) {
		t.Fatalf("ParseCharacterSet() error = %v, want ErrUnsupportedCharset", err)
	}
	if err == nil || !strings.Contains(err.Error(), "ISO_IR 192") {
		t.Fatalf("ParseCharacterSet() error = %v, want unsupported code in message", err)
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
		{name: "iso2022 spaces unsupported", code: "ISO 2022 IR 100", wantErr: true},
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
