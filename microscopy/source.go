package microscopy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/net/dicomweb"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	pixelframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var ErrUnsupportedFragmentLayout = errors.New("dicom/microscopy: compressed frame layout is not independently addressable")
var errNativeFrameCaptured = errors.New("dicom/microscopy: requested native frame captured")

type TileSource interface {
	FetchTile(context.Context, Tile) (image.Image, error)
}

type TileSourceFunc func(context.Context, Tile) (image.Image, error)

func (f TileSourceFunc) FetchTile(ctx context.Context, tile Tile) (image.Image, error) {
	return f(ctx, tile)
}

// FileOpener opens the source instance identified by ref. Implementations
// should honor ctx before expensive I/O.
type FileOpener func(context.Context, InstanceRef) (*object.File, error)

// LocalTileSource reopens a local object for each cache miss and decodes only
// the requested native frame. For encapsulated WSI, it deliberately supports
// only independently addressable one-fragment-per-frame layouts; it never
// falls back to decoding every frame in the instance.
type LocalTileSource struct {
	Open FileOpener
}

func (s LocalTileSource) FetchTile(ctx context.Context, tile Tile) (image.Image, error) {
	if s.Open == nil {
		return nil, fmt.Errorf("dicom/microscopy: local tile source has no opener")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := s.Open(ctx, tile.Source)
	if err != nil {
		return nil, err
	}
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("dicom/microscopy: opener returned no dataset")
	}
	defer func() { _ = file.Close() }()
	return frameFromFile(ctx, file, tile.FrameNumber)
}

func frameFromFile(ctx context.Context, file *object.File, frameNumber int) (image.Image, error) {
	if frameNumber <= 0 {
		return nil, fmt.Errorf("dicom/microscopy: frame numbers are one-based")
	}
	metadata, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		return nil, err
	}
	native, nativeErr := pixeldata.ExtractNativeFramesView(file.Dataset)
	if nativeErr == nil {
		if frameNumber > len(native.Data) {
			return nil, fmt.Errorf("dicom/microscopy: frame %d out of range [1,%d]", frameNumber, len(native.Data))
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return pixelframe.NewNativeFrame(
			frameNumber-1,
			native.Data[frameNumber-1],
			native.Metadata,
			pixelframe.WithByteOrder(file.TransferSyntax.ByteOrder),
		).GetImage()
	}
	if syntax, ok := transfer.DefaultRegistry.Get(transfer.NormalizeUID(file.TransferSyntax.UID)); ok && !syntax.Encapsulated {
		return deferredNativeFrame(file, metadata, frameNumber, syntax.ByteOrder)
	}
	if !errors.Is(nativeErr, pixeldata.ErrEncapsulatedPixelData) {
		return nil, nativeErr
	}
	pixels, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		return nil, err
	}
	fragments := pixels.Sequence.Fragments
	if metadata.NumberOfFrames <= 0 || len(fragments) != metadata.NumberOfFrames {
		return nil, fmt.Errorf(
			"%w: %d fragments for %d frames",
			ErrUnsupportedFragmentLayout, len(fragments), metadata.NumberOfFrames,
		)
	}
	if frameNumber > len(fragments) {
		return nil, fmt.Errorf("dicom/microscopy: frame %d out of range [1,%d]", frameNumber, len(fragments))
	}
	return decodeEncapsulatedFrame(ctx, fragments[frameNumber-1], file.TransferSyntax.UID, metadata)
}

func deferredNativeFrame(file *object.File, metadata pixeldata.Metadata, frameNumber int, order binary.ByteOrder) (image.Image, error) {
	if metadata.NumberOfFrames <= 0 || frameNumber > metadata.NumberOfFrames {
		return nil, fmt.Errorf("dicom/microscopy: frame %d out of range [1,%d]", frameNumber, metadata.NumberOfFrames)
	}
	frameBytes := metadata.FrameSize()
	if frameBytes <= 0 || frameBytes > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("dicom/microscopy: invalid native frame size %d", frameBytes)
	}
	writer := &nativeFrameWriter{
		start: int64(frameNumber-1) * frameBytes,
		end:   int64(frameNumber) * frameBytes,
		data:  make([]byte, int(frameBytes)),
	}
	_, err := file.Dataset.CopyValueTo(derivedio.TagPixelData, writer)
	if err != nil && !errors.Is(err, errNativeFrameCaptured) {
		return nil, err
	}
	if writer.copied != frameBytes {
		return nil, fmt.Errorf("dicom/microscopy: copied %d of %d native frame bytes", writer.copied, frameBytes)
	}
	return pixelframe.NewNativeFrame(
		frameNumber-1, writer.data, metadata,
		pixelframe.WithByteOrder(order),
	).GetImage()
}

type nativeFrameWriter struct {
	offset int64
	start  int64
	end    int64
	copied int64
	data   []byte
}

