package jpegxladapter

import (
	"errors"
	"io"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// ServeHelper speaks the private JPEG XL helper protocol on r/w until
// shutdown or channel close. decode must not retain fragment. The worker
// logs no patient, study, series, path, or other clinical identifiers.
func ServeHelper(r io.Reader, w io.Writer, decode func([]byte, pixeldata.Metadata) ([]byte, error)) error {
	if decode == nil {
		decode = func([]byte, pixeldata.Metadata) ([]byte, error) {
			return nil, ErrMalformedCodestream
		}
	}
	for {
		req, err := readHelperRequest(r, defaultMaxHelperPayloadBytes)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		switch req.Opcode {
		case helperOpcodeHello:
			if err := writeHelperReply(w, helperReply{RequestID: req.RequestID, Status: helperStatusOK}); err != nil {
				return err
			}
		case helperOpcodeShutdown:
			return nil
		case helperOpcodeDecode:
			if req.Rows > math.MaxUint16 || req.Columns > math.MaxUint16 ||
				req.Samples > math.MaxUint16 || req.BitsAllocated > math.MaxUint16 {
				if err := writeHelperReply(w, helperReply{
					RequestID: req.RequestID,
					Status:    helperStatusUnsupported,
				}); err != nil {
					return err
				}
				continue
			}
			meta := pixeldata.Metadata{
				Rows:            uint16(req.Rows),
				Columns:         uint16(req.Columns),
				SamplesPerPixel: uint16(req.Samples),
				BitsAllocated:   uint16(req.BitsAllocated),
			}
			pixels, err := decode(req.Payload, meta)
			reply := helperReply{
				RequestID: req.RequestID,
				Rows:      uint32(meta.Rows),
				Columns:   uint32(meta.Columns),
				Samples:   uint32(meta.SamplesPerPixel),
			}
			if err != nil {
				reply.Status = helperReplyStatus(err)
			} else {
				reply.Pixels = pixels
			}
			if writeErr := writeHelperReply(w, reply); writeErr != nil {
				return writeErr
			}
		default:
			if err := writeHelperReply(w, helperReply{
				RequestID: req.RequestID,
				Status:    helperStatusProtocol,
			}); err != nil {
				return err
			}
		}
	}
}
