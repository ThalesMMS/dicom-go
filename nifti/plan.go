package nifti

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dynamic"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parametricmap"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/render"
)

var (
	tagSeriesInstanceUID         = core.NewTag(0x0020, 0x000E)
	tagFrameOfReferenceUID       = core.NewTag(0x0020, 0x0052)
	tagSOPClassUID               = core.NewTag(0x0008, 0x0016)
	tagAnatomicalOrientationType = core.NewTag(0x0010, 0x2210)
	tagFloatPixelData            = core.NewTag(0x7FE0, 0x0008)
	tagDoubleFloatPixelData      = core.NewTag(0x7FE0, 0x0009)
	tagRescaleIntercept          = core.NewTag(0x0028, 0x1052)
	tagRescaleSlope              = core.NewTag(0x0028, 0x1053)
	tagModalityLUTSequence       = core.NewTag(0x0028, 0x3000)
	tagSharedFunctionalGroups    = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups  = core.NewTag(0x5200, 0x9230)
	tagPixelValueTransformation  = core.NewTag(0x0028, 0x9145)
)

type linearTransform struct {
	slope     float64
	intercept float64
}

type framePlan struct {
	sourceIndex int
	frameIndex  int
	ordinal     int

	geometry render.SliceGeometry
	temporal dynamic.FrameMetadata
	metadata pixeldata.Metadata
	rescale  linearTransform

	parametric  bool
	spoolOffset int64
	spoolLength int64
}

type timePointPlan struct {
	frames      []*framePlan
	geometry    render.VolumeGeometry
	regularGrid render.RegularGridGeometry
	timing      dynamic.TimePoint
}

type exportPlan struct {
	sources             int
	sourceEncapsulated  []bool
	seriesUID           string
	frameOfReferenceUID string
	frames              []*framePlan
	timePoints          []timePointPlan
	dimensions          [4]int
	datatype            int16
	bitpix              int16
	spoolDatatype       int16
	spoolBitpix         int16
	spacing             [4]float64
	affineLPS           render.GeometryAffine
	headerSlope         float64
	headerIntercept     float64
	scalingPolicy       ScalingPolicy
	parametric          bool
	unitCode            string
	unitScheme          string
	quantityCode        string
	quantityScheme      string
	reordered           bool
	resampled           bool
	warnings            []string
}

