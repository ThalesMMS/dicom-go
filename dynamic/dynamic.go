// Package dynamic separates temporal and spatial dimensions in dynamic DICOM
// acquisitions. It reads enhanced functional groups and legacy timing tags
// without decoding Pixel Data, then builds deterministic time points and
// independent spatial stacks for 4D navigation.
package dynamic

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagNumberOfFrames                 = core.NewTag(0x0028, 0x0008)
	tagSharedFunctionalGroups         = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups       = core.NewTag(0x5200, 0x9230)
	tagFrameContentSequence           = core.NewTag(0x0020, 0x9111)
	tagDimensionIndexSequence         = core.NewTag(0x0020, 0x9222)
	tagDimensionIndexPointer          = core.NewTag(0x0020, 0x9165)
	tagDimensionIndexValues           = core.NewTag(0x0020, 0x9157)
	tagTemporalPositionIndex          = core.NewTag(0x0020, 0x9128)
	tagTemporalPositionIdentifier     = core.NewTag(0x0020, 0x0100)
	tagNumberOfTemporalPositions      = core.NewTag(0x0020, 0x0105)
	tagStackID                        = core.NewTag(0x0020, 0x9056)
	tagInStackPositionNumber          = core.NewTag(0x0020, 0x9057)
	tagFrameAcquisitionDateTime       = core.NewTag(0x0018, 0x9074)
	tagAcquisitionDateTime            = core.NewTag(0x0008, 0x002A)
	tagFrameReferenceTime             = core.NewTag(0x0054, 0x1300)
	tagTriggerTime                    = core.NewTag(0x0018, 0x1060)
	tagNominalCardiacTriggerDelayTime = core.NewTag(0x0020, 0x9153)
	tagNominalCardiacPhasePercent     = core.NewTag(0x0020, 0x9241)
	tagActualFrameDuration            = core.NewTag(0x0018, 0x1242)
	tagFrameTime                      = core.NewTag(0x0018, 0x1063)
	tagFrameTimeVector                = core.NewTag(0x0018, 0x1065)
)

// FrameMetadata records temporal and spatial identity independently. Optional
// values carry an explicit Has flag so a real zero is never confused with an
// absent DICOM attribute.
type FrameMetadata struct {
	FrameIndex int

	TemporalPosition    int
	HasTemporalPosition bool
	// NumberOfTemporalPositions preserves the top-level DICOM declaration when
	// HasNumberOfTemporalPositions is true. It is provenance, not an inferred
	// count of the frames supplied to Build.
	NumberOfTemporalPositions    int
	HasNumberOfTemporalPositions bool
	StackID                      string
	InStackPosition              int
	HasInStackPosition           bool

	Offset      time.Duration
	HasOffset   bool
	Duration    time.Duration
	HasDuration bool
	Trigger     time.Duration
	HasTrigger  bool
	Phase       float64
	HasPhase    bool

	AcquisitionTime    time.Time
	HasAcquisitionTime bool
	SpatialPosition    float64
	HasSpatialPosition bool
}

type Stack struct {
	ID           string
	FrameIndices []int
}

type TimePoint struct {
	Ordinal          int
	TemporalPosition int
	HasPosition      bool
	Offset           time.Duration
	HasOffset        bool
	Duration         time.Duration
	HasDuration      bool
	Trigger          time.Duration
	HasTrigger       bool
	Phase            float64
	HasPhase         bool
	// UsedOccurrenceFallback reports that at least one frame was assigned to this
	// time point by repeated spatial occurrence rather than explicit identity.
	UsedOccurrenceFallback bool
	Stacks                 []Stack
}

type Timeline struct {
	Frames         []FrameMetadata
	Points         []TimePoint
	Dynamic        bool
	Irregular      bool
	Gated          bool
	MultipleStacks bool
	// UsedOccurrenceFallback reports that at least one time point contains a
	// frame assigned by repeated spatial occurrence.
	UsedOccurrenceFallback bool
}

// Read inspects temporal metadata without touching Pixel Data and validates
// declarations that describe the complete temporal acquisition.
func Read(file *object.File) (Timeline, error) {
	return read(file, true)
}

