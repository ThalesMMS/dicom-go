package nifti

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parametricmap"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	pixelframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
	"github.com/ThalesMMS/dicom-go/render"
)

// Plan is an immutable metadata decision over a replayable Source. The source
// must continue to identify the same bytes until Write completes; the data pass
// revalidates identity, frame metadata, geometry, and scaling before publishing.
type Plan struct {
	source  Source
	options Options
	value   *exportPlan
}

// PlanVolume performs the metadata-only validation phase. Pixel Data is
// deferred by path-backed sources and no destination bytes are written.
func PlanVolume(ctx context.Context, source Source, options Options) (*Plan, error) {
	normalized, err := normalizedOptions(options)
	if err != nil {
		return nil, err
	}
	value, err := buildPlan(ctx, source, normalized)
	if err != nil {
		return nil, err
	}
	return &Plan{source: source, options: normalized, value: value}, nil
}

// Write plans and writes one logical NIfTI-1 volume. The destination may be
// partially written on transport failure; applications writing paths should
// use an atomic temporary-file wrapper.
func Write(ctx context.Context, destination io.Writer, source Source, options Options) (Report, error) {
	plan, err := PlanVolume(ctx, source, options)
	if err != nil {
		return Report{}, err
	}
	return plan.Write(ctx, destination)
}

// WriteFiles is the in-memory convenience adapter. Ownership of files remains
// with the caller and none are closed by the exporter.
func WriteFiles(ctx context.Context, destination io.Writer, files []*object.File, options Options) (Report, error) {
	return Write(ctx, destination, NewFilesSource(files), options)
}

