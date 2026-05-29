package pixeldata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrEncoderNotFound            = errors.New("dicom: no pixel data encoder registered for transfer syntax")
	ErrEncoderRegistryNil         = errors.New("dicom: pixel data encoder registry is nil")
	ErrEncoderNil                 = errors.New("dicom: pixel data encoder is nil")
	ErrEncoderUIDInvalid          = errors.New("dicom: pixel data encoder transfer syntax UID is empty")
	ErrEncoderAlreadyRegistered   = errors.New("dicom: pixel data encoder transfer syntax is already registered")
	ErrEncoderCapabilitiesInvalid = errors.New("dicom: invalid pixel data encoder capabilities")
	ErrUnsupportedEncoderMetadata = errors.New("dicom: pixel metadata is unsupported by encoder")
	ErrEncoderFailed              = errors.New("dicom: pixel data encoder failed")
	ErrEncoderOutputInvalid       = errors.New("dicom: pixel data encoder returned invalid output")
)

// EncoderCapabilities is an immutable description of one frame encoder's
// accepted native input and encoded output. Native frame bytes are
// little-endian and sample-interleaved, matching ExtractNativeFrames and the
// output of DecompressDataSet.
type EncoderCapabilities struct {
	TransferSyntaxUID          string
	BitsAllocated              []uint16
	PixelRepresentations       []uint16
	SamplesPerPixel            []uint16
	PhotometricInterpretations []string
	// OutputPhotometricInterpretations and OutputPlanarConfigurations declare
	// metadata transforms that EncodedFrame may report. Empty slices mean that
	// the encoder preserves the corresponding input metadata.
	OutputPhotometricInterpretations []string
	OutputPlanarConfigurations       []uint16
	Lossless                         bool
	LossyMethod                      string
	SupportsMultiFrame               bool
	Backend                          string
}

// EncodedFrame contains one encoded frame. Empty metadata fields preserve the
// source values; non-empty values describe a real, encoder-validated transform
// that the transcoder must apply consistently across all frames.
type EncodedFrame struct {
	Data                      []byte
	PhotometricInterpretation string
	PlanarConfiguration       *uint16
}

// FrameEncoder encodes one canonical native frame. Implementations must be safe
// for concurrent use when shared by an EncoderRegistry.
type FrameEncoder interface {
	Capabilities() EncoderCapabilities
	EncodeFrame(context.Context, []byte, Metadata) (EncodedFrame, error)
}

// EncoderRegistry is the explicit registration and lookup boundary for frame
// encoders. Unlike decoder compatibility helpers, there is intentionally no
// mutable package-level default encoder registry.
type EncoderRegistry interface {
	RegisterEncoder(uid string, encoder FrameEncoder) error
	GetEncoder(uid string) (FrameEncoder, bool)
	EncodeFrame(ctx context.Context, uid string, frame []byte, metadata Metadata) (EncodedFrame, error)
}

// MemoryEncoderRegistry stores explicit frame encoder registrations.
type MemoryEncoderRegistry struct {
	mu    sync.RWMutex
	byUID map[string]registeredFrameEncoder
}

type registeredFrameEncoder struct {
	encoder      FrameEncoder
	capabilities EncoderCapabilities
}

func (e registeredFrameEncoder) Capabilities() EncoderCapabilities {
	return cloneEncoderCapabilities(e.capabilities)
}

func (e registeredFrameEncoder) EncodeFrame(ctx context.Context, frame []byte, metadata Metadata) (EncodedFrame, error) {
	return e.encoder.EncodeFrame(ctx, frame, metadata)
}

// EncoderAvailabilityError describes an encoder that was not registered.
type EncoderAvailabilityError struct {
	Err                   error
	TransferSyntaxUID     string
	TransferSyntaxName    string
	RegisteredEncoderUIDs []string
}

func (e *EncoderAvailabilityError) Error() string {
	if e == nil {
		return "dicom: pixel data encoder unavailable"
	}
	message := "dicom: pixel data encoder unavailable"
	if e.Err != nil {
		message = e.Err.Error()
	}
	if e.TransferSyntaxUID != "" {
		message += fmt.Sprintf(": transfer syntax %q", e.TransferSyntaxUID)
		if e.TransferSyntaxName != "" {
			message += fmt.Sprintf(" (%s)", e.TransferSyntaxName)
		}
	}
	if errors.Is(e.Err, ErrEncoderNotFound) {
		if len(e.RegisteredEncoderUIDs) == 0 {
			message += "; registered encoders: none"
		} else {
			message += "; registered encoders: " + strings.Join(e.RegisteredEncoderUIDs, ", ")
		}
	}
	return message
}

