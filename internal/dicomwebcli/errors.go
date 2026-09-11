package dicomwebcli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

type inputError struct {
	message string
}

func (e *inputError) Error() string { return e.message }

func invalidInput(message string) error { return &inputError{message: message} }

func classifyExit(ctx context.Context, err error) int {
	switch {
	case errors.Is(err, errPartialStore):
		return ExitPartialStore
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return ExitCanceled
	}
	var input *inputError
	if errors.As(err, &input) {
		return ExitInput
	}
	var protocolError *dicomweb.Error
	if !errors.As(err, &protocolError) {
		return ExitFailure
	}
	switch protocolError.Kind {
	case dicomweb.ErrorKindInvalidEndpoint:
		return ExitInput
	case dicomweb.ErrorKindAuthStatus, dicomweb.ErrorKindAuthToken:
		return ExitAuth
	case dicomweb.ErrorKindHTTPStatus:
		return ExitHTTP
	case dicomweb.ErrorKindDecodeResponse:
		return ExitDecode
	default:
		return ExitFailure
	}
}

func writeDiagnostic(w io.Writer, operation string, err error, exit int) {
	if exit == ExitInput {
		var input *inputError
		if errors.As(err, &input) && input.message != "" {
			_, _ = fmt.Fprintf(w, "%s: input error: %s\n", operation, input.message)
			return
		}
	}
	if exit == ExitPartialStore {
		_, _ = fmt.Fprintln(w, "store: partial result")
		return
	}
	if exit == ExitCanceled {
		_, _ = fmt.Fprintf(w, "%s: canceled\n", operation)
		return
	}
	var protocolError *dicomweb.Error
	if errors.As(err, &protocolError) {
		target := dicomweb.SafeURL(protocolError.URL)
		if protocolError.StatusCode != 0 {
			_, _ = fmt.Fprintf(w, "%s: kind=%s endpoint=%s status=%d\n", operation, protocolError.Kind, target, protocolError.StatusCode)
		} else {
			_, _ = fmt.Fprintf(w, "%s: kind=%s endpoint=%s\n", operation, protocolError.Kind, target)
		}
		return
	}
	_, _ = fmt.Fprintf(w, "%s: kind=io_or_request\n", operation)
}
