package ul

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const verificationSOPClassUID = "1.2.840.10008.1.1"

func closeErr(name string, c io.Closer) error {
	if c == nil {
		return nil
	}
	if err := c.Close(); err != nil {
		return fmt.Errorf("%s Close() failed: %w", name, err)
	}
	return nil
}

func cleanupClose(t *testing.T, name string, c io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if err := closeErr(name, c); err != nil {
			t.Errorf("%s", err)
		}
	})
}

type testAddr string

func (a testAddr) Network() string { return string(a) }
func (a testAddr) String() string  { return string(a) }

type deadlineTrackingConn struct {
	readData       *bytes.Reader
	written        bytes.Buffer
	readErr        error
	writeErr       error
	deadlines      []time.Time
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

type concurrentWriteConn struct {
	calls       atomic.Int32
	secondWrite chan struct{}
	signalOnce  sync.Once
	mu          sync.Mutex
	written     bytes.Buffer
}

type dataAndEOFConn struct {
	deadlineTrackingConn
	data []byte
}

func (c *dataAndEOFConn) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.data)
	c.data = c.data[n:]
	return n, io.EOF
}

func newConcurrentWriteConn() *concurrentWriteConn {
	return &concurrentWriteConn{secondWrite: make(chan struct{})}
}

func (c *concurrentWriteConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *concurrentWriteConn) Write(p []byte) (int, error) {
	call := c.calls.Add(1)
	c.mu.Lock()
	n, err := c.written.Write(p)
	c.mu.Unlock()
	if call == 1 {
		select {
		case <-c.secondWrite:
		case <-time.After(20 * time.Millisecond):
		}
	} else if call == 2 {
		c.signalOnce.Do(func() { close(c.secondWrite) })
	}
	return n, err
}
func (c *concurrentWriteConn) Close() error                     { return nil }
func (c *concurrentWriteConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *concurrentWriteConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *concurrentWriteConn) SetDeadline(time.Time) error      { return nil }
func (c *concurrentWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (c *concurrentWriteConn) SetWriteDeadline(time.Time) error { return nil }

func (c *deadlineTrackingConn) Read(p []byte) (int, error) {
	if c.readData != nil {
		return c.readData.Read(p)
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	return 0, io.EOF
}

func (c *deadlineTrackingConn) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.written.Write(p)
}

func (c *deadlineTrackingConn) Close() error         { return nil }
func (c *deadlineTrackingConn) LocalAddr() net.Addr  { return testAddr("local") }
func (c *deadlineTrackingConn) RemoteAddr() net.Addr { return testAddr("remote") }
func (c *deadlineTrackingConn) SetDeadline(t time.Time) error {
	c.deadlines = append(c.deadlines, t)
	return nil
}
func (c *deadlineTrackingConn) SetReadDeadline(t time.Time) error {
	c.readDeadlines = append(c.readDeadlines, t)
	return nil
}
func (c *deadlineTrackingConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadlines = append(c.writeDeadlines, t)
	return nil
}

func pduStream(t *testing.T, pdus ...PDU) *bytes.Reader {
	t.Helper()

	var buf bytes.Buffer
	for _, pdu := range pdus {
		if err := WritePDU(&buf, pdu); err != nil {
			t.Fatalf("WritePDU(%T) error = %v", pdu, err)
		}
	}
	return bytes.NewReader(buf.Bytes())
}

func writtenPDUs(t *testing.T, conn *deadlineTrackingConn) []PDU {
	t.Helper()

	reader := bytes.NewReader(conn.written.Bytes())
	pdus := make([]PDU, 0)
	for reader.Len() > 0 {
		pdu, err := ReadPDU(reader, DefaultMaxPDU)
		if err != nil {
			t.Fatalf("ReadPDU(written) error = %v", err)
		}
		pdus = append(pdus, pdu)
	}
	return pdus
}

func TestReadAssociationPDUUsesReadDeadlineOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	conn := &deadlineTrackingConn{readErr: io.EOF}
	_, err := readAssociationPDU(ctx, conn, DefaultMaxPDU)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("readAssociationPDU() error = %v, want io.EOF", err)
	}
	if len(conn.deadlines) != 0 {
		t.Fatalf("readAssociationPDU called SetDeadline %d times, want 0", len(conn.deadlines))
	}
	if len(conn.writeDeadlines) != 0 {
		t.Fatalf("readAssociationPDU called SetWriteDeadline %d times, want 0", len(conn.writeDeadlines))
	}
	if len(conn.readDeadlines) == 0 {
		t.Fatal("readAssociationPDU did not call SetReadDeadline")
	}
}

func TestAssociationReceiveResumesPartialPDUAfterTimeout(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	assoc := &Association{
		Conn:        server,
		MaxPDU:      DefaultMaxPDU,
		Context:     context.Background(),
		operational: &operationalState{monitoring: true},
	}
	var encoded bytes.Buffer
	if err := WritePDU(&encoded, &ReleaseRQ{}); err != nil {
		t.Fatal(err)
	}
	pduBytes := encoded.Bytes()
	continueWrite := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		if _, err := client.Write(pduBytes[:3]); err != nil {
			writeDone <- err
			return
		}
		<-continueWrite
		_, err := client.Write(pduBytes[3:])
		writeDone <- err
	}()

	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelFirst()
	if _, err := assoc.Receive(firstCtx); !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("first Receive error = %v, want ErrAssociationTimeout", err)
	}
	close(continueWrite)

	secondCtx, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecond()
	pdu, err := assoc.Receive(secondCtx)
	if err != nil {
		t.Fatalf("second Receive error = %v", err)
	}
	if _, ok := pdu.(*ReleaseRQ); !ok {
		t.Fatalf("second Receive PDU = %T, want *ReleaseRQ", pdu)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write partial PDU: %v", err)
	}
	if got := assoc.TelemetrySnapshot().BytesInbound; got != uint64(len(pduBytes)) {
		t.Fatalf("BytesInbound = %d, want exact wire bytes %d", got, len(pduBytes))
	}
}

func TestAssociationReceiveAcceptsFinalBytesReturnedWithEOF(t *testing.T) {
	var encoded bytes.Buffer
	if err := WritePDU(&encoded, &ReleaseRQ{}); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	conn := &dataAndEOFConn{data: append([]byte(nil), encoded.Bytes()...)}
	assoc := &Association{Conn: conn, MaxPDU: DefaultMaxPDU, Context: context.Background()}
	pdu, err := assoc.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if _, ok := pdu.(*ReleaseRQ); !ok {
		t.Fatalf("Receive() PDU = %T, want *ReleaseRQ", pdu)
	}
}

func TestAssociationIdleTimeoutAllowsSlowActivePDUProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverAssoc := make(chan *Association, 1)
	serverErr := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "IDLE_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
			IdleTimeout:               25 * time.Millisecond,
			ReadProgressTimeout:       25 * time.Millisecond,
		})
		if err != nil {
			serverErr <- err
			return
		}
		serverAssoc <- assoc
	}()
	client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "IDLE_SCP",
		CallingAETitle: "IDLE_SCU",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	var accepted *Association
	select {
	case accepted = <-serverAssoc:
	case err := <-serverErr:
		t.Fatalf("AcceptAssociation() error = %v", err)
	}
	defer func() { _ = accepted.Close() }()
	var encoded bytes.Buffer
	if err := WritePDU(&encoded, &ReleaseRQ{}); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		for _, value := range encoded.Bytes() {
			if _, err := client.Conn.Write([]byte{value}); err != nil {
				writeDone <- err
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		writeDone <- nil
	}()
	pdu, err := accepted.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive(slow active PDU) error = %v", err)
	}
	if _, ok := pdu.(*ReleaseRQ); !ok {
		t.Fatalf("Receive(slow active PDU) = %T, want *ReleaseRQ", pdu)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("slow write error = %v", err)
	}

	started := time.Now()
	_, err = accepted.Receive(context.Background())
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("Receive(idle) error = %v, want ErrAssociationTimeout", err)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("idle Receive() duration = %v", elapsed)
	}
}