// Write performs the pixel/data pass and serialization for an existing plan.
func (p *Plan) Write(ctx context.Context, destination io.Writer) (report Report, retErr error) {
	if p == nil || p.value == nil || p.source == nil || destination == nil {
		return Report{}, exportError(CodeWrite, -1, -1, ErrInvalidSource, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Report{}, exportError(CodeCanceled, -1, -1, err, err)
	}
	working := p.cloneForWrite()
	spool, err := os.CreateTemp(working.options.TempDir, "dicom-nifti-spool-*")
	if err != nil {
		return Report{}, exportError(CodeWrite, -1, -1, ErrInvalidSource, err)
	}
	spoolPath := spool.Name()
	defer func() {
		cleanupErr := errors.Join(spool.Close(), os.Remove(spoolPath))
		if cleanupErr != nil {
			retErr = errors.Join(retErr, exportError(CodeWrite, -1, -1, ErrInvalidSource, cleanupErr))
		}
	}()
	if err := working.spoolFrames(ctx, spool); err != nil {
		return Report{}, err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return Report{}, exportError(CodeWrite, -1, -1, ErrInvalidSource, err)
	}
	return working.writeFromSpool(ctx, destination, spool)
}

func (p *Plan) cloneForWrite() *Plan {
	cloned := *p
	value := *p.value
	value.frames = make([]*framePlan, len(p.value.frames))
	frameMap := make(map[*framePlan]*framePlan, len(p.value.frames))
	for index, frame := range p.value.frames {
		frameCopy := *frame
		value.frames[index] = &frameCopy
		frameMap[frame] = &frameCopy
	}
	value.timePoints = make([]timePointPlan, len(p.value.timePoints))
	for index, point := range p.value.timePoints {
		pointCopy := point
		pointCopy.frames = make([]*framePlan, len(point.frames))
		for frameIndex, frame := range point.frames {
			pointCopy.frames[frameIndex] = frameMap[frame]
		}
		value.timePoints[index] = pointCopy
	}
	value.warnings = append([]string(nil), p.value.warnings...)
	cloned.value = &value
	return &cloned
}

func (p *Plan) spoolFrames(ctx context.Context, spool *os.File) error {
	bySource := make([][]*framePlan, p.value.sources)
	for _, frame := range p.value.frames {
		bySource[frame.sourceIndex] = append(bySource[frame.sourceIndex], frame)
	}
	var spoolBytes uint64
	for sourceIndex, expected := range bySource {
		if err := ctx.Err(); err != nil {
			return exportError(CodeCanceled, sourceIndex, -1, err, err)
		}
		if len(expected) == 0 {
			return exportError(CodeSource, sourceIndex, -1, ErrInvalidSource, nil)
		}
		readOptions := object.ReadFileOptions{}
		stream := &nativeSpoolSink{ctx: ctx, plan: p, spool: spool, expected: expected, total: &spoolBytes}
		if !p.value.sourceEncapsulated[sourceIndex] && !expected[0].parametric {
			readOptions.FrameSink = stream
		} else if expected[0].parametric {
			frameBuffers, ok := parametricWorkingSetBytes(0, int(expected[0].metadata.Rows), int(expected[0].metadata.Columns))
			if !ok || frameBuffers >= p.options.Limits.MaxInMemorySourceBytes {
				return exportError(CodeLimit, sourceIndex, -1, ErrLimitExceeded, nil)
			}
			remaining := p.options.Limits.MaxInMemorySourceBytes - frameBuffers
			if remaining <= math.MaxInt64 {
				readOptions.MaxTotalBytes = int64(remaining)
			}
		} else if p.options.Limits.MaxInMemorySourceBytes <= math.MaxInt64 {
			readOptions.MaxTotalBytes = int64(p.options.Limits.MaxInMemorySourceBytes)
		}
		opened, err := p.source.Open(ctx, sourceIndex, readOptions)
		if err != nil {
			if stream.err != nil {
				return stream.err
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return exportError(CodeCanceled, sourceIndex, -1, err, err)
			}
			if errors.Is(err, ErrLimitExceeded) {
				return exportError(CodeLimit, sourceIndex, -1, ErrLimitExceeded, err)
			}
			return exportError(CodeSource, sourceIndex, -1, ErrInvalidSource, err)
		}
		if opened.File == nil || opened.File.Dataset == nil {
			_ = opened.Close()
			return exportError(CodeSource, sourceIndex, -1, ErrInvalidSource, nil)
		}
		file := opened.File
		if !sameIdentity(file, p.value.seriesUID, p.value.frameOfReferenceUID) ||
			file.TransferSyntax.Encapsulated != p.value.sourceEncapsulated[sourceIndex] {
			_ = opened.Close()
			return exportError(CodeIdentity, sourceIndex, -1, ErrMixedIdentity, nil)
		}
		if stream.count > 0 {
			if stream.count != len(expected) {
				err = exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, nil)
			} else {
				err = validateSourceFrames(file, expected)
			}
		} else if expected[0].parametric {
			err = p.spoolParametric(ctx, spool, file, expected, &spoolBytes)
		} else {
			err = p.spoolImages(ctx, spool, file, expected, &spoolBytes)
		}
		closeErr := opened.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return exportError(CodeSource, sourceIndex, -1, ErrInvalidSource, closeErr)
		}
	}
	return nil
}

type nativeSpoolSink struct {
	ctx      context.Context
	plan     *Plan
	spool    *os.File
	expected []*framePlan
	total    *uint64
	count    int
	err      error
}