func buildPlan(ctx context.Context, source Source, options Options) (*exportPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, exportError(CodeCanceled, -1, -1, err, err)
	}
	if source == nil || source.Len() <= 0 {
		return nil, exportError(CodeSource, -1, -1, ErrInvalidSource, nil)
	}
	if source.Len() > options.Limits.MaxInstances {
		return nil, exportError(CodeLimit, -1, -1, ErrLimitExceeded, nil)
	}

	plan := &exportPlan{
		sources: source.Len(), sourceEncapsulated: make([]bool, source.Len()),
		headerSlope: 1, spacing: [4]float64{1, 1, 1, 1},
	}
	var seriesUID, frameOfReferenceUID string
	for sourceIndex := 0; sourceIndex < source.Len(); sourceIndex++ {
		if err := ctx.Err(); err != nil {
			return nil, exportError(CodeCanceled, sourceIndex, -1, err, err)
		}
		opened, err := source.Open(ctx, sourceIndex, object.ReadFileOptions{DeferPixelData: true})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, exportError(CodeCanceled, sourceIndex, -1, err, err)
			}
			return nil, exportError(CodeSource, sourceIndex, -1, ErrInvalidSource, err)
		}
		if opened.File == nil || opened.File.Dataset == nil {
			_ = opened.Close()
			return nil, exportError(CodeSource, sourceIndex, -1, ErrInvalidSource, nil)
		}
		file := opened.File
		plan.sourceEncapsulated[sourceIndex] = file.TransferSyntax.Encapsulated
		currentSeries, _ := file.GetUID(tagSeriesInstanceUID)
		currentFOR, _ := file.GetUID(tagFrameOfReferenceUID)
		if strings.TrimSpace(currentSeries) == "" || strings.TrimSpace(currentFOR) == "" {
			_ = opened.Close()
			return nil, exportError(CodeIdentity, sourceIndex, -1, ErrMixedIdentity, nil)
		}
		if sourceIndex == 0 {
			seriesUID, frameOfReferenceUID = currentSeries, currentFOR
		} else if currentSeries != seriesUID || currentFOR != frameOfReferenceUID {
			_ = opened.Close()
			return nil, exportError(CodeIdentity, sourceIndex, -1, ErrMixedIdentity, nil)
		}
		if orientation, ok := file.GetString(tagAnatomicalOrientationType); ok {
			orientation = strings.ToUpper(strings.TrimSpace(orientation))
			if orientation != "" && orientation != "BIPED" {
				_ = opened.Close()
				return nil, exportError(CodeQuadruped, sourceIndex, -1, ErrInvalidGeometry, nil)
			}
		}
		sopClass, _ := file.GetUID(tagSOPClassUID)
		if sopClass == parametricmap.ParametricMapStorage {
			err = planParametricSource(plan, file, sourceIndex, options)
		} else {
			err = planImageSource(plan, file, sourceIndex, options)
		}
		closeErr := opened.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, exportError(CodeSource, sourceIndex, -1, ErrInvalidSource, closeErr)
		}
	}
	if len(plan.frames) == 0 {
		return nil, exportError(CodeSource, -1, -1, ErrInvalidSource, nil)
	}
	plan.seriesUID, plan.frameOfReferenceUID = seriesUID, frameOfReferenceUID
	if len(plan.frames) > options.Limits.MaxFrames {
		return nil, exportError(CodeLimit, -1, -1, ErrLimitExceeded, nil)
	}
	if err := planTimeAndGeometry(plan, options); err != nil {
		return nil, err
	}
	if err := chooseEncoding(plan, options); err != nil {
		return nil, err
	}
	return plan, nil
}

func planImageSource(plan *exportPlan, file *object.File, sourceIndex int, options Options) error {
	if _, ok := file.Dataset.Get(tagFloatPixelData); ok {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, nil)
	}
	if _, ok := file.Dataset.Get(tagDoubleFloatPixelData); ok {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, nil)
	}
	if plan.parametric {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, nil)
	}
	metadata, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, err)
	}
	if err := validatePixelMetadata(metadata); err != nil {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, err)
	}
	if metadata.NumberOfFrames > options.Limits.MaxFrames-len(plan.frames) {
		return exportError(CodeLimit, sourceIndex, -1, ErrLimitExceeded, nil)
	}
	temporal, err := dynamic.ReadFrameMetadata(file)
	if err != nil || len(temporal) != metadata.NumberOfFrames {
		return exportError(CodeTemporal, sourceIndex, -1, ErrUnsupportedTemporal, err)
	}
	for frameIndex := 0; frameIndex < metadata.NumberOfFrames; frameIndex++ {
		geometry, err := strictSliceGeometry(file.FrameGeometryAt(frameIndex), int(metadata.Rows), int(metadata.Columns))
		if err != nil {
			return exportError(CodeGeometry, sourceIndex, frameIndex, ErrInvalidGeometry, err)
		}
		rescale, err := frameLinearTransform(file.Dataset, frameIndex)
		if err != nil {
			return exportError(CodeUnsupportedModality, sourceIndex, frameIndex, ErrUnsupportedScaling, err)
		}
		frameTemporal := temporal[frameIndex]
		frameTemporal.FrameIndex = len(plan.frames)
		plan.frames = append(plan.frames, &framePlan{
			sourceIndex: sourceIndex,
			frameIndex:  frameIndex,
			ordinal:     len(plan.frames),
			geometry:    geometry,
			temporal:    frameTemporal,
			metadata:    metadata,
			rescale:     rescale,
		})
	}
	return nil
}