func TestAsynchronousOperationsWindowNegotiationAndSynchronousFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverAssociation := make(chan *Association, 1)
	serverError := make(chan error, 1)
	go func() {
		association, acceptErr := listener.AcceptAssociation(AcceptOptions{
			AETitle: "ASYNC_SCP", Context: ctx,
			SupportedAbstractSyntaxes:    []string{verificationSOPClassUID},
			SupportedTransferSyntaxes:    []string{ImplicitVRLittleEndian},
			AsynchronousOperationsWindow: &AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 3},
		})
		if acceptErr != nil {
			serverError <- acceptErr
			return
		}
		serverAssociation <- association
	}()
	client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle: "ASYNC_SCP", CallingAETitle: "ASYNC_SCU",
		Contexts:                     []PresentationContext{{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}}},
		AsynchronousOperationsWindow: &AsynchronousOperationsWindow{MaximumInvoked: 4, MaximumPerformed: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var server *Association
	select {
	case server = <-serverAssociation:
	case err := <-serverError:
		t.Fatal(err)
	}
	defer server.Close()

	if got := client.EffectiveAsynchronousOperationsWindow(); got != (AsynchronousOperationsWindow{MaximumInvoked: 3, MaximumPerformed: 2}) {
		t.Fatalf("client window = %#v", got)
	}
	if got := server.EffectiveAsynchronousOperationsWindow(); got != (AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 3}) {
		t.Fatalf("server window = %#v", got)
	}
	if got := (&Association{}).EffectiveAsynchronousOperationsWindow(); got != (AsynchronousOperationsWindow{MaximumInvoked: 1, MaximumPerformed: 1}) {
		t.Fatalf("fallback window = %#v", got)
	}
}

func TestNegotiateAsynchronousOperationsWindowPreservesUnlimitedValues(t *testing.T) {
	request := AsynchronousOperationsWindow{MaximumInvoked: 0, MaximumPerformed: 0}
	acceptor := AsynchronousOperationsWindow{MaximumInvoked: 5, MaximumPerformed: 0}
	response := negotiateAsynchronousOperationsWindow(request, acceptor)
	if response != (AsynchronousOperationsWindow{MaximumInvoked: 0, MaximumPerformed: 5}) {
		t.Fatalf("response window = %#v", response)
	}
}

func TestAsynchronousOperationsWindowRejectsDuplicatesAndExpandedAcceptance(t *testing.T) {
	_, _, err := asynchronousOperationsWindowFromUserInfo([]UserVariableItem{
		AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 3},
		AsynchronousOperationsWindow{MaximumInvoked: 1, MaximumPerformed: 1},
	})
	if !errors.Is(err, ErrInvalidUserItem) {
		t.Fatalf("duplicate error = %v, want ErrInvalidUserItem", err)
	}
	tests := []struct {
		name     string
		accepted AsynchronousOperationsWindow
		offered  AsynchronousOperationsWindow
		wantErr  bool
	}{
		{name: "finite downgrade", accepted: AsynchronousOperationsWindow{2, 3}, offered: AsynchronousOperationsWindow{4, 3}},
		{name: "unlimited offered finite accepted", accepted: AsynchronousOperationsWindow{9, 7}, offered: AsynchronousOperationsWindow{0, 0}},
		{name: "finite expanded", accepted: AsynchronousOperationsWindow{5, 3}, offered: AsynchronousOperationsWindow{4, 3}, wantErr: true},
		{name: "finite expanded to unlimited", accepted: AsynchronousOperationsWindow{0, 3}, offered: AsynchronousOperationsWindow{4, 3}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAcceptedAsynchronousOperationsWindow(test.accepted, test.offered)
			if test.wantErr && !errors.Is(err, ErrInvalidUserItem) {
				t.Fatalf("error = %v, want ErrInvalidUserItem", err)
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAsynchronousOperationsWindowDefaultsToOneWhenEitherSideOmits(t *testing.T) {
	window := &AsynchronousOperationsWindow{MaximumInvoked: 4, MaximumPerformed: 3}
	tests := []struct {
		name   string
		client *AsynchronousOperationsWindow
		server *AsynchronousOperationsWindow
	}{
		{name: "request omitted", server: window},
		{name: "acceptance omitted", client: window},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverResult := make(chan *Association, 1)
			serverError := make(chan error, 1)
			go func() {
				association, err := listener.AcceptAssociation(AcceptOptions{
					Context: ctx, AcceptAnyAbstractSyntax: true,
					SupportedTransferSyntaxes:    []string{ImplicitVRLittleEndian},
					AsynchronousOperationsWindow: test.server,
				})
				if err != nil {
					serverError <- err
					return
				}
				serverResult <- association
			}()
			client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
				Contexts:                     []PresentationContext{{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}}},
				AsynchronousOperationsWindow: test.client,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var server *Association
			select {
			case server = <-serverResult:
			case err := <-serverError:
				t.Fatal(err)
			}
			defer server.Close()
			want := AsynchronousOperationsWindow{MaximumInvoked: 1, MaximumPerformed: 1}
			if got := client.EffectiveAsynchronousOperationsWindow(); got != want || client.AsynchronousOperationsNegotiated {
				t.Fatalf("client window/negotiated = %#v/%t", got, client.AsynchronousOperationsNegotiated)
			}
			if got := server.EffectiveAsynchronousOperationsWindow(); got != want || server.AsynchronousOperationsNegotiated {
				t.Fatalf("server window/negotiated = %#v/%t", got, server.AsynchronousOperationsNegotiated)
			}
		})
	}
}

func TestAssociationActiveTransferDoesNotApplyIdleTimeoutBetweenPDUs(t *testing.T) {
	local, peer := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = peer.Close() }()
	assoc := &Association{
		Conn:        local,
		MaxPDU:      DefaultMaxPDU,
		Context:     context.Background(),
		idleTimeout: 10 * time.Millisecond,
	}
	writeDone := make(chan error, 1)
	go func() {
		first := &PDataTF{Values: []PDataValue{{PresentationContextID: 1, IsCommand: false, Data: []byte{1}}}}
		if err := WritePDU(peer, first); err != nil {
			writeDone <- err
			return
		}
		time.Sleep(30 * time.Millisecond)
		second := &PDataTF{Values: []PDataValue{{PresentationContextID: 1, IsCommand: false, IsLast: true, Data: []byte{2}}}}
		writeDone <- WritePDU(peer, second)
	}()
	ctx, cancel := context.WithTimeout(WithActiveTransfer(context.Background()), time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := assoc.Receive(ctx); err != nil {
			t.Fatalf("Receive(active transfer PDU %d) error = %v", i+1, err)
		}
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("peer WritePDU() error = %v", err)
	}
}

