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
// The parser records value offsets for skipped/deferred values while reading.
// CopyValueTo uses those offsets directly when available and falls back to a
// tag scan for readers created before an offset was recorded.
//
// Limitations:
//   - Only works when the original input passed to object.ReadFileWithOptions is
//     an io.ReadSeeker (e.g., *os.File).
//   - Supports defined-length primitive values and undefined-length encapsulated
//     Pixel Data values. General SQ replay is a permanent, intentional
//     non-goal (see docs/CONFORMANCE.md "Known Limitations" and #278);
//     sequence item values are always materialized instead.
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
