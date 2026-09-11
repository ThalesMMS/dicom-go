package codecfixture

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/jpegfixture"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/builtin"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeglossless"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const jpegLosslessSV1RGB8InterleavedBase64 = "/9j/7gAOQWRvYmUAZAAAAAAA/8MAEQgAAwAEA1IRAEcRAEIRAP/EABsAAQACAwEBAAAAAAAAAAAAAAgABwIFBgED/9oADANSAEcAQgABAAA/n8/v+QAP+QAP/wByz2G/752txd0CQhH9HxfVvGnT3LUt79u9ej//2Q=="

const (
	SizeSmall  SizeClass = "small"
	SizeMedium SizeClass = "medium"
	SizeLarge  SizeClass = "large"

	ErrorNone                   ErrorKind = ""
	ErrorUnknown                ErrorKind = "unknown"
	ErrorDecodeMismatch         ErrorKind = "decode_mismatch"
	ErrorUnsupportedMetadata    ErrorKind = "unsupported_metadata"
	ErrorMalformedStream        ErrorKind = "malformed_stream"
	ErrorMissingOptionalAdapter ErrorKind = "missing_optional_adapter"
	ErrorDependencyUnavailable  ErrorKind = "dependency_unavailable"
	ErrorMetadataMismatch       ErrorKind = "metadata_mismatch"
)

var (
	ErrDecodeMismatch        = errors.New("codecfixture: decoded frames do not match expected frames")
	ErrUnexpectedSuccess     = errors.New("codecfixture: decode succeeded but expected an error")
	ErrUnexpectedError       = errors.New("codecfixture: decode failed unexpectedly")
	ErrUnexpectedErrorKind   = errors.New("codecfixture: decode failed with unexpected error kind")
	ErrDependencyUnavailable = errors.New("codecfixture: optional codec dependency unavailable")
)

var (
	tagSOPClassUID               = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID            = core.NewTag(0x0008, 0x0018)
	tagStudyDate                 = core.NewTag(0x0008, 0x0020)
	tagModality                  = core.NewTag(0x0008, 0x0060)
	tagStudyDescription          = core.NewTag(0x0008, 0x1030)
	tagSeriesDescription         = core.NewTag(0x0008, 0x103e)
	tagPatientName               = core.NewTag(0x0010, 0x0010)
	tagPatientID                 = core.NewTag(0x0010, 0x0020)
	tagStudyInstanceUID          = core.NewTag(0x0020, 0x000d)
	tagSeriesInstanceUID         = core.NewTag(0x0020, 0x000e)
	tagInstanceNumber            = core.NewTag(0x0020, 0x0013)
	tagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	tagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	tagPlanarConfiguration       = core.NewTag(0x0028, 0x0006)
	tagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	tagRows                      = core.NewTag(0x0028, 0x0010)
	tagColumns                   = core.NewTag(0x0028, 0x0011)
	tagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	tagBitsStored                = core.NewTag(0x0028, 0x0101)
	tagHighBit                   = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
)

// SizeClass groups synthetic cases for benchmark smoke reporting.
type SizeClass string

// ErrorKind is the normalized conformance outcome for a decode failure.
type ErrorKind string

// Provenance documents where fixture bytes came from.
type Provenance struct {
	Source     string
	Synthetic  bool
	License    string
	Permission string
	NoPHI      bool
	Notes      string
}

// Case is a synthetic pixel-data conformance fixture.
type Case struct {
	Name            string
	Description     string
	Syntax          transfer.Syntax
	Size            SizeClass
	Provenance      Provenance
	Elements        []core.Element
	ExpectedFrames  [][]byte
	ExpectedError   ErrorKind
	Tolerance       byte
	SamplePolicy    *SamplePolicy
	DecodedLayout   *SampleLayout
	ReferenceLayout *SampleLayout
	RegisterCodecs  func(pixeldata.Registry) error
}

// Result captures the raw outcome of running a case through a registry.
type Result struct {
	Frames     pixeldata.Frames
	Err        error
	Kind       ErrorKind
	Comparison ComparisonReport
}

// Object builds a fresh DICOM object for the case.
func (c Case) Object() *object.Object {
	return object.FromElements(cloneElements(c.Elements), std.Dictionary)
}

// File builds a Part 10 file model for the case.
func (c Case) File() *object.File {
	return &object.File{
		Dataset:        c.Object(),
		TransferSyntax: c.Syntax,
	}
}

