// Package dicomxml converts DICOM data sets to and from the PS3.19 Native
// DICOM Model XML representation.
package dicomxml

import (
	"context"
	"encoding/binary"
	"errors"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	// Namespace is the namespace of the PS3.19 Native DICOM Model.
	Namespace = "http://dicom.nema.org/PS3.19/models/NativeDICOM"
	// MediaType is the PS3.18 media type for Native DICOM Model metadata.
	MediaType = "application/dicom+xml"
)

var (
	ErrInvalidXML             = errors.New("dicomxml: invalid Native DICOM Model XML")
	ErrInvalidLimits          = errors.New("dicomxml: invalid resource limits")
	ErrMaxXMLBytesExceeded    = errors.New("dicomxml: maximum XML bytes exceeded")
	ErrMaxValueBytesExceeded  = errors.New("dicomxml: maximum value bytes exceeded")
	ErrMaxInlineBytesExceeded = errors.New("dicomxml: maximum InlineBinary bytes exceeded")
	ErrMaxBinaryBytesExceeded = errors.New("dicomxml: maximum total binary bytes exceeded")
	ErrMaxDepthExceeded       = errors.New("dicomxml: maximum sequence depth exceeded")
	ErrMaxElementsExceeded    = errors.New("dicomxml: maximum element count exceeded")
	ErrMaxItemsExceeded       = errors.New("dicomxml: maximum sequence item count exceeded")
	ErrBulkDataRejected       = errors.New("dicomxml: BulkData rejected by policy")
	ErrBulkDataResolver       = errors.New("dicomxml: BulkData resolver failed")
	ErrTransferSyntaxRequired = errors.New("dicomxml: transfer syntax required for encapsulated Pixel Data")
)

// Error adds an operation and XML path to a conversion error. Callers can use
// errors.Is to classify the wrapped sentinel or context error.
type Error struct {
	Op   string
	Path string
	Err  error
}

func (e *Error) Error() string {
	if e.Path == "" {
		return "dicomxml: " + e.Op + ": " + e.Err.Error()
	}
	return "dicomxml: " + e.Op + " at " + e.Path + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Limits bounds XML conversion. A zero field uses the corresponding safe
// default. Negative fields are invalid. To accept larger data, callers must
// opt in by setting explicit larger limits.
type Limits struct {
	MaxXMLBytes          int64
	MaxValueBytes        int
	MaxInlineBinaryBytes int64
	MaxTotalBinaryBytes  int64
	MaxSequenceDepth     int
	MaxElements          int
	MaxSequenceItems     int
}

// DefaultLimits returns the finite resource limits used by zero-valued
// Options and UnmarshalOptions.
func DefaultLimits() Limits {
	return Limits{
		MaxXMLBytes:          64 << 20,
		MaxValueBytes:        16 << 20,
		MaxInlineBinaryBytes: 32 << 20,
		MaxTotalBinaryBytes:  64 << 20,
		MaxSequenceDepth:     64,
		MaxElements:          1_000_000,
		MaxSequenceItems:     100_000,
	}
}

// Options configures Native DICOM Model marshaling.
type Options struct {
	Pretty bool
	// OmitGroupLength is retained for option symmetry with dicomjson. Native
	// DICOM Model output always omits group-length elements as PS3.19 requires,
	// regardless of this value.
	OmitGroupLength bool
	Dictionary      dictionary.DataDictionary
	// ByteOrder controls raw numeric and binary Value Field interpretation.
	// Nil uses object.Object.ValueByteOrder.
	ByteOrder binary.ByteOrder
	// BulkDataURIFunc may replace an inline binary Value Field with a URI.
	// Returning an empty URI keeps InlineBinary. Existing core.BulkDataValue
	// references are always preserved and never resolved by Marshal.
	BulkDataURIFunc func(tag core.Tag, vr core.VR, data []byte) string
	Limits          Limits
}

// DefaultOptions returns standards-compliant, bounded marshaling options.
func DefaultOptions() Options {
	return Options{Pretty: true, OmitGroupLength: true, Limits: DefaultLimits()}
}

// BulkDataPolicy controls whether decoding preserves, rejects, or explicitly
// resolves a BulkData reference. PreserveBulkData performs no network or file
// access and is the default.
type BulkDataPolicy uint8

const (
	PreserveBulkData BulkDataPolicy = iota
	RejectBulkData
	ResolveBulkData
)

// BulkDataReference describes one PS3.19 BulkData element.
type BulkDataReference struct {
	Tag  core.Tag
	VR   core.VR
	URI  string
	UUID string
}

// BulkDataResolver opens content for a BulkData reference. It is called only
// under ResolveBulkData. The decoder closes the returned reader and applies
// binary byte limits while reading it.
type BulkDataResolver func(context.Context, BulkDataReference) (io.ReadCloser, error)

// UnmarshalOptions configures Native DICOM Model decoding.
type UnmarshalOptions struct {
	TextOptions object.TextOptions
	// AllowMissingKeyword accepts a known DICOM tag without its PS3.6 keyword.
	// The default is strict because PS3.19 requires keyword when the recipient's
	// dictionary knows the data element.
	AllowMissingKeyword bool
	// ByteOrder controls raw numeric and binary Value Field construction. Nil
	// uses TransferSyntax.ByteOrder, then Little Endian when no syntax is known.
	ByteOrder binary.ByteOrder
	// TransferSyntax is needed only to reconstruct encapsulated Pixel Data
	// carried in InlineBinary.
	TransferSyntax   transfer.Syntax
	BulkDataPolicy   BulkDataPolicy
	BulkDataResolver BulkDataResolver
	Limits           Limits
}

// DefaultUnmarshalOptions returns bounded options that preserve BulkData
// references without dereferencing them.
func DefaultUnmarshalOptions() UnmarshalOptions {
	return UnmarshalOptions{ByteOrder: binary.LittleEndian, Limits: DefaultLimits()}
}