func (e *EncoderAvailabilityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// UnsupportedEncoderMetadataError identifies the metadata field rejected by
// an encoder without including any pixel or patient value.
type UnsupportedEncoderMetadataError struct {
	Field string
}

func (e *UnsupportedEncoderMetadataError) Error() string {
	if e == nil || e.Field == "" {
		return ErrUnsupportedEncoderMetadata.Error()
	}
	return ErrUnsupportedEncoderMetadata.Error() + ": " + e.Field
}

func (*UnsupportedEncoderMetadataError) Unwrap() error { return ErrUnsupportedEncoderMetadata }

// EncoderEncodeError wraps a backend error while keeping its Error string free
// of backend-controlled text.
type EncoderEncodeError struct {
	Err               error
	TransferSyntaxUID string
}

func (e *EncoderEncodeError) Error() string {
	if e == nil || e.TransferSyntaxUID == "" {
		return ErrEncoderFailed.Error()
	}
	return fmt.Sprintf("%s for transfer syntax %q", ErrEncoderFailed, e.TransferSyntaxUID)
}

func (e *EncoderEncodeError) Unwrap() []error {
	if e == nil || e.Err == nil {
		return []error{ErrEncoderFailed}
	}
	return []error{ErrEncoderFailed, redactedCodecCause{cause: e.Err, classification: ErrEncoderFailed}}
}

// NewMemoryEncoderRegistry returns an empty explicit encoder registry.
func NewMemoryEncoderRegistry() *MemoryEncoderRegistry {
	return &MemoryEncoderRegistry{byUID: make(map[string]registeredFrameEncoder)}
}

func (r *MemoryEncoderRegistry) RegisterEncoder(uid string, encoder FrameEncoder) error {
	if r == nil {
		return ErrEncoderRegistryNil
	}
	if encoder == nil {
		return ErrEncoderNil
	}
	normalizedUID := transfer.NormalizeUID(uid)
	if normalizedUID == "" {
		return ErrEncoderUIDInvalid
	}
	capabilities, err := encoderCapabilities(encoder)
	if err != nil {
		return err
	}
	capabilities.TransferSyntaxUID = transfer.NormalizeUID(capabilities.TransferSyntaxUID)
	if capabilities.TransferSyntaxUID != normalizedUID || !validEncoderCapabilities(capabilities) {
		return ErrEncoderCapabilitiesInvalid
	}
	capabilities = cloneEncoderCapabilities(capabilities)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byUID == nil {
		r.byUID = make(map[string]registeredFrameEncoder)
	}
	if _, exists := r.byUID[normalizedUID]; exists {
		return ErrEncoderAlreadyRegistered
	}
	r.byUID[normalizedUID] = registeredFrameEncoder{encoder: encoder, capabilities: capabilities}
	return nil
}

func (r *MemoryEncoderRegistry) GetEncoder(uid string) (FrameEncoder, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	encoder, ok := r.byUID[transfer.NormalizeUID(uid)]
	if !ok {
		return nil, false
	}
	return encoder, true
}

// RegisteredEncoderUIDs returns a sorted detached registration snapshot.
func (r *MemoryEncoderRegistry) RegisteredEncoderUIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	uids := make([]string, 0, len(r.byUID))
	for uid := range r.byUID {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	return uids
}

func (r *MemoryEncoderRegistry) EncodeFrame(ctx context.Context, uid string, frame []byte, metadata Metadata) (EncodedFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return EncodedFrame{}, err
	}
	normalizedUID := transfer.NormalizeUID(uid)
	encoder, ok := r.GetEncoder(normalizedUID)
	if !ok {
		return EncodedFrame{}, encoderAvailabilityError(ErrEncoderNotFound, normalizedUID, r.RegisteredEncoderUIDs())
	}
	if err := validateEncoderMetadata(encoder.Capabilities(), metadata); err != nil {
		return EncodedFrame{}, err
	}
	encoded, err := callFrameEncoder(ctx, encoder, frame, metadata)
	if err != nil {
		return EncodedFrame{}, &EncoderEncodeError{Err: err, TransferSyntaxUID: normalizedUID}
	}
	if err := ctx.Err(); err != nil {
		return EncodedFrame{}, err
	}
	if len(encoded.Data) == 0 {
		return EncodedFrame{}, fmt.Errorf("%w: empty frame", ErrEncoderOutputInvalid)
	}
	return encoded, nil
}

// CheckEncoderAvailability reports whether registry contains an encoder for
// uid without invoking the backend.
func CheckEncoderAvailability(registry EncoderRegistry, uid string) error {
	normalizedUID := transfer.NormalizeUID(uid)
	if registry == nil {
		return encoderAvailabilityError(ErrEncoderRegistryNil, normalizedUID, nil)
	}
	if _, ok := registry.GetEncoder(normalizedUID); ok {
		return nil
	}
	var registered []string
	if snapshot, ok := registry.(interface{ RegisteredEncoderUIDs() []string }); ok {
		registered = snapshot.RegisteredEncoderUIDs()
	}
	return encoderAvailabilityError(ErrEncoderNotFound, normalizedUID, registered)
}

