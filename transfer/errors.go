package transfer

import "errors"

var (
	ErrUnknownTransferSyntax     = errors.New("dicom: unknown transfer syntax")
	ErrUnsupportedTransferSyntax = errors.New("dicom: unsupported transfer syntax")
)
