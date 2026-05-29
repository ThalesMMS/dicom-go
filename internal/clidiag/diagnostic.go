package clidiag

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	pixelframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type Class string

const (
	ClassError    Class = "error"
	ClassFile     Class = "file"
	ClassParse    Class = "parse"
	ClassTransfer Class = "transfer"
	ClassCodec    Class = "codec"
	ClassMedia    Class = "media"
	ClassNetwork  Class = "network"
)

func Classify(err error) Class {
	if err == nil {
		return ClassError
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return ClassFile
	}
	if isParseError(err) {
		return ClassParse
	}
	if errors.Is(err, transfer.ErrUnknownTransferSyntax) || errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		return ClassTransfer
	}
	if isMediaError(err) {
		return ClassMedia
	}
	if isCodecError(err) {
		return ClassCodec
	}
	if isNetworkError(err) {
		return ClassNetwork
	}
	return ClassError
}

func Fprintln(w io.Writer, tool string, err error) {
	if err == nil {
		return
	}
	if tool == "" {
		_, _ = fmt.Fprintf(w, "%s: %v\n", Classify(err), err)
		return
	}
	_, _ = fmt.Fprintf(w, "%s: %s: %v\n", tool, Classify(err), err)
}

func isParseError(err error) bool {
	var parseErr *parser.ParseError
	if errors.As(err, &parseErr) {
		return true
	}
	return errors.Is(err, object.ErrMissingPreamble) ||
		errors.Is(err, object.ErrMissingTransferSyntax) ||
		errors.Is(err, object.ErrFileMeta) ||
		errors.Is(err, object.ErrDataSet) ||
		errors.Is(err, object.ErrInvalidFileMetaGroupLength) ||
		errors.Is(err, object.ErrInvalidFileMetaGroupLengthValue) ||
		errors.Is(err, object.ErrInvalidPreambleLength) ||
		errors.Is(err, parser.ErrOddElementLength) ||
		errors.Is(err, parser.ErrMaxDepthExceeded) ||
		errors.Is(err, parser.ErrMaxElementBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementsExceeded) ||
		errors.Is(err, parser.ErrMaxFragmentsExceeded) ||
		errors.Is(err, parser.ErrMaxTotalBytesExceeded) ||
		errors.Is(err, parser.ErrUnexpectedItemDelimiter) ||
		errors.Is(err, parser.ErrUnexpectedSequenceDelimiter) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

func isCodecError(err error) bool {
	var availability *pixeldata.CodecAvailabilityError
	if errors.As(err, &availability) {
		return true
	}
	return errors.Is(err, pixeldata.ErrPixelDataNotFound) ||
		errors.Is(err, pixeldata.ErrMissingMetadata) ||
		errors.Is(err, pixeldata.ErrInvalidMetadata) ||
		errors.Is(err, pixeldata.ErrCodecNotFound) ||
		errors.Is(err, pixeldata.ErrCodecRegistryNil) ||
		errors.Is(err, pixeldata.ErrCodecNil) ||
		errors.Is(err, pixeldata.ErrCodecUIDInvalid) ||
		errors.Is(err, pixeldata.ErrIncompatiblePixelData) ||
		errors.Is(err, pixeldata.ErrEncapsulatedPixelData) ||
		errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) ||
		errors.Is(err, pixeldata.ErrUnsupportedPhotometricInterpretation) ||
		errors.Is(err, pixeldata.ErrUnsupportedPixelRepresentation) ||
		errors.Is(err, pixeldata.ErrUnsupportedPlanarConfiguration) ||
		errors.Is(err, pixelframe.ErrFrameTypeNotPresent) ||
		errors.Is(err, pixelframe.ErrUnsupportedSamplesPerPixel) ||
		errors.Is(err, pixelframe.ErrUnsupportedBitsAllocated) ||
		errors.Is(err, pixelframe.ErrUnsupportedPhotometricInterpretation) ||
		errors.Is(err, pixelframe.ErrUnsupportedPlanarConfiguration) ||
		errors.Is(err, pixelframe.ErrInvalidFrameMetadata) ||
		errors.Is(err, pixelframe.ErrPixelDataTooShort)
}

func isMediaError(err error) bool {
	var media *pixeldata.MediaPayloadNotRenderableError
	if errors.As(err, &media) {
		return true
	}
	return errors.Is(err, pixeldata.ErrMediaPayloadNotRenderable)
}

func isNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, ul.ErrAssociationRejected) ||
		errors.Is(err, ul.ErrAssociationAborted) ||
		errors.Is(err, ul.ErrUnexpectedPDU) ||
		errors.Is(err, ul.ErrAssociationTimeout) ||
		errors.Is(err, ul.ErrNoAcceptedPresentationContexts) ||
		errors.Is(err, ul.ErrInvalidPDU) ||
		errors.Is(err, ul.ErrPDUTooLarge) ||
		errors.Is(err, ul.ErrInvalidPDUSize) ||
		errors.Is(err, ul.ErrLengthOverflow) ||
		errors.Is(err, ul.ErrUnsupportedPDU) ||
		errors.Is(err, ul.ErrInvalidPDUField) ||
		errors.Is(err, ul.ErrMissingPDUField) ||
		errors.Is(err, ul.ErrInvalidPDUItem) ||
		errors.Is(err, ul.ErrInvalidUserItem) ||
		errors.Is(err, ul.ErrInvalidAEtitle) ||
		errors.Is(err, ul.ErrInvalidPCID) ||
		errors.Is(err, dimse.ErrPresentationContextMismatch)
}
