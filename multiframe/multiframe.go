// Package multiframe converts Enhanced CT and Enhanced MR multi-frame
// instances into independent classic single-frame Part 10 instances.
package multiframe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	EnhancedCTImageStorageUID                = "1.2.840.10008.5.1.4.1.1.2.1"
	LegacyConvertedEnhancedCTImageStorageUID = "1.2.840.10008.5.1.4.1.1.2.2"
	CTImageStorageUID                        = "1.2.840.10008.5.1.4.1.1.2"
	EnhancedMRImageStorageUID                = "1.2.840.10008.5.1.4.1.1.4.1"
	LegacyConvertedEnhancedMRImageStorageUID = "1.2.840.10008.5.1.4.1.1.4.4"
	MRImageStorageUID                        = "1.2.840.10008.5.1.4.1.1.4"
)

var (
	ErrInvalidOptions      = errors.New("dicom/multiframe: invalid options")
	ErrInvalidSource       = errors.New("dicom/multiframe: invalid source")
	ErrUnsupportedSOPClass = errors.New("dicom/multiframe: unsupported SOP class")
	ErrNonconformant       = errors.New("dicom/multiframe: non-conformant enhanced image")
	ErrResourceLimit       = errors.New("dicom/multiframe: resource limit exceeded")
)

const conversionDescription = "Classic Image created from Enhanced Image"

// Limits bound source transcoding and aggregate output construction. Zero
// fields select the finite defaults returned by DefaultLimits.
type Limits = pixeldata.TranscodeLimits

// DefaultLimits returns the finite limits used by ConvertToClassic.
func DefaultLimits() Limits { return pixeldata.DefaultTranscodeLimits() }

// ContributingEquipment identifies the software or device responsible for
// conversion. Manufacturer is required. Other fields are copied into the
// mandatory Contributing Equipment Sequence item when non-empty.
type ContributingEquipment struct {
	Manufacturer          string
	InstitutionName       string
	StationName           string
	ManufacturerModelName string
	DeviceSerialNumber    string
	SoftwareVersions      []string
}

// Options controls identity, frame selection, codecs, resource limits, and
// conversion provenance. Frame indices are zero-based. A nil FrameIndices
// selects all source frames; a non-nil empty slice is invalid.
type Options struct {
	FrameIndices           []int
	SeriesInstanceUID      string
	SOPInstanceUIDForFrame func(sourceFrameIndex int) (string, error)
	DecoderRegistry        pixeldata.Registry
	Limits                 Limits
	Equipment              ContributingEquipment
	// Now is optional and exists to make Contribution DateTime deterministic.
	Now func() time.Time
}

// Report contains non-patient operational metadata for a successful atomic
// conversion.
type Report struct {
	SourceFrames      int
	ConvertedFrames   int
	NativeBytes       int64
	OutputBytes       int64
	TargetSOPClassUID string
	TransferSyntaxUID string
}

// ConversionError identifies the conversion stage and, when applicable, the
// zero-based source frame. It intentionally omits DICOM values.
type ConversionError struct {
	Stage      string
	FrameIndex int
	Err        error
}

func (e *ConversionError) Error() string {
	if e == nil {
		return "dicom/multiframe: conversion failed"
	}
	if e.FrameIndex >= 0 {
		return fmt.Sprintf("dicom/multiframe: conversion failed during %s for frame %d", e.Stage, e.FrameIndex)
	}
	return "dicom/multiframe: conversion failed during " + e.Stage
}

