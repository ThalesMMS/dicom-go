package dimse

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
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

func TestPDataWriterWithContextCancelsBlockedWrite(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	writer := NewPDataWriterWithContext(ctx, assoc, 1, true, 1024)
	if _, err := writer.Write([]byte("blocked")); err != nil {
		t.Fatalf("Write() before flush error = %v", err)
	}
	started := time.Now()
	err := writer.Finish()
	if !errors.Is(err, ul.ErrAssociationTimeout) {
		t.Fatalf("Finish() error = %v, want ul.ErrAssociationTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Finish() cancellation took %s", elapsed)
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

func BenchmarkPDataWriterFlush(b *testing.B) {
	payload := bytes.Repeat([]byte{0x5a}, 32<<10)
	assoc := &ul.Association{Conn: &pDataDiscardConn{}, MaxPDU: ul.DefaultMaxPDU}
	maxPDU := len(payload) + int(ul.PDUHeaderSize+ul.PDVHeaderSize)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		writer := NewPDataWriter(assoc, 1, false, maxPDU)
		if _, err := writer.Write(payload); err != nil {
			b.Fatal(err)
		}
		if err := writer.Finish(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPDataReaderByteAtATime(b *testing.B) {
	payload := bytes.Repeat([]byte{0xa5}, 32<<10)
	one := make([]byte, 1)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader := &PDataReader{buf: append([]byte(nil), payload...), lastReceived: true}
		for {
			if _, err := reader.Read(one); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				b.Fatal(err)
			}
		}
	}
}

func TestPDataReaderAdvancesOffsetWithoutShiftingResidualBuffer(t *testing.T) {
	reader := &PDataReader{buf: []byte("abcdef"), lastReceived: true}
	out := make([]byte, 2)
	n, err := reader.Read(out)
	if err != nil || n != 2 || string(out) != "ab" {
		t.Fatalf("first Read() = %d,%v,%q, want 2,nil,ab", n, err, out)
	}
	if string(reader.buf) != "abcdef" || reader.bufOffset != 2 || reader.bufferedLen() != 4 {
		t.Fatalf("reader state after partial read = buf %q offset %d available %d", reader.buf, reader.bufOffset, reader.bufferedLen())
	}

	rest, err := io.ReadAll(reader)
	if err != nil || string(rest) != "cdef" {
		t.Fatalf("ReadAll() = %q,%v, want cdef,nil", rest, err)
	}
	if len(reader.buf) != 0 || reader.bufOffset != 0 {
		t.Fatalf("reader did not reset consumed buffer: len=%d offset=%d", len(reader.buf), reader.bufOffset)
	}
}

type pDataDiscardConn struct{}

func (*pDataDiscardConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*pDataDiscardConn) Write(p []byte) (int, error)      { return len(p), nil }
func (*pDataDiscardConn) Close() error                     { return nil }
func (*pDataDiscardConn) LocalAddr() net.Addr              { return pDataDiscardAddr("local") }
func (*pDataDiscardConn) RemoteAddr() net.Addr             { return pDataDiscardAddr("remote") }
func (*pDataDiscardConn) SetDeadline(time.Time) error      { return nil }
func (*pDataDiscardConn) SetReadDeadline(time.Time) error  { return nil }
func (*pDataDiscardConn) SetWriteDeadline(time.Time) error { return nil }

type pDataDiscardAddr string

func (a pDataDiscardAddr) Network() string { return "discard" }
func (a pDataDiscardAddr) String() string  { return string(a) }

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

func TestTypedPDataReaderRejectsCommandSetBeyondLimitBeforeAppending(t *testing.T) {
	reader := newTypedPDataReader(nil, 1, true)
	reader.receivedBytes = MaxCommandSetBytes - 1

	err := reader.consumePDataValues([]ul.PDataValue{{
		PresentationContextID: 1,
		IsCommand:             true,
		Data:                  []byte{0x01, 0x02},
	}})

	if !errors.Is(err, ErrCommandSetTooLarge) {
		t.Fatalf("consumePDataValues() error = %v, want ErrCommandSetTooLarge", err)
	}
	if len(reader.buf) != 0 {
		t.Fatalf("reader buffered %d byte(s) after rejecting oversized command", len(reader.buf))
	}
}

func TestTypedPDataReaderAcceptsCommandSetAtExactLimit(t *testing.T) {
	reader := newTypedPDataReader(nil, 1, true)
	reader.receivedBytes = MaxCommandSetBytes - 1

	err := reader.consumePDataValues([]ul.PDataValue{{
		PresentationContextID: 1,
		IsCommand:             true,
		IsLast:                true,
		Data:                  []byte{0x01},
	}})

	if err != nil {
		t.Fatalf("consumePDataValues() error = %v at exact limit", err)
	}
	if reader.receivedBytes != MaxCommandSetBytes || len(reader.buf) != 1 {
		t.Fatalf("reader size/buffer = %d/%d, want %d/1", reader.receivedBytes, len(reader.buf), MaxCommandSetBytes)
	}
}

func TestReceiveDataSetWithContextAndLimitRejectsOversizedIdentifier(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	dataset := object.FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0052), core.VRCS, []byte("A ")),
		core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("B ")),
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("C ")),
	}, std.Dictionary)
	var encoded bytes.Buffer
	if err := object.WriteDataSet(&encoded, dataset, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("WriteDataSet() error = %v", err)
	}
	if encoded.Len() <= 20 {
		t.Fatalf("encoded test identifier = %d bytes, want over 20", encoded.Len())
	}

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		done <- ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{{
			PresentationContextID: 1,
			IsCommand:             false,
			IsLast:                true,
			Data:                  encoded.Bytes(),
		}}})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := receiveDataSetWithContextAndLimit(ctx, assoc, 1, transfer.ImplicitVRLittleEndian, 20)
	if !errors.Is(err, parser.ErrMaxTotalBytesExceeded) {
		t.Fatalf("receiveDataSetWithContextAndLimit() error = %v, want ErrMaxTotalBytesExceeded", err)
	}
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestTypedPDataReaderCarriesMixedPDUToNextStream(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		done <- ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{
			{PresentationContextID: 1, IsCommand: true, IsLast: false, Data: []byte("com")},
			{PresentationContextID: 1, IsCommand: true, IsLast: true, Data: []byte("mand")},
			{PresentationContextID: 1, IsCommand: false, IsLast: false, Data: []byte("data")},
			{PresentationContextID: 1, IsCommand: false, IsLast: true, Data: []byte("set")},
		}})
	}()

	command, err := readAllPDataWithDeadline(t, assoc, newTypedPDataReader(assoc, 1, true))
	if err != nil {
		t.Fatalf("command ReadAll() error = %v", err)
	}
	if string(command) != "command" {
		t.Fatalf("command data = %q, want command", command)
	}
	dataset, err := readAllPDataWithDeadline(t, assoc, newTypedPDataReader(assoc, 1, false))
	if err != nil {
		t.Fatalf("dataset ReadAll() error = %v", err)
	}
	if string(dataset) != "dataset" {
		t.Fatalf("dataset data = %q, want dataset", dataset)
	}
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestReceiveCommandSetAnyCarriesMixedPDUToDatasetReader(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	commandData, datasetData := mixedCStoreCommandAndDataSetBytes(t)
	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	setAssociationReadDeadline(t, assoc)
	done := make(chan error, 1)
	go func() {
		done <- ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{
			{PresentationContextID: 5, IsCommand: true, IsLast: true, Data: commandData},
			{PresentationContextID: 5, IsCommand: false, IsLast: false, Data: datasetData[:len(datasetData)/2]},
			{PresentationContextID: 5, IsCommand: false, IsLast: true, Data: datasetData[len(datasetData)/2:]},
		}})
	}()

	pcID, req, err := ReceiveCStoreRequestAny(assoc)
	if err != nil {
		t.Fatalf("ReceiveCStoreRequestAny() error = %v", err)
	}
	if pcID != 5 {
		t.Fatalf("presentation context ID = %d, want 5", pcID)
	}
	if req.MessageID != 77 {
		t.Fatalf("C-STORE MessageID = %d, want 77", req.MessageID)
	}
	dataset, err := ReceiveDataSet(assoc, pcID, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReceiveDataSet() error = %v", err)
	}
	assertMixedPDUDataSet(t, dataset)
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestReceiveCStoreRequestCarriesMixedPDUDataSet(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	commandData, datasetData := mixedCStoreCommandAndDataSetBytes(t)
	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	setAssociationReadDeadline(t, assoc)
	done := make(chan error, 1)
	go func() {
		done <- ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{
			{PresentationContextID: 3, IsCommand: true, IsLast: true, Data: commandData},
			{PresentationContextID: 3, IsCommand: false, IsLast: false, Data: datasetData[:len(datasetData)/2]},
			{PresentationContextID: 3, IsCommand: false, IsLast: true, Data: datasetData[len(datasetData)/2:]},
		}})
	}()

	req, err := ReceiveCStoreRequest(assoc, 3)
	if err != nil {
		t.Fatalf("ReceiveCStoreRequest() error = %v", err)
	}
	if req.MessageID != 77 {
		t.Fatalf("C-STORE MessageID = %d, want 77", req.MessageID)
	}
	dataset, err := ReceiveDataSet(assoc, 3, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReceiveDataSet() error = %v", err)
	}
	assertMixedPDUDataSet(t, dataset)
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestAssociationCloseClearsPDataCarryover(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	if err := storePDataCarryoverWithContext(context.Background(), assoc, []ul.PDataValue{{
		PresentationContextID: 1,
		IsCommand:             false,
		IsLast:                true,
		Data:                  []byte("dataset"),
	}}); err != nil {
		t.Fatalf("storePDataCarryoverWithContext() error = %v", err)
	}

	if err := assoc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	values, err := takePDataCarryoverWithContext(context.Background(), assoc)
	if err != nil {
		t.Fatalf("takePDataCarryoverWithContext() error = %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("takePDataCarryoverWithContext() after Close() returned %d values, want 0", len(values))
	}
}

func TestTypedPDataReaderRejectsDatasetBeforeCommandLast(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		done <- ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{
			{PresentationContextID: 1, IsCommand: true, IsLast: false, Data: []byte("command-")},
			{PresentationContextID: 1, IsCommand: false, IsLast: true, Data: []byte("dataset")},
		}})
	}()

	_, err := readAllPDataWithDeadline(t, assoc, newTypedPDataReader(assoc, 1, true))
	if err == nil {
		t.Fatal("command ReadAll() error = nil, want command flag mismatch")
	}
	if err := waitDone(t, done, time.Second); err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestTypedPDataReaderRejectsCarryoverPresentationContextMismatch(t *testing.T) {
	server, client := net.Pipe()
	defer closeOrFail(t, "server pipe", server)
	defer closeOrFail(t, "client pipe", client)

	assoc := &ul.Association{Conn: client, MaxPDU: ul.DefaultMaxPDU}
	done := make(chan error, 1)
	go func() {
		done <- ul.WritePDU(server, &ul.PDataTF{Values: []ul.PDataValue{
			{PresentationContextID: 1, IsCommand: true, IsLast: true, Data: []byte("command")},
			{PresentationContextID: 3, IsCommand: false, IsLast: true, Data: []byte("dataset")},
		}})
	}()

	command, err := readAllPDataWithDeadline(t, assoc, newTypedPDataReader(assoc, 1, true))
	if err != nil {
		t.Fatalf("command ReadAll() error = %v", err)
	}
	if string(command) != "command" {
		t.Fatalf("command data = %q, want command", command)
	}
	_, err = readAllPDataWithDeadline(t, assoc, newTypedPDataReader(assoc, 1, false))
	if !errors.Is(err, ErrPresentationContextMismatch) {
		t.Fatalf("dataset ReadAll() error = %v, want ErrPresentationContextMismatch", err)
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

func mixedCStoreCommandAndDataSetBytes(t testing.TB) ([]byte, []byte) {
	t.Helper()
	commandData, err := EncodeCommandSet(CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              77,
		Priority:               0,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	}.CommandSet())
	if err != nil {
		t.Fatalf("EncodeCommandSet() error = %v", err)
	}
	dataset := object.FromElements([]core.Element{
		dicomtest.NewUIElement(core.NewTag(0x0008, 0x0016), dicomtest.TestSOPClassUID),
		dicomtest.NewUIElement(core.NewTag(0x0008, 0x0018), dicomtest.TestSOPInstanceUID),
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "MIXED^PDU"),
	}, std.Dictionary)
	var datasetData bytes.Buffer
	if err := object.WriteDataSet(&datasetData, dataset, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("WriteDataSet() error = %v", err)
	}
	return commandData, datasetData.Bytes()
}

func assertMixedPDUDataSet(t testing.TB, dataset *object.Object) {
	t.Helper()
	if got, ok := dataset.GetUID(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("SOPInstanceUID = %q ok=%v, want %q true", got, ok, dicomtest.TestSOPInstanceUID)
	}
	if got, ok := dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "MIXED^PDU" {
		t.Fatalf("PatientName = %q ok=%v, want MIXED^PDU true", got, ok)
	}
}

func setAssociationReadDeadline(t testing.TB, assoc *ul.Association) {
	t.Helper()
	if assoc == nil || assoc.Conn == nil {
		t.Fatal("nil association connection")
	}
	if err := assoc.Conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	t.Cleanup(func() {
		_ = assoc.Conn.SetReadDeadline(time.Time{})
	})
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