func planParametricSource(plan *exportPlan, file *object.File, sourceIndex int, options Options) error {
	if len(plan.frames) > 0 && !plan.parametric {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, nil)
	}
	pm, err := parametricmap.ReadMetadata(file)
	if err != nil {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, err)
	}
	payloadBytes, payloadOK := parametricPayloadBytes(file.Dataset)
	workingBytes, workingOK := parametricWorkingSetBytes(payloadBytes, pm.Rows, pm.Columns)
	if !payloadOK || !workingOK || workingBytes > options.Limits.MaxInMemorySourceBytes {
		return exportError(CodeLimit, sourceIndex, -1, ErrLimitExceeded, nil)
	}
	if pm.NumberOfFrames > options.Limits.MaxFrames-len(plan.frames) {
		return exportError(CodeLimit, sourceIndex, -1, ErrLimitExceeded, nil)
	}
	temporal, err := dynamic.ReadFrameMetadata(file)
	if err != nil || len(temporal) != pm.NumberOfFrames {
		return exportError(CodeTemporal, sourceIndex, -1, ErrUnsupportedTemporal, err)
	}
	unitCode, unitScheme := strings.TrimSpace(pm.Units.Value), strings.TrimSpace(pm.Units.Scheme)
	quantityCode, quantityScheme := strings.TrimSpace(pm.Quantity.Value), strings.TrimSpace(pm.Quantity.Scheme)
	if plan.parametric && (unitCode != plan.unitCode || unitScheme != plan.unitScheme ||
		quantityCode != plan.quantityCode || quantityScheme != plan.quantityScheme) {
		return exportError(CodePixels, sourceIndex, -1, ErrUnsupportedPixels, nil)
	}
	plan.parametric = true
	plan.unitCode, plan.unitScheme = unitCode, unitScheme
	plan.quantityCode, plan.quantityScheme = quantityCode, quantityScheme
	for frameIndex := 0; frameIndex < pm.NumberOfFrames; frameIndex++ {
		frameTemporal := temporal[frameIndex]
		frameTemporal.FrameIndex = len(plan.frames)
		plan.frames = append(plan.frames, &framePlan{
			sourceIndex: sourceIndex,
			frameIndex:  frameIndex,
			ordinal:     len(plan.frames),
			geometry:    pm.Frames[frameIndex].Geometry,
			temporal:    frameTemporal,
			parametric:  true,
			rescale:     linearTransform{slope: 1},
			metadata: pixeldata.Metadata{
				Rows: uint16(pm.Rows), Columns: uint16(pm.Columns), SamplesPerPixel: 1,
				BitsAllocated: 64, BitsStored: 64, HighBit: 63, NumberOfFrames: pm.NumberOfFrames,
				PhotometricInterpretation: "MONOCHROME2",
			},
		})
	}
	return nil
}

func parametricPayloadBytes(obj *object.Object) (uint64, bool) {
	if obj == nil {
		return 0, false
	}
	for _, tag := range []core.Tag{tagFloatPixelData, tagDoubleFloatPixelData, core.TagPixelData} {
		if element, ok := obj.Get(tag); ok {
			return uint64(element.Length()), true
		}
	}
	return 0, false
}

func parametricWorkingSetBytes(payloadBytes uint64, rows, columns int) (uint64, bool) {
	if rows <= 0 || columns <= 0 {
		return 0, false
	}
	frameVoxels, ok := checkedMul(uint64(rows), uint64(columns))
	if !ok {
		return 0, false
	}
	frameBuffers, ok := checkedMul(frameVoxels, 16) // one []float64 plus one encoded frame
	if !ok {
		return 0, false
	}
	return checkedAdd(payloadBytes, frameBuffers)
}

