package dimse

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	horosIntegrationEnv = "DICOMGO_HOROS_INTEGRATION"

	defaultHorosHost       = "192.168.100.62"
	defaultHorosPort       = "4007"
	defaultHorosAET        = "HOROS"
	defaultHorosCallingAET = "DICOMGO"
	defaultHorosTimeout    = "10s"
)

type horosInteropConfig struct {
	Address    string
	CalledAET  string
	CallingAET string
	Timeout    time.Duration
}

// These are opt-in integration tests that run against a real Horos node.
//
// Enable with:
//
//	DICOMGO_HOROS_INTEGRATION=1 go test ./net/dimse -run '^TestInteropHoros' -count=1 -v
func TestInteropHorosCEcho(t *testing.T) {
	skipUnlessHorosIntegration(t)
	cfg := loadHorosInteropConfig(t)

	result, err := VerifyCEcho(context.Background(), CEchoVerificationOptions{
		Address:            cfg.Address,
		CalledAETitle:      cfg.CalledAET,
		CallingAETitle:     cfg.CallingAET,
		TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		DialTimeout:        cfg.Timeout,
		ReleaseTimeout:     cfg.Timeout,
		MessageID:          1,
	})
	if err != nil {
		t.Fatalf("Horos C-ECHO failed (address=%s calledAET=%s callingAET=%s): %v", cfg.Address, cfg.CalledAET, cfg.CallingAET, err)
	}
	t.Logf("Horos C-ECHO status=0x%04X pc={id=%d abstract=%s transfer=%s} duration=%s",
		result.Status,
		result.PresentationContext.ID,
		result.PresentationContext.AbstractSyntaxUID,
		result.PresentationContext.TransferSyntaxUID,
		result.Duration,
	)
}

// This opt-in integration test validates the existing Study Root C-FIND SCU path
// against Horos. Empty results are acceptable; protocol, negotiation, and DIMSE
// status failures are not.
func TestInteropHorosCFindStudyRoot(t *testing.T) {
	skipUnlessHorosIntegration(t)
	cfg := loadHorosInteropConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	proposedContext := StudyRootFindPresentationContext()
	assoc, err := ul.DialContext(ctx, cfg.Address, ul.DialOptions{
		CalledAETitle:  cfg.CalledAET,
		CallingAETitle: cfg.CallingAET,
		Contexts:       []ul.PresentationContext{proposedContext},
	})
	if err != nil {
		t.Fatalf("Horos C-FIND association negotiation failed (address=%s calledAET=%s callingAET=%s proposedContext={%s}): %v",
			cfg.Address, cfg.CalledAET, cfg.CallingAET, formatProposedPresentationContext(proposedContext), err)
	}
	released := false
	defer func() {
		if !released {
			_ = assoc.Close()
		}
	}()

	client, err := NewFindClientForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("Horos C-FIND presentation context rejected (address=%s acceptedContexts=%s): %v",
			cfg.Address, formatAcceptedContexts(assoc.AcceptedContexts), err)
	}

	identifierElems, err := BuildStudyRootStudyFindKeys(
		map[string]string{"StudyInstanceUID": ""},
		"PatientID",
		"PatientName",
		"StudyDate",
		"AccessionNumber",
		"StudyDescription",
	)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	identifier := object.FromElements(identifierElems, std.Dictionary)

	results, errs := client.FindWithOptions(OperationOptions{
		Context:         ctx,
		ResponseTimeout: cfg.Timeout,
	}, CFindRequest{
		MessageID: 1,
		Priority:  PriorityMedium,
	}, identifier)

	var pendingResponses int
	var finalStatus uint16
	var finalComment string
	sawFinal := false
	for results != nil || errs != nil {
		select {
		case result, ok := <-results:
			if !ok {
				results = nil
				continue
			}
			if result.Response == nil {
				t.Fatalf("Horos C-FIND returned nil response (pc=%d syntax=%s)", client.PresentationCtxID, client.IdentifierSyntax.UID)
			}
			status := result.Status()
			if CategorizeCFindStatus(status) == CFindStatusPending {
				pendingResponses++
				continue
			}
			sawFinal = true
			finalStatus = status
			finalComment = result.ErrorComment()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("Horos C-FIND failed (address=%s calledAET=%s callingAET=%s pc=%d syntax=%s acceptedContexts=%s lastStatus=0x%04X lastComment=%q): %v",
					cfg.Address,
					cfg.CalledAET,
					cfg.CallingAET,
					client.PresentationCtxID,
					client.IdentifierSyntax.UID,
					formatAcceptedContexts(assoc.AcceptedContexts),
					finalStatus,
					finalComment,
					err,
				)
			}
		}
	}
	if !sawFinal {
		t.Fatalf("Horos C-FIND completed without a final response (pc=%d syntax=%s acceptedContexts=%s pendingResponses=%d)",
			client.PresentationCtxID, client.IdentifierSyntax.UID, formatAcceptedContexts(assoc.AcceptedContexts), pendingResponses)
	}
	if finalStatus != StatusSuccess {
		t.Fatalf("Horos C-FIND final status = 0x%04X, want 0x%04X (comment=%q pc=%d syntax=%s)",
			finalStatus, StatusSuccess, finalComment, client.PresentationCtxID, client.IdentifierSyntax.UID)
	}

	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer releaseCancel()
	if err := assoc.Release(releaseCtx); err != nil {
		t.Fatalf("Horos C-FIND release failed (address=%s pc=%d syntax=%s): %v",
			cfg.Address, client.PresentationCtxID, client.IdentifierSyntax.UID, err)
	}
	released = true
	t.Logf("Horos C-FIND finalStatus=0x%04X pendingResponses=%d pc={id=%d abstract=%s transfer=%s}",
		finalStatus,
		pendingResponses,
		client.PresentationCtxID,
		StudyRootFindSOPClassUID,
		client.IdentifierSyntax.UID,
	)
}

func skipUnlessHorosIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv(horosIntegrationEnv) == "" {
		t.Skipf("skipping Horos interop test (set %s=1 to enable)", horosIntegrationEnv)
	}
}

func loadHorosInteropConfig(t *testing.T) horosInteropConfig {
	t.Helper()

	timeoutText := horosEnvDefault("HOROS_TIMEOUT", defaultHorosTimeout)
	timeout, err := time.ParseDuration(timeoutText)
	if err != nil {
		t.Fatalf("HOROS_TIMEOUT = %q, want Go duration such as %q: %v", timeoutText, defaultHorosTimeout, err)
	}
	if timeout <= 0 {
		t.Fatalf("HOROS_TIMEOUT = %q, want positive duration", timeoutText)
	}

	return horosInteropConfig{
		Address:    net.JoinHostPort(horosEnvDefault("HOROS_HOST", defaultHorosHost), horosEnvDefault("HOROS_PORT", defaultHorosPort)),
		CalledAET:  horosEnvDefault("HOROS_AET", defaultHorosAET),
		CallingAET: horosEnvDefault("HOROS_CALLING_AET", defaultHorosCallingAET),
		Timeout:    timeout,
	}
}

func horosEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func formatProposedPresentationContext(pc ul.PresentationContext) string {
	return fmt.Sprintf("abstract=%s transferSyntaxes=%v", pc.AbstractSyntaxUID, pc.TransferSyntaxUIDs)
}

func formatAcceptedContexts(contexts []ul.AcceptedContext) string {
	if len(contexts) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(contexts))
	for _, pc := range contexts {
		parts = append(parts, fmt.Sprintf("id=%d abstract=%s transfer=%s", pc.ID, pc.AbstractSyntaxUID, pc.TransferSyntaxUID))
	}
	return strings.Join(parts, "; ")
}
