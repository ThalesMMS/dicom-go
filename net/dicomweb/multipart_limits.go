package dicomweb

import "io"

// multipartHeaderLimitReader observes the wire representation before
// mime/multipart parses and allocates MIME headers. It passes bytes through
// unchanged, but stops at the first byte beyond the configured header limit.
type multipartHeaderLimitReader struct {
	reader         io.Reader
	delimiter      string
	maxHeaderBytes int
	phase          multipartWirePhase
	lineState      multipartBoundaryLineState
	match          int
	headerBytes    int
	headerLine     int
	headerLast     byte
	failure        error
}

type multipartWirePhase uint8

const (
	multipartWirePreamble multipartWirePhase = iota
	multipartWireHeaders
	multipartWireBody
	multipartWireEpilogue
)

type multipartBoundaryLineState uint8

const (
	multipartBoundaryMatching multipartBoundaryLineState = iota
	multipartBoundarySuffix
	multipartBoundaryFirstCloseDash
	multipartBoundaryOpenPadding
	multipartBoundaryClosePadding
	multipartBoundaryOpenCR
	multipartBoundaryCloseCR
	multipartBoundaryInvalid
)

func newMultipartHeaderLimitReader(reader io.Reader, boundary string, maximum int) *multipartHeaderLimitReader {
	return &multipartHeaderLimitReader{
		reader:         reader,
		delimiter:      "--" + boundary,
		maxHeaderBytes: maximum,
		phase:          multipartWirePreamble,
		lineState:      multipartBoundaryMatching,
	}
}

func (r *multipartHeaderLimitReader) Read(buffer []byte) (int, error) {
	if r.failure != nil {
		return 0, r.failure
	}
	n, readErr := r.reader.Read(buffer)
	for index := 0; index < n; index++ {
		if err := r.observe(buffer[index]); err != nil {
			r.failure = err
			if index > 0 {
				return index, nil
			}
			return 0, err
		}
	}
	return n, readErr
}

func (r *multipartHeaderLimitReader) Err() error {
	return r.failure
}

func (r *multipartHeaderLimitReader) observe(character byte) error {
	if r.phase == multipartWireHeaders {
		r.headerBytes++
		if r.headerBytes > r.maxHeaderBytes {
			return &ResponseDecodeError{
				Kind: ResponseDecodeHeaderBytes, Limit: int64(r.maxHeaderBytes), Value: int64(r.headerBytes),
			}
		}
		if character == '\n' {
			if r.headerLine == 0 || r.headerLine == 1 && r.headerLast == '\r' {
				r.phase = multipartWireBody
				r.resetBoundaryLine()
			}
			r.headerLine = 0
			r.headerLast = 0
			return nil
		}
		r.headerLine++
		r.headerLast = character
		return nil
	}
	if r.phase == multipartWireEpilogue {
		return nil
	}

	r.observeBoundaryLine(character)
	return nil
}

func (r *multipartHeaderLimitReader) observeBoundaryLine(character byte) {
	lineEnded := character == '\n'
	boundaryKind := multipartWirePhase(0)
	boundaryFound := false

	switch r.lineState {
	case multipartBoundaryMatching:
		if r.match < len(r.delimiter) && character == r.delimiter[r.match] {
			r.match++
			if r.match == len(r.delimiter) {
				r.lineState = multipartBoundarySuffix
			}
		} else {
			r.lineState = multipartBoundaryInvalid
		}
	case multipartBoundarySuffix:
		switch character {
		case '-':
			r.lineState = multipartBoundaryFirstCloseDash
		case ' ', '\t':
			r.lineState = multipartBoundaryOpenPadding
		case '\r':
			r.lineState = multipartBoundaryOpenCR
		case '\n':
			boundaryKind, boundaryFound = multipartWireHeaders, true
		default:
			r.lineState = multipartBoundaryInvalid
		}
	case multipartBoundaryFirstCloseDash:
		if character == '-' {
			r.lineState = multipartBoundaryClosePadding
		} else {
			r.lineState = multipartBoundaryInvalid
		}
	case multipartBoundaryOpenPadding:
		switch character {
		case ' ', '\t':
		case '\r':
			r.lineState = multipartBoundaryOpenCR
		case '\n':
			boundaryKind, boundaryFound = multipartWireHeaders, true
		default:
			r.lineState = multipartBoundaryInvalid
		}
	case multipartBoundaryClosePadding:
		switch character {
		case ' ', '\t':
		case '\r':
			r.lineState = multipartBoundaryCloseCR
		case '\n':
			boundaryKind, boundaryFound = multipartWireEpilogue, true
		default:
			r.lineState = multipartBoundaryInvalid
		}
	case multipartBoundaryOpenCR:
		if character == '\n' {
			boundaryKind, boundaryFound = multipartWireHeaders, true
		} else {
			r.lineState = multipartBoundaryInvalid
		}
	case multipartBoundaryCloseCR:
		if character == '\n' {
			boundaryKind, boundaryFound = multipartWireEpilogue, true
		} else {
			r.lineState = multipartBoundaryInvalid
		}
	}

	if boundaryFound {
		r.phase = boundaryKind
		if boundaryKind == multipartWireHeaders {
			r.headerBytes = 0
			r.headerLine = 0
			r.headerLast = 0
		}
	}
	if lineEnded && !boundaryFound {
		r.resetBoundaryLine()
	}
}

func (r *multipartHeaderLimitReader) resetBoundaryLine() {
	r.lineState = multipartBoundaryMatching
	r.match = 0
}