// Part10Bytes serializes the case as a DICOM Part 10 file.
func (c Case) Part10Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, c.File()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PixelData extracts a fresh PixelData value from the case.
func (c Case) PixelData() (pixeldata.PixelData, error) {
	return pixeldata.Extract(c.Object())
}

// Registry builds a fresh registry for this case.
func (c Case) Registry() (*pixeldata.MemoryRegistry, error) {
	registry := pixeldata.NewMemoryRegistry()
	if c.RegisterCodecs != nil {
		if err := c.RegisterCodecs(registry); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// RunCase decodes a case and classifies the outcome.
func RunCase(registry pixeldata.Registry, c Case) Result {
	obj := c.Object()
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		return Result{Err: err, Kind: ClassifyError(err)}
	}
	if registry == nil {
		var buildErr error
		registry, buildErr = c.Registry()
		if buildErr != nil {
			return Result{Err: buildErr, Kind: ClassifyError(buildErr)}
		}
	}
	frames, err := registry.DecodeFrames(c.Syntax.UID, pixel, obj)
	if err != nil {
		return Result{Err: err, Kind: ClassifyError(err)}
	}
	comparison, err := compareCaseSamples(c, frames)
	if err != nil {
		return Result{Frames: frames, Err: err, Kind: ClassifyError(err), Comparison: comparison}
	}
	return Result{Frames: frames, Comparison: comparison}
}

// ValidateCase returns nil when the case outcome matches its expectation.
func ValidateCase(registry pixeldata.Registry, c Case) error {
	result := RunCase(registry, c)
	if c.ExpectedError == ErrorNone {
		if result.Err != nil {
			return result.Err
		}
		return nil
	}
	if result.Err == nil {
		return fmt.Errorf("%w: %s expected %s", ErrUnexpectedSuccess, c.Name, c.ExpectedError)
	}
	if result.Kind != c.ExpectedError {
		return fmt.Errorf("%w: %s got %s want %s: %w", ErrUnexpectedErrorKind, c.Name, result.Kind, c.ExpectedError, result.Err)
	}
	return nil
}

// ClassifyError normalizes codec and registry errors for conformance reports.
func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ErrorNone
	}
	switch {
	case errors.Is(err, ErrDecodeMismatch):
		return ErrorDecodeMismatch
	case errors.Is(err, ErrDependencyUnavailable) || errorChainContains(err, "decoder unavailable"):
		return ErrorDependencyUnavailable
	case errors.Is(err, pixeldata.ErrCodecNotFound), errors.Is(err, pixeldata.ErrCodecRegistryNil):
		return ErrorMissingOptionalAdapter
	case errors.Is(err, pixeldata.ErrPixelDataSizeMismatch),
		errors.Is(err, jpeg.ErrImageSizeMismatch),
		errors.Is(err, jpeglossless.ErrImageSizeMismatch):
		return ErrorMetadataMismatch
	case errors.Is(err, jpeg.ErrInvalidFragment),
		errors.Is(err, jpeglossless.ErrInvalidStream),
		errors.Is(err, rle.ErrInvalidHeader),
		errors.Is(err, rle.ErrInvalidSegmentCount),
		errors.Is(err, rle.ErrInvalidSegmentOffset),
		errors.Is(err, rle.ErrSegmentDecodeFailed):
		return ErrorMalformedStream
	case errors.Is(err, pixeldata.ErrUnsupportedPhotometricInterpretation),
		errors.Is(err, pixeldata.ErrUnsupportedPixelRepresentation),
		errors.Is(err, pixeldata.ErrUnsupportedPlanarConfiguration),
		errors.Is(err, jpeg.ErrUnsupportedBitsAllocated),
		errors.Is(err, jpeg.ErrUnsupportedBitsStored),
		errors.Is(err, jpeg.ErrUnsupportedHighBit),
		errors.Is(err, jpeg.ErrUnsupportedSamplesPerPixel),
		errors.Is(err, jpeglossless.ErrUnsupportedBitsAllocated),
		errors.Is(err, jpeglossless.ErrUnsupportedScan),
		errors.Is(err, rle.ErrUnsupportedBitsAllocated),
		errors.Is(err, rle.ErrUnsupportedSamplesPerPixel):
		return ErrorUnsupportedMetadata
	default:
		return ErrorUnknown
	}
}

