//go:build (jpegls_charls || codecfull) && (darwin || linux || windows)

package jpegls

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ebitengine/purego"
)

const (
	// QualifiedCharLSVersion is the exact CharLS release audited by codecfull.
	QualifiedCharLSVersion = "2.4.2"

	charlsSuccess = 0

	charlsErrInvalidArgument              = 1
	charlsErrParameterValueNotSupported   = 2
	charlsErrDestinationBufferTooSmall    = 3
	charlsErrSourceBufferTooSmall         = 4
	charlsErrInvalidEncodedData           = 5
	charlsErrTooMuchEncodedData           = 6
	charlsErrInvalidOperation             = 7
	charlsErrBitDepthForTransform         = 8
	charlsErrColorTransformNotSupported   = 9
	charlsErrEncodingNotSupported         = 10
	charlsErrNotEnoughMemory              = 13
	charlsErrUnexpectedFailure            = 14
	charlsErrCallbackFailed               = 27
	charlsErrInvalidArgumentWidth         = 100
	charlsErrInvalidArgumentHeight        = 101
	charlsErrInvalidArgumentComponent     = 102
	charlsErrInvalidArgumentBitsPerSample = 103
	charlsErrInvalidArgumentInterleave    = 104
	charlsErrInvalidArgumentNearLossless  = 105
	charlsErrInvalidArgumentPreset        = 106
	charlsErrInvalidArgumentSize          = 110
	charlsErrInvalidArgumentColor         = 111
	charlsErrInvalidArgumentStride        = 112
	charlsErrInvalidArgumentEncoding      = 113
	charlsErrInvalidParameterWidth        = 200
	charlsErrInvalidParameterHeight       = 201
	charlsErrInvalidParameterComponent    = 202
	charlsErrInvalidParameterBits         = 203
	charlsErrInvalidParameterInterleave   = 204
	charlsErrInvalidParameterNearLossless = 205
	charlsErrInvalidParameterPreset       = 206

	charlsInterleaveNone   = 0
	charlsInterleaveSample = 2

	charlsColorTransformationNone = 0
)

type charlsFrameInfo struct {
	width          uint32
	height         uint32
	bitsPerSample  int32
	componentCount int32
}

type charlsAPI struct {
	versionNumber              func(*int32, *int32, *int32)
	decoderCreate              func() uintptr
	decoderDestroy             func(uintptr)
	decoderSetSourceBuffer     func(uintptr, unsafe.Pointer, uintptr) int32
	decoderReadHeader          func(uintptr) int32
	decoderGetFrameInfo        func(uintptr, *charlsFrameInfo) int32
	decoderGetNearLossless     func(uintptr, int32, *int32) int32
	decoderGetInterleaveMode   func(uintptr, *int32) int32
	decoderGetColorTransform   func(uintptr, *int32) int32
	decoderGetDestinationSize  func(uintptr, uint32, *uintptr) int32
	decoderDecodeToBuffer      func(uintptr, unsafe.Pointer, uintptr, uint32) int32
	encoderCreate              func() uintptr
	encoderDestroy             func(uintptr)
	encoderSetFrameInfo        func(uintptr, *charlsFrameInfo) int32
	encoderSetNearLossless     func(uintptr, int32) int32
	encoderEstimateDestination func(uintptr, *uintptr) int32
	encoderSetDestination      func(uintptr, unsafe.Pointer, uintptr) int32
	encoderEncodeFromBuffer    func(uintptr, unsafe.Pointer, uintptr, uint32) int32
	encoderGetBytesWritten     func(uintptr, *uintptr) int32
}

var (
	charlsOnce    sync.Once
	charlsLoaded  *charlsAPI
	charlsLoadErr error
)