func TestAssociationOptionalReceiveSuppressesIdleUntilFirstByte(t *testing.T) {
	local, peer := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = peer.Close() }()
	assoc := &Association{Conn: local, MaxPDU: DefaultMaxPDU, Context: context.Background(), idleTimeout: 10 * time.Millisecond}
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		writeDone <- WritePDU(peer, &ReleaseRQ{})
	}()
	ctx, cancel := context.WithTimeout(WithoutIdleTimeout(context.Background()), time.Second)
	defer cancel()
	if pdu, err := assoc.Receive(ctx); err != nil {
		t.Fatalf("Receive(optional wait) error = %v", err)
	} else if _, ok := pdu.(*ReleaseRQ); !ok {
		t.Fatalf("Receive(optional wait) PDU = %T, want *ReleaseRQ", pdu)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("peer WritePDU() error = %v", err)
	}
}

func TestAssociationIdleTimeoutPrecedesCommandProgressBeforeFirstByte(t *testing.T) {
	local, peer := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = peer.Close() }()
	assoc := &Association{Conn: local, MaxPDU: DefaultMaxPDU, Context: context.Background(), idleTimeout: 15 * time.Millisecond}
	ctx, cancel := context.WithTimeout(WithReadProgressTimeout(context.Background(), 250*time.Millisecond), time.Second)
	defer cancel()
	started := time.Now()
	_, err := assoc.Receive(ctx)
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("Receive(idle) error = %v, want ErrAssociationTimeout", err)
	}
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("idle receive took %v; command progress timeout replaced idle timeout", elapsed)
	}
}

func TestAssociationReleaseTimeoutOverridesShorterIdleTimeout(t *testing.T) {
	local, peer := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = peer.Close() }()
	assoc := &Association{
		Conn:                local,
		MaxPDU:              DefaultMaxPDU,
		Context:             context.Background(),
		idleTimeout:         10 * time.Millisecond,
		readProgressTimeout: 10 * time.Millisecond,
		releaseTimeout:      200 * time.Millisecond,
	}
	peerDone := make(chan error, 1)
	go func() {
		pdu, err := ReadPDU(peer, DefaultMaxPDU)
		if err != nil {
			peerDone <- err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			peerDone <- fmt.Errorf("peer received %T, want *ReleaseRQ", pdu)
			return
		}
		time.Sleep(30 * time.Millisecond)
		peerDone <- WritePDU(peer, &ReleaseRP{})
	}()
	if err := assoc.Release(context.Background()); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer release error = %v", err)
	}
}

func TestAssociationReleaseAppliesProgressTimeoutAfterFirstReplyByte(t *testing.T) {
	local, peer := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = peer.Close() }()
	assoc := &Association{
		Conn:                local,
		MaxPDU:              DefaultMaxPDU,
		Context:             context.Background(),
		readProgressTimeout: 15 * time.Millisecond,
		releaseTimeout:      200 * time.Millisecond,
	}
	var encoded bytes.Buffer
	if err := WritePDU(&encoded, &ReleaseRP{}); err != nil {
		t.Fatalf("WritePDU(ReleaseRP) error = %v", err)
	}
	replyBytes := encoded.Bytes()
	peerDone := make(chan error, 1)
	go func() {
		pdu, err := ReadPDU(peer, DefaultMaxPDU)
		if err != nil {
			peerDone <- err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			peerDone <- fmt.Errorf("peer received %T, want *ReleaseRQ", pdu)
			return
		}
		if _, err := peer.Write(replyBytes[:1]); err != nil {
			peerDone <- err
			return
		}
		time.Sleep(40 * time.Millisecond)
		peerDone <- nil
	}()
	if err := assoc.Release(context.Background()); !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("Release() error = %v, want ErrAssociationTimeout", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer release error = %v", err)
	}
}

func TestAcceptAssociationNegotiationTimeoutBoundsSilentPeer(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	acceptDone := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:            "TIMEOUT_SCP",
			NegotiationTimeout: 25 * time.Millisecond,
		})
		acceptDone <- err
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	started := time.Now()
	select {
	case err := <-acceptDone:
		if !errors.Is(err, ErrAssociationTimeout) {
			t.Fatalf("AcceptAssociation() error = %v, want ErrAssociationTimeout", err)
		}
		if elapsed := time.Since(started); elapsed < 15*time.Millisecond || elapsed > 250*time.Millisecond {
			t.Fatalf("negotiation elapsed = %v, want configured timeout bound", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("AcceptAssociation() did not time out silent negotiation")
	}
}

func TestAssociationReleaseTimeoutBoundsSilentPeer(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	assoc := &Association{
		Conn:           client,
		Context:        context.Background(),
		releaseTimeout: 25 * time.Millisecond,
	}
	readDone := make(chan error, 1)
	go func() {
		pdu, err := ReadPDU(server, DefaultMaxPDU)
		if err == nil {
			if _, ok := pdu.(*ReleaseRQ); !ok {
				err = fmt.Errorf("received %T, want *ReleaseRQ", pdu)
			}
		}
		readDone <- err
	}()

	started := time.Now()
	err := assoc.Release(context.Background())
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("Release() error = %v, want ErrAssociationTimeout", err)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("Release() elapsed = %v, want configured release timeout bound", elapsed)
	}
	if err := <-readDone; err != nil {
		t.Fatalf("peer read error = %v", err)
	}
}

func TestAssociationRejectionCallbackPanicDoesNotPreventRJ(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	acceptDone := make(chan error, 1)
	go func() {
		_, err := Accept(server, AcceptOptions{
			AETitle:                 "EXPECTED",
			RequireMatchingCalledAE: true,
			OnAssociationRejected: func() {
				panic("legacy callback failure")
			},
		})
		acceptDone <- err
	}()

	rq := buildAssociateRQ(DialOptions{
		CalledAETitle: "WRONG",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err := WritePDU(client, &rq); err != nil {
		t.Fatalf("WritePDU(A-ASSOCIATE-RQ) error = %v", err)
	}
	pdu, err := ReadPDU(client, DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU(A-ASSOCIATE-RJ) error = %v", err)
	}
	if _, ok := pdu.(*AssociationRJ); !ok {
		t.Fatalf("response PDU = %T, want *AssociationRJ", pdu)
	}
	if err := <-acceptDone; !errors.Is(err, ErrAssociationRejected) {
		t.Fatalf("Accept() error = %v, want ErrAssociationRejected", err)
	}
}

func TestAssociationWriteProgressTimeoutAllowsSlowActiveTransfer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	assoc := &Association{
		Conn:                 clientConn,
		Context:              context.Background(),
		writeProgressTimeout: 25 * time.Millisecond,
		operational:          &operationalState{},
	}
	pdu := &PDataTF{Values: []PDataValue{{
		PresentationContextID: 1,
		IsCommand:             false,
		IsLast:                true,
		Data:                  make([]byte, 256*1024),
	}}}

	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 32*1024)
		remaining := int64(PDUHeaderSize + PDVHeaderSize + uint32(len(pdu.Values[0].Data)))
		for remaining > 0 {
			time.Sleep(10 * time.Millisecond)
			want := int64(len(buffer))
			if remaining < want {
				want = remaining
			}
			n, err := io.ReadFull(serverConn, buffer[:want])
			remaining -= int64(n)
			if err != nil {
				readDone <- err
				return
			}
		}
		readDone <- nil
	}()

	started := time.Now()
	if err := assoc.Send(context.Background(), pdu); err != nil {
		t.Fatalf("Send(active transfer) error = %v", err)
	}
	if elapsed := time.Since(started); elapsed <= assoc.writeProgressTimeout {
		t.Fatalf("active transfer elapsed %v, want greater than progress timeout %v", elapsed, assoc.writeProgressTimeout)
	}
	if err := <-readDone; err != nil {
		t.Fatalf("slow reader error = %v", err)
	}

	started = time.Now()
	idleCtx, cancelIdle := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelIdle()
	err := assoc.Send(idleCtx, &ReleaseRQ{})
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("Send(idle peer) error = %v, want ErrAssociationTimeout", err)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("idle write elapsed = %v, want progress timeout bound", elapsed)
	}
}