func (s *nativeSpoolSink) HandleFrame(frame object.Frame) error {
	if s == nil || s.count >= len(s.expected) {
		return ErrUnsupportedPixels
	}
	expect := s.expected[s.count]
	metadata := pixeldata.Metadata{
		Rows: frame.Metadata.Rows, Columns: frame.Metadata.Columns,
		SamplesPerPixel: frame.Metadata.SamplesPerPixel, BitsAllocated: frame.Metadata.BitsAllocated,
		BitsStored: frame.Metadata.BitsStored, HighBit: frame.Metadata.HighBit,
		PixelRepresentation:        frame.Metadata.PixelRepresentation,
		PlanarConfiguration:        frame.Metadata.PlanarConfiguration,
		PlanarConfigurationPresent: frame.Metadata.PlanarConfigurationPresent,
		NumberOfFrames:             frame.Metadata.NumberOfFrames,
		PhotometricInterpretation:  frame.Metadata.PhotometricInterpretation,
	}
	if frame.Index != expect.frameIndex || !compatiblePixelMetadata(metadata, expect.metadata) {
		s.err = exportError(CodePixels, expect.sourceIndex, expect.frameIndex, ErrUnsupportedPixels, nil)
		return s.err
	}
	datatype := s.plan.value.spoolDatatype
	transform := expect.rescale
	if s.plan.value.resampled {
		datatype, transform = integerDatatypeOnly(expect.metadata), linearTransform{slope: 1}
	}
	encoded, err := encodeStoredFrame(s.ctx, frame.Data, metadata, frame.TransferSyntax.ByteOrder, transform, datatype)
	if err != nil {
		code, sentinel := CodePixels, ErrUnsupportedPixels
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code, sentinel = CodeCanceled, err
		}
		s.err = exportError(code, expect.sourceIndex, expect.frameIndex, sentinel, err)
		return s.err
	}
	if err := s.plan.appendSpool(s.spool, expect, encoded, s.total); err != nil {
		s.err = err
		return err
	}
	s.count++
	return nil
}

func (s *nativeSpoolSink) Close() error { return nil }

func validateSourceFrames(file *object.File, expected []*framePlan) error {
	for frameIndex, expect := range expected {
		geometry, geometryErr := strictSliceGeometry(file.FrameGeometryAt(frameIndex), int(expect.metadata.Rows), int(expect.metadata.Columns))
		rescale, rescaleErr := frameLinearTransform(file.Dataset, frameIndex)
		if geometryErr != nil || !sliceGeometryClose(geometry, expect.geometry) || rescaleErr != nil || rescale != expect.rescale {
			return exportError(CodeSource, expect.sourceIndex, frameIndex, ErrInvalidSource, errors.Join(geometryErr, rescaleErr))
		}
	}
	return nil
}

func (p *Plan) spoolImages(ctx context.Context, spool *os.File, file *object.File, expected []*framePlan, spoolBytes *uint64) error {
	if err := validateSourceFrames(file, expected); err != nil {
		return err
	}
	if native, err := pixeldata.ExtractNativeFramesView(file.Dataset); err == nil {
		if len(native.Data) != len(expected) {
			return exportError(CodePixels, expected[0].sourceIndex, -1, ErrUnsupportedPixels, nil)
		}
		for frameIndex, raw := range native.Data {
			if err := p.spoolImageFrame(ctx, spool, expected[frameIndex], raw, native.Metadata, file.TransferSyntax.ByteOrder, spoolBytes); err != nil {
				return err
			}
		}
		return nil
	} else if !errors.Is(err, pixeldata.ErrEncapsulatedPixelData) {
		return exportError(CodePixels, expected[0].sourceIndex, -1, ErrUnsupportedPixels, err)
	}
	pixels, err := pixeldata.ExtractView(file.Dataset)
	if err != nil || !pixels.Encapsulated {
		return exportError(CodePixels, expected[0].sourceIndex, -1, ErrUnsupportedPixels, err)
	}
	encodedBytes, ok := encapsulatedResidentBytes(pixels)
	if !ok {
		return exportError(CodeLimit, expected[0].sourceIndex, -1, ErrLimitExceeded, nil)
	}
	decodedBytes, ok := checkedMul(uint64(len(expected)), uint64(expected[0].metadata.FrameSize()))
	frameOutputBytes, outputOK := checkedMul(
		uint64(expected[0].metadata.Rows)*uint64(expected[0].metadata.Columns),
		uint64(p.value.spoolBitpix/8),
	)
	workingBytes, workingOK := checkedAdd(encodedBytes, decodedBytes)
	if workingOK {
		workingBytes, workingOK = checkedAdd(workingBytes, frameOutputBytes)
	}
	if !ok || !outputOK || !workingOK || workingBytes > p.options.Limits.MaxInMemorySourceBytes {
		return exportError(CodeLimit, expected[0].sourceIndex, -1, ErrLimitExceeded, nil)
	}
	frames, err := pixelframe.ExtractFrames(file)
	if err != nil || len(frames) != len(expected) {
		return exportError(CodePixels, expected[0].sourceIndex, -1, ErrUnsupportedPixels, err)
	}
	for frameIndex, wrapped := range frames {
		expect := expected[frameIndex]
		var raw []byte
		var metadata pixeldata.Metadata
		var order binary.ByteOrder
		if wrapped.IsEncapsulated() {
			decoded, getErr := wrapped.GetEncapsulatedFrame()
			if getErr != nil {
				return exportError(CodePixels, expect.sourceIndex, frameIndex, ErrUnsupportedPixels, getErr)
			}
			raw, metadata, order = decoded.Data, decoded.Metadata, decoded.ByteOrder
		} else {
			native, getErr := wrapped.GetNativeFrame()
			if getErr != nil {
				return exportError(CodePixels, expect.sourceIndex, frameIndex, ErrUnsupportedPixels, getErr)
			}
			raw, metadata, order = native.Data, native.Metadata, native.ByteOrder
		}
		if err := p.spoolImageFrame(ctx, spool, expect, raw, metadata, order, spoolBytes); err != nil {
			return err
		}
	}
	return nil
}