func (charLSDecoder) DecodeJPEGLS(fragment []byte, input DecoderInput) ([]byte, error) {
	if len(fragment) == 0 {
		return nil, fmt.Errorf("charls: empty JPEG-LS fragment")
	}
	api, err := loadCharLSAPI()
	if err != nil {
		return nil, err
	}

	decoder := api.decoderCreate()
	if decoder == 0 {
		return nil, ErrBackendInternal
	}
	defer api.decoderDestroy(decoder)

	source, err := nativeBytes(fragment)
	if err != nil {
		return nil, err
	}
	defer releaseNativeBytes(source)
	if err := charlsErr("set source buffer", api.decoderSetSourceBuffer(decoder, unsafe.Pointer(&source[0]), uintptr(len(source)))); err != nil {
		return nil, err
	}
	if err := charlsStreamErr("read header", api.decoderReadHeader(decoder)); err != nil {
		return nil, err
	}

	var info charlsFrameInfo
	if err := charlsErr("get frame info", api.decoderGetFrameInfo(decoder, &info)); err != nil {
		return nil, err
	}
	if err := validateCharLSFrameInfo(input.Metadata, info); err != nil {
		return nil, err
	}

	var near int32
	if err := charlsErr("get near-lossless", api.decoderGetNearLossless(decoder, 0, &near)); err != nil {
		return nil, err
	}
	if err := validateCharLSNearLossless(input.NearLossless, int(near)); err != nil {
		return nil, err
	}

	var interleave int32
	if err := charlsErr("get interleave mode", api.decoderGetInterleaveMode(decoder, &interleave)); err != nil {
		return nil, err
	}
	if err := validateCharLSInterleave(input.Metadata, interleave); err != nil {
		return nil, err
	}

	var colorTransformation int32
	if err := charlsErr("get color transformation", api.decoderGetColorTransform(decoder, &colorTransformation)); err != nil {
		return nil, err
	}
	if colorTransformation != charlsColorTransformationNone {
		return nil, fmt.Errorf("%w: JPEG-LS color transformation=%d", ErrUnsupportedMetadata, colorTransformation)
	}

	var destinationSize uintptr
	if err := charlsErr("get destination size", api.decoderGetDestinationSize(decoder, 0, &destinationSize)); err != nil {
		return nil, err
	}
	if destinationSize == 0 {
		return nil, fmt.Errorf("%w: decoded destination size is zero", pixeldata.ErrPixelDataSizeMismatch)
	}
	if got, want := int64(destinationSize), input.Metadata.FrameSize(); got != want {
		return nil, fmt.Errorf("%w: CharLS destination size=%d expected=%d", pixeldata.ErrPixelDataSizeMismatch, got, want)
	}
	maxInt := int(^uint(0) >> 1)
	if uint64(destinationSize) > uint64(maxInt) {
		return nil, fmt.Errorf("%w: decoded destination too large: %d", pixeldata.ErrPixelDataSizeMismatch, uint64(destinationSize))
	}

	destination, err := nativeBytes(make([]byte, int(destinationSize)))
	if err != nil {
		return nil, err
	}
	defer releaseNativeBytes(destination)
	if err := charlsStreamErr("decode", api.decoderDecodeToBuffer(decoder, unsafe.Pointer(&destination[0]), destinationSize, 0)); err != nil {
		return nil, err
	}

	return append([]byte(nil), destination...), nil
}

func validateCharLSFrameInfo(metadata pixeldata.Metadata, info charlsFrameInfo) error {
	if got, want := info.width, uint32(metadata.Columns); got != want {
		return fmt.Errorf("%w: JPEG-LS width=%d Columns=%d", pixeldata.ErrPixelDataSizeMismatch, got, want)
	}
	if got, want := info.height, uint32(metadata.Rows); got != want {
		return fmt.Errorf("%w: JPEG-LS height=%d Rows=%d", pixeldata.ErrPixelDataSizeMismatch, got, want)
	}
	if metadata.BitsStored == 0 {
		return fmt.Errorf("%w: BitsStored=0", ErrUnsupportedMetadata)
	}
	if metadata.HighBit != metadata.BitsStored-1 {
		return fmt.Errorf("%w: HighBit=%d BitsStored=%d", ErrUnsupportedMetadata, metadata.HighBit, metadata.BitsStored)
	}
	if got := info.bitsPerSample; got < int32(metadata.BitsStored) || got > int32(metadata.BitsAllocated) {
		return fmt.Errorf(
			"%w: JPEG-LS bits_per_sample=%d outside BitsStored=%d BitsAllocated=%d",
			pixeldata.ErrPixelDataSizeMismatch,
			got,
			metadata.BitsStored,
			metadata.BitsAllocated,
		)
	}
	if got, want := info.componentCount, int32(metadata.SamplesPerPixel); got != want {
		return fmt.Errorf("%w: JPEG-LS component_count=%d SamplesPerPixel=%d", pixeldata.ErrPixelDataSizeMismatch, got, want)
	}
	return nil
}

