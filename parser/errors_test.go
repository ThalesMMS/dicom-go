package parser

import (
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

// TestParseErrorNilReceiverErrorReturnsNilString verifies that a nil
// *ParseError pointer returns "<nil>" from Error(), matching the documented
// nil-safe contract.
func TestParseErrorNilReceiverErrorReturnsNilString(t *testing.T) {
	var e *ParseError
	got := e.Error()
	if got != "<nil>" {
		t.Fatalf("(*ParseError)(nil).Error() = %q, want %q", got, "<nil>")
	}
}

// TestParseErrorNilReceiverUnwrapReturnsNil verifies that Unwrap() on a nil
// *ParseError returns nil without panicking.
func TestParseErrorNilReceiverUnwrapReturnsNil(t *testing.T) {
	var e *ParseError
	if got := e.Unwrap(); got != nil {
		t.Fatalf("(*ParseError)(nil).Unwrap() = %v, want nil", got)
	}
}

// TestParseErrorErrorStringContainsOpAndOffset verifies that a populated
// ParseError embeds both the Op name and the byte offset in its message.
func TestParseErrorErrorStringContainsOpAndOffset(t *testing.T) {
	e := &ParseError{
		Op:     OpReadTag,
		Offset: 42,
	}
	msg := e.Error()
	if !strings.Contains(msg, string(OpReadTag)) {
		t.Fatalf("ParseError message %q missing Op %q", msg, OpReadTag)
	}
	if !strings.Contains(msg, "42") {
		t.Fatalf("ParseError message %q missing offset 42", msg)
	}
}

// TestParseErrorErrorStringFallsBackToParseWhenOpIsEmpty verifies that a zero
// Op value produces "parse" rather than an empty operation label.
func TestParseErrorErrorStringFallsBackToParseWhenOpIsEmpty(t *testing.T) {
	e := &ParseError{}
	msg := e.Error()
	if !strings.Contains(msg, "parse") {
		t.Fatalf("ParseError with empty Op: message %q missing fallback \"parse\"", msg)
	}
}

// TestParseErrorErrorStringIncludesTagAndVR verifies that a ParseError with a
// non-zero tag and VR includes both in the formatted message.
func TestParseErrorErrorStringIncludesTagAndVR(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0010)
	e := &ParseError{
		Op:  OpReadValue,
		Tag: tag,
		VR:  core.VRPN,
	}
	msg := e.Error()
	if !strings.Contains(msg, "(0010,0010)") {
		t.Fatalf("ParseError message %q missing tag (0010,0010)", msg)
	}
	if !strings.Contains(msg, "PN") {
		t.Fatalf("ParseError message %q missing VR PN", msg)
	}
}

// TestParseErrorErrorStringIncludesWrappedError verifies that the underlying
// Err is appended after a colon in the formatted message.
func TestParseErrorErrorStringIncludesWrappedError(t *testing.T) {
	cause := errors.New("unexpected EOF")
	e := &ParseError{
		Op:  OpReadValue,
		Err: cause,
	}
	msg := e.Error()
	if !strings.Contains(msg, "unexpected EOF") {
		t.Fatalf("ParseError message %q missing wrapped error text", msg)
	}
}

// TestParseErrorUnwrapReturnsWrappedError verifies that errors.Is traverses
// the ParseError chain to find the wrapped sentinel.
func TestParseErrorUnwrapReturnsWrappedError(t *testing.T) {
	e := &ParseError{
		Op:  OpReadTag,
		Err: ErrOddElementLength,
	}
	if !errors.Is(e, ErrOddElementLength) {
		t.Fatalf("errors.Is(parseErr, ErrOddElementLength) = false, want true")
	}
}

// TestWriteErrorNilReceiverErrorReturnsNilString verifies that a nil
// *WriteError pointer returns "<nil>" from Error().
func TestWriteErrorNilReceiverErrorReturnsNilString(t *testing.T) {
	var e *WriteError
	got := e.Error()
	if got != "<nil>" {
		t.Fatalf("(*WriteError)(nil).Error() = %q, want %q", got, "<nil>")
	}
}

// TestWriteErrorNilReceiverUnwrapReturnsNil verifies that Unwrap() on a nil
// *WriteError returns nil without panicking.
func TestWriteErrorNilReceiverUnwrapReturnsNil(t *testing.T) {
	var e *WriteError
	if got := e.Unwrap(); got != nil {
		t.Fatalf("(*WriteError)(nil).Unwrap() = %v, want nil", got)
	}
}

// TestWriteErrorErrorStringContainsOpAndTag verifies that a populated
// WriteError embeds both the Op name and the tag in its message.
func TestWriteErrorErrorStringContainsOpAndTag(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	e := &WriteError{
		Op:  OpWriteValue,
		Tag: tag,
	}
	msg := e.Error()
	if !strings.Contains(msg, string(OpWriteValue)) {
		t.Fatalf("WriteError message %q missing Op %q", msg, OpWriteValue)
	}
	if !strings.Contains(msg, "(7FE0,0010)") {
		t.Fatalf("WriteError message %q missing tag (7FE0,0010)", msg)
	}
}

// TestWriteErrorErrorStringFallsBackToWriteWhenOpIsEmpty verifies that a zero
// Op value produces "write" rather than an empty operation label.
func TestWriteErrorErrorStringFallsBackToWriteWhenOpIsEmpty(t *testing.T) {
	e := &WriteError{}
	msg := e.Error()
	if !strings.Contains(msg, "write") {
		t.Fatalf("WriteError with empty Op: message %q missing fallback \"write\"", msg)
	}
}

// TestWriteErrorUnwrapReturnsWrappedError verifies that errors.Is traverses
// the WriteError chain to find the wrapped sentinel.
func TestWriteErrorUnwrapReturnsWrappedError(t *testing.T) {
	e := &WriteError{
		Op:  OpWriteLength,
		Err: ErrOddElementLength,
	}
	if !errors.Is(e, ErrOddElementLength) {
		t.Fatalf("errors.Is(writeErr, ErrOddElementLength) = false, want true")
	}
}

// TestParseErrorLengthIncludedForOpReadValue verifies that the length field
// appears in the message when Op is OpReadValue, even for zero length.
func TestParseErrorLengthIncludedForOpReadValue(t *testing.T) {
	e := &ParseError{
		Op:     OpReadValue,
		Offset: 0,
		Length: 0,
	}
	msg := e.Error()
	if !strings.Contains(msg, "length") {
		t.Fatalf("ParseError with OpReadValue: message %q missing \"length\"", msg)
	}
}

// TestWriteErrorLengthIncludedForOpWriteValue verifies that the length field
// appears in the message when Op is OpWriteValue, even for zero length.
func TestWriteErrorLengthIncludedForOpWriteValue(t *testing.T) {
	e := &WriteError{
		Op:     OpWriteValue,
		Length: 0,
	}
	msg := e.Error()
	if !strings.Contains(msg, "length") {
		t.Fatalf("WriteError with OpWriteValue: message %q missing \"length\"", msg)
	}
}