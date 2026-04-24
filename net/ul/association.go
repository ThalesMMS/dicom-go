package ul

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"time"
)

var (
	ErrAssociationRejected            = errors.New("dicom ul: association rejected")
	ErrAssociationAborted             = errors.New("dicom ul: association aborted")
	ErrUnexpectedPDU                  = errors.New("dicom ul: unexpected PDU")
	ErrAssociationTimeout             = errors.New("dicom ul: association timeout")
	ErrNoAcceptedPresentationContexts = errors.New("dicom ul: no accepted presentation contexts")
)

type RejectionError struct {
	Result byte
	Source byte
	Reason byte
}

func (e *RejectionError) Error() string {
	if e == nil {
		return ErrAssociationRejected.Error()
	}
	return fmt.Sprintf("%s: result=%d source=%d reason=%d", ErrAssociationRejected, e.Result, e.Source, e.Reason)
}

func (e *RejectionError) Unwrap() error {
	return ErrAssociationRejected
}

func (e *RejectionError) PDU() AssociationRJ {
	if e == nil {
		return AssociationRJ{}
	}
	return AssociationRJ{Result: e.Result, Source: e.Source, Reason: e.Reason}
}

type AbortError struct {
	Source byte
	Reason byte
}

func (e *AbortError) Error() string {
	if e == nil {
		return ErrAssociationAborted.Error()
	}
	return fmt.Sprintf("%s: source=%d reason=%d", ErrAssociationAborted, e.Source, e.Reason)
}

func (e *AbortError) Unwrap() error {
	return ErrAssociationAborted
}

type AcceptedContext struct {
	ID                byte
	AbstractSyntaxUID string
	TransferSyntaxUID string
}

type Association struct {
	Conn                   net.Conn
	CalledAETitle          string
	CallingAETitle         string
	Contexts               []PresentationContext
	AcceptedContexts       []AcceptedContext
	PeerMaxPDU             uint32
	MaxPDU                 uint32
	ProtocolVersion        uint16
	ApplicationContextName string
	Context                context.Context
}

type DialOptions struct {
	CalledAETitle             string
	CallingAETitle            string
	MaxPDU                    uint32
	Context                   context.Context
	ImplementationClassUID    string
	ImplementationVersionName string
	ProtocolVersion           uint16
	Contexts                  []PresentationContext
	ApplicationContextName    string
}

type ListenOptions struct {
	Address string
	Context context.Context
}

type AcceptOptions struct {
	AETitle                   string
	MaxPDU                    uint32
	SupportedAbstractSyntaxes []string
	SupportedTransferSyntaxes []string
	AcceptAnyAbstractSyntax   bool
	RequireMatchingCalledAE   bool
	Context                   context.Context
	ImplementationClassUID    string
	ImplementationVersionName string
}

type Listener struct {
	listener net.Listener
}

func Dial(address string, opts DialOptions) (*Association, error) {
	opts = normalizeDialOptions(opts)
	if err := validateDialOptions(opts); err != nil {
		return nil, err
	}

	conn, err := (&net.Dialer{}).DialContext(opts.Context, "tcp", address)
	if err != nil {
		return nil, associationContextError(opts.Context, err)
	}

	assoc, err := negotiateSCU(conn, opts)
	if err != nil {
		_ = writeAssociationPDU(opts.Context, conn, &AbortRQ{Source: AbortSourceServiceUser, Reason: AbortReasonNotSpecified})
		_ = conn.Close()
		return nil, err
	}
	return assoc, nil
}

func DialContext(ctx context.Context, address string, opts DialOptions) (*Association, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts.Context = ctx
	return Dial(address, opts)
}

func Listen(opts ListenOptions) (*Listener, error) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	address := opts.Address
	if address == "" {
		address = "127.0.0.1:0"
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", address)
	if err != nil {
		return nil, associationContextError(ctx, err)
	}
	return &Listener{listener: ln}, nil
}

func (l *Listener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Close()
}

func (l *Listener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *Listener) Accept() (net.Conn, error) {
	if l == nil || l.listener == nil {
		return nil, net.ErrClosed
	}
	return l.listener.Accept()
}

