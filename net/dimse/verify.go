package dimse

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

var (
	ErrCEchoAssociation                   = errors.New("dicom dimse: C-ECHO association")
	ErrCEchoNoAcceptedPresentationContext = errors.New("dicom dimse: C-ECHO verification presentation context not accepted")
	ErrCEchoRequest                       = errors.New("dicom dimse: C-ECHO request")
	ErrCEchoRelease                       = errors.New("dicom dimse: C-ECHO release")
)

// CEchoVerificationOptions configures a one-shot Verification SOP Class C-ECHO.
type CEchoVerificationOptions struct {
	Address            string
	CalledAETitle      string
	CallingAETitle     string
	TransferSyntaxUIDs []string
	TLSConfig          *tls.Config
	DialTimeout        time.Duration
	ReleaseTimeout     time.Duration
	MessageID          uint16
}

// CEchoVerificationResult describes a successful C-ECHO verification exchange.
type CEchoVerificationResult struct {
	Status              uint16
	MessageID           uint16
	PresentationContext ul.AcceptedContext
	// Duration covers association setup, the C-ECHO exchange, and association
	// release. Successful operations shorter than the platform clock resolution
	// are reported as one nanosecond instead of zero.
	Duration  time.Duration
	StartedAt time.Time
}

// VerifyCEcho associates with a Verification SCP, sends one C-ECHO request, and
// releases the association. It returns structured errors for association,
// presentation-context, request, and release failures.
func VerifyCEcho(ctx context.Context, opts CEchoVerificationOptions) (CEchoVerificationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	result := CEchoVerificationResult{StartedAt: started.UTC()}
	if opts.Address == "" {
		return result, fmt.Errorf("%w: address is required", ErrCEchoAssociation)
	}

	messageID := opts.MessageID
	if messageID == 0 {
		messageID = 1
	}
	result.MessageID = messageID

	dialCtx, cancelDial := contextWithOptionalTimeout(ctx, opts.DialTimeout)
	defer cancelDial()
	assoc, err := ul.DialContext(dialCtx, opts.Address, ul.DialOptions{
		CalledAETitle:  opts.CalledAETitle,
		CallingAETitle: opts.CallingAETitle,
		TLSConfig:      opts.TLSConfig,
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  VerificationSOPClassUID,
			TransferSyntaxUIDs: verificationTransferSyntaxes(opts.TransferSyntaxUIDs),
		}},
	})
	if err != nil {
		if errors.Is(err, ul.ErrNoAcceptedPresentationContexts) {
			return result, fmt.Errorf("%w: %w: %w", ErrCEchoAssociation, ErrCEchoNoAcceptedPresentationContext, err)
		}
		return result, fmt.Errorf("%w: associate with %s: %w", ErrCEchoAssociation, opts.Address, err)
	}

	released := false
	defer func() {
		if !released {
			_ = assoc.Close()
		}
	}()

	pc, ok := AcceptedVerificationContext(assoc)
	if !ok {
		return result, fmt.Errorf("%w: %s", ErrCEchoNoAcceptedPresentationContext, opts.Address)
	}
	result.PresentationContext = pc

	response, err := SendCEcho(assoc, pc.ID, messageID)
	if err != nil {
		return result, fmt.Errorf("%w: send to %s: %w", ErrCEchoRequest, opts.Address, err)
	}
	result.Status = response.Status

	releaseCtx, cancelRelease := contextWithOptionalTimeout(ctx, opts.ReleaseTimeout)
	defer cancelRelease()
	if err := assoc.Release(releaseCtx); err != nil {
		return result, fmt.Errorf("%w: release association with %s: %w", ErrCEchoRelease, opts.Address, err)
	}
	released = true
	result.Duration = positiveElapsed(started, time.Now())
	return result, nil
}

func positiveElapsed(started, finished time.Time) time.Duration {
	if elapsed := finished.Sub(started); elapsed > 0 {
		return elapsed
	}
	return time.Nanosecond
}

func verificationTransferSyntaxes(transferSyntaxUIDs []string) []string {
	if len(transferSyntaxUIDs) == 0 {
		return []string{ul.ImplicitVRLittleEndian}
	}
	return append([]string(nil), transferSyntaxUIDs...)
}

func contextWithOptionalTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}
