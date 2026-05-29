package parser

import "github.com/ThalesMMS/dicom-go/core"

type TokenKind uint8

const (
	TokenElement TokenKind = iota
	TokenStartSequence
	TokenStartPixelSequence
	TokenEndSequence
	TokenStartItem
	TokenEndItem
	// TokenStop is emitted once when a SelectiveReader selector requests an
	// early stop. The token carries the just-read element header and its offset;
	// no value bytes have been consumed. Subsequent calls to Next return io.EOF.
	TokenStop
)

type Token struct {
	Kind    TokenKind
	Header  core.ElementHeader
	Element core.Element
	// Offset is the absolute encoded position of the token's tag.
	Offset int64
}

func (k TokenKind) String() string {
	switch k {
	case TokenElement:
		return "element"
	case TokenStartSequence:
		return "sequence start"
	case TokenStartPixelSequence:
		return "pixel sequence start"
	case TokenEndSequence:
		return "sequence end"
	case TokenStartItem:
		return "item start"
	case TokenEndItem:
		return "item end"
	case TokenStop:
		return "stop"
	default:
		return "unknown"
	}
}