// ReadFrameMetadata returns the per-frame temporal/spatial descriptors without
// requiring one object to contain the acquisition's complete set of temporal
// positions. This is the additive seam used by multi-instance consumers: they
// combine descriptors from every instance, then call Build once for the whole
// series. Pixel Data is never touched.
func ReadFrameMetadata(file *object.File) ([]FrameMetadata, error) {
	timeline, err := read(file, false)
	if err != nil {
		return nil, err
	}
	return append([]FrameMetadata(nil), timeline.Frames...), nil
}

func read(file *object.File, validateDeclaredPositions bool) (Timeline, error) {
	if file == nil || file.Dataset == nil {
		return Timeline{}, fmt.Errorf("dicom/dynamic: nil dataset")
	}
	obj := file.Dataset
	frameCount := derivedio.Int(obj, tagNumberOfFrames)
	if frameCount <= 0 {
		frameCount = 1
	}
	if frameCount > 1_000_000 {
		return Timeline{}, fmt.Errorf("dicom/dynamic: unreasonable frame count %d", frameCount)
	}
	perFrame := derivedio.Sequence(obj, tagPerFrameFunctionalGroups)
	if len(perFrame) != 0 && len(perFrame) != frameCount {
		return Timeline{}, fmt.Errorf("dicom/dynamic: %d per-frame groups for %d frames", len(perFrame), frameCount)
	}
	shared := firstSequenceItem(obj, tagSharedFunctionalGroups)
	pointers := dimensionPointers(obj)
	vector := directFloats(obj, tagFrameTimeVector)
	if len(vector) > 0 {
		if len(vector) != frameCount {
			return Timeline{}, fmt.Errorf("dicom/dynamic: FrameTimeVector has %d values for %d frames", len(vector), frameCount)
		}
		for index, increment := range vector {
			if !finite(increment) || increment < 0 {
				return Timeline{}, fmt.Errorf("dicom/dynamic: invalid FrameTimeVector increment %v at frame %d", increment, index)
			}
		}
		if vector[0] != 0 {
			return Timeline{}, fmt.Errorf("dicom/dynamic: first FrameTimeVector increment is %v, want 0", vector[0])
		}
	}
	frameTime, hasFrameTime := directMilliseconds(obj, tagFrameTime)
	legacyPosition, hasLegacyPosition := directInt(obj, tagTemporalPositionIdentifier)
	numberOfPositions, hasNumberOfPositions := directInt(obj, tagNumberOfTemporalPositions)

	frames := make([]FrameMetadata, frameCount)
	var vectorOffset time.Duration
	for index := 0; index < frameCount; index++ {
		containers := []*object.Object{nil, shared}
		if index < len(perFrame) {
			containers[0] = perFrame[index]
		}
		frame := FrameMetadata{
			FrameIndex:                index,
			NumberOfTemporalPositions: numberOfPositions, HasNumberOfTemporalPositions: hasNumberOfPositions,
		}
		frame.TemporalPosition, frame.HasTemporalPosition = firstFrameInt(containers, obj, tagTemporalPositionIndex)
		frame.StackID, _ = firstFrameString(containers, obj, tagStackID)
		frame.InStackPosition, frame.HasInStackPosition = firstFrameInt(containers, obj, tagInStackPositionNumber)

		if content := firstRecursiveSequenceItem(containers, tagFrameContentSequence); content != nil {
			applyDimensionValues(&frame, pointers, derivedio.Ints(content, tagDimensionIndexValues))
		}
		if !frame.HasTemporalPosition && hasLegacyPosition {
			frame.TemporalPosition, frame.HasTemporalPosition = legacyPosition, true
		}

		if milliseconds, ok := firstFrameFloat(containers, obj, tagFrameReferenceTime); ok {
			frame.Offset, frame.HasOffset = millisecondsDuration(milliseconds)
		}
		if milliseconds, ok := firstFrameFloat(containers, obj, tagTriggerTime); ok {
			frame.Trigger, frame.HasTrigger = millisecondsDuration(milliseconds)
		} else if milliseconds, ok := firstFrameFloat(containers, obj, tagNominalCardiacTriggerDelayTime); ok {
			frame.Trigger, frame.HasTrigger = millisecondsDuration(milliseconds)
		}
		if percent, ok := firstFrameFloat(containers, obj, tagNominalCardiacPhasePercent); ok && finite(percent) {
			frame.Phase, frame.HasPhase = percent, true
		}
		if milliseconds, ok := firstFrameFloat(containers, obj, tagActualFrameDuration); ok {
			frame.Duration, frame.HasDuration = millisecondsDuration(milliseconds)
		} else if len(vector) > 0 {
			durationIndex := index + 1
			if durationIndex >= len(vector) {
				durationIndex = index
			}
			if durationIndex < len(vector) {
				frame.Duration, frame.HasDuration = millisecondsDuration(vector[durationIndex])
			}
		} else if hasFrameTime {
			frame.Duration, frame.HasDuration = millisecondsDuration(frameTime)
		}
		if len(vector) > 0 {
			if index > 0 && index < len(vector) {
				if increment, ok := millisecondsDuration(vector[index]); ok {
					var addOK bool
					vectorOffset, addOK = addDuration(vectorOffset, increment)
					if !addOK {
						return Timeline{}, fmt.Errorf("dicom/dynamic: FrameTimeVector overflows at frame %d", index)
					}
				}
			}
			if !frame.HasOffset {
				frame.Offset, frame.HasOffset = vectorOffset, true
			}
		} else if hasFrameTime && !frame.HasOffset {
			step, _ := millisecondsDuration(frameTime)
			offset, ok := multiplyDuration(step, index)
			if !ok {
				return Timeline{}, fmt.Errorf("dicom/dynamic: FrameTime overflows at frame %d", index)
			}
			frame.Offset, frame.HasOffset = offset, true
		}

		if value, ok := firstFrameString(containers, obj, tagFrameAcquisitionDateTime); ok {
			frame.AcquisitionTime, frame.HasAcquisitionTime = parseDateTime(value)
		} else if value, ok := firstFrameString(containers, obj, tagAcquisitionDateTime); ok {
			frame.AcquisitionTime, frame.HasAcquisitionTime = parseDateTime(value)
		}
		geometry := file.FrameGeometryAt(index)
		if len(geometry.ImagePositionPatient) == 3 && len(geometry.ImageOrientationPatient) == 6 {
			row := vector3(geometry.ImageOrientationPatient[:3])
			column := vector3(geometry.ImageOrientationPatient[3:6])
			normal := row.cross(column)
			origin := vector3(geometry.ImagePositionPatient)
			if normal.length() > 0 && origin.finite() {
				frame.SpatialPosition = origin.dot(normal.normalize())
				frame.HasSpatialPosition = finite(frame.SpatialPosition)
			}
		}
		frames[index] = frame
	}
	timeline := Build(frames)
	if validateDeclaredPositions && hasNumberOfPositions && numberOfPositions > 1 && len(timeline.Points) <= 1 {
		return Timeline{}, fmt.Errorf("dicom/dynamic: NumberOfTemporalPositions=%d but temporal frames cannot be separated", numberOfPositions)
	}
	return timeline, nil
}