func (l *Listener) AcceptAssociation(opts AcceptOptions) (*Association, error) {
	if l == nil || l.listener == nil {
		return nil, net.ErrClosed
	}
	opts = normalizeAcceptOptions(opts)

	conn, err := acceptWithContext(opts.Context, l.listener)
	if err != nil {
		return nil, associationContextError(opts.Context, err)
	}
	assoc, err := Accept(conn, opts)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return assoc, nil
}

func Accept(conn net.Conn, opts AcceptOptions) (*Association, error) {
	opts = normalizeAcceptOptions(opts)

	pdu, err := readAssociationPDU(opts.Context, conn, opts.MaxPDU)
	if err != nil {
		return nil, err
	}

	rq, ok := pdu.(*AssociationRQ)
	if !ok {
		response := unexpectedAssociationResponse(pdu)
		_ = writeAssociationPDU(opts.Context, conn, response)
		_ = conn.Close()
		return nil, fmt.Errorf("%w: got %s while waiting for A-ASSOCIATE-RQ", ErrUnexpectedPDU, pduName(pdu))
	}

	if rj, ok := rejectAssociationRequest(rq, opts); ok {
		if err := writeAssociationPDU(opts.Context, conn, rj); err != nil {
			return nil, err
		}
		return nil, &RejectionError{Result: rj.Result, Source: rj.Source, Reason: rj.Reason}
	}

	results, accepted := negotiatePresentationContexts(rq.PresentationContexts, opts)
	if len(accepted) == 0 {
		rj := &AssociationRJ{
			Result: AssociateRJResultPermanent,
			Source: AssociateRJSourceServiceUser,
			Reason: AssociateRJReasonNoReasonGiven,
		}
		_ = writeAssociationPDU(opts.Context, conn, rj)
		_ = conn.Close()
		return nil, ErrNoAcceptedPresentationContexts
	}

	ac := &AssociationAC{
		ProtocolVersion:        DefaultProtocolVersion,
		CalledAETitle:          rq.CalledAETitle,
		CallingAETitle:         rq.CallingAETitle,
		ApplicationContextName: ApplicationContextName,
		PresentationContexts:   results,
		UserInfo: implementationUserInfo(
			opts.MaxPDU,
			opts.ImplementationClassUID,
			opts.ImplementationVersionName,
		),
	}
	if err := writeAssociationPDU(opts.Context, conn, ac); err != nil {
		return nil, err
	}

	return &Association{
		Conn:                   conn,
		CalledAETitle:          rq.CalledAETitle,
		CallingAETitle:         rq.CallingAETitle,
		Contexts:               append([]PresentationContext(nil), rq.PresentationContexts...),
		AcceptedContexts:       accepted,
		PeerMaxPDU:             maxPDUFromUserInfo(rq.UserInfo),
		MaxPDU:                 opts.MaxPDU,
		ProtocolVersion:        DefaultProtocolVersion,
		ApplicationContextName: ApplicationContextName,
		Context:                opts.Context,
	}, nil
}

func (a *Association) Send(ctx context.Context, pdu PDU) error {
	if a == nil || a.Conn == nil {
		return net.ErrClosed
	}
	if ctx == nil {
		ctx = a.context()
	}
	return writeAssociationPDU(ctx, a.Conn, pdu)
}

func (a *Association) WritePDU(pdu PDU) error {
	if a == nil || a.Conn == nil {
		return net.ErrClosed
	}
	return writeAssociationPDU(a.context(), a.Conn, pdu)
}

func (a *Association) Receive(ctx context.Context) (PDU, error) {
	if a == nil || a.Conn == nil {
		return nil, net.ErrClosed
	}
	if ctx == nil {
		ctx = a.context()
	}
	pdu, err := readAssociationPDU(ctx, a.Conn, a.MaxPDU)
	if err != nil {
		return nil, err
	}
	if abort, ok := pdu.(*AbortRQ); ok {
		_ = a.Close()
		return nil, &AbortError{Source: abort.Source, Reason: abort.Reason}
	}
	return pdu, nil
}