func TestWriteAssociationPDUUsesWriteDeadlineOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	conn := &deadlineTrackingConn{}
	if err := writeAssociationPDU(ctx, conn, &ReleaseRQ{}); err != nil {
		t.Fatalf("writeAssociationPDU() error = %v", err)
	}
	if len(conn.deadlines) != 0 {
		t.Fatalf("writeAssociationPDU called SetDeadline %d times, want 0", len(conn.deadlines))
	}
	if len(conn.readDeadlines) != 0 {
		t.Fatalf("writeAssociationPDU called SetReadDeadline %d times, want 0", len(conn.readDeadlines))
	}
	if len(conn.writeDeadlines) == 0 {
		t.Fatal("writeAssociationPDU did not call SetWriteDeadline")
	}
}

func TestAssociationSerializesConcurrentPDUWrites(t *testing.T) {
	conn := newConcurrentWriteConn()
	assoc := &Association{Conn: conn, Context: context.Background()}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, pdu := range []PDU{&ReleaseRQ{}, &ReleaseRP{}} {
		pdu := pdu
		go func() {
			<-start
			errs <- assoc.Send(context.Background(), pdu)
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	}

	conn.mu.Lock()
	encoded := append([]byte(nil), conn.written.Bytes()...)
	conn.mu.Unlock()
	reader := bytes.NewReader(encoded)
	for i := 0; i < 2; i++ {
		if _, err := ReadPDU(reader, DefaultMaxPDU); err != nil {
			t.Fatalf("ReadPDU(%d) error = %v; concurrent writes corrupted stream %x", i, err, encoded)
		}
	}
	if reader.Len() != 0 {
		t.Fatalf("encoded stream has %d trailing bytes", reader.Len())
	}
}

func assertUserInfo(t *testing.T, items []UserVariableItem, wantMaxPDU uint32, wantImplementationClassUID, wantImplementationVersionName string) {
	t.Helper()

	var foundMaxPDU, foundImplementationClassUID, foundImplementationVersionName bool
	for _, item := range items {
		switch item := item.(type) {
		case MaxLengthItem:
			foundMaxPDU = true
			if item.Value != wantMaxPDU {
				t.Fatalf("MaxLengthItem.Value = %d, want %d", item.Value, wantMaxPDU)
			}
		case ImplementationClassUIDItem:
			foundImplementationClassUID = true
			if item.UID != wantImplementationClassUID {
				t.Fatalf("ImplementationClassUIDItem.UID = %q, want %q", item.UID, wantImplementationClassUID)
			}
		case ImplementationVersionNameItem:
			foundImplementationVersionName = true
			if item.Name != wantImplementationVersionName {
				t.Fatalf("ImplementationVersionNameItem.Name = %q, want %q", item.Name, wantImplementationVersionName)
			}
		}
	}

	if !foundMaxPDU {
		t.Fatalf("UserInfo missing MaxLengthItem")
	}
	if !foundImplementationClassUID {
		t.Fatalf("UserInfo missing ImplementationClassUIDItem")
	}
	if !foundImplementationVersionName {
		t.Fatalf("UserInfo missing ImplementationVersionNameItem")
	}
}

func testTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("rand.Int() error = %v", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey() error = %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair() error = %v", err)
	}
	parsed, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)

	serverConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	clientConfig := &tls.Config{RootCAs: roots, ServerName: "localhost"}
	return serverConfig, clientConfig
}

func TestAssociationNegotiationAndRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	acceptOpts := AcceptOptions{
		AETitle:                   "SCP_AE",
		RequireMatchingCalledAE:   true,
		MaxPDU:                    8192,
		Context:                   ctx,
		SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
		SupportedTransferSyntaxes: []string{ExplicitVRLittleEndian},
	}

	if listener.Addr() == nil {
		t.Fatal("listener Addr() = nil")
	}

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		assoc, err := listener.AcceptAssociation(acceptOpts)
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()

		if got, want := assoc.CalledAETitle, "SCP_AE"; got != want {
			result = errors.New("server called AE title mismatch")
			return
		}
		if got, want := assoc.CallingAETitle, "SCU_AE"; got != want {
			result = errors.New("server calling AE title mismatch")
			return
		}
		if got, want := assoc.PeerMaxPDU, uint32(4096); got != want {
			result = errors.New("server peer max PDU mismatch")
			return
		}
		if len(assoc.AcceptedContexts) != 1 || assoc.AcceptedContexts[0].TransferSyntaxUID != ExplicitVRLittleEndian {
			result = errors.New("server accepted context mismatch")
			return
		}

		pdu, err := assoc.ReadPDU()
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			result = errors.New("server expected A-RELEASE-RQ")
			return
		}
		result = assoc.WritePDU(&ReleaseRP{})
	}()

	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "SCP_AE",
		CallingAETitle: "SCU_AE",
		MaxPDU:         4096,
		Contexts: []PresentationContext{
			{
				AbstractSyntaxUID:  verificationSOPClassUID,
				TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
			},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	if got, want := assoc.PeerMaxPDU, uint32(8192); got != want {
		t.Fatalf("client PeerMaxPDU = %d, want %d", got, want)
	}
	if len(assoc.AcceptedContexts) != 1 {
		t.Fatalf("len(client AcceptedContexts) = %d, want 1", len(assoc.AcceptedContexts))
	}
	if got, want := assoc.AcceptedContexts[0], (AcceptedContext{ID: 1, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUID: ExplicitVRLittleEndian}); got != want {
		t.Fatalf("client accepted context = %#v, want %#v", got, want)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestReleaseTimeoutBoundsCleanupAbort(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = peer.Close() }()
	assoc := &Association{Conn: client, MaxPDU: DefaultMaxPDU, Context: context.Background()}

	releaseRead := make(chan error, 1)
	go func() {
		pdu, err := ReadPDU(peer, DefaultMaxPDU)
		if err == nil {
			if _, ok := pdu.(*ReleaseRQ); !ok {
				err = fmt.Errorf("peer got %T, want *ReleaseRQ", pdu)
			}
		}
		releaseRead <- err
		// Deliberately stop reading so an unbounded cleanup A-ABORT blocks.
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- assoc.Release(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrAssociationTimeout) {
			t.Fatalf("Release() error = %v, want ErrAssociationTimeout", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Release() cleanup abort remained blocked after timeout")
	}
	if err := <-releaseRead; err != nil {
		t.Fatalf("peer release read: %v", err)
	}
}

func TestDialContextDoesNotBecomeAssociationDefaultContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "SCP_AE",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()

		pdu, err := assoc.ReadPDU()
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			result = fmt.Errorf("server got %T, want *ReleaseRQ", pdu)
			return
		}
		result = assoc.WritePDU(&ReleaseRP{})
	}()

	dialCtx, cancelDial := context.WithCancel(ctx)
	assoc, err := DialContext(dialCtx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "SCP_AE",
		CallingAETitle: "SCU_AE",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	cancelDial()
	defaultContextErr := assoc.context().Err()

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if defaultContextErr != nil {
		t.Fatalf("Association default context after dial context cancellation = %v, want active context", defaultContextErr)
	}
}

func TestAssociationNegotiationOverTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	serverTLS, clientTLS := testTLSConfigs(t)
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "SCP_AE",
			RequireMatchingCalledAE:   true,
			Context:                   ctx,
			TLSConfig:                 serverTLS,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ExplicitVRLittleEndian},
		})
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()
		tlsConn, ok := assoc.Conn.(*tls.Conn)
		if !ok {
			result = fmt.Errorf("server connection type = %T, want *tls.Conn", assoc.Conn)
			return
		}
		if !tlsConn.ConnectionState().HandshakeComplete {
			result = errors.New("server TLS handshake not complete")
			return
		}

		pdu, err := assoc.ReadPDU()
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			result = errors.New("server expected A-RELEASE-RQ")
			return
		}
		result = assoc.WritePDU(&ReleaseRP{})
	}()

	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "SCP_AE",
		CallingAETitle: "SCU_AE",
		TLSConfig:      clientTLS,
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	tlsConn, ok := assoc.Conn.(*tls.Conn)
	if !ok {
		t.Fatalf("client connection type = %T, want *tls.Conn", assoc.Conn)
	}
	if !tlsConn.ConnectionState().HandshakeComplete {
		t.Fatal("client TLS handshake not complete")
	}
	if got, want := assoc.AcceptedContexts[0].TransferSyntaxUID, ExplicitVRLittleEndian; got != want {
		t.Fatalf("accepted transfer syntax = %q, want %q", got, want)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestTLSAssociationRejectsUntrustedCertificate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	serverTLS, _ := testTLSConfigs(t)
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			TLSConfig:                 serverTLS,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		serverDone <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		TLSConfig: &tls.Config{ServerName: "localhost"},
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err == nil {
		t.Fatal("DialContext() succeeded with untrusted server certificate")
	}
	if !strings.Contains(err.Error(), "TLS client handshake") {
		t.Fatalf("DialContext() error = %v, want TLS client handshake context", err)
	}

	select {
	case err := <-serverDone:
		if err == nil {
			t.Fatal("AcceptAssociation() succeeded after client rejected certificate")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("server goroutine did not finish after TLS certificate rejection")
	}
}

func TestAssociationNegotiationUserIdentityAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		assoc, err := listener.AcceptAssociation(AcceptOptions{
			Context: ctx,
			UserIdentityHandler: func(req UserIdentityRequest) (UserIdentityResult, error) {
				if req.Type != UserIdentityUsernamePassword {
					return UserIdentityResult{}, fmt.Errorf("identity type = %d, want %d", req.Type, UserIdentityUsernamePassword)
				}
				if string(req.PrimaryField) != "alice" || string(req.SecondaryField) != "secret" {
					return UserIdentityResult{}, fmt.Errorf("identity fields = %q/%q", req.PrimaryField, req.SecondaryField)
				}
				if !req.PositiveResponseRequested {
					return UserIdentityResult{}, errors.New("positive response not requested")
				}
				return UserIdentityResult{Action: UserIdentityActionAccept, ServerResponse: []byte("ok")}, nil
			},
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ExplicitVRLittleEndian},
		})
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()

		pdu, err := assoc.ReadPDU()
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			result = fmt.Errorf("server got %T, want *ReleaseRQ", pdu)
			return
		}
		result = assoc.WritePDU(&ReleaseRP{})
	}()

	identity := NewUsernamePasswordIdentity("alice", "secret", true)
	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		UserIdentity: &identity,
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if !assoc.UserIdentityResponded {
		t.Fatal("UserIdentityResponded = false, want true")
	}
	if string(assoc.UserIdentityResponse) != "ok" {
		t.Fatalf("UserIdentityResponse = %q, want %q", assoc.UserIdentityResponse, "ok")
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestAssociationNegotiationRoleSelectionAndExtendedNegotiation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		assoc, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ExplicitVRLittleEndian},
			RoleSelections: []RoleSelectionItem{
				{SopClassUID: verificationSOPClassUID, SCURole: true, SCPRole: true},
			},
			ExtendedNegotiationHandler: func(item SopClassExtendedNegotiationItem) (ExtendedNegotiationResult, error) {
				if item.SopClassUID != verificationSOPClassUID {
					return ExtendedNegotiationResult{}, fmt.Errorf("SOP Class UID = %q", item.SopClassUID)
				}
				if string(item.Data) != "\x01\x02" {
					return ExtendedNegotiationResult{}, fmt.Errorf("extended negotiation data = %v", item.Data)
				}
				return ExtendedNegotiationResult{Action: ExtendedNegotiationActionAccept, Data: []byte{0x09}}, nil
			},
		})
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()

		wantRole := RoleSelectionItem{SopClassUID: verificationSOPClassUID, SCURole: true, SCPRole: true}
		if got := assoc.RequestedRoleSelections; len(got) != 1 || got[0] != wantRole {
			result = fmt.Errorf("server requested role selections = %#v, want %#v", got, []RoleSelectionItem{wantRole})
			return
		}
		if got := assoc.AcceptedRoleSelections; len(got) != 1 || got[0] != wantRole {
			result = fmt.Errorf("server accepted role selections = %#v, want %#v", got, []RoleSelectionItem{wantRole})
			return
		}
		if got := assoc.RequestedExtendedNegotiation; len(got) != 1 || got[0].SopClassUID != verificationSOPClassUID || string(got[0].Data) != "\x01\x02" {
			result = fmt.Errorf("server requested extended negotiation = %#v", got)
			return
		}
		if got := assoc.AcceptedExtendedNegotiation; len(got) != 1 || got[0].SopClassUID != verificationSOPClassUID || string(got[0].Data) != "\x09" {
			result = fmt.Errorf("server accepted extended negotiation = %#v", got)
			return
		}

		pdu, err := assoc.ReadPDU()
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			result = fmt.Errorf("server got %T, want *ReleaseRQ", pdu)
			return
		}
		result = assoc.WritePDU(&ReleaseRP{})
	}()

	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
		}},
		RoleSelections: []RoleSelectionItem{
			{SopClassUID: verificationSOPClassUID, SCURole: true, SCPRole: true},
		},
		ExtendedNegotiation: []SopClassExtendedNegotiationItem{
			{SopClassUID: verificationSOPClassUID, Data: []byte{0x01, 0x02}},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	wantRole := RoleSelectionItem{SopClassUID: verificationSOPClassUID, SCURole: true, SCPRole: true}
	if got := assoc.AcceptedRoleSelections; len(got) != 1 || got[0] != wantRole {
		t.Fatalf("client accepted role selections = %#v, want %#v", got, []RoleSelectionItem{wantRole})
	}
	if got := assoc.AcceptedExtendedNegotiation; len(got) != 1 || got[0].SopClassUID != verificationSOPClassUID || string(got[0].Data) != "\x09" {
		t.Fatalf("client accepted extended negotiation = %#v", got)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestAssociationRejectsExtendedNegotiationByPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
			ExtendedNegotiationHandler: func(SopClassExtendedNegotiationItem) (ExtendedNegotiationResult, error) {
				return ExtendedNegotiationResult{Action: ExtendedNegotiationActionReject}, nil
			},
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
		ExtendedNegotiation: []SopClassExtendedNegotiationItem{
			{SopClassUID: verificationSOPClassUID, Data: []byte{0x01}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if err := <-serverErr; !errors.Is(err, ErrExtendedNegotiationRejected) {
		t.Fatalf("AcceptAssociation() error = %v, want ErrExtendedNegotiationRejected", err)
	}
}

func TestAssociationNegotiationUserIdentityIgnored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		assoc, err := listener.AcceptAssociation(AcceptOptions{
			Context: ctx,
			UserIdentityHandler: func(UserIdentityRequest) (UserIdentityResult, error) {
				return UserIdentityResult{Action: UserIdentityActionIgnore}, nil
			},
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()

		pdu, err := assoc.ReadPDU()
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			result = fmt.Errorf("server got %T, want *ReleaseRQ", pdu)
			return
		}
		result = assoc.WritePDU(&ReleaseRP{})
	}()

	identity := NewUsernameIdentity("bob", true)
	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		UserIdentity: &identity,
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if assoc.UserIdentityResponded {
		t.Fatalf("UserIdentityResponded = true with response %q, want false", assoc.UserIdentityResponse)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestAssociationRejectsUserIdentityWithoutPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		serverErr <- err
	}()

	identity := NewUsernameIdentity("carol", false)
	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		UserIdentity: &identity,
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if got, want := reject.PDU(), (AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceUser, Reason: AssociateRJReasonNoReasonGiven}); got != want {
		t.Fatalf("rejection PDU = %#v, want %#v", got, want)
	}
	if err := <-serverErr; !errors.Is(err, ErrUserIdentityRejected) {
		t.Fatalf("AcceptAssociation() error = %v, want ErrUserIdentityRejected", err)
	}
}

func TestDialRejectsUnsupportedUserIdentityTypeBeforeConnecting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenConfig.Listen() error = %v", err)
	}
	cleanupClose(t, "listener", ln)

	identity := NewTokenIdentity(0x99, []byte("token"), false)
	_, err = Dial(ln.Addr().String(), DialOptions{
		Context:      ctx,
		UserIdentity: &identity,
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if !errors.Is(err, ErrUnsupportedUserIdentityType) {
		t.Fatalf("Dial() error = %v, want ErrUnsupportedUserIdentityType", err)
	}

	if tcp, ok := ln.(*net.TCPListener); ok {
		if err := tcp.SetDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
			t.Fatalf("SetDeadline() error = %v", err)
		}
		defer func() { _ = tcp.SetDeadline(time.Time{}) }()
	}
	conn, err := ln.Accept()
	if err == nil {
		if cerr := closeErr("unexpected accepted conn", conn); cerr != nil {
			t.Errorf("%s", cerr)
		}
		t.Fatal("listener accepted a connection for invalid user identity")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Accept() error = %v, want timeout proving no connection was opened", err)
	}
}