// Build groups frames into time points, then independently groups and sorts
// spatial stacks within each point.
func Build(input []FrameMetadata) Timeline {
	frames := append([]FrameMetadata(nil), input...)
	timeline := Timeline{Frames: frames}
	if len(frames) == 0 {
		return timeline
	}
	duplicateSpatial := hasDuplicateSpatialPosition(frames)
	hasExplicitPosition := false
	hasAcquisitionTime := false
	var earliestAcquisition time.Time
	for _, frame := range frames {
		hasExplicitPosition = hasExplicitPosition || frame.HasTemporalPosition
		if frame.HasAcquisitionTime {
			hasAcquisitionTime = true
			if earliestAcquisition.IsZero() || frame.AcquisitionTime.Before(earliestAcquisition) {
				earliestAcquisition = frame.AcquisitionTime
			}
		}
		timeline.Gated = timeline.Gated || frame.HasTrigger || frame.HasPhase
	}
	explicitOffsets := map[int]time.Duration{}
	for _, frame := range frames {
		if frame.HasTemporalPosition && frame.HasOffset {
			explicitOffsets[frame.TemporalPosition] = frame.Offset
		}
	}
	occurrence := map[string]int{}
	type pointBuilder struct {
		key    string
		point  TimePoint
		frames []FrameMetadata
	}
	builders := map[string]*pointBuilder{}
	order := make([]string, 0)
	for _, frame := range frames {
		spatialKey := frameSpatialKey(frame)
		ordinal := occurrence[spatialKey]
		occurrence[spatialKey] = ordinal + 1
		key, position, hasPosition, usedOccurrenceFallback := temporalKeyWithProvenance(frame, hasExplicitPosition, explicitOffsets, duplicateSpatial && hasAcquisitionTime, ordinal)
		builder := builders[key]
		if builder == nil {
			builder = &pointBuilder{key: key, point: TimePoint{
				TemporalPosition: position, HasPosition: hasPosition,
				UsedOccurrenceFallback: usedOccurrenceFallback,
			}}
			builders[key] = builder
			order = append(order, key)
		} else if usedOccurrenceFallback {
			builder.point.UsedOccurrenceFallback = true
		}
		builder.frames = append(builder.frames, frame)
		mergeTiming(&builder.point, frame, earliestAcquisition, hasAcquisitionTime)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return lessTimePoint(builders[order[i]].point, builders[order[j]].point, order[i], order[j])
	})
	for ordinal, key := range order {
		builder := builders[key]
		builder.point.Ordinal = ordinal
		builder.point.Stacks = buildStacks(builder.frames)
		timeline.Points = append(timeline.Points, builder.point)
		timeline.UsedOccurrenceFallback = timeline.UsedOccurrenceFallback || builder.point.UsedOccurrenceFallback
		if len(builder.point.Stacks) > 1 {
			timeline.MultipleStacks = true
		}
	}
	timeline.Dynamic = len(timeline.Points) > 1
	timeline.Irregular = irregularTiming(timeline.Points)
	return timeline
}

