package pixeldata

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrPixelDataNotFound         = errors.New("dicom: pixel data element not found")
	ErrMissingMetadata           = errors.New("dicom: missing required pixel data metadata")
	ErrInvalidMetadata           = errors.New("dicom: invalid pixel data metadata")
	ErrCodecNotFound             = errors.New("dicom: no pixel data codec registered for transfer syntax")
	ErrCodecRegistryNil          = errors.New("dicom: pixel data codec registry is nil")
	ErrCodecNil                  = errors.New("dicom: pixel data codec is nil")
	ErrCodecUIDInvalid           = errors.New("dicom: pixel data codec transfer syntax UID is empty")
	ErrIncompatiblePixelData     = errors.New("dicom: pixel data is incompatible with transfer syntax")
	ErrEncapsulatedPixelData     = errors.New("dicom: native frame extraction does not support encapsulated pixel data")
	ErrPixelDataSizeMismatch     = errors.New("dicom: pixel data size does not match metadata")
	tagRows                      = core.NewTag(0x0028, 0x0010)
	tagColumns                   = core.NewTag(0x0028, 0x0011)
	tagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	tagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	tagBitsStored                = core.NewTag(0x0028, 0x0101)
	tagHighBit                   = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
	tagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	tagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
)

type PixelData struct {
	Raw          []byte
	Sequence     core.FragmentSequence
	Encapsulated bool
}

type Metadata struct {
	Rows                      uint16
	Columns                   uint16
	SamplesPerPixel           uint16
	BitsAllocated             uint16
	BitsStored                uint16
	HighBit                   uint16
	PixelRepresentation       uint16
	NumberOfFrames            int
	PhotometricInterpretation string
}

func (m Metadata) BytesPerSample() int {
	return int((m.BitsAllocated + 7) / 8)
}

func (m Metadata) FrameSize() int64 {
	return int64(m.Rows) * int64(m.Columns) * int64(m.SamplesPerPixel) * int64(m.BytesPerSample())
}

func (m Metadata) TotalSize() int64 {
	return m.FrameSize() * int64(m.NumberOfFrames)
}

type Codec interface {
	Decode(pixel PixelData, obj *object.Object) (Frames, error)
}

type Registry interface {
	RegisterCodec(uid string, codec Codec) error
	GetCodec(uid string) (Codec, bool)
	DecodeFrames(uid string, pixel PixelData, obj *object.Object) (Frames, error)
}

type MemoryRegistry struct {
	mu    sync.RWMutex
	byUID map[string]Codec
}

type Frames struct {
	Rows    int
	Columns int
	Data    [][]byte
}

// NativeFrames contains native, uncompressed pixel data split by frame.
// Frame slices share the same underlying byte array and should not be mutated
// independently.
type NativeFrames struct {
	Metadata Metadata
	Data     [][]byte
}

var DefaultRegistry Registry = NewMemoryRegistry()

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{byUID: make(map[string]Codec)}
}