func errorChainContains(err error, text string) bool {
	if err == nil {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(text)) {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if errorChainContains(child, text) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return errorChainContains(wrapped.Unwrap(), text)
	}
	return false
}

// RegisterBuiltinCodecs registers built-in pure-Go codecs used by the baseline.
func RegisterBuiltinCodecs(registry pixeldata.Registry) error {
	return builtin.Register(registry)
}

// NativeSmall returns a 2x2 native Explicit VR Little Endian case.
func NativeSmall() Case {
	return nativeCase("native-small", SizeSmall, 2, 2, [][]byte{{0, 64, 128, 255}})
}

// NativeMedium returns a 64x64 native Explicit VR Little Endian case.
func NativeMedium() Case {
	return nativeCase("native-medium", SizeMedium, 64, 64, [][]byte{sequentialBytes(64 * 64)})
}

// NativeLarge returns a 256x256 native Explicit VR Little Endian case.
func NativeLarge() Case {
	return nativeCase("native-large", SizeLarge, 256, 256, [][]byte{sequentialBytes(256 * 256)})
}

// NativeMultiFrame returns a 2-frame native case.
func NativeMultiFrame() Case {
	return nativeCase("native-multiframe", SizeSmall, 2, 2, [][]byte{{0, 1, 2, 3}, {4, 5, 6, 7}})
}

// RLELosslessSmall returns a supported RLE Lossless case.
func RLELosslessSmall() Case {
	frame := []byte{0, 64, 128, 255}
	return encapsulatedCase("rle-lossless-small", transfer.RLELossless, SizeSmall, 2, 2, [][]byte{frame}, ErrorNone, rleFragment(frame), RegisterBuiltinCodecs)
}

// JPEGBaselineSmall returns a supported JPEG Baseline 8-bit case.
func JPEGBaselineSmall() Case {
	frame := []byte{0, 255}
	fragment := mustEncodeGrayJPEG(2, 1, frame)
	c := encapsulatedCase("jpeg-baseline-small", transfer.JPEGBaseline, SizeSmall, 1, 2, [][]byte{frame}, ErrorNone, fragment, RegisterBuiltinCodecs)
	c.Tolerance = 32
	c.SamplePolicy = smallJPEGPolicy()
	return c
}

// JPEGExtendedSmall returns a supported JPEG Extended 8-bit case.
func JPEGExtendedSmall() Case {
	frame := []byte{0, 255}
	fragment := makeJPEGExtendedSOF1(mustEncodeGrayJPEG(2, 1, frame))
	c := encapsulatedCase("jpeg-extended-small", transfer.JPEGExtended, SizeSmall, 1, 2, [][]byte{frame}, ErrorNone, fragment, RegisterBuiltinCodecs)
	c.Tolerance = 32
	c.SamplePolicy = smallJPEGPolicy()
	return c
}

func smallJPEGPolicy() *SamplePolicy {
	return &SamplePolicy{MaxAbsoluteError: 32, MinPSNR: 35, MaxTileMeanAbsoluteError: 4, Rationale: "8-bit synthetic two-pixel DCT boundary: existing max-abs 32 supplemented by >=35 dB and tile mean <=4; source comparison is a regression, not independent decoder evidence"}
}

// JPEGExtendedProcess4Mono12 returns an independently encoded JPEG Extended
// Process 4/SOF1 12-bit unsigned monochrome case. It shares the complete
// independent libjpeg-turbo reconstruction with pixeldata/jpeg.
func JPEGExtendedProcess4Mono12() Case {
	fragment, err := base64.StdEncoding.DecodeString(jpegfixture.Process4Base64)
	if err != nil {
		panic("codecfixture: invalid embedded JPEG Extended Process 4 fixture: " + err.Error())
	}
	c := encapsulatedCase("jpeg-extended-process4-mono12", transfer.JPEGExtended, SizeSmall, 8, 8, nil, ErrorNone, fragment, RegisterBuiltinCodecs)
	c.Description = "independently encoded JPEG Extended Process 4/SOF1 12-bit unsigned monochrome case"
	c.Provenance.Source = "libjpeg-turbo 3.1.0 cjpeg -precision 12 from synthetic pixels"
	c.Provenance.Permission = "generated non-PHI test vector"
	c.Elements = replaceElement(c.Elements, uint16Element(tagBitsAllocated, 16))
	c.Elements = replaceElement(c.Elements, uint16Element(tagBitsStored, 12))
	c.Elements = replaceElement(c.Elements, uint16Element(tagHighBit, 11))
	frame := make([]byte, 8*8*2)
	for i, sample := range jpegfixture.ReferenceSamples() {
		binary.LittleEndian.PutUint16(frame[i*2:], sample)
	}
	c.ExpectedFrames = [][]byte{frame}
	c.SamplePolicy = &SamplePolicy{MaxAbsoluteError: 8, MinPSNR: 54, MaxTileMeanAbsoluteError: 4,
		Rationale: "12-bit sequential DCT: independent integer versus floating IDCT rounding; existing max-abs 8, PSNR >=54 dB and 8x8 mean <=4 sample units"}
	return c
}