func validateCharLSNearLossless(expectNearLossless bool, near int) error {
	switch {
	case expectNearLossless && near == 0:
		return fmt.Errorf("%w: near-lossless transfer syntax received JPEG-LS NEAR=0", ErrUnsupportedMetadata)
	case !expectNearLossless && near != 0:
		return fmt.Errorf("%w: lossless transfer syntax received JPEG-LS NEAR=%d", ErrUnsupportedMetadata, near)
	default:
		return nil
	}
}

func validateCharLSInterleave(metadata pixeldata.Metadata, interleave int32) error {
	switch metadata.SamplesPerPixel {
	case 1:
		if interleave != charlsInterleaveNone {
			return fmt.Errorf("%w: monochrome JPEG-LS interleave mode=%d", ErrUnsupportedMetadata, interleave)
		}
	case 3:
		if interleave != charlsInterleaveSample {
			return fmt.Errorf("%w: RGB JPEG-LS interleave mode=%d", ErrUnsupportedMetadata, interleave)
		}
	default:
		return fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedMetadata, metadata.SamplesPerPixel)
	}
	return nil
}

func charlsErr(op string, code int32) error {
	if code == charlsSuccess {
		return nil
	}
	return fmt.Errorf("%w: charls %s failed with code %d", charlsErrorKind(code), op, code)
}

func charlsStreamErr(op string, code int32) error {
	if code == charlsSuccess {
		return nil
	}
	switch code {
	case charlsErrDestinationBufferTooSmall:
		return fmt.Errorf("%w: charls %s failed with code %d", pixeldata.ErrPixelDataSizeMismatch, op, code)
	case charlsErrParameterValueNotSupported,
		charlsErrBitDepthForTransform,
		charlsErrColorTransformNotSupported,
		charlsErrEncodingNotSupported:
		return fmt.Errorf("%w: charls %s failed with code %d", ErrUnsupportedMetadata, op, code)
	case charlsErrNotEnoughMemory,
		charlsErrUnexpectedFailure,
		charlsErrCallbackFailed,
		charlsErrInvalidOperation:
		return fmt.Errorf("%w: charls %s failed with code %d", ErrBackendInternal, op, code)
	default:
		return fmt.Errorf("charls %s failed with code %d", op, code)
	}
}

func charlsErrorKind(code int32) error {
	switch code {
	case charlsErrParameterValueNotSupported,
		charlsErrBitDepthForTransform,
		charlsErrColorTransformNotSupported,
		charlsErrEncodingNotSupported,
		charlsErrInvalidArgument,
		charlsErrInvalidArgumentWidth,
		charlsErrInvalidArgumentHeight,
		charlsErrInvalidArgumentComponent,
		charlsErrInvalidArgumentBitsPerSample,
		charlsErrInvalidArgumentInterleave,
		charlsErrInvalidArgumentNearLossless,
		charlsErrInvalidArgumentPreset,
		charlsErrInvalidArgumentSize,
		charlsErrInvalidArgumentColor,
		charlsErrInvalidArgumentStride,
		charlsErrInvalidArgumentEncoding,
		charlsErrInvalidParameterWidth,
		charlsErrInvalidParameterHeight,
		charlsErrInvalidParameterComponent,
		charlsErrInvalidParameterBits,
		charlsErrInvalidParameterInterleave,
		charlsErrInvalidParameterNearLossless,
		charlsErrInvalidParameterPreset:
		return ErrUnsupportedMetadata
	case charlsErrDestinationBufferTooSmall,
		charlsErrSourceBufferTooSmall:
		return pixeldata.ErrPixelDataSizeMismatch
	case charlsErrNotEnoughMemory,
		charlsErrUnexpectedFailure,
		charlsErrCallbackFailed,
		charlsErrInvalidOperation:
		return ErrBackendInternal
	default:
		return ErrBackendInternal
	}
}