func (r *MemoryRegistry) RegisterCodec(uid string, codec Codec) error {
	if r == nil {
		return ErrCodecRegistryNil
	}
	if codec == nil {
		return ErrCodecNil
	}

	normalizedUID := transfer.NormalizeUID(uid)
	if normalizedUID == "" {
		return ErrCodecUIDInvalid
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.byUID == nil {
		r.byUID = make(map[string]Codec)
	}
	r.byUID[normalizedUID] = codec
	return nil
}

func (r *MemoryRegistry) GetCodec(uid string) (Codec, bool) {
	if r == nil {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	codec, ok := r.byUID[transfer.NormalizeUID(uid)]
	return codec, ok
}

func (r *MemoryRegistry) DecodeFrames(uid string, pixel PixelData, obj *object.Object) (Frames, error) {
	normalizedUID := transfer.NormalizeUID(uid)
	if syntax, ok := transfer.DefaultRegistry.Get(normalizedUID); ok {
		switch {
		case syntax.Encapsulated && !pixel.Encapsulated:
			return Frames{}, fmt.Errorf("%w: transfer syntax %q expects encapsulated pixel data", ErrIncompatiblePixelData, syntax.UID)
		case !syntax.Encapsulated && pixel.Encapsulated:
			return Frames{}, fmt.Errorf("%w: transfer syntax %q expects native pixel data", ErrIncompatiblePixelData, syntax.UID)
		}
	}

	if !pixel.Encapsulated {
		return decodeNativeFrames(pixel, obj)
	}
	if r == nil {
		return Frames{}, fmt.Errorf("%w: transfer syntax %q", ErrCodecRegistryNil, normalizedUID)
	}

	codec, ok := r.GetCodec(normalizedUID)
	if !ok {
		return Frames{}, fmt.Errorf("%w: transfer syntax %q", ErrCodecNotFound, normalizedUID)
	}
	return codec.Decode(pixel, obj)
}

func RegisterCodec(uid string, codec Codec) error {
	if DefaultRegistry == nil {
		return ErrCodecRegistryNil
	}
	return DefaultRegistry.RegisterCodec(uid, codec)
}

func GetCodec(uid string) (Codec, bool, error) {
	if DefaultRegistry == nil {
		return nil, false, ErrCodecRegistryNil
	}
	codec, ok := DefaultRegistry.GetCodec(uid)
	return codec, ok, nil
}

func DecodeFrames(uid string, pixel PixelData, obj *object.Object) (Frames, error) {
	if DefaultRegistry == nil {
		return Frames{}, ErrCodecRegistryNil
	}
	return DefaultRegistry.DecodeFrames(uid, pixel, obj)
}

func Extract(obj *object.Object) (PixelData, error) {
	elem, ok := obj.Get(core.TagPixelData)
	if !ok {
		return PixelData{}, ErrPixelDataNotFound
	}
	raw, ok := elem.RawBytes()
	if ok {
		return PixelData{Raw: core.CloneBytes(raw)}, nil
	}
	fragments, ok := elem.Value.(core.FragmentSequence)
	if !ok {
		return PixelData{}, errors.New("dicom: pixel data is not a supported value type")
	}
	cloned := core.FragmentSequence{
		OffsetTable: core.CloneBytes(fragments.OffsetTable),
		Fragments:   make([][]byte, len(fragments.Fragments)),
	}
	for i := range fragments.Fragments {
		cloned.Fragments[i] = core.CloneBytes(fragments.Fragments[i])
	}
	return PixelData{
		Sequence:     cloned,
		Encapsulated: true,
	}, nil
}

func ExtractMetadata(obj *object.Object) (Metadata, error) {
	rows, ok := getUint16(obj, tagRows)
	if !ok {
		return Metadata{}, missingMetadataError("Rows", tagRows)
	}
	columns, ok := getUint16(obj, tagColumns)
	if !ok {
		return Metadata{}, missingMetadataError("Columns", tagColumns)
	}
	samplesPerPixel, ok := getUint16(obj, tagSamplesPerPixel)
	if !ok {
		return Metadata{}, missingMetadataError("SamplesPerPixel", tagSamplesPerPixel)
	}
	bitsAllocated, ok := getUint16(obj, tagBitsAllocated)
	if !ok {
		return Metadata{}, missingMetadataError("BitsAllocated", tagBitsAllocated)
	}
	bitsStored, ok := getUint16(obj, tagBitsStored)
	if !ok {
		return Metadata{}, missingMetadataError("BitsStored", tagBitsStored)
	}
	highBit, ok := getUint16(obj, tagHighBit)
	if !ok {
		return Metadata{}, missingMetadataError("HighBit", tagHighBit)
	}
	pixelRepresentation, ok := getUint16(obj, tagPixelRepresentation)
	if !ok {
		return Metadata{}, missingMetadataError("PixelRepresentation", tagPixelRepresentation)
	}

	photometricInterpretation, ok := obj.GetString(tagPhotometricInterpretation)
	if !ok || strings.TrimSpace(photometricInterpretation) == "" {
		return Metadata{}, missingMetadataError("PhotometricInterpretation", tagPhotometricInterpretation)
	}

	numberOfFrames, err := numberOfFrames(obj)
	if err != nil {
		return Metadata{}, err
	}

	return Metadata{
		Rows:                      rows,
		Columns:                   columns,
		SamplesPerPixel:           samplesPerPixel,
		BitsAllocated:             bitsAllocated,
		BitsStored:                bitsStored,
		HighBit:                   highBit,
		PixelRepresentation:       pixelRepresentation,
		NumberOfFrames:            numberOfFrames,
		PhotometricInterpretation: photometricInterpretation,
	}, nil
}

// ExtractNativeFrames returns native, uncompressed frames split without
// additional per-frame copies. Each frame slice references the cloned pixel
// buffer produced by Extract.
func ExtractNativeFrames(obj *object.Object) (*NativeFrames, error) {
	pixel, err := Extract(obj)
	if err != nil {
		return nil, err
	}
	native, err := extractNativeFrames(pixel, obj)
	if err != nil {
		return nil, err
	}

	return &NativeFrames{
		Metadata: native.Metadata,
		Data:     native.Data,
	}, nil
}

func extractNativeFrames(pixel PixelData, obj *object.Object) (*NativeFrames, error) {
	metadata, frames, err := splitNativeFrames(pixel, obj)
	if err != nil {
		return nil, err
	}
	return &NativeFrames{
		Metadata: metadata,
		Data:     frames,
	}, nil
}

func decodeNativeFrames(pixel PixelData, obj *object.Object) (Frames, error) {
	metadata, frames, err := splitNativeFrames(pixel, obj)
	if err != nil {
		return Frames{}, err
	}
	return Frames{
		Rows:    int(metadata.Rows),
		Columns: int(metadata.Columns),
		Data:    frames,
	}, nil
}

func splitNativeFrames(pixel PixelData, obj *object.Object) (Metadata, [][]byte, error) {
	metadata, err := ExtractMetadata(obj)
	if err != nil {
		return Metadata{}, nil, err
	}
	if pixel.Encapsulated {
		return Metadata{}, nil, ErrEncapsulatedPixelData
	}

	frameSize := metadata.FrameSize()
	totalSize := metadata.TotalSize()
	maxInt := int64(^uint(0) >> 1)
	if frameSize <= 0 || totalSize <= 0 || frameSize > maxInt || totalSize > maxInt {
		return Metadata{}, nil, fmt.Errorf(
			"%w: rows=%d columns=%d samples_per_pixel=%d bits_allocated=%d number_of_frames=%d",
			ErrInvalidMetadata,
			metadata.Rows,
			metadata.Columns,
			metadata.SamplesPerPixel,
			metadata.BitsAllocated,
			metadata.NumberOfFrames,
		)
	}

	frameSizeInt := int(frameSize)
	totalSizeInt := int(totalSize)

	if len(pixel.Raw) != totalSizeInt {
		return Metadata{}, nil, fmt.Errorf(
			"%w: expected %d bytes for %d frame(s), got %d",
			ErrPixelDataSizeMismatch,
			totalSizeInt,
			metadata.NumberOfFrames,
			len(pixel.Raw),
		)
	}

	frames := make([][]byte, metadata.NumberOfFrames)
	for i := range frames {
		start := i * frameSizeInt
		frames[i] = pixel.Raw[start : start+frameSizeInt]
	}

	return metadata, frames, nil
}

func getUint16(obj *object.Object, tag core.Tag) (uint16, bool) {
	raw, ok := obj.GetRaw(tag)
	if !ok || len(raw) < 2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(raw[:2]), true
}

func numberOfFrames(obj *object.Object) (int, error) {
	value, ok := obj.GetString(tagNumberOfFrames)
	v := strings.TrimSpace(value)
	if !ok || v == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: NumberOfFrames=%q", ErrInvalidMetadata, value)
	}
	return n, nil
}

func missingMetadataError(name string, tag core.Tag) error {
	return fmt.Errorf("%w: %s (%s)", ErrMissingMetadata, name, tag)
}
