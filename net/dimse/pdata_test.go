package dimse

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestPDataWriterFragmentsAndSetsMessageControlHeader(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		writer := NewPDataWriter(assoc, 1, true, 16)
		if _, err := writer.Write([]byte("abcdefghi")); err != nil {
			done <- err
			return
		}
		done <- writer.Finish()
	}()

	var got []ul.PDataValue
	for len(got) < 3 {
		pdu := readPDUWithDeadline(t, server)
		pdata, ok := pdu.(*ul.PDataTF)
		if !ok {
			t.Fatalf("PDU type = %T, want *ul.PDataTF", pdu)
		}
		got = append(got, pdata.Values...)
	}
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}

	// PDV data is a byte stream fragment, not a DICOM element value; odd-sized
	// fragments are valid and must not be padded because that would mutate the
	// DIMSE message reassembled by PDataReader.
	wantData := [][]byte{[]byte("abcd"), []byte("efgh"), []byte("i")}
	for i := range wantData {
		if got[i].PresentationContextID != 1 {
			t.Fatalf("fragment %d presentation context ID = %d, want 1", i, got[i].PresentationContextID)
		}
		if !got[i].IsCommand {
			t.Fatalf("fragment %d IsCommand = false, want true", i)
		}
		if !bytes.Equal(got[i].Data, wantData[i]) {
			t.Fatalf("fragment %d data = %q, want %q", i, got[i].Data, wantData[i])
		}
	}
	if got[0].IsLast || got[1].IsLast || !got[2].IsLast {
		t.Fatalf("IsLast flags = [%t %t %t], want [false false true]", got[0].IsLast, got[1].IsLast, got[2].IsLast)
	}
}

func TestPDataWriterExactBoundaryMarksDataFragmentLast(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		writer := NewPDataWriter(assoc, 1, true, 16)
		if _, err := writer.Write([]byte("abcd")); err != nil {
			done <- err
			return
		}
		done <- writer.Finish()
	}()

	pdu := readPDUWithDeadline(t, server)
	pdata, ok := pdu.(*ul.PDataTF)
	if !ok {
		t.Fatalf("PDU type = %T, want *ul.PDataTF", pdu)
	}
	if len(pdata.Values) != 1 {
		t.Fatalf("len(PDataTF.Values) = %d, want 1", len(pdata.Values))
	}
	value := pdata.Values[0]
	if !value.IsLast {
		t.Fatal("exact-boundary fragment IsLast = false, want true")
	}
	if !bytes.Equal(value.Data, []byte("abcd")) {
		t.Fatalf("exact-boundary data = %q, want %q", value.Data, "abcd")
	}
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestPDataWriterSingleByteFragments(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		writer := NewPDataWriter(assoc, 1, false, 13)
		if _, err := writer.Write([]byte("abc")); err != nil {
			done <- err
			return
		}
		done <- writer.Finish()
	}()

	var got []ul.PDataValue
	for len(got) < 3 {
		pdu := readPDUWithDeadline(t, server)
		pdata, ok := pdu.(*ul.PDataTF)
		if !ok {
			t.Fatalf("PDU type = %T, want *ul.PDataTF", pdu)
		}
		got = append(got, pdata.Values...)
	}
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
	for i, want := range []byte("abc") {
		if len(got[i].Data) != 1 || got[i].Data[0] != want {
			t.Fatalf("fragment %d data = %q, want %q", i, got[i].Data, []byte{want})
		}
		if got[i].IsCommand {
			t.Fatalf("fragment %d IsCommand = true, want false", i)
		}
	}
	if got[0].IsLast || got[1].IsLast || !got[2].IsLast {
		t.Fatalf("IsLast flags = [%t %t %t], want [false false true]", got[0].IsLast, got[1].IsLast, got[2].IsLast)
	}
}

func TestPDataReaderReassemblesFragments(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		for _, value := range []ul.PDataValue{
			{PresentationContextID: 1, IsCommand: true, IsLast: false, Data: []byte("hello ")},
			{PresentationContextID: 1, IsCommand: true, IsLast: true, Data: []byte("world")},
		} {
			if err := ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{value}}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	reader := NewPDataReader(assoc, 1)
	data, err := readAllPDataWithDeadline(t, assoc, reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("reassembled data = %q, want %q", data, "hello world")
	}
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestPDataReaderRejectsPresentationContextMismatch(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		done <- ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{{
			PresentationContextID: 3,
			IsCommand:             true,
			IsLast:                true,
			Data:                  []byte("bad"),
		}}})
	}()

	_, err := readAllPDataWithDeadline(t, assoc, NewPDataReader(assoc, 1))
	if !errors.Is(err, ErrPresentationContextMismatch) {
		t.Fatalf("ReadAll() error = %v, want ErrPresentationContextMismatch", err)
	}
	if err := waitDone(t, done, time.Second); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("writer error = %v", err)
	}
}

func readAllPDataWithDeadline(t testing.TB, assoc *ul.Association, reader *PDataReader) ([]byte, error) {
	t.Helper()
	if assoc == nil || assoc.Conn == nil {
		t.Fatal("nil association connection")
	}
	if err := assoc.Conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	defer func() {
		if err := assoc.Conn.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("clear SetReadDeadline() error = %v", err)
		}
	}()
	return io.ReadAll(reader)
}

func waitDone(t testing.TB, done <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for P-DATA goroutine after %s", timeout)
		return nil
	}
}

func readPDUWithDeadline(t testing.TB, conn net.Conn) ul.PDU {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	defer func() {
		if err := conn.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("clear SetReadDeadline() error = %v", err)
		}
	}()
	pdu, err := ul.ReadPDU(conn, ul.DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	return pdu
}