func temporalKey(frame FrameMetadata, hasExplicit bool, explicitOffsets map[int]time.Duration, duplicateSpatial bool, occurrence int) (string, int, bool) {
	key, position, hasPosition, _ := temporalKeyWithProvenance(frame, hasExplicit, explicitOffsets, duplicateSpatial, occurrence)
	return key, position, hasPosition
}

func temporalKeyWithProvenance(frame FrameMetadata, hasExplicit bool, explicitOffsets map[int]time.Duration, duplicateSpatial bool, occurrence int) (string, int, bool, bool) {
	if frame.HasTemporalPosition {
		return "position:" + strconv.Itoa(frame.TemporalPosition), frame.TemporalPosition, true, false
	}
	if hasExplicit {
		if frame.HasOffset {
			bestPosition, bestDelta, found := 0, time.Duration(0), false
			for position, offset := range explicitOffsets {
				delta := absDuration(frame.Offset - offset)
				if delta > time.Microsecond {
					continue
				}
				if !found || delta < bestDelta || (delta == bestDelta && position < bestPosition) {
					bestPosition, bestDelta, found = position, delta, true
				}
			}
			if found {
				return "position:" + strconv.Itoa(bestPosition), bestPosition, true, false
			}
		}
		position := occurrence + 1
		return "position:" + strconv.Itoa(position), position, true, true
	}
	if frame.HasOffset {
		return "offset:" + strconv.FormatInt(frame.Offset.Nanoseconds(), 10), 0, false, false
	}
	if frame.HasTrigger {
		return "trigger:" + strconv.FormatInt(frame.Trigger.Nanoseconds(), 10), 0, false, false
	}
	if frame.HasPhase {
		return "phase:" + strconv.FormatFloat(frame.Phase, 'g', -1, 64), 0, false, false
	}
	if duplicateSpatial {
		return fmt.Sprintf("occurrence:%09d", occurrence), 0, false, true
	}
	return "static", 0, false, false
}

