package transfer

import (
	"errors"
	"fmt"
	"testing"
)

func TestTransferSyntaxErrorsAreDistinct(t *testing.T) {
	if ErrUnknownTransferSyntax == ErrUnsupportedTransferSyntax {
		t.Fatal("expected distinct transfer syntax sentinels")
	}
}

func TestTransferSyntaxErrorsSupportErrorsIs(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
	}{
		{
			name:   "unknown transfer syntax",
			err:    fmt.Errorf("%w: %q", ErrUnknownTransferSyntax, "1.2.3"),
			target: ErrUnknownTransferSyntax,
		},
		{
			name:   "unsupported transfer syntax",
			err:    fmt.Errorf("%w: %q", ErrUnsupportedTransferSyntax, "1.2.3"),
			target: ErrUnsupportedTransferSyntax,
		},
	}

	for _, tt := range tests {
		if !errors.Is(tt.err, tt.target) {
			t.Fatalf("%s: expected wrapped error %v to match %v", tt.name, tt.err, tt.target)
		}
	}
}