func (a *Association) ReadPDU() (PDU, error) {
	if a == nil || a.Conn == nil {
		return nil, net.ErrClosed
	}
	pdu, err := readAssociationPDU(a.context(), a.Conn, a.MaxPDU)
	if err != nil {
		return nil, err
	}
	if abort, ok := pdu.(*AbortRQ); ok {
		_ = a.Close()
		return nil, &AbortError{Source: abort.Source, Reason: abort.Reason}
	}
	return pdu, nil
}

func (a *Association) Release(ctx context.Context) error {
	if a == nil || a.Conn == nil {
		return nil
	}
	if ctx == nil {
		ctx = a.context()
	}
	if err := a.Send(ctx, &ReleaseRQ{}); err != nil {
		if errors.Is(err, ErrAssociationTimeout) {
			_ = a.AbortWithContext(context.Background(), AbortReasonNotSpecified)
		}
		return err
	}
	pdu, err := a.Receive(ctx)
	if err != nil {
		if errors.Is(err, ErrAssociationTimeout) {
			_ = a.AbortWithContext(context.Background(), AbortReasonNotSpecified)
		} else {
			var abortErr *AbortError
			if errors.As(err, &abortErr) {
				_ = a.Close()
			}
		}
		return err
	}
	if _, ok := pdu.(*ReleaseRP); !ok {
		_ = a.AbortWithContext(ctx, AbortReasonUnexpectedPDU)
		_ = a.Close()
		return fmt.Errorf("%w: got %s while waiting for A-RELEASE-RP", ErrUnexpectedPDU, pduName(pdu))
	}
	return a.Close()
}

func (a *Association) Abort(reason byte) error {
	return a.AbortWithContext(a.context(), reason)
}