func buildStacks(frames []FrameMetadata) []Stack {
	byID := map[string][]FrameMetadata{}
	order := make([]string, 0)
	for _, frame := range frames {
		id := strings.TrimSpace(frame.StackID)
		if id == "" {
			id = "default"
		}
		if _, found := byID[id]; !found {
			order = append(order, id)
		}
		byID[id] = append(byID[id], frame)
	}
	sort.Strings(order)
	out := make([]Stack, 0, len(order))
	for _, id := range order {
		items := byID[id]
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].HasInStackPosition && items[j].HasInStackPosition && items[i].InStackPosition != items[j].InStackPosition {
				return items[i].InStackPosition < items[j].InStackPosition
			}
			if items[i].HasSpatialPosition && items[j].HasSpatialPosition && items[i].SpatialPosition != items[j].SpatialPosition {
				return items[i].SpatialPosition < items[j].SpatialPosition
			}
			return items[i].FrameIndex < items[j].FrameIndex
		})
		stack := Stack{ID: id, FrameIndices: make([]int, len(items))}
		for index := range items {
			stack.FrameIndices[index] = items[index].FrameIndex
		}
		out = append(out, stack)
	}
	return out
}

func mergeTiming(point *TimePoint, frame FrameMetadata, earliestAcquisition time.Time, hasAcquisitionTime bool) {
	if point == nil {
		return
	}
	if frame.HasOffset && (!point.HasOffset || frame.Offset < point.Offset) {
		point.Offset, point.HasOffset = frame.Offset, true
	} else if !frame.HasOffset && hasAcquisitionTime && frame.HasAcquisitionTime {
		offset := frame.AcquisitionTime.Sub(earliestAcquisition)
		if offset >= 0 && (!point.HasOffset || offset < point.Offset) {
			point.Offset, point.HasOffset = offset, true
		}
	}
	if frame.HasDuration && (!point.HasDuration || frame.Duration > point.Duration) {
		point.Duration, point.HasDuration = frame.Duration, true
	}
	if frame.HasTrigger && !point.HasTrigger {
		point.Trigger, point.HasTrigger = frame.Trigger, true
	}
	if frame.HasPhase && !point.HasPhase {
		point.Phase, point.HasPhase = frame.Phase, true
	}
}

func lessTimePoint(a, b TimePoint, aKey, bKey string) bool {
	if a.HasPosition && b.HasPosition && a.TemporalPosition != b.TemporalPosition {
		return a.TemporalPosition < b.TemporalPosition
	}
	if a.HasPosition != b.HasPosition {
		return a.HasPosition
	}
	if a.HasOffset && b.HasOffset && a.Offset != b.Offset {
		return a.Offset < b.Offset
	}
	if a.HasOffset != b.HasOffset {
		return a.HasOffset
	}
	if a.HasTrigger && b.HasTrigger && a.Trigger != b.Trigger {
		return a.Trigger < b.Trigger
	}
	if a.HasPhase && b.HasPhase && a.Phase != b.Phase {
		return a.Phase < b.Phase
	}
	return aKey < bKey
}

func irregularTiming(points []TimePoint) bool {
	if len(points) < 3 {
		return false
	}
	var prior time.Duration
	havePrior := false
	for index := 1; index < len(points); index++ {
		if !points[index-1].HasOffset || !points[index].HasOffset {
			continue
		}
		delta := points[index].Offset - points[index-1].Offset
		if delta <= 0 {
			return true
		}
		if havePrior && absDuration(delta-prior) > time.Millisecond {
			return true
		}
		prior, havePrior = delta, true
	}
	return false
}

func hasDuplicateSpatialPosition(frames []FrameMetadata) bool {
	counts := map[string]int{}
	for _, frame := range frames {
		if !frame.HasSpatialPosition {
			continue
		}
		key := strings.TrimSpace(frame.StackID) + ":" + strconv.FormatInt(int64(math.Round(frame.SpatialPosition*1000)), 10)
		counts[key]++
		if counts[key] > 1 {
			return true
		}
	}
	return false
}

