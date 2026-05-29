package dimse

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

// MaxCommandSetBytes bounds one reassembled DIMSE command set across all PDVs.
const MaxCommandSetBytes = 4 << 20

var ErrPresentationContextMismatch = errors.New("dicom dimse: presentation context mismatch")

// ErrCommandSetTooLarge reports that a peer exceeded MaxCommandSetBytes
// before terminating the command PDV stream.
var ErrCommandSetTooLarge = errors.New("dicom dimse: command set too large")

type PDataWriter struct {
	assoc     *ul.Association
	ctx       context.Context
	pcID      byte
	isCommand bool
	maxPDU    int
	buf       []byte
	finished  bool
}

func NewPDataWriter(assoc *ul.Association, pcID byte, isCommand bool, maxPDU int) *PDataWriter {
	return NewPDataWriterWithContext(nil, assoc, pcID, isCommand, maxPDU)
}

// NewPDataWriterWithContext creates a fragmenting DIMSE P-DATA writer whose
// socket writes observe ctx. A nil context inherits the association context.
func NewPDataWriterWithContext(ctx context.Context, assoc *ul.Association, pcID byte, isCommand bool, maxPDU int) *PDataWriter {
	return &PDataWriter{
		assoc:     assoc,
		ctx:       ctx,
		pcID:      pcID,
		isCommand: isCommand,
		maxPDU:    maxPDU,
	}
}

func (w *PDataWriter) Write(p []byte) (int, error) {
	if w == nil {
		return 0, fmt.Errorf("dicom dimse: nil PDataWriter")
	}
	if w.finished {
		return 0, fmt.Errorf("dicom dimse: write after P-DATA finish")
	}
	payloadMax := w.maxPayload()
	written := 0
	for len(p) > 0 {
		space := payloadMax - len(w.buf)
		if space <= 0 {
			if err := w.flush(false); err != nil {
				return written, err
			}
			space = payloadMax
		}
		if space > len(p) {
			space = len(p)
		}
		w.buf = append(w.buf, p[:space]...)
		p = p[space:]
		written += space
	}
	return written, nil
}

func (w *PDataWriter) Finish() error {
	if w == nil {
		return fmt.Errorf("dicom dimse: nil PDataWriter")
	}
	if w.finished {
		return nil
	}
	w.finished = true
	return w.flush(true)
}

func (w *PDataWriter) flush(isLast bool) error {
	if w.assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	if !isLast && len(w.buf) == 0 {
		return nil
	}
	data := w.buf
	pdu := &ul.PDataTF{Values: []ul.PDataValue{{
		PresentationContextID: w.pcID,
		IsCommand:             w.isCommand,
		IsLast:                isLast,
		Data:                  data,
	}}}
	var err error
	if w.ctx != nil {
		err = w.assoc.Send(w.ctx, pdu)
	} else {
		err = w.assoc.WritePDU(pdu)
	}
	// Association.WritePDU serializes synchronously and copies Data into its PDU
	// body before returning, so the writer can safely reclaim this backing array
	// without a defensive full-payload clone.
	w.buf = data[:0]
	return err
}

func (w *PDataWriter) maxPayload() int {
	maxPDU := w.maxPDU
	if maxPDU <= 0 {
		maxPDU = int(ul.DefaultMaxPDU + ul.PDUHeaderSize)
	}
	payloadMax := maxPDU - int(ul.PDUHeaderSize+ul.PDVHeaderSize)
	if payloadMax < 1 {
		return 1
	}
	return payloadMax
}

type PDataReader struct {
	assoc         *ul.Association
	ctx           context.Context
	pcID          byte
	expectedType  *bool
	buf           []byte
	bufOffset     int
	lastReceived  bool
	receivedBytes int
}

func NewPDataReader(assoc *ul.Association, pcID byte) *PDataReader {
	return &PDataReader{assoc: assoc, pcID: pcID}
}

func newTypedPDataReader(assoc *ul.Association, pcID byte, isCommand bool) *PDataReader {
	return &PDataReader{assoc: assoc, pcID: pcID, expectedType: &isCommand}
}

func newTypedPDataReaderWithContext(ctx context.Context, assoc *ul.Association, pcID byte, isCommand bool) *PDataReader {
	return &PDataReader{assoc: assoc, ctx: ctx, pcID: pcID, expectedType: &isCommand}
}