type charlsFrameSpec struct {
	rows            int
	columns         int
	bitsAllocated   int
	samplesPerPixel int
	nearLossless    int
}

// encodeCharLSFrameForTest supports tagged conformance tests.
func encodeCharLSFrameForTest(spec charlsFrameSpec, frame []byte) ([]byte, error) {
	if len(frame) == 0 {
		return nil, fmt.Errorf("charls test encoder: empty source frame")
	}
	api, err := loadCharLSAPI()
	if err != nil {
		return nil, err
	}
	encoder := api.encoderCreate()
	if encoder == 0 {
		return nil, ErrBackendInternal
	}
	defer api.encoderDestroy(encoder)

	info := charlsFrameInfo{
		width:          uint32(spec.columns),
		height:         uint32(spec.rows),
		bitsPerSample:  int32(spec.bitsAllocated),
		componentCount: int32(spec.samplesPerPixel),
	}
	if err := charlsErr("set encoder frame info", api.encoderSetFrameInfo(encoder, &info)); err != nil {
		return nil, err
	}
	if err := charlsErr("set encoder near-lossless", api.encoderSetNearLossless(encoder, int32(spec.nearLossless))); err != nil {
		return nil, err
	}

	var estimated uintptr
	if err := charlsErr("estimate encoder destination size", api.encoderEstimateDestination(encoder, &estimated)); err != nil {
		return nil, err
	}
	if estimated == 0 {
		return nil, fmt.Errorf("charls test encoder: estimated destination size is zero")
	}
	if uint64(estimated) > uint64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("charls test encoder: estimated output too large: %d", uint64(estimated))
	}

	source, err := nativeBytes(frame)
	if err != nil {
		return nil, err
	}
	defer releaseNativeBytes(source)
	destination, err := nativeBytes(make([]byte, int(estimated)))
	if err != nil {
		return nil, err
	}
	defer releaseNativeBytes(destination)
	if err := charlsErr("set encoder destination buffer", api.encoderSetDestination(encoder, unsafe.Pointer(&destination[0]), estimated)); err != nil {
		return nil, err
	}
	if err := charlsErr("encode", api.encoderEncodeFromBuffer(encoder, unsafe.Pointer(&source[0]), uintptr(len(source)), 0)); err != nil {
		return nil, err
	}

	var written uintptr
	if err := charlsErr("get encoded byte count", api.encoderGetBytesWritten(encoder, &written)); err != nil {
		return nil, err
	}
	if written == 0 {
		return nil, fmt.Errorf("charls test encoder: wrote zero bytes")
	}
	if uint64(written) > uint64(len(destination)) {
		return nil, fmt.Errorf("charls test encoder: wrote %d bytes into %d-byte buffer", uint64(written), len(destination))
	}
	return append([]byte(nil), destination[:int(written)]...), nil
}

func loadCharLSAPI() (*charlsAPI, error) {
	charlsOnce.Do(func() {
		charlsLoaded, charlsLoadErr = openCharLSAPI()
	})
	return charlsLoaded, charlsLoadErr
}

// ValidateRuntime loads CharLS and verifies the supported ABI before a
// codecfull application starts accepting studies.
func ValidateRuntime() error {
	_, err := loadCharLSAPI()
	return err
}