func validatePixelMetadata(metadata pixeldata.Metadata) error {
	if metadata.Rows == 0 || metadata.Columns == 0 || metadata.NumberOfFrames <= 0 || metadata.SamplesPerPixel != 1 {
		return ErrUnsupportedPixels
	}
	photometric := strings.ToUpper(strings.Trim(strings.TrimSpace(metadata.PhotometricInterpretation), "\x00"))
	if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" {
		return ErrUnsupportedPixels
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 && metadata.BitsAllocated != 32 {
		return ErrUnsupportedPixels
	}
	if metadata.PixelRepresentation > 1 || metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated ||
		metadata.HighBit >= metadata.BitsAllocated || metadata.HighBit+1 < metadata.BitsStored {
		return ErrUnsupportedPixels
	}
	return nil
}

func strictSliceGeometry(input object.FrameGeometry, rows, columns int) (render.SliceGeometry, error) {
	if rows <= 0 || columns <= 0 || len(input.ImagePositionPatient) != 3 ||
		len(input.ImageOrientationPatient) != 6 || len(input.PixelSpacing) != 2 {
		return render.SliceGeometry{}, ErrInvalidGeometry
	}
	row := render.Vec3{X: input.ImageOrientationPatient[0], Y: input.ImageOrientationPatient[1], Z: input.ImageOrientationPatient[2]}
	column := render.Vec3{X: input.ImageOrientationPatient[3], Y: input.ImageOrientationPatient[4], Z: input.ImageOrientationPatient[5]}
	origin := render.Vec3{X: input.ImagePositionPatient[0], Y: input.ImagePositionPatient[1], Z: input.ImagePositionPatient[2]}
	if !finiteVec(row) || !finiteVec(column) || !finiteVec(origin) ||
		math.Abs(row.Length()-1) > 1e-4 || math.Abs(column.Length()-1) > 1e-4 ||
		math.Abs(row.Dot(column)) > 1e-4 || !finitePositive(input.PixelSpacing[0]) || !finitePositive(input.PixelSpacing[1]) {
		return render.SliceGeometry{}, ErrInvalidGeometry
	}
	row = row.Normalize()
	column = column.Sub(row.Scale(column.Dot(row))).Normalize()
	normal := row.Cross(column).Normalize()
	if normal == (render.Vec3{}) {
		return render.SliceGeometry{}, ErrInvalidGeometry
	}
	return render.SliceGeometry{
		Origin: origin, RowDir: row, ColDir: column, Normal: normal,
		RowSpacing: input.PixelSpacing[0], ColSpacing: input.PixelSpacing[1],
		Rows: rows, Columns: columns,
	}, nil
}

func frameLinearTransform(obj *object.Object, frameIndex int) (linearTransform, error) {
	transform := linearTransform{slope: 1}
	var err error
	if transform, err = overlayLinearTransform(transform, obj); err != nil {
		return linearTransform{}, err
	}
	if items, ok := obj.GetSequence(tagSharedFunctionalGroups); ok && len(items) > 0 {
		if transform, err = overlayPixelValueTransform(transform, items[0]); err != nil {
			return linearTransform{}, err
		}
	}
	if items, ok := obj.GetSequence(tagPerFrameFunctionalGroups); ok && frameIndex >= 0 && frameIndex < len(items) {
		if transform, err = overlayPixelValueTransform(transform, items[frameIndex]); err != nil {
			return linearTransform{}, err
		}
	}
	if !finite(transform.slope) || transform.slope == 0 || !finite(transform.intercept) {
		return linearTransform{}, ErrUnsupportedScaling
	}
	return transform, nil
}

func overlayPixelValueTransform(base linearTransform, container *object.Object) (linearTransform, error) {
	if container == nil {
		return base, nil
	}
	items, ok := container.GetSequence(tagPixelValueTransformation)
	if !ok || len(items) == 0 {
		return base, nil
	}
	return overlayLinearTransform(base, items[0])
}