// JPEGLosslessSmall returns a supported JPEG Lossless Process 14 case.
func JPEGLosslessSmall() Case {
	frame := []byte{0, 64, 128, 255}
	samples := []int32{0, 64, 128, 255}
	fragment := encodeLossless(2, 2, 8, 1, samples)
	return encapsulatedCase("jpeg-lossless-small", transfer.JPEGLosslessNonHierarchical, SizeSmall, 2, 2, [][]byte{frame}, ErrorNone, fragment, RegisterBuiltinCodecs)
}

// JPEGLosslessSV1RGB8Interleaved returns an independently encoded JPEG
// Lossless Process 14 SV1 RGB case with exact native interleaved reference
// samples.
func JPEGLosslessSV1RGB8Interleaved() Case {
	fragment, err := base64.StdEncoding.DecodeString(jpegLosslessSV1RGB8InterleavedBase64)
	if err != nil {
		panic("codecfixture: invalid embedded JPEG Lossless SV1 RGB fixture: " + err.Error())
	}
	frame := []byte{
		0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255,
		1, 2, 3, 17, 33, 65, 254, 253, 252, 128, 64, 32,
		5, 250, 125, 99, 100, 101, 200, 10, 220, 255, 255, 255,
	}
	c := encapsulatedCase("jpeg-lossless-sv1-rgb8-interleaved", transfer.JPEGLosslessSV1, SizeSmall, 3, 4, [][]byte{frame}, ErrorNone, fragment, RegisterBuiltinCodecs)
	c.Description = "independently encoded JPEG Lossless Process 14 SV1 8-bit unsigned RGB case"
	c.Provenance = Provenance{
		Source:     "libjpeg-turbo 3.1.0 cjpeg -precision 8 -lossless 1,0 -rgb -sample 1x1,1x1,1x1 from a synthetic 4x3 RGB PPM",
		Synthetic:  true,
		License:    "generated non-PHI test vector; libjpeg-turbo BSD-3-Clause",
		Permission: "generated from synthetic source for redistribution with the repository",
		NoPHI:      true,
		Notes:      "codestream SHA-256 c9643dc57a6e631865084223af411c514d52edc30caaa3fe99eab5c9561d7e68; exact reference from djpeg -strict -rgb -pnm",
	}
	c.Elements = replaceElement(c.Elements, uint16Element(tagSamplesPerPixel, 3))
	c.Elements = replaceElement(c.Elements, stringElement(tagPhotometricInterpretation, core.VRCS, "RGB"))
	c.Elements = replaceElement(c.Elements, uint16Element(tagPlanarConfiguration, 0))
	return c
}

// MalformedJPEGExtended returns a JPEG case with malformed encoded bytes.
func MalformedJPEGExtended() Case {
	return encapsulatedCase("jpeg-extended-malformed", transfer.JPEGExtended, SizeSmall, 1, 2, nil, ErrorMalformedStream, []byte{0xff, 0xd8, 0x00, 0x01}, RegisterBuiltinCodecs)
}

// UnsupportedMetadataJPEGExtended returns a JPEG case with unsupported metadata.
func UnsupportedMetadataJPEGExtended() Case {
	c := JPEGExtendedSmall()
	c.Name = "jpeg-extended-unsupported-metadata"
	c.Elements = withDeterministicUIDs(c.Elements, c.Name+"|"+c.Syntax.UID)
	c.Elements = replaceElement(c.Elements, uint16Element(tagBitsAllocated, 16))
	c.ExpectedFrames = nil
	c.ExpectedError = ErrorUnsupportedMetadata
	return c
}