func frameSpatialKey(frame FrameMetadata) string {
	if frame.HasSpatialPosition {
		return strings.TrimSpace(frame.StackID) + ":z:" + strconv.FormatInt(int64(math.Round(frame.SpatialPosition*1000)), 10)
	}
	if frame.HasInStackPosition {
		return strings.TrimSpace(frame.StackID) + ":i:" + strconv.Itoa(frame.InStackPosition)
	}
	return strings.TrimSpace(frame.StackID) + ":frame:" + strconv.Itoa(frame.FrameIndex)
}

func dimensionPointers(obj *object.Object) []core.Tag {
	items := derivedio.Sequence(obj, tagDimensionIndexSequence)
	out := make([]core.Tag, 0, len(items))
	for _, item := range items {
		element, ok := item.Get(tagDimensionIndexPointer)
		if !ok {
			out = append(out, core.Tag{})
			continue
		}
		raw, ok := element.RawBytes()
		if !ok || len(raw) < 4 {
			out = append(out, core.Tag{})
			continue
		}
		order := item.ValueByteOrder()
		if order == nil {
			order = binary.LittleEndian
		}
		out = append(out, core.NewTag(order.Uint16(raw), order.Uint16(raw[2:])))
	}
	return out
}

func applyDimensionValues(frame *FrameMetadata, pointers []core.Tag, values []int64) {
	if frame == nil || len(pointers) != len(values) {
		return
	}
	for index, pointer := range pointers {
		value := int(values[index])
		switch pointer {
		case tagTemporalPositionIndex:
			if !frame.HasTemporalPosition {
				frame.TemporalPosition, frame.HasTemporalPosition = value, true
			}
		case tagInStackPositionNumber:
			if !frame.HasInStackPosition {
				frame.InStackPosition, frame.HasInStackPosition = value, true
			}
		}
	}
}

func firstRecursiveSequenceItem(containers []*object.Object, tag core.Tag) *object.Object {
	for _, container := range containers {
		if item := recursiveSequenceItem(container, tag, 0); item != nil {
			return item
		}
	}
	return nil
}

func recursiveSequenceItem(obj *object.Object, tag core.Tag, depth int) *object.Object {
	if obj == nil || depth > 4 {
		return nil
	}
	if items := derivedio.Sequence(obj, tag); len(items) > 0 {
		return items[0]
	}
	for _, element := range obj.Elements() {
		if element.VR() != core.VRSQ {
			continue
		}
		items, ok := obj.GetSequence(element.Tag())
		if !ok {
			continue
		}
		for _, item := range items {
			if found := recursiveSequenceItem(item, tag, depth+1); found != nil {
				return found
			}
		}
	}
	return nil
}