func (e *ConversionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ConvertToClassic converts selected frames from an Enhanced or Legacy
// Converted Enhanced CT/MR instance into detached classic single-frame files.
// The source is borrowed and never mutated. The result is atomic: on any error
// no partially built files are returned. Output is always Explicit VR Little
// Endian and contains native Pixel Data.
func ConvertToClassic(ctx context.Context, src *object.File, opts Options) ([]*object.File, Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := normalizeLimits(opts.Limits)
	if err != nil {
		return nil, Report{}, wrap("options", -1, err)
	}
	ctx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, Report{}, err
	}
	if src == nil || src.Dataset == nil {
		return nil, Report{}, wrap("source", -1, fmt.Errorf("%w: file and dataset are required", ErrInvalidSource))
	}

	sourceClassUID, targetClassUID, modality, err := conversionClass(src.Dataset)
	if err != nil {
		return nil, Report{}, wrap("source", -1, err)
	}
	sourceSOPUID, err := requiredUID(src.Dataset, tagSOPInstanceUID)
	if err != nil {
		return nil, Report{}, wrap("source", -1, err)
	}
	sourceSeriesUID, err := requiredUID(src.Dataset, tagSeriesInstanceUID)
	if err != nil {
		return nil, Report{}, wrap("source", -1, err)
	}
	seriesUID := strings.TrimSpace(opts.SeriesInstanceUID)
	if !core.IsValidUID(seriesUID) || seriesUID == sourceSeriesUID {
		return nil, Report{}, wrap("options", -1, fmt.Errorf("%w: SeriesInstanceUID must be valid and differ from the source", ErrInvalidOptions))
	}
	if opts.SOPInstanceUIDForFrame == nil {
		return nil, Report{}, wrap("options", -1, fmt.Errorf("%w: SOPInstanceUIDForFrame is required", ErrInvalidOptions))
	}
	if err := validateEquipment(opts.Equipment); err != nil {
		return nil, Report{}, wrap("options", -1, err)
	}

	transcoded, transcodeReport, err := pixeldata.TranscodeFile(ctx, src, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{
		DecoderRegistry: opts.DecoderRegistry,
		Limits:          limits,
	})
	if err != nil {
		if errors.Is(err, pixeldata.ErrTranscodeResourceLimit) {
			err = errors.Join(ErrResourceLimit, err)
		}
		return nil, Report{}, wrap("transcode", -1, err)
	}
	defer func() { _ = transcoded.Close() }()

	frames, err := pixeldata.ExtractNativeFramesView(transcoded.Dataset)
	if err != nil {
		return nil, Report{}, wrap("pixel_data", -1, err)
	}
	if frames.Metadata.NumberOfFrames < 1 {
		return nil, Report{}, wrap("source", -1, fmt.Errorf("%w: NumberOfFrames must be positive", ErrNonconformant))
	}
	selected, err := selectedFrames(opts.FrameIndices, frames.Metadata.NumberOfFrames, limits.MaxFrames)
	if err != nil {
		return nil, Report{}, wrap("selection", -1, err)
	}
	shared, perFrame, err := functionalGroups(transcoded.Dataset, frames.Metadata.NumberOfFrames)
	if err != nil {
		return nil, Report{}, wrap("functional_groups", -1, err)
	}

	uids := make(map[string]struct{}, len(selected))
	frameUIDs := make([]string, len(selected))
	for i, frameIndex := range selected {
		if err := ctx.Err(); err != nil {
			return nil, Report{}, err
		}
		uid, uidErr := opts.SOPInstanceUIDForFrame(frameIndex)
		if uidErr != nil {
			return nil, Report{}, wrap("uid", frameIndex, uidErr)
		}
		uid = strings.TrimSpace(uid)
		if !core.IsValidUID(uid) || uid == sourceSOPUID {
			return nil, Report{}, wrap("uid", frameIndex, fmt.Errorf("%w: generated SOP Instance UID is invalid or equals the source", ErrInvalidOptions))
		}
		if _, duplicate := uids[uid]; duplicate {
			return nil, Report{}, wrap("uid", frameIndex, fmt.Errorf("%w: duplicate generated SOP Instance UID", ErrInvalidOptions))
		}
		uids[uid] = struct{}{}
		frameUIDs[i] = uid
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	conversionTime := now()
	budget := conversionBudget{ctx: ctx, maxElements: limits.MaxElements, maxDepth: limits.MaxDepth}
	outputs := make([]*object.File, 0, len(selected))
	closeOutputs := func() {
		for _, file := range outputs {
			_ = file.Close()
		}
	}

	var outputBytes int64
	for outputIndex, frameIndex := range selected {
		if err := ctx.Err(); err != nil {
			closeOutputs()
			return nil, Report{}, err
		}
		dataset, buildErr := buildFrameDataset(&budget, transcoded.Dataset, shared, perFrame[frameIndex], frameIndex, frames.Data[frameIndex], sourceClassUID, sourceSOPUID, targetClassUID, modality, seriesUID, frameUIDs[outputIndex], opts.Equipment, conversionTime)
		if buildErr != nil {
			closeOutputs()
			return nil, Report{}, wrap("frame", frameIndex, buildErr)
		}
		file := &object.File{Dataset: dataset, Meta: object.New(std.Dictionary), TransferSyntax: transfer.ExplicitVRLittleEndian}
		if err := file.RebuildFileMeta(); err != nil {
			closeOutputs()
			return nil, Report{}, wrap("file_meta", frameIndex, err)
		}
		remaining := limits.MaxOutputBytes - outputBytes
		counter := &limitWriter{ctx: ctx, remaining: remaining}
		if err := object.WriteFile(counter, file); err != nil {
			closeOutputs()
			if errors.Is(err, ErrResourceLimit) {
				return nil, Report{}, wrap("output", frameIndex, err)
			}
			return nil, Report{}, wrap("write_validation", frameIndex, err)
		}
		outputBytes += counter.written
		outputs = append(outputs, file)
	}

	return outputs, Report{
		SourceFrames:      frames.Metadata.NumberOfFrames,
		ConvertedFrames:   len(outputs),
		NativeBytes:       transcodeReport.NativeBytes,
		OutputBytes:       outputBytes,
		TargetSOPClassUID: targetClassUID,
		TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
	}, nil
}

func buildFrameDataset(budget *conversionBudget, source, shared, perFrame *object.Object, frameIndex int, pixels []byte, sourceClassUID, sourceSOPUID, targetClassUID, modality, seriesUID, sopUID string, equipment ContributingEquipment, now time.Time) (*object.Object, error) {
	elements := make([]core.Element, 0, source.Len()+32)
	for _, element := range source.Elements() {
		if _, remove := multiFrameOnlyTags[element.Tag()]; remove {
			continue
		}
		clone, err := budget.cloneElement(element, 1)
		if err != nil {
			return nil, err
		}
		elements = append(elements, clone)
	}
	dataset := object.FromElements(elements, std.Dictionary)
	dataset.SetValueByteOrder(nil)

	sharedState, err := flattenFunctionalGroupItem(budget, dataset, shared, nil)
	if err != nil {
		return nil, err
	}
	perFrameState, err := flattenFunctionalGroupItem(budget, dataset, perFrame, &sharedState)
	if err != nil {
		return nil, err
	}
	if err := validateRequiredFunctionalGroups(sharedState, perFrameState, targetClassUID); err != nil {
		return nil, err
	}
	dataset.Put(ui(tagSOPClassUID, targetClassUID))
	dataset.Put(ui(tagSOPInstanceUID, sopUID))
	dataset.Put(ui(tagSeriesInstanceUID, seriesUID))
	dataset.Put(str(tagModality, core.VRCS, modality))
	dataset.Put(str(tagInstanceNumber, core.VRIS, strconv.Itoa(frameIndex+1)))

	pixelVR := core.VROB
	if bits, ok := uint16Value(dataset, tagBitsAllocated); ok && bits > 8 {
		pixelVR = core.VROW
	}
	pixelCopy := core.CloneBytes(pixels)
	if len(pixelCopy)%2 != 0 {
		pixelCopy = append(pixelCopy, 0)
	}
	dataset.Put(core.NewRawElement(tagPixelData, pixelVR, pixelCopy))

	conversionSource := dataSet(
		ui(tagReferencedSOPClassUID, sourceClassUID),
		ui(tagReferencedSOPInstanceUID, sourceSOPUID),
		str(tagReferencedFrameNumber, core.VRIS, strconv.Itoa(frameIndex+1)),
	)
	dataset.Put(seq(tagConversionSourceAttributesSequence, conversionSource))
	if err := appendSourceReference(dataset, conversionSource); err != nil {
		return nil, err
	}
	if !dataset.Has(tagDerivationDescription) {
		dataset.Put(str(tagDerivationDescription, core.VRST, conversionDescription))
	}
	if err := appendContributingEquipment(dataset, equipment, now); err != nil {
		return nil, err
	}
	emitMissingType2Attributes(dataset, targetClassUID)
	if err := validateClassicDataset(dataset, targetClassUID, modality); err != nil {
		return nil, err
	}
	return dataset, nil
}

func functionalGroups(dataset *object.Object, frames int) (*object.Object, []*object.Object, error) {
	sharedItems, ok := dataset.GetSequence(tagSharedFunctionalGroupsSequence)
	if !ok || len(sharedItems) != 1 {
		return nil, nil, fmt.Errorf("%w: SharedFunctionalGroupsSequence must contain exactly one item", ErrNonconformant)
	}
	perFrameItems, ok := dataset.GetSequence(tagPerFrameFunctionalGroupsSequence)
	if !ok || len(perFrameItems) != frames {
		return nil, nil, fmt.Errorf("%w: PerFrameFunctionalGroupsSequence item count must equal NumberOfFrames", ErrNonconformant)
	}
	return sharedItems[0], perFrameItems, nil
}

type flattenedFunctionalGroups struct {
	macros map[core.Tag]struct{}
	leaves map[core.Tag]core.Element
}

func validateRequiredFunctionalGroups(shared, perFrame flattenedFunctionalGroups, targetClassUID string) error {
	required := []core.Tag{tagPixelMeasuresSequence, tagPlanePositionSequence, tagPlaneOrientationSequence}
	switch targetClassUID {
	case CTImageStorageUID:
		required = append(required, tagCTImageFrameTypeSequence, tagPixelValueTransformationSequence)
	case MRImageStorageUID:
		required = append(required, tagMRImageFrameTypeSequence)
	}
	for _, tag := range required {
		_, inShared := shared.macros[tag]
		_, inPerFrame := perFrame.macros[tag]
		if !inShared && !inPerFrame {
			return fmt.Errorf("%w: required functional group macro %s is missing", ErrNonconformant, tag)
		}
	}
	return nil
}

func flattenFunctionalGroupItem(budget *conversionBudget, target, item *object.Object, forbidden *flattenedFunctionalGroups) (flattenedFunctionalGroups, error) {
	state := flattenedFunctionalGroups{
		macros: make(map[core.Tag]struct{}, item.Len()),
		leaves: make(map[core.Tag]core.Element),
	}
	for _, macro := range item.Elements() {
		if macro.VR() != core.VRSQ {
			return flattenedFunctionalGroups{}, fmt.Errorf("%w: functional group macro %s is not SQ", ErrNonconformant, macro.Tag())
		}
		if forbidden != nil {
			if _, duplicate := forbidden.macros[macro.Tag()]; duplicate {
				return flattenedFunctionalGroups{}, fmt.Errorf("%w: functional group macro %s appears in Shared and PerFrame", ErrNonconformant, macro.Tag())
			}
		}
		sequence, ok := macro.Value.(core.SequenceValue)
		if !ok || len(sequence.Items) != 1 {
			return flattenedFunctionalGroups{}, fmt.Errorf("%w: functional group macro %s must contain exactly one item", ErrNonconformant, macro.Tag())
		}
		state.macros[macro.Tag()] = struct{}{}
		for _, leaf := range sequence.Items[0].Elements {
			mapped := leaf
			if leaf.Tag() == tagFrameType {
				mapped.Header.Tag = tagImageType
			}
			clone, err := budget.cloneElement(mapped, 2)
			if err != nil {
				return flattenedFunctionalGroups{}, err
			}
			if previous, exists := state.leaves[clone.Tag()]; exists && !elementsEqual(previous, clone) {
				return flattenedFunctionalGroups{}, fmt.Errorf("%w: conflicting functional group values for %s", ErrNonconformant, clone.Tag())
			}
			if forbidden != nil {
				if previous, exists := forbidden.leaves[clone.Tag()]; exists && !elementsEqual(previous, clone) {
					return flattenedFunctionalGroups{}, fmt.Errorf("%w: conflicting Shared and PerFrame values for %s", ErrNonconformant, clone.Tag())
				}
			}
			state.leaves[clone.Tag()] = clone
			target.Put(clone)
		}
	}
	return state, nil
}

func appendSourceReference(dataset *object.Object, source core.DataSet) error {
	item := source
	item.Elements = append(item.Elements, seq(tagPurposeOfReferenceCodeSequence, codeItem("121322", "DCM", "Source image for image processing operation")))
	items := []core.DataSet{}
	if existing, ok := dataset.Get(tagSourceImageSequence); ok {
		sequence, sequenceOK := existing.Value.(core.SequenceValue)
		if !sequenceOK {
			return fmt.Errorf("%w: SourceImageSequence is malformed", ErrNonconformant)
		}
		items = append(items, sequence.Items...)
	}
	items = append(items, item)
	dataset.Put(seq(tagSourceImageSequence, items...))
	return nil
}

func appendContributingEquipment(dataset *object.Object, equipment ContributingEquipment, now time.Time) error {
	item := []core.Element{
		seq(tagPurposeOfReferenceCodeSequence, codeItem("109106", "DCM", "Enhanced Multi-frame Conversion Equipment")),
		str(tagManufacturer, core.VRLO, strings.TrimSpace(equipment.Manufacturer)),
		str(tagContributionDateTime, core.VRDT, now.Format("20060102150405-0700")),
		str(tagContributionDescription, core.VRST, conversionDescription),
	}
	optional := []struct {
		tag   core.Tag
		vr    core.VR
		value string
	}{
		{tagInstitutionName, core.VRLO, equipment.InstitutionName},
		{tagStationName, core.VRSH, equipment.StationName},
		{tagManufacturerModelName, core.VRLO, equipment.ManufacturerModelName},
		{tagDeviceSerialNumber, core.VRLO, equipment.DeviceSerialNumber},
	}
	for _, value := range optional {
		if trimmed := strings.TrimSpace(value.value); trimmed != "" {
			item = append(item, str(value.tag, value.vr, trimmed))
		}
	}
	if len(equipment.SoftwareVersions) > 0 {
		values := make([]string, 0, len(equipment.SoftwareVersions))
		for _, version := range equipment.SoftwareVersions {
			if version = strings.TrimSpace(version); version != "" {
				values = append(values, version)
			}
		}
		if len(values) > 0 {
			item = append(item, stringsElement(tagSoftwareVersions, core.VRLO, values))
		}
	}
	items := []core.DataSet{}
	if existing, ok := dataset.Get(tagContributingEquipmentSequence); ok {
		sequence, sequenceOK := existing.Value.(core.SequenceValue)
		if !sequenceOK {
			return fmt.Errorf("%w: ContributingEquipmentSequence is malformed", ErrNonconformant)
		}
		items = append(items, sequence.Items...)
	}
	items = append(items, dataSet(item...))
	dataset.Put(seq(tagContributingEquipmentSequence, items...))
	return nil
}

func validateEquipment(equipment ContributingEquipment) error {
	values := []struct {
		name     string
		value    string
		maxBytes int
		required bool
	}{
		{"Manufacturer", equipment.Manufacturer, 64, true},
		{"InstitutionName", equipment.InstitutionName, 64, false},
		{"StationName", equipment.StationName, 16, false},
		{"ManufacturerModelName", equipment.ManufacturerModelName, 64, false},
		{"DeviceSerialNumber", equipment.DeviceSerialNumber, 64, false},
	}
	for _, field := range values {
		value := strings.TrimSpace(field.value)
		if field.required && value == "" {
			return fmt.Errorf("%w: Equipment.%s is required", ErrInvalidOptions, field.name)
		}
		if len(value) > field.maxBytes || strings.Contains(value, `\`) {
			return fmt.Errorf("%w: Equipment.%s is not a valid single DICOM value", ErrInvalidOptions, field.name)
		}
	}
	for _, version := range equipment.SoftwareVersions {
		version = strings.TrimSpace(version)
		if len(version) > 64 || strings.Contains(version, `\`) {
			return fmt.Errorf("%w: Equipment.SoftwareVersions contains an invalid DICOM value", ErrInvalidOptions)
		}
	}
	return nil
}

func conversionClass(dataset *object.Object) (source, target, modality string, err error) {
	source, err = requiredUID(dataset, tagSOPClassUID)
	if err != nil {
		return "", "", "", err
	}
	switch source {
	case EnhancedCTImageStorageUID, LegacyConvertedEnhancedCTImageStorageUID:
		return source, CTImageStorageUID, "CT", nil
	case EnhancedMRImageStorageUID, LegacyConvertedEnhancedMRImageStorageUID:
		return source, MRImageStorageUID, "MR", nil
	default:
		return "", "", "", fmt.Errorf("%w", ErrUnsupportedSOPClass)
	}
}

// TargetSOPClassUID returns the classic SOP Class UID corresponding to a
// supported Enhanced or Legacy Converted Enhanced CT/MR SOP Class UID.
func TargetSOPClassUID(sourceSOPClassUID string) (string, bool) {
	switch strings.TrimSpace(sourceSOPClassUID) {
	case EnhancedCTImageStorageUID, LegacyConvertedEnhancedCTImageStorageUID:
		return CTImageStorageUID, true
	case EnhancedMRImageStorageUID, LegacyConvertedEnhancedMRImageStorageUID:
		return MRImageStorageUID, true
	default:
		return "", false
	}
}

// CanConvertSOPClass reports whether sourceSOPClassUID is supported by
// ConvertToClassic.
func CanConvertSOPClass(sourceSOPClassUID string) bool {
	_, ok := TargetSOPClassUID(sourceSOPClassUID)
	return ok
}

func selectedFrames(indices []int, sourceFrames, maxFrames int) ([]int, error) {
	if indices == nil {
		if sourceFrames > maxFrames {
			return nil, fmt.Errorf("%w: frame count exceeds limit", ErrResourceLimit)
		}
		out := make([]int, sourceFrames)
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("%w: FrameIndices must not be empty", ErrInvalidOptions)
	}
	if len(indices) > maxFrames {
		return nil, fmt.Errorf("%w: selected frame count exceeds limit", ErrResourceLimit)
	}
	seen := make(map[int]struct{}, len(indices))
	out := append([]int(nil), indices...)
	for _, index := range out {
		if index < 0 || index >= sourceFrames {
			return nil, fmt.Errorf("%w: frame index is out of range", ErrInvalidOptions)
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, fmt.Errorf("%w: duplicate frame index", ErrInvalidOptions)
		}
		seen[index] = struct{}{}
	}
	return out, nil
}

func requiredUID(dataset *object.Object, tag core.Tag) (string, error) {
	value, ok := dataset.GetString(tag)
	value = strings.TrimSpace(value)
	if !ok || !core.IsValidUID(value) {
		return "", fmt.Errorf("%w: required UID %s is missing or invalid", ErrNonconformant, tag)
	}
	return value, nil
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxFrames < 0 || limits.MaxInputBytes < 0 || limits.MaxOutputBytes < 0 || limits.MaxFragments < 0 || limits.MaxElements < 0 || limits.MaxDepth < 0 || limits.MaxExpansionRatio < 0 || limits.MaxDuration < 0 {
		return Limits{}, fmt.Errorf("%w: limits must not be negative", ErrInvalidOptions)
	}
	defaults := DefaultLimits()
	if limits.MaxFrames == 0 {
		limits.MaxFrames = defaults.MaxFrames
	}
	if limits.MaxPixels == 0 {
		limits.MaxPixels = defaults.MaxPixels
	}
	if limits.MaxInputBytes == 0 {
		limits.MaxInputBytes = defaults.MaxInputBytes
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if limits.MaxFragments == 0 {
		limits.MaxFragments = defaults.MaxFragments
	}
	if limits.MaxElements == 0 {
		limits.MaxElements = defaults.MaxElements
	}
	if limits.MaxDepth == 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxExpansionRatio == 0 {
		limits.MaxExpansionRatio = defaults.MaxExpansionRatio
	}
	if limits.MaxDuration == 0 {
		limits.MaxDuration = defaults.MaxDuration
	}
	return limits, nil
}

type limitWriter struct {
	ctx       context.Context
	remaining int64
	written   int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("%w: aggregate output bytes", ErrResourceLimit)
	}
	w.remaining -= int64(len(p))
	w.written += int64(len(p))
	return len(p), nil
}

var _ io.Writer = (*limitWriter)(nil)

func wrap(stage string, frameIndex int, err error) error {
	return &ConversionError{Stage: stage, FrameIndex: frameIndex, Err: err}
}