func TestBuildAssociateRQDefaultsAndIDs(t *testing.T) {
	rq := buildAssociateRQ(DialOptions{
		CalledAETitle:  "SCP_AE",
		CallingAETitle: "SCU_AE",
		MaxPDU:         4096,
		Contexts: []PresentationContext{
			{ID: 9, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
			{AbstractSyntaxUID: "1.2.3", TransferSyntaxUIDs: []string{ExplicitVRLittleEndian}},
		},
	})
	if rq.ProtocolVersion != DefaultProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", rq.ProtocolVersion, DefaultProtocolVersion)
	}
	if rq.ApplicationContextName != ApplicationContextName {
		t.Fatalf("ApplicationContextName = %q, want %q", rq.ApplicationContextName, ApplicationContextName)
	}
	if got := []byte{rq.PresentationContexts[0].ID, rq.PresentationContexts[1].ID}; got[0] != 1 || got[1] != 3 {
		t.Fatalf("presentation context IDs = %v, want [1 3]", got)
	}

	if len(rq.UserInfo) != 3 {
		t.Fatalf("len(UserInfo) = %d, want 3", len(rq.UserInfo))
	}
	assertUserInfo(t, rq.UserInfo, 4096, ImplementationClassUID, ImplementationVersionName)
}

func TestProposedContextsRejectsMoreThanAvailableOddIDs(t *testing.T) {
	contexts := make([]PresentationContext, MaxPresentationContexts+1)
	for i := range contexts {
		contexts[i] = PresentationContext{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}
	}
	if _, err := proposedContexts(contexts); !errors.Is(err, ErrInvalidPCID) {
		t.Fatalf("proposedContexts(%d) error = %v, want ErrInvalidPCID", len(contexts), err)
	}

	proposed, err := proposedContexts(contexts[:MaxPresentationContexts])
	if err != nil {
		t.Fatalf("proposedContexts(%d) error = %v", MaxPresentationContexts, err)
	}
	if got := proposed[len(proposed)-1].ID; got != 255 {
		t.Fatalf("last presentation context ID = %d, want 255", got)
	}
}

func TestBuildAssociateRQImplementationOverrides(t *testing.T) {
	rq := buildAssociateRQ(DialOptions{
		CalledAETitle:             "SCP_AE",
		CallingAETitle:            "SCU_AE",
		ImplementationClassUID:    "1.2.826.0.1",
		ImplementationVersionName: "TESTAPP",
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	assertUserInfo(t, rq.UserInfo, DefaultMaxPDU, "1.2.826.0.1", "TESTAPP")
}

func TestBuildAssociateRQValidation(t *testing.T) {
	err := validateDialOptions(DialOptions{CalledAETitle: "TOO-LONG-CALLED-AE", Contexts: []PresentationContext{
		{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
	}})
	if !errors.Is(err, ErrInvalidAEtitle) {
		t.Fatalf("validateDialOptions(long AE) error = %v, want ErrInvalidAEtitle", err)
	}

	err = validateDialOptions(DialOptions{})
	if !errors.Is(err, ErrMissingPDUField) {
		t.Fatalf("validateDialOptions(no contexts) error = %v, want ErrMissingPDUField", err)
	}
}

func TestDialValidatesOptionsBeforeConnecting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenConfig.Listen() error = %v", err)
	}
	cleanupClose(t, "listener", ln)

	_, err = Dial(ln.Addr().String(), DialOptions{Context: ctx})
	if !errors.Is(err, ErrMissingPDUField) {
		t.Fatalf("Dial() error = %v, want ErrMissingPDUField", err)
	}

	if tcp, ok := ln.(*net.TCPListener); ok {
		if err := tcp.SetDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
			t.Fatalf("SetDeadline() error = %v", err)
		}
		defer func() { _ = tcp.SetDeadline(time.Time{}) }()
	}
	conn, err := ln.Accept()
	if err == nil {
		if cerr := closeErr("unexpected accepted conn", conn); cerr != nil {
			t.Errorf("%s", cerr)
		}
		t.Fatal("listener accepted a connection for invalid DialOptions")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Accept() error = %v, want timeout proving no connection was opened", err)
	}
}