func overlayLinearTransform(base linearTransform, obj *object.Object) (linearTransform, error) {
	if obj == nil {
		return base, nil
	}
	if items, ok := obj.GetSequence(tagModalityLUTSequence); ok && len(items) > 0 {
		return linearTransform{}, ErrUnsupportedScaling
	}
	if _, present := obj.Get(tagRescaleSlope); present {
		value, err := obj.GetFloat(tagRescaleSlope)
		if err != nil || !finite(value) || value == 0 {
			return linearTransform{}, ErrUnsupportedScaling
		}
		base.slope = value
	}
	if _, present := obj.Get(tagRescaleIntercept); present {
		value, err := obj.GetFloat(tagRescaleIntercept)
		if err != nil || !finite(value) {
			return linearTransform{}, ErrUnsupportedScaling
		}
		base.intercept = value
	}
	return base, nil
}

func planTimeAndGeometry(plan *exportPlan, options Options) error {
	metadata := make([]dynamic.FrameMetadata, len(plan.frames))
	declaredTemporalPositions := 0
	for index, frame := range plan.frames {
		metadata[index] = frame.temporal
		metadata[index].FrameIndex = index
		if metadata[index].HasNumberOfTemporalPositions {
			declared := metadata[index].NumberOfTemporalPositions
			if declared <= 0 || (declaredTemporalPositions != 0 && declared != declaredTemporalPositions) {
				return exportError(CodeTemporal, frame.sourceIndex, frame.frameIndex, ErrUnsupportedTemporal, nil)
			}
			declaredTemporalPositions = declared
		}
	}
	timeline := dynamic.Build(metadata)
	if len(timeline.Points) == 0 || timeline.MultipleStacks {
		return exportError(CodeMultipleStacks, -1, -1, ErrUnsupportedTemporal, nil)
	}
	if timeline.UsedOccurrenceFallback || (declaredTemporalPositions != 0 && len(timeline.Points) != declaredTemporalPositions) {
		return exportError(CodeTemporal, -1, -1, ErrUnsupportedTemporal, nil)
	}
	if timeline.Dynamic {
		for _, frame := range metadata {
			if !frame.HasTemporalPosition && !frame.HasOffset && !frame.HasTrigger && !frame.HasPhase {
				return exportError(CodeTemporal, -1, -1, ErrUnsupportedTemporal, nil)
			}
		}
	}
	outputOrdinal := 0
	for _, point := range timeline.Points {
		if len(point.Stacks) != 1 || len(point.Stacks[0].FrameIndices) < 2 {
			code := CodeMultipleStacks
			if len(point.Stacks) == 1 {
				code = CodeSingleSlice
			}
			return exportError(code, -1, -1, ErrInvalidGeometry, nil)
		}
		frames := make([]*framePlan, len(point.Stacks[0].FrameIndices))
		for index, frameIndex := range point.Stacks[0].FrameIndices {
			if frameIndex < 0 || frameIndex >= len(plan.frames) {
				return exportError(CodeTemporal, -1, -1, ErrUnsupportedTemporal, nil)
			}
			frames[index] = plan.frames[frameIndex]
		}
		refNormal := frames[0].geometry.Normal
		sort.SliceStable(frames, func(i, j int) bool {
			return frames[i].geometry.PositionAlong(refNormal) < frames[j].geometry.PositionAlong(refNormal)
		})
		for _, frame := range frames {
			if frame.ordinal != outputOrdinal {
				plan.reordered = true
			}
			outputOrdinal++
		}
		geometries := make([]render.SliceGeometry, len(frames))
		for index, frame := range frames {
			geometries[index] = frame.geometry
		}
		geometry := render.BuildVolumeGeometry(geometries, options.Tolerances)
		if geometry.Disposition == render.GeometryUnsupported {
			return geometryExportError(frames[0], geometry.PrimaryIssue)
		}
		if geometry.RequiresResampling && options.Geometry != GeometryResampleLinear {
			return geometryExportError(frames[0], geometry.PrimaryIssue)
		}
		if geometry.RequiresResampling {
			plan.resampled = true
		}
		plan.timePoints = append(plan.timePoints, timePointPlan{frames: frames, geometry: geometry, timing: point})
	}
	if plan.resampled {
		for index := range plan.timePoints {
			grid, err := render.PlanRegularGridGeometry(plan.timePoints[index].geometry)
			if err != nil {
				return exportError(CodeGeometry, plan.timePoints[index].frames[0].sourceIndex,
					plan.timePoints[index].frames[0].frameIndex, ErrInvalidGeometry, err)
			}
			plan.timePoints[index].regularGrid = grid
		}
	}
	first := plan.timePoints[0]
	if plan.resampled {
		plan.dimensions = [4]int{first.regularGrid.Dimensions[0], first.regularGrid.Dimensions[1], first.regularGrid.Dimensions[2], len(plan.timePoints)}
		plan.affineLPS = first.regularGrid.VoxelToPatientAffine
		plan.spacing = [4]float64{first.regularGrid.ColSpacing, first.regularGrid.RowSpacing, first.regularGrid.SliceSpacing, 1}
	} else {
		plan.dimensions = [4]int{first.geometry.Slices[0].Columns, first.geometry.Slices[0].Rows, len(first.frames), len(plan.timePoints)}
		plan.affineLPS = first.geometry.VoxelToPatientAffine
		plan.spacing = [4]float64{first.geometry.Slices[0].ColSpacing, first.geometry.Slices[0].RowSpacing, affineColumnNorm(plan.affineLPS, 2), 1}
	}
	for timeIndex := 1; timeIndex < len(plan.timePoints); timeIndex++ {
		point := plan.timePoints[timeIndex]
		if plan.resampled {
			if point.regularGrid.Dimensions != [3]int{plan.dimensions[0], plan.dimensions[1], plan.dimensions[2]} ||
				!affineClose(point.regularGrid.VoxelToPatientAffine, plan.affineLPS, 1e-5) {
				return exportError(CodeTemporal, -1, -1, ErrUnsupportedTemporal, nil)
			}
		} else if point.geometry.Slices[0].Columns != plan.dimensions[0] || point.geometry.Slices[0].Rows != plan.dimensions[1] ||
			len(point.frames) != plan.dimensions[2] || !affineClose(point.geometry.VoxelToPatientAffine, plan.affineLPS, 1e-5) {
			return exportError(CodeTemporal, -1, -1, ErrUnsupportedTemporal, nil)
		}
	}
	spacing, err := temporalSpacing(plan.timePoints, options.TemporalSpacingSeconds)
	if err != nil {
		return err
	}
	plan.spacing[3] = spacing
	return validatePlanDimensions(plan, options)
}