func (r *PDataReader) Read(p []byte) (int, error) {
	if r == nil {
		return 0, fmt.Errorf("dicom dimse: nil PDataReader")
	}
	if len(p) == 0 {
		return 0, nil
	}
	for r.bufferedLen() == 0 && !r.lastReceived {
		r.resetConsumedBuffer()
		if err := r.readNextPData(); err != nil {
			return 0, err
		}
	}
	if r.bufferedLen() == 0 && r.lastReceived {
		r.resetConsumedBuffer()
		return 0, io.EOF
	}
	n := copy(p, r.buf[r.bufOffset:])
	r.bufOffset += n
	if r.bufOffset == len(r.buf) {
		r.resetConsumedBuffer()
	}
	return n, nil
}

func (r *PDataReader) bufferedLen() int {
	return len(r.buf) - r.bufOffset
}

func (r *PDataReader) resetConsumedBuffer() {
	if r.bufOffset == len(r.buf) {
		r.buf = r.buf[:0]
		r.bufOffset = 0
	}
}

func (r *PDataReader) readNextPData() error {
	values, err := receivePDataValues(r.ctx, r.assoc)
	if err != nil {
		return err
	}
	if err := r.consumePDataValues(values); err != nil {
		return err
	}
	if !r.lastReceived {
		ctx := r.ctx
		if ctx == nil && r.assoc != nil {
			ctx = r.assoc.Context
		}
		r.ctx = ul.WithActiveTransfer(ctx)
	}
	return nil
}

func (r *PDataReader) consumePDataValues(values []ul.PDataValue) error {
	for i, value := range values {
		if r.lastReceived {
			return storePDataCarryoverWithContext(r.ctx, r.assoc, values[i:])
		}
		if value.PresentationContextID != r.pcID {
			return fmt.Errorf("%w: got %d, want %d", ErrPresentationContextMismatch, value.PresentationContextID, r.pcID)
		}
		if r.expectedType != nil && value.IsCommand != *r.expectedType {
			return fmt.Errorf("dicom dimse: P-DATA command flag %t, want %t", value.IsCommand, *r.expectedType)
		}
		if r.expectedType != nil && *r.expectedType {
			if err := checkCommandSetSize(r.receivedBytes, len(value.Data)); err != nil {
				return err
			}
		}
		if len(value.Data) > int(^uint(0)>>1)-r.receivedBytes {
			return fmt.Errorf("dicom dimse: P-DATA byte count overflow")
		}
		r.receivedBytes += len(value.Data)
		r.buf = append(r.buf, value.Data...)
		if value.IsLast {
			r.lastReceived = true
			return storePDataCarryoverWithContext(r.ctx, r.assoc, values[i+1:])
		}
	}
	return nil
}

func appendCommandSetFragment(buf []byte, fragment []byte) ([]byte, error) {
	if err := checkCommandSetSize(len(buf), len(fragment)); err != nil {
		return nil, err
	}
	return append(buf, fragment...), nil
}

func checkCommandSetSize(current, additional int) error {
	if current < 0 || additional < 0 || current > MaxCommandSetBytes || additional > MaxCommandSetBytes-current {
		return fmt.Errorf("%w: %d existing byte(s) plus %d incoming byte(s) exceeds %d", ErrCommandSetTooLarge, current, additional, MaxCommandSetBytes)
	}
	return nil
}

func receivePDataValues(ctx context.Context, assoc *ul.Association) ([]ul.PDataValue, error) {
	if assoc == nil {
		return nil, fmt.Errorf("dicom dimse: nil association")
	}
	values, err := takePDataCarryoverWithContext(ctx, assoc)
	if err != nil {
		return nil, err
	}
	if len(values) > 0 {
		return values, nil
	}
	var pdu ul.PDU
	if ctx != nil {
		pdu, err = assoc.Receive(ctx)
	} else {
		pdu, err = assoc.ReadPDU()
	}
	if err != nil {
		return nil, err
	}
	pdata, ok := pdu.(*ul.PDataTF)
	if !ok {
		return nil, fmt.Errorf("%w: got %T while waiting for P-DATA-TF", ul.ErrUnexpectedPDU, pdu)
	}
	return pdata.Values, nil
}

func takePDataCarryoverWithContext(ctx context.Context, assoc *ul.Association) ([]ul.PDataValue, error) {
	if assoc == nil {
		return nil, nil
	}
	return assoc.TakePDataCarryoverContext(ctx)
}

func storePDataCarryoverWithContext(ctx context.Context, assoc *ul.Association, values []ul.PDataValue) error {
	if assoc == nil {
		return nil
	}
	return assoc.StorePDataCarryoverContext(ctx, values)
}
