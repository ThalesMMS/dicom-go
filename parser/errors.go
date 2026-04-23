package parser

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

var ErrOddElementLength = errors.New("dicom: element length must be even")
var ErrMaxDepthExceeded = errors.New("dicom: maximum sequence nesting depth exceeded")
var ErrMaxElementBytesExceeded = errors.New("dicom: maximum element value size exceeded")
var ErrMaxElementsExceeded = errors.New("dicom: maximum element count exceeded")
var ErrMaxFragmentsExceeded = errors.New("dicom: maximum fragment count exceeded")
var ErrMaxTotalBytesExceeded = errors.New("dicom: maximum total bytes exceeded")
var ErrUnexpectedItemDelimiter = errors.New("dicom: item delimitation tag outside of item context")
var ErrUnexpectedSequenceDelimiter = errors.New("dicom: sequence delimitation tag outside of sequence context")

type Op string

const (
	OpReadTag            Op = "read tag"
	OpReadVR             Op = "read VR"
	OpReadLength         Op = "read length"
	OpReadReserved       Op = "read reserved bytes"
	OpValidateReserved   Op = "validate reserved bytes"
	OpReadValue          Op = "read value"
	OpCheckDepth         Op = "check depth"
	OpCheckElementCount  Op = "check element count"
	OpCheckFragmentCount Op = "check fragment count"
	OpCheckTotalBytes    Op = "check total bytes"
	OpWriteTag           Op = "write tag"
	OpWriteVR            Op = "write VR"
	OpWriteReserved      Op = "write reserved bytes"
	OpWriteLength        Op = "write length"
	OpWriteValue         Op = "write value"
)

type ParseError struct {
	Op     Op
	Offset int64
	Tag    core.Tag
	VR     core.VR
	Length core.Length
	Err    error
}

func (e *ParseError) Error() string {
	if e == nil {
		return "<nil>"
	}

	var b strings.Builder
	b.WriteString("dicom: ")
	if e.Op != "" {
		b.WriteString(string(e.Op))
	} else {
		b.WriteString("parse")
	}
	fmt.Fprintf(&b, " at offset %d", e.Offset)
	if e.Tag != (core.Tag{}) {
		fmt.Fprintf(&b, " for tag %s", e.Tag)
	}
	if e.VR != "" {
		fmt.Fprintf(&b, " with VR %s", e.VR)
	}
	if e.Length != 0 || e.Length.IsUndefined() || e.Op == OpReadValue {
		fmt.Fprintf(&b, " with length %s", e.Length)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *ParseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type WriteError struct {
	Op     Op
	Tag    core.Tag
	VR     core.VR
	Length core.Length
	Err    error
}

func (e *WriteError) Error() string {
	if e == nil {
		return "<nil>"
	}

	var b strings.Builder
	b.WriteString("dicom: ")
	if e.Op != "" {
		b.WriteString(string(e.Op))
	} else {
		b.WriteString("write")
	}
	if e.Tag != (core.Tag{}) {
		fmt.Fprintf(&b, " for tag %s", e.Tag)
	}
	if e.VR != "" {
		fmt.Fprintf(&b, " with VR %s", e.VR)
	}
	if e.Length != 0 || e.Length.IsUndefined() || e.Op == OpWriteLength || e.Op == OpWriteValue {
		fmt.Fprintf(&b, " with length %s", e.Length)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *WriteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