func encoderAvailabilityError(cause error, uid string, registered []string) error {
	err := &EncoderAvailabilityError{
		Err:                   cause,
		TransferSyntaxUID:     uid,
		RegisteredEncoderUIDs: append([]string(nil), registered...),
	}
	if syntax, ok := transfer.DefaultRegistry.Get(uid); ok {
		err.TransferSyntaxName = syntax.Name
	}
	return err
}

func encoderCapabilities(encoder FrameEncoder) (capabilities EncoderCapabilities, err error) {
	defer func() {
		if recover() != nil {
			capabilities = EncoderCapabilities{}
			err = ErrEncoderCapabilitiesInvalid
		}
	}()
	return encoder.Capabilities(), nil
}

func callFrameEncoder(ctx context.Context, encoder FrameEncoder, frame []byte, metadata Metadata) (encoded EncodedFrame, err error) {
	defer func() {
		if recover() != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				err = contextErr
			} else {
				err = ErrEncoderFailed
			}
			encoded = EncodedFrame{}
		}
	}()
	return encoder.EncodeFrame(ctx, core.CloneBytes(frame), metadata)
}

func validEncoderCapabilities(capabilities EncoderCapabilities) bool {
	if capabilities.TransferSyntaxUID == "" || len(capabilities.BitsAllocated) == 0 ||
		len(capabilities.PixelRepresentations) == 0 || len(capabilities.SamplesPerPixel) == 0 ||
		len(capabilities.PhotometricInterpretations) == 0 {
		return false
	}
	if !capabilities.Lossless && !validLossyCompressionMethod(capabilities.LossyMethod) {
		return false
	}
	for _, bits := range capabilities.BitsAllocated {
		if bits == 0 || (bits != 1 && bits%8 != 0) {
			return false
		}
	}
	for _, representation := range capabilities.PixelRepresentations {
		if representation > 1 {
			return false
		}
	}
	for _, samples := range capabilities.SamplesPerPixel {
		if samples == 0 {
			return false
		}
	}
	for _, photometric := range capabilities.PhotometricInterpretations {
		if strings.TrimSpace(photometric) == "" {
			return false
		}
	}
	for _, photometric := range capabilities.OutputPhotometricInterpretations {
		if strings.TrimSpace(photometric) == "" {
			return false
		}
	}
	for _, planar := range capabilities.OutputPlanarConfigurations {
		if planar > 1 {
			return false
		}
	}
	return true
}

func validLossyCompressionMethod(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 16 {
		return false
	}
	for _, char := range value {
		if char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == ' ' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validateEncoderMetadata(capabilities EncoderCapabilities, metadata Metadata) error {
	if metadata.NumberOfFrames > 1 && !capabilities.SupportsMultiFrame {
		return &UnsupportedEncoderMetadataError{Field: "NumberOfFrames"}
	}
	if !containsUint16(capabilities.BitsAllocated, metadata.BitsAllocated) {
		return &UnsupportedEncoderMetadataError{Field: "BitsAllocated"}
	}
	if !containsUint16(capabilities.PixelRepresentations, metadata.PixelRepresentation) {
		return &UnsupportedEncoderMetadataError{Field: "PixelRepresentation"}
	}
	if !containsUint16(capabilities.SamplesPerPixel, metadata.SamplesPerPixel) {
		return &UnsupportedEncoderMetadataError{Field: "SamplesPerPixel"}
	}
	photometric := normalizedPhotometric(metadata.PhotometricInterpretation)
	found := false
	for _, supported := range capabilities.PhotometricInterpretations {
		if normalizedPhotometric(supported) == photometric {
			found = true
			break
		}
	}
	if !found {
		return &UnsupportedEncoderMetadataError{Field: "PhotometricInterpretation"}
	}
	return nil
}

func containsUint16(values []uint16, want uint16) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneEncoderCapabilities(capabilities EncoderCapabilities) EncoderCapabilities {
	capabilities.BitsAllocated = append([]uint16(nil), capabilities.BitsAllocated...)
	capabilities.PixelRepresentations = append([]uint16(nil), capabilities.PixelRepresentations...)
	capabilities.SamplesPerPixel = append([]uint16(nil), capabilities.SamplesPerPixel...)
	capabilities.PhotometricInterpretations = append([]string(nil), capabilities.PhotometricInterpretations...)
	capabilities.OutputPhotometricInterpretations = append([]string(nil), capabilities.OutputPhotometricInterpretations...)
	capabilities.OutputPlanarConfigurations = append([]uint16(nil), capabilities.OutputPlanarConfigurations...)
	return capabilities
}