func (a *Association) AbortWithContext(ctx context.Context, reason byte) error {
	if a == nil || a.Conn == nil {
		return nil
	}
	err := a.Send(ctx, &AbortRQ{Source: AbortSourceServiceUser, Reason: reason})
	closeErr := a.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (a *Association) Close() error {
	if a == nil || a.Conn == nil {
		return nil
	}
	conn := a.Conn
	a.Conn = nil
	return conn.Close()
}

func (a *Association) context() context.Context {
	if a == nil || a.Context == nil {
		return context.Background()
	}
	return a.Context
}

func negotiateSCU(conn net.Conn, opts DialOptions) (*Association, error) {
	rq := buildAssociateRQ(opts)
	if err := writeAssociationPDU(opts.Context, conn, &rq); err != nil {
		return nil, err
	}

	pdu, err := readAssociationPDU(opts.Context, conn, opts.MaxPDU)
	if err != nil {
		return nil, err
	}

	switch resp := pdu.(type) {
	case *AssociationAC:
		accepted, err := processAssociationAC(resp, rq.PresentationContexts, opts)
		if err != nil {
			return nil, err
		}
		return &Association{
			Conn:                   conn,
			CalledAETitle:          resp.CalledAETitle,
			CallingAETitle:         resp.CallingAETitle,
			Contexts:               append([]PresentationContext(nil), rq.PresentationContexts...),
			AcceptedContexts:       accepted,
			PeerMaxPDU:             maxPDUFromUserInfo(resp.UserInfo),
			MaxPDU:                 opts.MaxPDU,
			ProtocolVersion:        resp.ProtocolVersion,
			ApplicationContextName: resp.ApplicationContextName,
			Context:                opts.Context,
		}, nil
	case *AssociationRJ:
		return nil, &RejectionError{Result: resp.Result, Source: resp.Source, Reason: resp.Reason}
	case *AbortRQ:
		return nil, &AbortError{Source: resp.Source, Reason: resp.Reason}
	default:
		return nil, fmt.Errorf("%w: got %s while waiting for A-ASSOCIATE-AC", ErrUnexpectedPDU, pduName(pdu))
	}
}

func processAssociationAC(ac *AssociationAC, proposed []PresentationContextProposed, opts DialOptions) ([]AcceptedContext, error) {
	if ac.ProtocolVersion != opts.ProtocolVersion {
		return nil, fmt.Errorf("%w: protocol version %d, want %d", ErrInvalidPDUField, ac.ProtocolVersion, opts.ProtocolVersion)
	}
	if ac.ApplicationContextName != opts.ApplicationContextName {
		return nil, fmt.Errorf("%w: application context %q, want %q", ErrInvalidPDUField, ac.ApplicationContextName, opts.ApplicationContextName)
	}

	proposedByID := make(map[byte]PresentationContextProposed, len(proposed))
	for _, pc := range proposed {
		proposedByID[pc.ID] = pc
	}

	var accepted []AcceptedContext
	for _, result := range ac.PresentationContexts {
		if result.Result != PresentationContextAcceptance {
			continue
		}
		pc, ok := proposedByID[result.ID]
		if !ok {
			return nil, fmt.Errorf("%w: accepted presentation context %d with transfer syntax %q was not proposed", ErrInvalidPDUField, result.ID, result.TransferSyntaxUID)
		}
		if !containsString(pc.TransferSyntaxUIDs, result.TransferSyntaxUID) {
			return nil, fmt.Errorf("%w: accepted transfer syntax %q was not proposed for context %d", ErrInvalidPDUField, result.TransferSyntaxUID, result.ID)
		}
		accepted = append(accepted, AcceptedContext{
			ID:                result.ID,
			AbstractSyntaxUID: pc.AbstractSyntaxUID,
			TransferSyntaxUID: result.TransferSyntaxUID,
		})
	}
	if len(accepted) == 0 {
		return nil, ErrNoAcceptedPresentationContexts
	}
	return accepted, nil
}

func rejectAssociationRequest(rq *AssociationRQ, opts AcceptOptions) (AssociationRJ, bool) {
	if rq.ProtocolVersion != DefaultProtocolVersion {
		return AssociationRJ{
			Result: AssociateRJResultPermanent,
			Source: AssociateRJSourceServiceProviderACSE,
			Reason: AssociateRJReasonProtocolVersionNotSupported,
		}, true
	}
	if rq.ApplicationContextName != ApplicationContextName {
		return AssociationRJ{
			Result: AssociateRJResultPermanent,
			Source: AssociateRJSourceServiceUser,
			Reason: AssociateRJReasonApplicationContextNameNotSupported,
		}, true
	}
	if opts.RequireMatchingCalledAE && rq.CalledAETitle != opts.AETitle {
		return AssociationRJ{
			Result: AssociateRJResultPermanent,
			Source: AssociateRJSourceServiceUser,
			Reason: AssociateRJReasonCalledAETitleNotRecognized,
		}, true
	}
	return AssociationRJ{}, false
}

func negotiatePresentationContexts(requested []PresentationContextProposed, opts AcceptOptions) ([]PresentationContextResult, []AcceptedContext) {
	results := make([]PresentationContextResult, 0, len(requested))
	accepted := make([]AcceptedContext, 0, len(requested))
	for _, pc := range requested {
		transferSyntax := ImplicitVRLittleEndian
		result := PresentationContextAcceptance

		abstractSupported := opts.AcceptAnyAbstractSyntax || containsString(opts.SupportedAbstractSyntaxes, pc.AbstractSyntaxUID)
		if !abstractSupported {
			result = PresentationContextAbstractSyntaxNotSupported
		} else if ts, ok := chooseServerTransferSyntax(pc.TransferSyntaxUIDs, opts.SupportedTransferSyntaxes); ok {
			transferSyntax = ts
		} else {
			result = PresentationContextTransferSyntaxesNotSupported
		}

		results = append(results, PresentationContextResult{
			ID:                pc.ID,
			Result:            result,
			TransferSyntaxUID: transferSyntax,
		})
		if result == PresentationContextAcceptance {
			accepted = append(accepted, AcceptedContext{
				ID:                pc.ID,
				AbstractSyntaxUID: pc.AbstractSyntaxUID,
				TransferSyntaxUID: transferSyntax,
			})
		}
	}
	return results, accepted
}

func chooseServerTransferSyntax(requested, supported []string) (string, bool) {
	if len(requested) == 0 {
		return "", false
	}
	if len(supported) == 0 {
		return requested[0], true
	}
	for _, supportedTS := range supported {
		if containsString(requested, supportedTS) {
			return supportedTS, true
		}
	}
	return "", false
}

func buildAssociateRQ(opts DialOptions) AssociationRQ {
	opts = normalizeDialOptions(opts)
	proposed, _ := proposedContexts(opts.Contexts)
	return AssociationRQ{
		ProtocolVersion:        opts.ProtocolVersion,
		CalledAETitle:          opts.CalledAETitle,
		CallingAETitle:         opts.CallingAETitle,
		ApplicationContextName: opts.ApplicationContextName,
		PresentationContexts:   proposed,
		UserInfo: implementationUserInfo(
			opts.MaxPDU,
			opts.ImplementationClassUID,
			opts.ImplementationVersionName,
		),
	}
}

func validateDialOptions(opts DialOptions) error {
	opts = normalizeDialOptions(opts)
	if len(opts.CalledAETitle) > MaxAETitleLength {
		return fmt.Errorf("%w: called AE title length %d exceeds %d", ErrInvalidAEtitle, len(opts.CalledAETitle), MaxAETitleLength)
	}
	if len(opts.CallingAETitle) > MaxAETitleLength {
		return fmt.Errorf("%w: calling AE title length %d exceeds %d", ErrInvalidAEtitle, len(opts.CallingAETitle), MaxAETitleLength)
	}
	_, err := proposedContexts(opts.Contexts)
	return err
}

func proposedContexts(contexts []PresentationContext) ([]PresentationContextProposed, error) {
	if len(contexts) == 0 {
		return nil, fmt.Errorf("%w: presentation context", ErrMissingPDUField)
	}
	proposed := make([]PresentationContextProposed, len(contexts))
	for i, pc := range contexts {
		pc.ID = byte(2*i + 1)
		if !validPresentationContextID(pc.ID) {
			return nil, fmt.Errorf("%w: %d", ErrInvalidPCID, pc.ID)
		}
		if pc.AbstractSyntaxUID == "" {
			return nil, fmt.Errorf("%w: abstract syntax", ErrMissingPDUField)
		}
		if len(pc.TransferSyntaxUIDs) == 0 {
			return nil, fmt.Errorf("%w: transfer syntax", ErrMissingPDUField)
		}
		proposed[i] = pc
	}
	return proposed, nil
}

func implementationUserInfo(maxPDU uint32, implementationClassUID, implementationVersionName string) []UserVariableItem {
	if implementationClassUID == "" {
		implementationClassUID = ImplementationClassUID
	}
	if implementationVersionName == "" {
		implementationVersionName = ImplementationVersionName
	}
	return []UserVariableItem{
		MaxLengthItem{Value: maxPDU},
		ImplementationClassUIDItem{UID: implementationClassUID},
		ImplementationVersionNameItem{Name: implementationVersionName},
	}
}

func maxPDUFromUserInfo(items []UserVariableItem) uint32 {
	for _, item := range items {
		if maxLength, ok := item.(MaxLengthItem); ok {
			if maxLength.Value == 0 {
				return math.MaxUint32
			}
			return maxLength.Value
		}
		if maxLength, ok := item.(*MaxLengthItem); ok && maxLength != nil {
			if maxLength.Value == 0 {
				return math.MaxUint32
			}
			return maxLength.Value
		}
	}
	return DefaultMaxPDU
}

func normalizeDialOptions(opts DialOptions) DialOptions {
	if opts.CalledAETitle == "" {
		opts.CalledAETitle = "ANY-SCP"
	}
	if opts.CallingAETitle == "" {
		opts.CallingAETitle = "THIS-SCU"
	}
	if opts.ApplicationContextName == "" {
		opts.ApplicationContextName = ApplicationContextName
	}
	if opts.ImplementationClassUID == "" {
		opts.ImplementationClassUID = ImplementationClassUID
	}
	if opts.ImplementationVersionName == "" {
		opts.ImplementationVersionName = ImplementationVersionName
	}
	if opts.ProtocolVersion == 0 {
		opts.ProtocolVersion = DefaultProtocolVersion
	}
	if opts.MaxPDU == 0 {
		opts.MaxPDU = DefaultMaxPDU
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	return opts
}

func normalizeAcceptOptions(opts AcceptOptions) AcceptOptions {
	if opts.AETitle == "" {
		opts.AETitle = "THIS-SCP"
	}
	if opts.MaxPDU == 0 {
		opts.MaxPDU = DefaultMaxPDU
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.ImplementationClassUID == "" {
		opts.ImplementationClassUID = ImplementationClassUID
	}
	if opts.ImplementationVersionName == "" {
		opts.ImplementationVersionName = ImplementationVersionName
	}
	return opts
}

func readAssociationPDU(ctx context.Context, conn net.Conn, maxPDU uint32) (PDU, error) {
	var pdu PDU
	err := withConnDeadline(ctx, conn, func() error {
		var err error
		pdu, err = ReadPDU(conn, maxPDU)
		return err
	})
	if err != nil {
		return nil, err
	}
	return pdu, nil
}

func writeAssociationPDU(ctx context.Context, conn net.Conn, pdu PDU) error {
	return withConnDeadline(ctx, conn, func() error {
		return WritePDU(conn, pdu)
	})
}

func withConnDeadline(ctx context.Context, conn net.Conn, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	if ctx.Done() != nil {
		cancelWatch := make(chan struct{})
		watchDone := make(chan struct{})
		go func() {
			defer close(watchDone)
			select {
			case <-ctx.Done():
				_ = conn.SetDeadline(time.Now())
			case <-cancelWatch:
			}
		}()
		defer func() {
			close(cancelWatch)
			<-watchDone
			_ = conn.SetDeadline(time.Time{})
		}()
	}

	err := fn()
	if ctxErr := ctx.Err(); ctxErr != nil && err != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %v", ErrAssociationTimeout, ctxErr)
		}
		return ctxErr
	}
	return associationContextError(ctx, err)
}

func acceptWithContext(ctx context.Context, ln net.Listener) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if tcp, ok := ln.(*net.TCPListener); ok {
		if deadline, ok := ctx.Deadline(); ok {
			if err := tcp.SetDeadline(deadline); err != nil {
				return nil, err
			}
			defer func() { _ = tcp.SetDeadline(time.Time{}) }()
		}
		if ctx.Done() != nil {
			cancelWatch := make(chan struct{})
			watchDone := make(chan struct{})
			go func() {
				defer close(watchDone)
				select {
				case <-ctx.Done():
					_ = tcp.SetDeadline(time.Now())
				case <-cancelWatch:
				}
			}()
			defer func() {
				close(cancelWatch)
				<-watchDone
				_ = tcp.SetDeadline(time.Time{})
			}()
		}
		conn, err := tcp.Accept()
		if ctxErr := ctx.Err(); ctxErr != nil && err != nil {
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				return nil, fmt.Errorf("%w: %v", ErrAssociationTimeout, ctxErr)
			}
			return nil, ctxErr
		}
		return conn, err
	}

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan acceptResult, 1)
	go func() {
		conn, err := ln.Accept()
		resultCh <- acceptResult{conn: conn, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.conn, result.err
	case <-ctx.Done():
		_ = ln.Close()
		result := <-resultCh
		if result.conn != nil {
			_ = result.conn.Close()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %v", ErrAssociationTimeout, ctx.Err())
		}
		return nil, ctx.Err()
	}
}

func associationContextError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrAssociationTimeout, ctx.Err())
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %v", ErrAssociationTimeout, err)
	}
	return err
}

func unexpectedAssociationResponse(pdu PDU) PDU {
	switch pdu.(type) {
	case *ReleaseRQ:
		return &ReleaseRP{}
	case *UnknownPDU:
		return &AbortRQ{Source: AbortSourceServiceProvider, Reason: AbortReasonUnrecognizedPDU}
	default:
		return &AbortRQ{Source: AbortSourceServiceProvider, Reason: AbortReasonUnexpectedPDU}
	}
}

func pduName(pdu PDU) string {
	if pdu == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", pdu)
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
