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
)

type Token struct {
	Kind    TokenKind
	Header  core.ElementHeader
	Element core.Element
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
	default:
		return "unknown"
	}
}