func TestDialDoesNotAbortAfterAssociationReject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, serverDone := terminalNegotiationPeer(t, &AssociationRJ{
		Result: AssociateRJResultPermanent,
		Source: AssociateRJSourceServiceUser,
		Reason: AssociateRJReasonNoReasonGiven,
	})

	_, err := DialContext(ctx, addr, DialOptions{
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestDialDoesNotAbortAfterPeerAbort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, serverDone := terminalNegotiationPeer(t, &AbortRQ{
		Source: AbortSourceServiceProvider,
		Reason: AbortReasonNotSpecified,
	})

	_, err := DialContext(ctx, addr, DialOptions{
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	var abortErr *AbortError
	if !errors.As(err, &abortErr) {
		t.Fatalf("DialContext() error = %v, want AbortError", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func terminalNegotiationPeer(t *testing.T, terminal PDU) (string, <-chan error) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	cleanupClose(t, "listener", ln)

	done := make(chan error, 1)
	go func() {
		var result error
		defer func() { done <- result }()

		conn, err := ln.Accept()
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server conn", conn); result == nil && err != nil {
				result = err
			}
		}()

		pdu, err := ReadPDU(conn, DefaultMaxPDU)
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*AssociationRQ); !ok {
			result = fmt.Errorf("server got %T, want *AssociationRQ", pdu)
			return
		}
		if err := WritePDU(conn, terminal); err != nil {
			result = err
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			result = err
			return
		}
		pdu, err = ReadPDU(conn, DefaultMaxPDU)
		if err != nil {
			return
		}
		result = fmt.Errorf("server received extra %T after terminal negotiation PDU", pdu)
	}()

	return ln.Addr().String(), done
}

func TestAssociationRejectReturnsStructuredReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "EXPECTED_SCP",
			RequireMatchingCalledAE:   true,
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "WRONG_SCP",
		CallingAETitle: "SCU_AE",
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if !errors.Is(err, ErrAssociationRejected) {
		t.Fatalf("DialContext() error does not wrap ErrAssociationRejected: %v", err)
	}
	if got, want := reject.PDU(), (AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceUser, Reason: AssociateRJReasonCalledAETitleNotRecognized}); got != want {
		t.Fatalf("rejection PDU = %#v, want %#v", got, want)
	}

	err = <-serverErr
	if !errors.As(err, &reject) {
		t.Fatalf("AcceptAssociation() error = %v, want RejectionError", err)
	}
}

func TestAssociationRejectsProtocolVersionMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		ProtocolVersion: 2,
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if got, want := reject.PDU(), (AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceProviderACSE, Reason: AssociateRJReasonProtocolVersionNotSupported}); got != want {
		t.Fatalf("rejection PDU = %#v, want %#v", got, want)
	}

	err = <-serverErr
	if !errors.As(err, &reject) {
		t.Fatalf("AcceptAssociation() error = %v, want RejectionError", err)
	}
}

func TestAssociationRejectsUnsupportedAbstractSyntax(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	rejected := make(chan struct{})
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{"1.2.3.4"},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
			OnAssociationRejected:     func() { close(rejected) },
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if got, want := reject.PDU(), (AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceUser, Reason: AssociateRJReasonNoReasonGiven}); got != want {
		t.Fatalf("rejection PDU = %#v, want %#v", got, want)
	}
	select {
	case <-rejected:
	default:
		t.Fatal("OnAssociationRejected was not called before the peer observed the rejection")
	}
	if err := <-serverErr; !errors.Is(err, ErrNoAcceptedPresentationContexts) {
		t.Fatalf("AcceptAssociation() error = %v, want ErrNoAcceptedPresentationContexts", err)
	}
}

func TestAcceptRejectsUnexpectedPDU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, server := net.Pipe()
	cleanupClose(t, "client pipe", client)

	writeErr := make(chan error, 1)
	go func() {
		if err := WritePDU(client, &ReleaseRQ{}); err != nil {
			writeErr <- err
			return
		}
		pdu, err := ReadPDU(client, DefaultMaxPDU)
		if err != nil {
			writeErr <- err
			return
		}
		if _, ok := pdu.(*ReleaseRP); !ok {
			writeErr <- fmt.Errorf("unexpected response PDU = %T, want *ReleaseRP", pdu)
			return
		}
		writeErr <- nil
	}()

	_, err := Accept(server, AcceptOptions{Context: ctx})
	if !errors.Is(err, ErrUnexpectedPDU) {
		t.Fatalf("Accept() error = %v, want ErrUnexpectedPDU", err)
	}
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("client WritePDU() error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("client goroutine did not finish after unexpected PDU response")
	}
}

func TestAssociationAbort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverErr <- result }()

		assoc, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()
		_, result = assoc.Receive(ctx)
	}()

	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if err := assoc.Abort(AbortReasonNotSpecified); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}

	err = <-serverErr
	var abortErr *AbortError
	if !errors.As(err, &abortErr) {
		t.Fatalf("server Receive() error = %v, want AbortError", err)
	}
	if abortErr.Source != AbortSourceServiceUser || abortErr.Reason != AbortReasonNotSpecified {
		t.Fatalf("abort = source %d reason %d, want %d/%d", abortErr.Source, abortErr.Reason, AbortSourceServiceUser, AbortReasonNotSpecified)
	}
}

func TestReleaseDrainsPDataBeforeReleaseResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn := &deadlineTrackingConn{readData: pduStream(t,
		&PDataTF{Values: []PDataValue{{
			PresentationContextID: 1,
			IsCommand:             true,
			IsLast:                true,
			Data:                  []byte{1},
		}}},
		&ReleaseRP{},
	)}
	assoc := &Association{Conn: conn, MaxPDU: DefaultMaxPDU, Context: ctx}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if assoc.Conn != nil {
		t.Fatal("Release() left association connection open")
	}

	written := writtenPDUs(t, conn)
	if len(written) != 1 {
		t.Fatalf("written PDUs = %d, want 1", len(written))
	}
	if _, ok := written[0].(*ReleaseRQ); !ok {
		t.Fatalf("written[0] = %T, want *ReleaseRQ", written[0])
	}
}

func TestReleaseCollisionRespondsAndWaitsForPeerReleaseResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn := &deadlineTrackingConn{readData: pduStream(t, &ReleaseRQ{}, &ReleaseRP{})}
	assoc := &Association{Conn: conn, MaxPDU: DefaultMaxPDU, Context: ctx}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if assoc.Conn != nil {
		t.Fatal("Release() left association connection open")
	}

	written := writtenPDUs(t, conn)
	if len(written) != 2 {
		t.Fatalf("written PDUs = %d, want 2", len(written))
	}
	if _, ok := written[0].(*ReleaseRQ); !ok {
		t.Fatalf("written[0] = %T, want *ReleaseRQ", written[0])
	}
	if _, ok := written[1].(*ReleaseRP); !ok {
		t.Fatalf("written[1] = %T, want *ReleaseRP", written[1])
	}
}