func firstSequenceItem(obj *object.Object, tag core.Tag) *object.Object {
	items := derivedio.Sequence(obj, tag)
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func firstRecursiveInt(containers []*object.Object, tag core.Tag) (int, bool) {
	for _, container := range containers {
		if value, ok := recursiveInt(container, tag); ok {
			return value, true
		}
	}
	return 0, false
}

func firstFrameInt(containers []*object.Object, root *object.Object, tag core.Tag) (int, bool) {
	if value, ok := firstRecursiveInt(containers, tag); ok {
		return value, true
	}
	return directInt(root, tag)
}

func recursiveInt(obj *object.Object, tag core.Tag) (int, bool) {
	if target := recursiveObjectWithTag(obj, tag, 0); target != nil {
		values := derivedio.Ints(target, tag)
		if len(values) > 0 {
			return int(values[0]), true
		}
	}
	return 0, false
}

func directInt(obj *object.Object, tag core.Tag) (int, bool) {
	if obj == nil {
		return 0, false
	}
	values := derivedio.Ints(obj, tag)
	if len(values) == 0 {
		return 0, false
	}
	return int(values[0]), true
}

func firstRecursiveFloat(containers []*object.Object, tag core.Tag) (float64, bool) {
	for _, container := range containers {
		if target := recursiveObjectWithTag(container, tag, 0); target != nil {
			values := numericValues(target, tag)
			if len(values) > 0 && finite(values[0]) {
				return values[0], true
			}
		}
	}
	return 0, false
}

func firstFrameFloat(containers []*object.Object, root *object.Object, tag core.Tag) (float64, bool) {
	if value, ok := firstRecursiveFloat(containers, tag); ok {
		return value, true
	}
	values := numericValues(root, tag)
	if len(values) > 0 && finite(values[0]) {
		return values[0], true
	}
	return 0, false
}

func directFloats(obj *object.Object, tag core.Tag) []float64 {
	return numericValues(obj, tag)
}

func directMilliseconds(obj *object.Object, tag core.Tag) (float64, bool) {
	values := numericValues(obj, tag)
	if len(values) > 0 && finite(values[0]) && values[0] >= 0 {
		return values[0], true
	}
	return 0, false
}

func firstRecursiveString(containers []*object.Object, tag core.Tag) (string, bool) {
	for _, container := range containers {
		if target := recursiveObjectWithTag(container, tag, 0); target != nil {
			value := strings.TrimSpace(derivedio.CleanString(target, tag))
			if value != "" {
				return value, true
			}
		}
	}
	return "", false
}

func firstFrameString(containers []*object.Object, root *object.Object, tag core.Tag) (string, bool) {
	if value, ok := firstRecursiveString(containers, tag); ok {
		return value, true
	}
	if root == nil {
		return "", false
	}
	value := strings.TrimSpace(derivedio.CleanString(root, tag))
	return value, value != ""
}

func recursiveObjectWithTag(obj *object.Object, tag core.Tag, depth int) *object.Object {
	if obj == nil || depth > 4 {
		return nil
	}
	if _, ok := obj.Get(tag); ok {
		return obj
	}
	for _, element := range obj.Elements() {
		if element.VR() != core.VRSQ {
			continue
		}
		items, ok := obj.GetSequence(element.Tag())
		if !ok {
			continue
		}
		for _, item := range items {
			if found := recursiveObjectWithTag(item, tag, depth+1); found != nil {
				return found
			}
		}
	}
	return nil
}

func numericValues(obj *object.Object, tag core.Tag) []float64 {
	if values := derivedio.Floats(obj, tag); len(values) > 0 {
		return values
	}
	ints := derivedio.Ints(obj, tag)
	out := make([]float64, len(ints))
	for index, value := range ints {
		out[index] = float64(value)
	}
	return out
}

func parseDateTime(value string) (time.Time, bool) {
	parsed, err := dcmtime.ParseDatetime(strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	return parsed.Time, true
}

func millisecondsDuration(value float64) (time.Duration, bool) {
	if !finite(value) || value < 0 || value > float64(math.MaxInt64)/float64(time.Millisecond) {
		return 0, false
	}
	return time.Duration(math.Round(value * float64(time.Millisecond))), true
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func addDuration(a, b time.Duration) (time.Duration, bool) {
	if b > 0 && a > time.Duration(math.MaxInt64)-b {
		return 0, false
	}
	return a + b, true
}

func multiplyDuration(value time.Duration, factor int) (time.Duration, bool) {
	if value < 0 || factor < 0 {
		return 0, false
	}
	if value == 0 || factor == 0 {
		return 0, true
	}
	if int64(value) > math.MaxInt64/int64(factor) {
		return 0, false
	}
	return value * time.Duration(factor), true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

type vec3 struct{ x, y, z float64 }

func vector3(values []float64) vec3 {
	if len(values) < 3 {
		return vec3{}
	}
	return vec3{x: values[0], y: values[1], z: values[2]}
}
func (v vec3) dot(other vec3) float64 { return v.x*other.x + v.y*other.y + v.z*other.z }
func (v vec3) cross(other vec3) vec3 {
	return vec3{x: v.y*other.z - v.z*other.y, y: v.z*other.x - v.x*other.z, z: v.x*other.y - v.y*other.x}
}
func (v vec3) length() float64 { return math.Sqrt(v.dot(v)) }
func (v vec3) normalize() vec3 {
	length := v.length()
	if length == 0 || !finite(length) {
		return vec3{}
	}
	return vec3{x: v.x / length, y: v.y / length, z: v.z / length}
}
func (v vec3) finite() bool { return finite(v.x) && finite(v.y) && finite(v.z) }