// MetadataMismatchRLE returns an RLE case whose frame count and fragments differ.
func MetadataMismatchRLE() Case {
	c := RLELosslessSmall()
	c.Name = "rle-metadata-mismatch"
	c.Elements = withDeterministicUIDs(c.Elements, c.Name+"|"+c.Syntax.UID)
	c.Elements = replaceElement(c.Elements, stringElement(tagNumberOfFrames, core.VRIS, "2"))
	c.ExpectedFrames = nil
	c.ExpectedError = ErrorMetadataMismatch
	return c
}

// MissingJPEGXLAdapter returns a recognized JPEG XL syntax with no registered adapter.
func MissingJPEGXLAdapter() Case {
	return encapsulatedCase("jpegxl-missing-adapter", transfer.JPEGXL, SizeSmall, 2, 2, nil, ErrorMissingOptionalAdapter, []byte{0xff, 0x0a, 0x20, 0x01}, nil)
}

// DependencyUnavailableJPEGLS returns a JPEG-LS case with a registered unavailable dependency.
func DependencyUnavailableJPEGLS() Case {
	return encapsulatedCase("jpegls-dependency-unavailable", transfer.JPEGLSLossless, SizeSmall, 1, 2, nil, ErrorDependencyUnavailable, []byte("encoded-jpegls"), RegisterUnavailableJPEGLS)
}

// DependencyUnavailableJPEGXL returns a JPEG XL case with a registered codec
// boundary that reports unavailable dependency.
func DependencyUnavailableJPEGXL() Case {
	return encapsulatedCase("jpegxl-dependency-unavailable", transfer.JPEGXL, SizeSmall, 2, 2, nil, ErrorDependencyUnavailable, []byte{0xff, 0x0a, 0x20, 0x01}, RegisterUnavailableJPEGXL)
}

// JPEGLSLosslessSmall builds a JPEG-LS Lossless case from caller-supplied codestream bytes.
func JPEGLSLosslessSmall(fragment []byte, expectedFrame []byte) Case {
	return JPEGLSLossless(2, 2, fragment, expectedFrame)
}

