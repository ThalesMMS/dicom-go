package parser

import (
	"errors"
	"testing"
)

func TestFrameSinkFuncNilAndClose(t *testing.T) {
	var sink FrameSinkFunc
	if err := sink.HandleFrame(Frame{Index: 1}); err != nil {
		t.Fatalf("nil FrameSinkFunc.HandleFrame() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("FrameSinkFunc.Close() error = %v", err)
	}

	var got Frame
	sink = func(frame Frame) error {
		got = frame
		return nil
	}
	if err := sink.HandleFrame(Frame{Index: 7, Data: []byte{1, 2}}); err != nil {
		t.Fatalf("FrameSinkFunc.HandleFrame() error = %v", err)
	}
	if got.Index != 7 || len(got.Data) != 2 {
		t.Fatalf("FrameSinkFunc received %+v", got)
	}
}

func TestFrameChannelSinkSendsAndClosesOnce(t *testing.T) {
	ch := make(chan Frame, 1)
	sink := NewFrameChannelSink(ch)

	if err := sink.HandleFrame(Frame{Index: 3, Data: []byte{0xAA}}); err != nil {
		t.Fatalf("HandleFrame() error = %v", err)
	}
	got := <-ch
	if got.Index != 3 || len(got.Data) != 1 || got.Data[0] != 0xAA {
		t.Fatalf("channel received %+v", got)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel remains open after Close()")
	}
}

func TestFrameChannelSinkRejectsNilChannel(t *testing.T) {
	var nilSink *frameChannelSink
	if err := nilSink.HandleFrame(Frame{}); !errors.Is(err, ErrFrameSink) {
		t.Fatalf("nil receiver HandleFrame() error = %v, want ErrFrameSink", err)
	}
	if err := nilSink.Close(); err != nil {
		t.Fatalf("nil receiver Close() error = %v", err)
	}

	sink := NewFrameChannelSink(nil)
	if err := sink.HandleFrame(Frame{}); !errors.Is(err, ErrFrameSink) {
		t.Fatalf("nil channel HandleFrame() error = %v, want ErrFrameSink", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("nil channel Close() error = %v", err)
	}
}

func TestTokenKindString(t *testing.T) {
	tests := []struct {
		kind TokenKind
		want string
	}{
		{TokenElement, "element"},
		{TokenStartSequence, "sequence start"},
		{TokenStartPixelSequence, "pixel sequence start"},
		{TokenEndSequence, "sequence end"},
		{TokenStartItem, "item start"},
		{TokenEndItem, "item end"},
		{TokenKind(255), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Fatalf("%v.String() = %q, want %q", uint8(tt.kind), got, tt.want)
		}
	}
}