func temporalSpacing(points []timePointPlan, override float64) (float64, error) {
	if len(points) <= 1 {
		return 1, nil
	}
	allOffsets := true
	for _, point := range points {
		allOffsets = allOffsets && point.timing.HasOffset
	}
	if allOffsets {
		first := points[1].timing.Offset - points[0].timing.Offset
		if first <= 0 {
			return 0, exportError(CodeIrregularTime, -1, -1, ErrUnsupportedTemporal, nil)
		}
		for index := 2; index < len(points); index++ {
			delta := points[index].timing.Offset - points[index-1].timing.Offset
			if delta <= 0 || absDuration(delta-first) > time.Microsecond {
				return 0, exportError(CodeIrregularTime, -1, -1, ErrUnsupportedTemporal, nil)
			}
		}
		spacing := first.Seconds()
		if override != 0 && (!finitePositive(override) || math.Abs(override-spacing) > 1e-6) {
			return 0, exportError(CodeIrregularTime, -1, -1, ErrUnsupportedTemporal, nil)
		}
		return spacing, nil
	}
	allDurations := true
	for _, point := range points {
		allDurations = allDurations && point.timing.HasDuration && point.timing.Duration > 0
	}
	if allDurations {
		first := points[0].timing.Duration
		for _, point := range points[1:] {
			if absDuration(point.timing.Duration-first) > time.Microsecond {
				return 0, exportError(CodeIrregularTime, -1, -1, ErrUnsupportedTemporal, nil)
			}
		}
		spacing := first.Seconds()
		if override != 0 && (!finitePositive(override) || math.Abs(override-spacing) > 1e-6) {
			return 0, exportError(CodeIrregularTime, -1, -1, ErrUnsupportedTemporal, nil)
		}
		return spacing, nil
	}
	if override != 0 {
		if !finitePositive(override) {
			return 0, exportError(CodeMissingTiming, -1, -1, ErrUnsupportedTemporal, nil)
		}
		return override, nil
	}
	return 0, exportError(CodeMissingTiming, -1, -1, ErrUnsupportedTemporal, nil)
}