func TestReleaseAbortsAndClosesOnUnexpectedPDU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn := &deadlineTrackingConn{readData: pduStream(t, &AssociationRJ{
		Result: AssociateRJResultPermanent,
		Source: AssociateRJSourceServiceUser,
		Reason: AssociateRJReasonNoReasonGiven,
	})}
	assoc := &Association{Conn: conn, MaxPDU: DefaultMaxPDU, Context: ctx}

	err := assoc.Release(ctx)
	if !errors.Is(err, ErrUnexpectedPDU) {
		t.Fatalf("Release() error = %v, want ErrUnexpectedPDU", err)
	}
	if assoc.Conn != nil {
		t.Fatal("Release() left association connection open after unexpected PDU")
	}

	written := writtenPDUs(t, conn)
	if len(written) != 2 {
		t.Fatalf("written PDUs = %d, want 2", len(written))
	}
	if _, ok := written[0].(*ReleaseRQ); !ok {
		t.Fatalf("written[0] = %T, want *ReleaseRQ", written[0])
	}
	abort, ok := written[1].(*AbortRQ)
	if !ok {
		t.Fatalf("written[1] = %T, want *AbortRQ", written[1])
	}
	if abort.Source != AbortSourceServiceUser || abort.Reason != AbortReasonUnexpectedPDU {
		t.Fatalf("abort = source %d reason %d, want %d/%d", abort.Source, abort.Reason, AbortSourceServiceUser, AbortReasonUnexpectedPDU)
	}
}

func TestReleaseClosesAssociationOnReleaseRequestWriteError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	writeErr := errors.New("write failed")
	conn := &deadlineTrackingConn{writeErr: writeErr}
	assoc := &Association{Conn: conn, MaxPDU: DefaultMaxPDU, Context: ctx}

	err := assoc.Release(ctx)
	if !errors.Is(err, writeErr) {
		t.Fatalf("Release() error = %v, want %v", err, writeErr)
	}
	if assoc.Conn != nil {
		t.Fatal("Release() left association connection open after ReleaseRQ write error")
	}
}

func TestReleaseClosesAssociationOnReceiveError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	readErr := errors.New("read failed")
	conn := &deadlineTrackingConn{readErr: readErr}
	assoc := &Association{Conn: conn, MaxPDU: DefaultMaxPDU, Context: ctx}

	err := assoc.Release(ctx)
	if !errors.Is(err, readErr) {
		t.Fatalf("Release() error = %v, want %v", err, readErr)
	}
	if assoc.Conn != nil {
		t.Fatal("Release() left association connection open after receive error")
	}

	written := writtenPDUs(t, conn)
	if len(written) != 1 {
		t.Fatalf("written PDUs = %d, want ReleaseRQ only", len(written))
	}
	if _, ok := written[0].(*ReleaseRQ); !ok {
		t.Fatalf("written[0] = %T, want *ReleaseRQ", written[0])
	}
}

func TestAssociationConnectionLockIsPerInstance(t *testing.T) {
	client, server := net.Pipe()
	cleanupClose(t, "server pipe", server)
	cleanupClose(t, "client pipe", client)

	blocked := &Association{}
	unrelated := &Association{Conn: client}

	blocked.connMu.Lock()
	defer blocked.connMu.Unlock()

	done := make(chan net.Conn, 1)
	go func() {
		done <- unrelated.connection()
	}()

	select {
	case conn := <-done:
		if conn != client {
			t.Fatalf("connection() = %v, want unrelated connection", conn)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("connection() on unrelated association blocked behind another association lock")
	}
}

func TestDialContextAssociationTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenConfig.Listen() error = %v", err)
	}
	cleanupClose(t, "listener", ln)

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		conn, err := ln.Accept()
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server conn", conn); result == nil && err != nil {
				result = err
			}
		}()
		_, result = io.Copy(io.Discard, conn)
	}()

	_, err = DialContext(ctx, ln.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("DialContext() error = %v, want ErrAssociationTimeout", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("server goroutine did not finish after Dial timeout")
	}
}

func TestAcceptAssociationTimeout(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_, err = listener.AcceptAssociation(AcceptOptions{Context: ctx})
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("AcceptAssociation() error = %v, want ErrAssociationTimeout", err)
	}
}

func TestAssociationOperationStateIsInstanceLocal(t *testing.T) {
	first := &Association{}
	second := &Association{}

	if !first.TryBeginOperation() {
		t.Fatal("first TryBeginOperation() = false, want true")
	}
	if first.TryBeginOperation() {
		t.Fatal("concurrent first TryBeginOperation() = true, want false")
	}
	if !second.TryBeginOperation() {
		t.Fatal("second TryBeginOperation() = false, want independent state")
	}

	first.EndOperation()
	if !first.TryBeginOperation() {
		t.Fatal("first TryBeginOperation() after EndOperation = false, want true")
	}
	first.EndOperation()
	second.EndOperation()
}

func TestAssociationPDataCarryoverIsOwnedClonedAndCleared(t *testing.T) {
	first := &Association{}
	second := &Association{}
	firstValues := []PDataValue{{PresentationContextID: 1, Data: []byte("first")}}
	first.StorePDataCarryover(firstValues)
	second.StorePDataCarryover([]PDataValue{{PresentationContextID: 3, Data: []byte("second")}})
	firstValues[0].Data[0] = 'X'

	gotFirst := first.TakePDataCarryover()
	if len(gotFirst) != 1 || string(gotFirst[0].Data) != "first" {
		t.Fatalf("first carryover = %#v, want cloned first value", gotFirst)
	}
	gotSecond := second.TakePDataCarryover()
	if len(gotSecond) != 1 || string(gotSecond[0].Data) != "second" {
		t.Fatalf("second carryover = %#v, want independent second value", gotSecond)
	}
	if got := first.TakePDataCarryover(); len(got) != 0 {
		t.Fatalf("second first.TakePDataCarryover() = %#v, want empty", got)
	}

	first.StorePDataCarryover([]PDataValue{{Data: []byte("stale")}})
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := first.TakePDataCarryover(); len(got) != 0 {
		t.Fatalf("TakePDataCarryover() after Close = %#v, want empty", got)
	}
}

func TestAssociationExclusiveTokenProtectsCarryoverAndIsAssociationLocal(t *testing.T) {
	first := &Association{}
	second := &Association{}
	firstOwner, ok := first.TryBeginExclusiveOperation()
	if !ok {
		t.Fatal("first TryBeginExclusiveOperation() = false")
	}
	defer firstOwner.End()
	secondOwner, ok := second.TryBeginExclusiveOperation()
	if !ok {
		t.Fatal("second TryBeginExclusiveOperation() = false")
	}
	defer secondOwner.End()

	firstCtx := firstOwner.Context(context.Background())
	secondCtx := secondOwner.Context(context.Background())
	value := []PDataValue{{PresentationContextID: 1, Data: []byte("owned")}}
	if err := first.StorePDataCarryoverContext(firstCtx, value); err != nil {
		t.Fatalf("owner StorePDataCarryoverContext() error = %v", err)
	}
	if err := first.StorePDataCarryoverContext(secondCtx, value); !errors.Is(err, ErrAssociationExclusivelyOwned) {
		t.Fatalf("cross-association StorePDataCarryoverContext() error = %v", err)
	}
	if values, err := first.TakePDataCarryoverContext(secondCtx); !errors.Is(err, ErrAssociationExclusivelyOwned) || len(values) != 0 {
		t.Fatalf("cross-association TakePDataCarryoverContext() = %#v, %v", values, err)
	}
	if values := first.TakePDataCarryover(); len(values) != 0 {
		t.Fatalf("contextless TakePDataCarryover() consumed owned values: %#v", values)
	}
	values, err := first.TakePDataCarryoverContext(firstCtx)
	if err != nil || len(values) != 1 || string(values[0].Data) != "owned" {
		t.Fatalf("owner TakePDataCarryoverContext() = %#v, %v", values, err)
	}
}
