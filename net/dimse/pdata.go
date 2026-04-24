package dimse

import (
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

var ErrPresentationContextMismatch = errors.New("dicom dimse: presentation context mismatch")

type PDataWriter struct {
	assoc     *ul.Association
	pcID      byte
	isCommand bool
	maxPDU    int
	buf       []byte
	finished  bool
}

func NewPDataWriter(assoc *ul.Association, pcID byte, isCommand bool, maxPDU int) *PDataWriter {
	return &PDataWriter{
		assoc:     assoc,
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
	data := append([]byte(nil), w.buf...)
	w.buf = w.buf[:0]
	return w.assoc.WritePDU(&ul.PDataTF{Values: []ul.PDataValue{{
		PresentationContextID: w.pcID,
		IsCommand:             w.isCommand,
		IsLast:                isLast,
		Data:                  data,
	}}})
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
	assoc        *ul.Association
	pcID         byte
	expectedType *bool
	buf          []byte
	lastReceived bool
}

func NewPDataReader(assoc *ul.Association, pcID byte) *PDataReader {
	return &PDataReader{assoc: assoc, pcID: pcID}
}

func newTypedPDataReader(assoc *ul.Association, pcID byte, isCommand bool) *PDataReader {
	return &PDataReader{assoc: assoc, pcID: pcID, expectedType: &isCommand}
}

func (r *PDataReader) Read(p []byte) (int, error) {
	if r == nil {
		return 0, fmt.Errorf("dicom dimse: nil PDataReader")
	}
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.buf) == 0 && !r.lastReceived {
		if err := r.readNextPData(); err != nil {
			return 0, err
		}
	}
	if len(r.buf) == 0 && r.lastReceived {
		return 0, io.EOF
	}
	n := copy(p, r.buf)
	copy(r.buf, r.buf[n:])
	r.buf = r.buf[:len(r.buf)-n]
	return n, nil
}

func (r *PDataReader) readNextPData() error {
	if r.assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	pdu, err := r.assoc.ReadPDU()
	if err != nil {
		return err
	}
	pdata, ok := pdu.(*ul.PDataTF)
	if !ok {
		return fmt.Errorf("%w: got %T while waiting for P-DATA-TF", ul.ErrUnexpectedPDU, pdu)
	}
	for _, value := range pdata.Values {
		if value.PresentationContextID != r.pcID {
			return fmt.Errorf("%w: got %d, want %d", ErrPresentationContextMismatch, value.PresentationContextID, r.pcID)
		}
		if r.expectedType != nil && value.IsCommand != *r.expectedType {
			return fmt.Errorf("dicom dimse: P-DATA command flag %t, want %t", value.IsCommand, *r.expectedType)
		}
		r.buf = append(r.buf, value.Data...)
		if value.IsLast {
			r.lastReceived = true
		}
	}
	return nil
}