func (w *nativeFrameWriter) Write(p []byte) (int, error) {
	chunkStart := w.offset
	chunkEnd := chunkStart + int64(len(p))
	copyStart := max(chunkStart, w.start)
	copyEnd := min(chunkEnd, w.end)
	if copyStart < copyEnd {
		destination := copyStart - w.start
		source := copyStart - chunkStart
		count := copyEnd - copyStart
		copy(w.data[destination:destination+count], p[source:source+count])
		w.copied += count
	}
	w.offset = chunkEnd
	if w.offset >= w.end {
		return len(p), errNativeFrameCaptured
	}
	return len(p), nil
}

type DICOMwebFrameClient interface {
	RetrieveFrames(context.Context, dicomweb.InstanceRef, []int, dicomweb.RetrieveOptions) ([]dicomweb.FramePart, error)
}

// DICOMwebTileSource retrieves one frame per cache miss. Metadata is keyed by
// SOP Instance UID and is supplied from WADO-RS metadata, never from a complete
// instance retrieval.
type DICOMwebTileSource struct {
	Client   DICOMwebFrameClient
	Metadata map[string]pixeldata.Metadata
	Options  dicomweb.RetrieveOptions
}

func (s DICOMwebTileSource) FetchTile(ctx context.Context, tile Tile) (image.Image, error) {
	if s.Client == nil {
		return nil, fmt.Errorf("dicom/microscopy: DICOMweb tile source has no client")
	}
	metadata, ok := s.Metadata[tile.Source.SOPInstanceUID]
	if !ok {
		return nil, fmt.Errorf("dicom/microscopy: no pixel metadata for instance %s", tile.Source.SOPInstanceUID)
	}
	parts, err := s.Client.RetrieveFrames(ctx, dicomweb.InstanceRef{
		StudyInstanceUID:  tile.Source.StudyInstanceUID,
		SeriesInstanceUID: tile.Source.SeriesInstanceUID,
		SOPInstanceUID:    tile.Source.SOPInstanceUID,
	}, []int{tile.FrameNumber}, s.Options)
	if err != nil {
		return nil, err
	}
	if len(parts) != 1 || parts[0].FrameNumber != tile.FrameNumber {
		return nil, fmt.Errorf("dicom/microscopy: WADO-RS returned no matching frame %d", tile.FrameNumber)
	}
	syntaxUID := parts[0].TransferSyntaxUID
	if syntaxUID == "" && len(s.Options.TransferSyntaxUIDs) == 1 {
		syntaxUID = s.Options.TransferSyntaxUIDs[0]
	}
	if syntax, ok := transfer.DefaultRegistry.Get(transfer.NormalizeUID(syntaxUID)); ok && !syntax.Encapsulated {
		return pixelframe.NewNativeFrame(
			tile.FrameNumber-1, parts[0].Data, metadata,
			pixelframe.WithByteOrder(syntax.ByteOrder),
		).GetImage()
	}
	return decodeEncapsulatedFrame(ctx, parts[0].Data, syntaxUID, metadata)
}

func decodeEncapsulatedFrame(ctx context.Context, payload []byte, syntaxUID string, metadata pixeldata.Metadata) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	metadata.NumberOfFrames = 1
	template := metadataObject(metadata)
	decoded, err := pixeldata.DecodeFrames(
		syntaxUID,
		pixeldata.PixelData{
			Encapsulated: true,
			Sequence: core.FragmentSequence{
				OffsetTable: make([]byte, 4),
				Fragments:   [][]byte{payload},
			},
		},
		template,
	)
	if err != nil {
		return nil, err
	}
	if len(decoded.Data) != 1 {
		return nil, fmt.Errorf("dicom/microscopy: codec returned %d frames for one tile", len(decoded.Data))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return pixelframe.NewEncapsulatedFrame(0, decoded.Data[0], metadata).GetImage()
}

func metadataObject(metadata pixeldata.Metadata) *object.Object {
	elements := []core.Element{
		derivedio.US(derivedio.TagRows, metadata.Rows),
		derivedio.US(derivedio.TagColumns, metadata.Columns),
		derivedio.US(derivedio.TagSamplesPerPixel, metadata.SamplesPerPixel),
		derivedio.CS(derivedio.TagPhotometricInterpretation, metadata.PhotometricInterpretation),
		derivedio.IS(derivedio.TagNumberOfFrames, 1),
		derivedio.US(derivedio.TagBitsAllocated, metadata.BitsAllocated),
		derivedio.US(derivedio.TagBitsStored, metadata.BitsStored),
		derivedio.US(derivedio.TagHighBit, metadata.HighBit),
		derivedio.US(derivedio.TagPixelRepresentation, metadata.PixelRepresentation),
	}
	if metadata.PlanarConfigurationPresent {
		elements = append(elements, derivedio.US(core.NewTag(0x0028, 0x0006), metadata.PlanarConfiguration))
	}
	obj := object.FromElements(elements, std.Dictionary)
	obj.SetValueByteOrder(binary.LittleEndian)
	return obj
}