func validatePlanDimensions(plan *exportPlan, options Options) error {
	voxels := uint64(1)
	for _, dimension := range plan.dimensions {
		if dimension <= 0 || dimension > math.MaxInt16 {
			return exportError(CodeNIfTI2, -1, -1, ErrNIfTI2Required, nil)
		}
		var ok bool
		voxels, ok = checkedMul(voxels, uint64(dimension))
		if !ok || voxels > options.Limits.MaxVoxels {
			return exportError(CodeLimit, -1, -1, ErrLimitExceeded, nil)
		}
	}
	if plan.resampled && voxels > options.Limits.MaxResampleVoxels {
		return exportError(CodeLimit, -1, -1, ErrLimitExceeded, nil)
	}
	return nil
}

func chooseEncoding(plan *exportPlan, options Options) error {
	if plan.parametric {
		if plan.resampled {
			return exportError(CodeGeometry, -1, -1, ErrInvalidGeometry, nil)
		}
		plan.datatype, plan.bitpix = DatatypeFloat64, 64
		plan.spoolDatatype, plan.spoolBitpix = plan.datatype, plan.bitpix
		plan.headerSlope, plan.headerIntercept = 1, 0
		plan.scalingPolicy = ScalingApplyFloat64
		return validateOutputSize(plan, options)
	}
	first := plan.frames[0]
	for _, frame := range plan.frames[1:] {
		if frame.metadata.Rows != first.metadata.Rows || frame.metadata.Columns != first.metadata.Columns ||
			frame.metadata.SamplesPerPixel != first.metadata.SamplesPerPixel ||
			frame.metadata.BitsAllocated != first.metadata.BitsAllocated ||
			frame.metadata.PixelRepresentation != first.metadata.PixelRepresentation {
			return exportError(CodePixels, frame.sourceIndex, frame.frameIndex, ErrUnsupportedPixels, nil)
		}
	}
	storedDatatype, storedBitpix, err := integerDatatype(first.metadata)
	if err != nil {
		return exportError(CodePixels, first.sourceIndex, first.frameIndex, ErrUnsupportedPixels, err)
	}
	plan.spoolDatatype, plan.spoolBitpix = storedDatatype, storedBitpix
	switch {
	case plan.resampled:
		plan.datatype, plan.bitpix = DatatypeFloat32, 32
		plan.headerSlope, plan.headerIntercept = 1, 0
		plan.scalingPolicy = ScalingApplyFloat32
	case options.Scaling == ScalingApplyFloat32:
		plan.datatype, plan.bitpix = DatatypeFloat32, 32
		plan.spoolDatatype, plan.spoolBitpix = plan.datatype, plan.bitpix
		plan.headerSlope, plan.headerIntercept = 1, 0
		plan.scalingPolicy = ScalingApplyFloat32
	case options.Scaling == ScalingApplyFloat64:
		plan.datatype, plan.bitpix = DatatypeFloat64, 64
		plan.spoolDatatype, plan.spoolBitpix = plan.datatype, plan.bitpix
		plan.headerSlope, plan.headerIntercept = 1, 0
		plan.scalingPolicy = ScalingApplyFloat64
	default:
		plan.datatype, plan.bitpix = storedDatatype, storedBitpix
		plan.headerSlope, plan.headerIntercept = first.rescale.slope, first.rescale.intercept
		plan.scalingPolicy = ScalingPreserveUniform
		for _, frame := range plan.frames[1:] {
			if frame.rescale != first.rescale {
				return exportError(CodeScaling, frame.sourceIndex, frame.frameIndex, ErrUnsupportedScaling, nil)
			}
		}
		if !float32Representable(plan.headerSlope) || !float32Representable(plan.headerIntercept) {
			return exportError(CodeScaling, -1, -1, ErrUnsupportedScaling, nil)
		}
	}
	return validateOutputSize(plan, options)
}