func encapsulatedResidentBytes(pixels pixeldata.PixelData) (uint64, bool) {
	total := uint64(len(pixels.Sequence.OffsetTable))
	for _, fragment := range pixels.Sequence.Fragments {
		var ok bool
		total, ok = checkedAdd(total, uint64(len(fragment)))
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func (p *Plan) spoolImageFrame(ctx context.Context, spool *os.File, expect *framePlan, raw []byte, metadata pixeldata.Metadata, order binary.ByteOrder, spoolBytes *uint64) error {
	if !compatiblePixelMetadata(metadata, expect.metadata) {
		return exportError(CodePixels, expect.sourceIndex, expect.frameIndex, ErrUnsupportedPixels, nil)
	}
	datatype := p.value.spoolDatatype
	transform := expect.rescale
	if p.value.resampled {
		datatype, transform = integerDatatypeOnly(expect.metadata), linearTransform{slope: 1}
	}
	encoded, err := encodeStoredFrame(ctx, raw, metadata, order, transform, datatype)
	if err != nil {
		return exportError(CodePixels, expect.sourceIndex, expect.frameIndex, ErrUnsupportedPixels, err)
	}
	return p.appendSpool(spool, expect, encoded, spoolBytes)
}

func (p *Plan) spoolParametric(ctx context.Context, spool *os.File, file *object.File, expected []*framePlan, spoolBytes *uint64) error {
	payloadBytes, payloadOK := parametricPayloadBytes(file.Dataset)
	workingBytes, workingOK := parametricWorkingSetBytes(payloadBytes, int(expected[0].metadata.Rows), int(expected[0].metadata.Columns))
	if !payloadOK || !workingOK || workingBytes > p.options.Limits.MaxInMemorySourceBytes {
		return exportError(CodeLimit, expected[0].sourceIndex, -1, ErrLimitExceeded, nil)
	}
	pm, err := parametricmap.Read(file)
	if err != nil || pm.NumberOfFrames != len(expected) ||
		strings.TrimSpace(pm.Units.Value) != p.value.unitCode || strings.TrimSpace(pm.Units.Scheme) != p.value.unitScheme ||
		strings.TrimSpace(pm.Quantity.Value) != p.value.quantityCode || strings.TrimSpace(pm.Quantity.Scheme) != p.value.quantityScheme {
		return exportError(CodePixels, expected[0].sourceIndex, -1, ErrUnsupportedPixels, err)
	}
	values := make([]float64, pm.Rows*pm.Columns)
	encoded := make([]byte, len(values)*8)
	for frameIndex, expect := range expected {
		if !sliceGeometryClose(pm.Frames[frameIndex].Geometry, expect.geometry) {
			return exportError(CodeSource, expect.sourceIndex, frameIndex, ErrInvalidSource, nil)
		}
		if valuesErr := pm.FrameValuesInto(frameIndex, values); valuesErr != nil {
			return exportError(CodePixels, expect.sourceIndex, frameIndex, ErrUnsupportedPixels, valuesErr)
		}
		if encodeErr := encodeFloat64ValuesInto(ctx, encoded, values); encodeErr != nil {
			return exportError(CodePixels, expect.sourceIndex, frameIndex, ErrUnsupportedPixels, encodeErr)
		}
		if err := p.appendSpool(spool, expect, encoded, spoolBytes); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plan) appendSpool(spool *os.File, frame *framePlan, encoded []byte, total *uint64) error {
	next, ok := checkedAdd(*total, uint64(len(encoded)))
	if !ok || next > p.options.Limits.MaxSpoolBytes {
		return exportError(CodeLimit, frame.sourceIndex, frame.frameIndex, ErrLimitExceeded, nil)
	}
	offset, err := spool.Seek(0, io.SeekCurrent)
	if err != nil {
		return exportError(CodeWrite, frame.sourceIndex, frame.frameIndex, ErrInvalidSource, err)
	}
	if err := writeExact(spool, encoded); err != nil {
		return exportError(CodeWrite, frame.sourceIndex, frame.frameIndex, ErrInvalidSource, err)
	}
	frame.spoolOffset, frame.spoolLength = offset, int64(len(encoded))
	*total = next
	return nil
}

func (p *Plan) writeFromSpool(ctx context.Context, destination io.Writer, spool *os.File) (report Report, err error) {
	affineRAS := affineLPS2RAS(p.value.affineLPS)
	header, err := buildHeader(headerSpec{
		Dimensions:  p.value.dimensions,
		Datatype:    p.value.datatype,
		BitPix:      p.value.bitpix,
		Spacing:     p.value.spacing,
		VoxelOffset: 352,
		Slope:       p.value.headerSlope,
		Intercept:   p.value.headerIntercept,
		AffineRAS:   affineRAS,
	})
	if err != nil {
		return Report{}, err
	}
	counter := &countingWriter{destination: destination}
	var output io.Writer = counter
	var zipper *gzip.Writer
	if p.options.Compression == CompressionGZIP {
		zipper = gzip.NewWriter(counter)
		output = zipper
	}
	writeErr := writeExact(output, header)
	if writeErr == nil {
		writeErr = writeExact(output, []byte{0, 0, 0, 0})
	}
	if writeErr == nil {
		if p.value.resampled {
			writeErr = p.writeResampled(ctx, output, spool)
		} else {
			writeErr = p.writeOrderedFrames(ctx, output, spool)
		}
	}
	if zipper != nil {
		writeErr = errors.Join(writeErr, zipper.Close())
	}
	if writeErr != nil {
		code := CodeWrite
		if errors.Is(writeErr, context.Canceled) || errors.Is(writeErr, context.DeadlineExceeded) {
			code = CodeCanceled
		}
		return Report{}, exportError(code, -1, -1, writeErr, writeErr)
	}
	report = Report{
		Dimensions: p.value.dimensions, Datatype: p.value.datatype, BitPix: p.value.bitpix,
		VoxelOffset: 352, IndexToRAS: affineRAS,
		ScalingPolicy: p.value.scalingPolicy, ScalingSlope: p.value.headerSlope, ScalingIntercept: p.value.headerIntercept,
		Compression: p.options.Compression, InputReordered: p.value.reordered,
		Resampled: p.value.resampled, Warnings: append([]string(nil), p.value.warnings...), BytesWritten: counter.count,
	}
	if p.value.resampled {
		report.Interpolation = "linear"
	}
	report.Sidecar = reportSidecar(report, p.value)
	return report, nil
}

func reportSidecar(report Report, plan *exportPlan) Sidecar {
	offsets := make([]float64, len(plan.timePoints))
	durations := make([]float64, len(plan.timePoints))
	allOffsets, allDurations := len(plan.timePoints) > 1, len(plan.timePoints) > 1
	for _, point := range plan.timePoints {
		allOffsets = allOffsets && point.timing.HasOffset
		allDurations = allDurations && point.timing.HasDuration
	}
	for index, point := range plan.timePoints {
		offsets[index] = point.timing.Offset.Seconds()
		durations[index] = point.timing.Duration.Seconds()
	}
	if !allOffsets {
		offsets = nil
	}
	if !allDurations {
		durations = nil
	}
	return Sidecar{
		Dimensions:               report.Dimensions,
		Datatype:                 datatypeName(report.Datatype),
		ScalingPolicy:            report.ScalingPolicy.String(),
		Reordered:                report.InputReordered,
		Resampled:                report.Resampled,
		Interpolation:            report.Interpolation,
		Units:                    CodedUnits{Code: plan.unitCode, Scheme: plan.unitScheme},
		Quantity:                 CodedUnits{Code: plan.quantityCode, Scheme: plan.quantityScheme},
		TemporalOffsetsSeconds:   offsets,
		TemporalDurationsSeconds: durations,
		Warnings:                 append([]string(nil), report.Warnings...),
	}
}

func datatypeName(datatype int16) string {
	switch datatype {
	case DatatypeUint8:
		return "uint8"
	case DatatypeInt8:
		return "int8"
	case DatatypeInt16:
		return "int16"
	case DatatypeUint16:
		return "uint16"
	case DatatypeInt32:
		return "int32"
	case DatatypeUint32:
		return "uint32"
	case DatatypeFloat32:
		return "float32"
	case DatatypeFloat64:
		return "float64"
	default:
		return "unknown"
	}
}

func (p *Plan) writeOrderedFrames(ctx context.Context, output io.Writer, spool *os.File) error {
	buffer := make([]byte, 128<<10)
	for _, point := range p.value.timePoints {
		for _, frame := range point.frames {
			if err := copySectionContext(ctx, output, spool, frame.spoolOffset, frame.spoolLength, buffer); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Plan) writeResampled(ctx context.Context, output io.Writer, spool *os.File) error {
	for timeIndex, point := range p.value.timePoints {
		stack := &render.Stack{Frames: make([]*render.Frame, len(point.frames))}
		for frameIndex, frame := range point.frames {
			if frame.spoolLength > int64(math.MaxInt) {
				return ErrLimitExceeded
			}
			pixels := make([]byte, int(frame.spoolLength))
			if _, err := spool.ReadAt(pixels, frame.spoolOffset); err != nil {
				return err
			}
			metadata := frame.metadata
			metadata.BitsStored = metadata.BitsAllocated
			metadata.HighBit = metadata.BitsAllocated - 1
			stack.Frames[frameIndex] = renderFrame(frame, metadata, pixels)
		}
		volume, err := render.BuildVolumeWithTolerances(stack, p.options.Tolerances)
		if err != nil {
			return fmt.Errorf("resample time point %d: %w", timeIndex+1, err)
		}
		lease, acquireErr := volume.AcquireSnapshotContext(ctx)
		if acquireErr != nil {
			_ = volume.Close()
			return acquireErr
		}
		snapshot, snapshotErr := lease.Snapshot()
		if snapshotErr == nil {
			var descriptor render.VolumeDescriptor
			descriptor, snapshotErr = snapshot.Descriptor()
			if snapshotErr == nil && (descriptor.Dimensions != [3]uint32{uint32(p.value.dimensions[0]), uint32(p.value.dimensions[1]), uint32(p.value.dimensions[2])} ||
				!affineClose(descriptor.IndexToPatientLPS, p.value.affineLPS, 1e-5)) {
				snapshotErr = ErrInvalidGeometry
			}
			if snapshotErr == nil {
				snapshotErr = snapshot.WriteModalityF32Context(ctx, output)
			}
		}
		releaseErr := lease.Release()
		closeErr := volume.Close()
		if err := errors.Join(snapshotErr, releaseErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func renderFrame(frame *framePlan, metadata pixeldata.Metadata, pixels []byte) *render.Frame {
	geometry := frame.geometry
	return &render.Frame{
		FrameIndex: frame.frameIndex, Metadata: metadata, ByteOrder: binary.LittleEndian, PixelBytes: pixels,
		Rescale:          render.Rescale{Slope: frame.rescale.slope, Intercept: frame.rescale.intercept},
		ImagePosition:    []float64{geometry.Origin.X, geometry.Origin.Y, geometry.Origin.Z},
		ImageOrientation: []float64{geometry.RowDir.X, geometry.RowDir.Y, geometry.RowDir.Z, geometry.ColDir.X, geometry.ColDir.Y, geometry.ColDir.Z},
		PixelSpacing:     []float64{geometry.RowSpacing, geometry.ColSpacing},
		Temporal:         frame.temporal,
	}
}

func sameIdentity(file *object.File, seriesUID, frameOfReferenceUID string) bool {
	if file == nil {
		return false
	}
	series, _ := file.GetUID(tagSeriesInstanceUID)
	frameOfReference, _ := file.GetUID(tagFrameOfReferenceUID)
	return series == seriesUID && frameOfReference == frameOfReferenceUID
}

func compatiblePixelMetadata(actual, expected pixeldata.Metadata) bool {
	return actual.Rows == expected.Rows && actual.Columns == expected.Columns &&
		actual.SamplesPerPixel == expected.SamplesPerPixel && actual.BitsAllocated == expected.BitsAllocated &&
		actual.BitsStored == expected.BitsStored && actual.HighBit == expected.HighBit &&
		actual.PixelRepresentation == expected.PixelRepresentation
}

func sliceGeometryClose(a, b render.SliceGeometry) bool {
	return a.Rows == b.Rows && a.Columns == b.Columns &&
		math.Abs(a.RowSpacing-b.RowSpacing) <= 1e-9 && math.Abs(a.ColSpacing-b.ColSpacing) <= 1e-9 &&
		vecClose(a.Origin, b.Origin, 1e-9) && vecClose(a.RowDir, b.RowDir, 1e-9) && vecClose(a.ColDir, b.ColDir, 1e-9)
}

func vecClose(a, b render.Vec3, tolerance float64) bool {
	return math.Abs(a.X-b.X) <= tolerance && math.Abs(a.Y-b.Y) <= tolerance && math.Abs(a.Z-b.Z) <= tolerance
}

func integerDatatypeOnly(metadata pixeldata.Metadata) int16 {
	datatype, _, _ := integerDatatype(metadata)
	return datatype
}

func checkedAdd(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}

type countingWriter struct {
	destination io.Writer
	count       int64
}

func (w *countingWriter) Write(payload []byte) (int, error) {
	written, err := w.destination.Write(payload)
	w.count += int64(written)
	return written, err
}

func copySectionContext(ctx context.Context, destination io.Writer, source io.ReaderAt, offset, length int64, buffer []byte) error {
	if offset < 0 || length < 0 {
		return ErrInvalidSource
	}
	reader := io.NewSectionReader(source, offset, length)
	remaining := length
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		read, readErr := io.ReadFull(reader, chunk)
		if read > 0 {
			if err := writeExact(destination, chunk[:read]); err != nil {
				return err
			}
			remaining -= int64(read)
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}