// JPEGLSLossless builds a JPEG-LS Lossless case from caller-supplied codestream bytes.
func JPEGLSLossless(rows, columns int, fragment []byte, expectedFrame []byte) Case {
	return encapsulatedCase("jpegls-lossless-small", transfer.JPEGLSLossless, SizeSmall, rows, columns, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
}

// JPEG2000LosslessSmall builds a JPEG 2000 case from caller-supplied codestream bytes.
func JPEG2000LosslessSmall(fragment []byte, expectedFrame []byte) Case {
	return JPEG2000Lossless(2, 2, fragment, expectedFrame)
}

// JPEG2000Lossless builds a JPEG 2000 case from caller-supplied codestream bytes.
func JPEG2000Lossless(rows, columns int, fragment []byte, expectedFrame []byte) Case {
	return encapsulatedCase("jpeg2000-lossless-small", transfer.JPEG2000LosslessOnly, SizeSmall, rows, columns, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
}

// JPEG2000Lossy builds a JPEG 2000 lossy case from caller-supplied codestream bytes.
func JPEG2000Lossy(rows, columns int, fragment []byte, expectedFrame []byte, tolerance byte) Case {
	c := encapsulatedCase("jpeg2000-lossy-small", transfer.JPEG2000, SizeSmall, rows, columns, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
	c.Tolerance = tolerance
	return c
}

// JPEG2000Part2Lossless builds a JPEG 2000 Part 2 lossless case from caller-supplied codestream bytes.
func JPEG2000Part2Lossless(rows, columns int, fragment []byte, expectedFrame []byte) Case {
	return encapsulatedCase("jpeg2000-part2-lossless-small", transfer.JPEG2000Part2Lossless, SizeSmall, rows, columns, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
}

// JPEG2000Part2Lossy builds a JPEG 2000 Part 2 lossy case from caller-supplied codestream bytes.
func JPEG2000Part2Lossy(rows, columns int, fragment []byte, expectedFrame []byte, tolerance byte) Case {
	c := encapsulatedCase("jpeg2000-part2-lossy-small", transfer.JPEG2000Part2, SizeSmall, rows, columns, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
	c.Tolerance = tolerance
	return c
}

// HTJ2KLosslessSmall builds an HTJ2K case from caller-supplied codestream bytes.
func HTJ2KLosslessSmall(fragment []byte, expectedFrame []byte) Case {
	return encapsulatedCase("htj2k-lossless-small", transfer.HTJ2KLossless, SizeSmall, 2, 2, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
}

// HTJ2KLosslessRPCL builds an HTJ2K RPCL lossless case from caller-supplied codestream bytes.
func HTJ2KLosslessRPCL(rows, columns int, fragment []byte, expectedFrame []byte) Case {
	return encapsulatedCase("htj2k-lossless-rpcl-small", transfer.HTJ2KLosslessRPCL, SizeSmall, rows, columns, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
}

// HTJ2KLossy builds an HTJ2K lossy case from caller-supplied codestream bytes.
func HTJ2KLossy(rows, columns int, fragment []byte, expectedFrame []byte, tolerance byte) Case {
	c := encapsulatedCase("htj2k-lossy-small", transfer.HTJ2K, SizeSmall, rows, columns, [][]byte{append([]byte(nil), expectedFrame...)}, ErrorNone, fragment, nil)
	c.Tolerance = tolerance
	return c
}

// RegisterUnavailableJPEGLS registers a JPEG-LS codec that reports unavailable dependency.
func RegisterUnavailableJPEGLS(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return registry.RegisterCodec(transfer.JPEGLSLossless.UID, unavailableCodec{})
}

// RegisterUnavailableJPEGXL registers a JPEG XL codec that reports unavailable dependency.
func RegisterUnavailableJPEGXL(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return registry.RegisterCodec(transfer.JPEGXL.UID, unavailableCodec{})
}

func nativeCase(name string, size SizeClass, rows, columns int, frames [][]byte) Case {
	raw := bytes.Join(frames, nil)
	return Case{
		Name:           name,
		Description:    "synthetic native uncompressed grayscale case",
		Syntax:         transfer.ExplicitVRLittleEndian,
		Size:           size,
		Provenance:     syntheticProvenance(),
		Elements:       metadataElements(name+"|"+transfer.ExplicitVRLittleEndian.UID, rows, columns, len(frames), rawElement(raw)),
		ExpectedFrames: cloneFrames(frames),
	}
}

func encapsulatedCase(name string, syntax transfer.Syntax, size SizeClass, rows, columns int, frames [][]byte, want ErrorKind, fragment []byte, register func(pixeldata.Registry) error) Case {
	return Case{
		Name:           name,
		Description:    "synthetic encapsulated grayscale case",
		Syntax:         syntax,
		Size:           size,
		Provenance:     syntheticProvenance(),
		Elements:       metadataElements(name+"|"+syntax.UID, rows, columns, len(frames), fragmentElement(fragment)),
		ExpectedFrames: cloneFrames(frames),
		ExpectedError:  want,
		RegisterCodecs: register,
	}
}

func syntheticProvenance() Provenance {
	return Provenance{
		Source:     "generated synthetic pixels",
		Synthetic:  true,
		License:    "repository test fixture",
		Permission: "generated in-tree",
		NoPHI:      true,
	}
}

func metadataElements(uidSeed string, rows, columns, frames int, pixelElement core.Element) []core.Element {
	if frames <= 0 {
		frames = 1
	}
	return []core.Element{
		stringElement(tagSOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
		stringElement(tagSOPInstanceUID, core.VRUI, deterministicUID(1, uidSeed)),
		stringElement(tagPatientName, core.VRPN, "SYNTHETIC^CODECFIXTURE"),
		stringElement(tagPatientID, core.VRLO, "SYNTHETIC-CODEC"),
		stringElement(tagModality, core.VRCS, "CT"),
		stringElement(tagStudyDate, core.VRDA, "20260622"),
		stringElement(tagStudyDescription, core.VRLO, "Synthetic Codec Fixture"),
		stringElement(tagSeriesDescription, core.VRLO, "Codec Conformance"),
		stringElement(tagStudyInstanceUID, core.VRUI, deterministicUID(2, uidSeed)),
		stringElement(tagSeriesInstanceUID, core.VRUI, deterministicUID(3, uidSeed)),
		stringElement(tagInstanceNumber, core.VRIS, "1"),
		stringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		stringElement(tagNumberOfFrames, core.VRIS, strconv.Itoa(frames)),
		uint16Element(tagSamplesPerPixel, 1),
		uint16Element(tagRows, uint16(rows)),
		uint16Element(tagColumns, uint16(columns)),
		uint16Element(tagBitsAllocated, 8),
		uint16Element(tagBitsStored, 8),
		uint16Element(tagHighBit, 7),
		uint16Element(tagPixelRepresentation, 0),
		pixelElement,
	}
}

func withDeterministicUIDs(elements []core.Element, uidSeed string) []core.Element {
	out := cloneElements(elements)
	out = replaceElement(out, stringElement(tagSOPInstanceUID, core.VRUI, deterministicUID(1, uidSeed)))
	out = replaceElement(out, stringElement(tagStudyInstanceUID, core.VRUI, deterministicUID(2, uidSeed)))
	out = replaceElement(out, stringElement(tagSeriesInstanceUID, core.VRUI, deterministicUID(3, uidSeed)))
	return out
}

func deterministicUID(component int, seed string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.Itoa(component)))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write([]byte(seed))
	return fmt.Sprintf("1.2.826.0.1.3680043.10.543.132.%d.%d", component, h.Sum32())
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue{value},
	}
}

func uint16Element(tag core.Tag, value uint16) core.Element {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	return core.NewRawElement(tag, core.VRUS, raw)
}

func rawElement(raw []byte) core.Element {
	return core.NewRawElement(core.TagPixelData, core.VROB, raw)
}

func fragmentElement(fragments ...[]byte) core.Element {
	cloned := cloneFrames(fragments)
	return core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: cloned},
	}
}

func compareFrames(c Case, got pixeldata.Frames) error {
	_, err := compareCaseSamples(c, got)
	return err
}

func cloneElements(elements []core.Element) []core.Element {
	out := make([]core.Element, len(elements))
	for i := range elements {
		out[i] = elements[i]
		out[i].Value = cloneValue(elements[i].Value)
	}
	return out
}

func cloneValue(value core.Value) core.Value {
	switch v := value.(type) {
	case nil:
		return nil
	case core.RawValue:
		return core.RawValue(core.CloneBytes(v))
	case core.StringValue:
		return append(core.StringValue(nil), v...)
	case core.Uint16Value:
		return append(core.Uint16Value(nil), v...)
	case core.Int16Value:
		return append(core.Int16Value(nil), v...)
	case core.Uint32Value:
		return append(core.Uint32Value(nil), v...)
	case core.Int32Value:
		return append(core.Int32Value(nil), v...)
	case core.Uint64Value:
		return append(core.Uint64Value(nil), v...)
	case core.Int64Value:
		return append(core.Int64Value(nil), v...)
	case core.Float32Value:
		return append(core.Float32Value(nil), v...)
	case core.Float64Value:
		return append(core.Float64Value(nil), v...)
	case core.TagValue:
		return append(core.TagValue(nil), v...)
	case core.FragmentSequence:
		return core.FragmentSequence{
			OffsetTable: core.CloneBytes(v.OffsetTable),
			Fragments:   cloneFrames(v.Fragments),
		}
	case core.SequenceValue:
		items := make([]core.DataSet, len(v.Items))
		for i := range v.Items {
			items[i].Elements = cloneElements(v.Items[i].Elements)
		}
		return core.SequenceValue{Items: items}
	default:
		return value
	}
}

func cloneFrames(frames [][]byte) [][]byte {
	out := make([][]byte, len(frames))
	for i := range frames {
		out[i] = append([]byte(nil), frames[i]...)
	}
	return out
}

func replaceElement(elements []core.Element, replacement core.Element) []core.Element {
	out := cloneElements(elements)
	for i := range out {
		if out[i].Header.Tag == replacement.Header.Tag {
			out[i] = replacement
			return out
		}
	}
	return append(out, replacement)
}

func sequentialBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}

func rleFragment(segments ...[]byte) []byte {
	encodedSegments := make([][]byte, len(segments))
	for i, segment := range segments {
		encodedSegments[i] = packLiteral(segment)
	}
	fragment := make([]byte, 64)
	binary.LittleEndian.PutUint32(fragment[:4], uint32(len(encodedSegments)))
	offset := uint32(64)
	for i, segment := range encodedSegments {
		binary.LittleEndian.PutUint32(fragment[4+i*4:], offset)
		offset += uint32(len(segment))
	}
	for _, segment := range encodedSegments {
		fragment = append(fragment, segment...)
	}
	return fragment
}

func packLiteral(data []byte) []byte {
	var out []byte
	for len(data) > 0 {
		n := len(data)
		if n > 128 {
			n = 128
		}
		out = append(out, byte(n-1))
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return out
}

func mustEncodeGrayJPEG(width, height int, pixels []byte) []byte {
	img := image.NewGray(image.Rect(0, 0, width, height))
	if len(pixels) != len(img.Pix) {
		panic(fmt.Sprintf("codecfixture: pixel count = %d, want %d", len(pixels), len(img.Pix)))
	}
	copy(img.Pix, pixels)
	for i := range img.Pix {
		img.SetGray(i%width, i/width, color.Gray{Y: img.Pix[i]})
	}
	var buf bytes.Buffer
	if err := stdjpeg.Encode(&buf, img, &stdjpeg.Options{Quality: 100}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func makeJPEGExtendedSOF1(data []byte) []byte {
	out := append([]byte(nil), data...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == 0xff && out[i+1] == 0xc0 {
			out[i+1] = 0xc1
			return out
		}
	}
	panic("codecfixture: encoded JPEG did not contain SOF0 marker")
}

type losslessBitWriter struct {
	out   []byte
	cur   byte
	nbits int
}

func (bw *losslessBitWriter) writeBits(v, n int) {
	for i := n - 1; i >= 0; i-- {
		bw.cur = (bw.cur << 1) | byte((v>>uint(i))&1)
		bw.nbits++
		if bw.nbits == 8 {
			bw.flushByte()
		}
	}
}

func (bw *losslessBitWriter) flushByte() {
	bw.out = append(bw.out, bw.cur)
	if bw.cur == 0xff {
		bw.out = append(bw.out, 0x00)
	}
	bw.cur = 0
	bw.nbits = 0
}

func (bw *losslessBitWriter) pad() {
	if bw.nbits > 0 {
		bw.cur = (bw.cur << uint(8-bw.nbits)) | byte((1<<uint(8-bw.nbits))-1)
		bw.nbits = 8
		bw.flushByte()
	}
}

func bitLen(v int32) int {
	if v < 0 {
		v = -v
	}
	n := 0
	for v > 0 {
		n++
		v >>= 1
	}
	return n
}

func encodeLossless(width, height, precision, predictor int, samples []int32) []byte {
	var out []byte
	out = append(out, 0xff, 0xd8)
	out = append(out, 0xff, 0xc3, 0x00, 0x0b, byte(precision),
		byte(height>>8), byte(height), byte(width>>8), byte(width), 0x01,
		0x01, 0x11, 0x00)
	dht := []byte{0xff, 0xc4, 0x00, 0x24, 0x00,
		0, 0, 0, 0, 17, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	for s := 0; s <= 16; s++ {
		dht = append(dht, byte(s))
	}
	out = append(out, dht...)
	out = append(out, 0xff, 0xda, 0x00, 0x08, 0x01, 0x01, 0x00, byte(predictor), 0x00, 0x00)

	bw := &losslessBitWriter{}
	defaultPred := int32(1) << (precision - 1)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			px := predict(samples, x, y, width, predictor, defaultPred)
			diff := samples[y*width+x] - px
			s := bitLen(diff)
			bw.writeBits(s, 5)
			if s > 0 {
				enc := diff
				if diff < 0 {
					enc = diff + (1 << uint(s)) - 1
				}
				bw.writeBits(int(enc)&((1<<uint(s))-1), s)
			}
		}
	}
	bw.pad()
	out = append(out, bw.out...)
	out = append(out, 0xff, 0xd9)
	return out
}

func predict(samples []int32, x, y, width, predictor int, defaultPred int32) int32 {
	if x == 0 && y == 0 {
		return defaultPred
	}
	left := func() int32 { return samples[y*width+(x-1)] }
	up := func() int32 { return samples[(y-1)*width+x] }
	upperLeft := func() int32 { return samples[(y-1)*width+(x-1)] }
	if y == 0 {
		return left()
	}
	if x == 0 {
		return up()
	}
	a, b, c := left(), up(), upperLeft()
	switch predictor {
	case 1:
		return a
	case 2:
		return b
	case 3:
		return c
	case 4:
		return a + b - c
	case 5:
		return a + ((b - c) >> 1)
	case 6:
		return b + ((a - c) >> 1)
	case 7:
		return (a + b) / 2
	default:
		return a
	}
}

type unavailableCodec struct{}

func (unavailableCodec) Decode(pixeldata.PixelData, *object.Object) (pixeldata.Frames, error) {
	return pixeldata.Frames{}, ErrDependencyUnavailable
}