func validateOutputSize(plan *exportPlan, options Options) error {
	voxels := uint64(1)
	for _, dimension := range plan.dimensions {
		var ok bool
		voxels, ok = checkedMul(voxels, uint64(dimension))
		if !ok {
			return exportError(CodeLimit, -1, -1, ErrLimitExceeded, nil)
		}
	}
	bytes, ok := checkedMul(voxels, uint64(plan.bitpix/8))
	if !ok || bytes > options.Limits.MaxUncompressedBytes {
		return exportError(CodeLimit, -1, -1, ErrLimitExceeded, nil)
	}
	return nil
}

func integerDatatype(metadata pixeldata.Metadata) (int16, int16, error) {
	signed := metadata.PixelRepresentation == 1
	switch metadata.BitsAllocated {
	case 8:
		if signed {
			return DatatypeInt8, 8, nil
		}
		return DatatypeUint8, 8, nil
	case 16:
		if signed {
			return DatatypeInt16, 16, nil
		}
		return DatatypeUint16, 16, nil
	case 32:
		if signed {
			return DatatypeInt32, 32, nil
		}
		return DatatypeUint32, 32, nil
	default:
		return 0, 0, ErrUnsupportedPixels
	}
}

func exportError(code ErrorCode, sourceIndex, frameIndex int, sentinel, cause error) error {
	if cause == nil {
		cause = sentinel
	} else if sentinel != nil && !errors.Is(cause, sentinel) {
		cause = errors.Join(sentinel, cause)
	}
	return &ExportError{Code: code, SourceIndex: sourceIndex, FrameIndex: frameIndex, Err: cause}
}

func geometryExportError(frame *framePlan, issue render.GeometryIssue) error {
	sourceIndex, frameIndex := -1, -1
	if frame != nil {
		sourceIndex, frameIndex = frame.sourceIndex, frame.frameIndex
	}
	return &ExportError{
		Code: CodeGeometry, SourceIndex: sourceIndex, FrameIndex: frameIndex,
		GeometryIssue: issue, Err: ErrInvalidGeometry,
	}
}

func finite(value float64) bool         { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finitePositive(value float64) bool { return finite(value) && value > 0 }
func finiteVec(value render.Vec3) bool  { return finite(value.X) && finite(value.Y) && finite(value.Z) }

func affineColumnNorm(affine render.GeometryAffine, column int) float64 {
	return math.Sqrt(affine[column]*affine[column] + affine[4+column]*affine[4+column] + affine[8+column]*affine[8+column])
}

func affineClose(a, b render.GeometryAffine, tolerance float64) bool {
	for index := range a {
		if math.Abs(a[index]-b[index]) > tolerance*math.Max(1, math.Max(math.Abs(a[index]), math.Abs(b[index]))) {
			return false
		}
	}
	return true
}

func checkedMul(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}

func float32Representable(value float64) bool {
	converted := float32(value)
	if math.IsNaN(float64(converted)) || math.IsInf(float64(converted), 0) {
		return false
	}
	delta := math.Abs(value - float64(converted))
	return delta <= 1e-7*math.Max(1, math.Abs(value))
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
