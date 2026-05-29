package clidiag

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Class
	}{
		{name: "file", err: fmt.Errorf("open: %w", os.ErrNotExist), want: ClassFile},
		{name: "object parse", err: object.ErrMissingPreamble, want: ClassParse},
		{name: "parser parse", err: &parser.ParseError{Err: io.ErrUnexpectedEOF}, want: ClassParse},
		{name: "transfer", err: fmt.Errorf("read: %w", transfer.ErrUnsupportedTransferSyntax), want: ClassTransfer},
		{name: "codec", err: fmt.Errorf("decode: %w", pixeldata.ErrCodecNotFound), want: ClassCodec},
		{name: "media", err: fmt.Errorf("decode: %w", pixeldata.ErrMediaPayloadNotRenderable), want: ClassMedia},
		{name: "network", err: fmt.Errorf("dial: %w", ul.ErrAssociationRejected), want: ClassNetwork},
		{name: "context deadline", err: context.DeadlineExceeded, want: ClassNetwork},
		{name: "fallback", err: errors.New("plain error"), want: ClassError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Fatalf("Classify(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestFprintln(t *testing.T) {
	var buf bytes.Buffer
	Fprintln(&buf, "dcmdump", object.ErrMissingPreamble)
	want := "dcmdump: parse: dicom: missing Part 10 preamble or DICM marker\n"
	if got := buf.String(); got != want {
		t.Fatalf("Fprintln() = %q, want %q", got, want)
	}
}
