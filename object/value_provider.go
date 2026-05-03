package object

import (
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/parser"
)

// readerValueProvider replays a seekable source to stream raw value bytes for
// elements that were not materialized in memory.
//
// It scans from the beginning of the data set each time. This keeps the design
// simple and avoids retaining offsets in the core model.
//
// Limitations:
//   - Only works when the original input passed to object.ReadFileWithOptions is
//     an io.ReadSeeker (e.g., *os.File).
//   - Only supports defined-length primitive values.
//   - Undefined-length values (e.g., sequences, encapsulated Pixel Data) are not
//     supported.
//
// Future improvements can cache element offsets or add a real value source type.
//
// NOTE: This is an internal helper; the public entrypoint is Object.CopyValueTo.

type readerValueProvider struct {
	reader *parser.Reader
}

func (p *readerValueProvider) CopyValueTo(tag core.Tag, w io.Writer) (int64, error) {
	if p == nil || p.reader == nil {
		return 0, fmt.Errorf("dicom: nil value provider")
	}
	return p.reader.CopyElementValueTo(tag, w)
}