// ValidateQualifiedRuntime verifies the exact CharLS release audited by the
// codecfull profile. Optional non-release adapters may use ValidateRuntime to
// accept ABI-compatible 2.x releases.
func ValidateQualifiedRuntime() error {
	api, err := loadCharLSAPI()
	if err != nil {
		return err
	}
	return validateQualifiedCharLSAPI(api)
}

func validateQualifiedCharLSAPI(api *charlsAPI) error {
	var major, minor, patch int32
	api.versionNumber(&major, &minor, &patch)
	version := fmt.Sprintf("%d.%d.%d", major, minor, patch)
	if version != QualifiedCharLSVersion {
		return fmt.Errorf(
			"%w: CharLS runtime %s is not the qualified codecfull release %s",
			ErrDecoderUnavailable,
			version,
			QualifiedCharLSVersion,
		)
	}
	return nil
}

func validateCharLSAPI(api *charlsAPI) error {
	var major, minor, patch int32
	api.versionNumber(&major, &minor, &patch)
	if major != 2 || minor < 4 {
		return fmt.Errorf(
			"%w: incompatible CharLS runtime %d.%d.%d; require ABI-compatible CharLS 2.4 or newer within major version 2",
			ErrDecoderUnavailable,
			major,
			minor,
			patch,
		)
	}
	return nil
}

func bindCharLSAPI(handle uintptr) (api *charlsAPI, err error) {
	defer func() {
		if r := recover(); r != nil {
			api = nil
			err = fmt.Errorf("%w: missing CharLS symbol: %v", ErrDecoderUnavailable, r)
		}
	}()
	api = &charlsAPI{}
	purego.RegisterLibFunc(&api.versionNumber, handle, "charls_get_version_number")
	purego.RegisterLibFunc(&api.decoderCreate, handle, "charls_jpegls_decoder_create")
	purego.RegisterLibFunc(&api.decoderDestroy, handle, "charls_jpegls_decoder_destroy")
	purego.RegisterLibFunc(&api.decoderSetSourceBuffer, handle, "charls_jpegls_decoder_set_source_buffer")
	purego.RegisterLibFunc(&api.decoderReadHeader, handle, "charls_jpegls_decoder_read_header")
	purego.RegisterLibFunc(&api.decoderGetFrameInfo, handle, "charls_jpegls_decoder_get_frame_info")
	purego.RegisterLibFunc(&api.decoderGetNearLossless, handle, "charls_jpegls_decoder_get_near_lossless")
	purego.RegisterLibFunc(&api.decoderGetInterleaveMode, handle, "charls_jpegls_decoder_get_interleave_mode")
	purego.RegisterLibFunc(&api.decoderGetColorTransform, handle, "charls_jpegls_decoder_get_color_transformation")
	purego.RegisterLibFunc(&api.decoderGetDestinationSize, handle, "charls_jpegls_decoder_get_destination_size")
	purego.RegisterLibFunc(&api.decoderDecodeToBuffer, handle, "charls_jpegls_decoder_decode_to_buffer")
	purego.RegisterLibFunc(&api.encoderCreate, handle, "charls_jpegls_encoder_create")
	purego.RegisterLibFunc(&api.encoderDestroy, handle, "charls_jpegls_encoder_destroy")
	purego.RegisterLibFunc(&api.encoderSetFrameInfo, handle, "charls_jpegls_encoder_set_frame_info")
	purego.RegisterLibFunc(&api.encoderSetNearLossless, handle, "charls_jpegls_encoder_set_near_lossless")
	purego.RegisterLibFunc(&api.encoderEstimateDestination, handle, "charls_jpegls_encoder_get_estimated_destination_size")
	purego.RegisterLibFunc(&api.encoderSetDestination, handle, "charls_jpegls_encoder_set_destination_buffer")
	purego.RegisterLibFunc(&api.encoderEncodeFromBuffer, handle, "charls_jpegls_encoder_encode_from_buffer")
	purego.RegisterLibFunc(&api.encoderGetBytesWritten, handle, "charls_jpegls_encoder_get_bytes_written")
	return api, nil
}
