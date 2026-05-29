package dynamic

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestBuildSeparatesIrregularDynamicPETFromSpatialOrder(t *testing.T) {
	var frames []FrameMetadata
	index := 0
	for temporal, offset := range []time.Duration{0, time.Second, 2500 * time.Millisecond} {
		for spatial := 0; spatial < 2; spatial++ {
			frames = append(frames, FrameMetadata{
				FrameIndex: index, TemporalPosition: temporal + 1, HasTemporalPosition: true,
				Offset: offset, HasOffset: true, Duration: time.Second, HasDuration: true,
				SpatialPosition: float64(spatial * 5), HasSpatialPosition: true,
				InStackPosition: spatial + 1, HasInStackPosition: true,
			})
			index++
		}
	}
	timeline := Build(frames)
	if !timeline.Dynamic || !timeline.Irregular || len(timeline.Points) != 3 {
		t.Fatalf("timeline = %+v, want three irregular time points", timeline)
	}
	for pointIndex, point := range timeline.Points {
		if len(point.Stacks) != 1 || len(point.Stacks[0].FrameIndices) != 2 {
			t.Fatalf("point %d stacks = %+v", pointIndex, point.Stacks)
		}
		want := []int{pointIndex * 2, pointIndex*2 + 1}
		if got := point.Stacks[0].FrameIndices; got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("point %d spatial frame order = %v, want %v", pointIndex, got, want)
		}
	}
}

func TestBuildInfersMissingTemporalIndicesBySpatialOccurrence(t *testing.T) {
	frames := []FrameMetadata{
		{FrameIndex: 0, TemporalPosition: 1, HasTemporalPosition: true, SpatialPosition: 0, HasSpatialPosition: true},
		{FrameIndex: 1, SpatialPosition: 5, HasSpatialPosition: true},
		{FrameIndex: 2, TemporalPosition: 2, HasTemporalPosition: true, SpatialPosition: 0, HasSpatialPosition: true},
		{FrameIndex: 3, SpatialPosition: 5, HasSpatialPosition: true},
	}
	timeline := Build(frames)
	if !timeline.Dynamic || len(timeline.Points) != 2 {
		t.Fatalf("missing-index timeline = %+v", timeline)
	}
	for index, point := range timeline.Points {
		if !point.HasPosition || point.TemporalPosition != index+1 ||
			len(point.Stacks) != 1 || len(point.Stacks[0].FrameIndices) != 2 {
			t.Fatalf("inferred point %d = %+v", index, point)
		}
	}
}

func TestBuildReportsOccurrenceFallbackForMixedExplicitTemporalPositions(t *testing.T) {
	frames := []FrameMetadata{
		{FrameIndex: 0, TemporalPosition: 1, HasTemporalPosition: true, SpatialPosition: 0, HasSpatialPosition: true},
		{FrameIndex: 1, SpatialPosition: 5, HasSpatialPosition: true},
		{FrameIndex: 2, TemporalPosition: 2, HasTemporalPosition: true, SpatialPosition: 0, HasSpatialPosition: true},
		{FrameIndex: 3, SpatialPosition: 5, HasSpatialPosition: true},
	}

	timeline := Build(frames)
	if !timeline.UsedOccurrenceFallback {
		t.Fatal("Timeline.UsedOccurrenceFallback = false, want true")
	}
	if len(timeline.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(timeline.Points))
	}
	for index, point := range timeline.Points {
		if !point.UsedOccurrenceFallback {
			t.Fatalf("point %d UsedOccurrenceFallback = false, want true", index)
		}
	}
}

func TestTemporalKeySelectsNearestExplicitOffsetDeterministically(t *testing.T) {
	frame := FrameMetadata{Offset: time.Microsecond, HasOffset: true}
	offsets := map[int]time.Duration{
		3: 1500 * time.Nanosecond,
		2: 500 * time.Nanosecond,
		1: 0,
	}
	key, position, explicit := temporalKey(frame, true, offsets, false, 0)
	if key != "position:2" || position != 2 || !explicit {
		t.Fatalf("nearest temporal key = %q, %d, %v; want position:2, 2, true", key, position, explicit)
	}

	offsets[1] = 500 * time.Nanosecond
	key, position, explicit = temporalKey(frame, true, offsets, false, 0)
	if key != "position:1" || position != 1 || !explicit {
		t.Fatalf("tied temporal key = %q, %d, %v; want position:1, 1, true", key, position, explicit)
	}
}

func TestBuildPreservesGatedPhasesAndMultipleStacks(t *testing.T) {
	var frames []FrameMetadata
	index := 0
	for phase, trigger := range []time.Duration{0, 500 * time.Millisecond} {
		for _, stack := range []string{"A", "B"} {
			frames = append(frames, FrameMetadata{
				FrameIndex: index, StackID: stack,
				Trigger: trigger, HasTrigger: true, Phase: float64(phase * 50), HasPhase: true,
				SpatialPosition: 0, HasSpatialPosition: true,
			})
			index++
		}
	}
	timeline := Build(frames)
	if !timeline.Dynamic || !timeline.Gated || !timeline.MultipleStacks || len(timeline.Points) != 2 {
		t.Fatalf("gated multi-stack timeline = %+v", timeline)
	}
	for _, point := range timeline.Points {
		if len(point.Stacks) != 2 || point.Stacks[0].ID != "A" || point.Stacks[1].ID != "B" {
			t.Fatalf("stack separation = %+v", point.Stacks)
		}
	}
}

func TestBuildUsesAcquisitionTimeOnlyForRepeatedSpatialLocations(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	static := Build([]FrameMetadata{
		{FrameIndex: 0, SpatialPosition: 0, HasSpatialPosition: true, AcquisitionTime: base, HasAcquisitionTime: true},
		{FrameIndex: 1, SpatialPosition: 5, HasSpatialPosition: true, AcquisitionTime: base.Add(time.Second), HasAcquisitionTime: true},
	})
	if static.Dynamic || len(static.Points) != 1 {
		t.Fatalf("ordinary spatial CT was treated as dynamic: %+v", static)
	}
	dynamic := Build([]FrameMetadata{
		{FrameIndex: 0, SpatialPosition: 0, HasSpatialPosition: true, AcquisitionTime: base, HasAcquisitionTime: true},
		{FrameIndex: 1, SpatialPosition: 0, HasSpatialPosition: true, AcquisitionTime: base.Add(time.Second), HasAcquisitionTime: true},
	})
	if !dynamic.Dynamic || len(dynamic.Points) != 2 {
		t.Fatalf("repeated-location acquisition was not dynamic: %+v", dynamic)
	}
}

func TestBuildUsesSpatialOccurrenceWhenAcquisitionTimesDifferWithinVolume(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	frames := []FrameMetadata{
		{FrameIndex: 0, SpatialPosition: 0, HasSpatialPosition: true, AcquisitionTime: base, HasAcquisitionTime: true},
		{FrameIndex: 1, SpatialPosition: 5, HasSpatialPosition: true, AcquisitionTime: base.Add(100 * time.Millisecond), HasAcquisitionTime: true},
		{FrameIndex: 2, SpatialPosition: 0, HasSpatialPosition: true, AcquisitionTime: base.Add(time.Second), HasAcquisitionTime: true},
		{FrameIndex: 3, SpatialPosition: 5, HasSpatialPosition: true, AcquisitionTime: base.Add(1100 * time.Millisecond), HasAcquisitionTime: true},
	}
	timeline := Build(frames)
	if !timeline.Dynamic || len(timeline.Points) != 2 {
		t.Fatalf("occurrence timeline = %+v", timeline)
	}
	for index, point := range timeline.Points {
		if len(point.Stacks) != 1 || len(point.Stacks[0].FrameIndices) != 2 {
			t.Fatalf("point %d frames = %+v", index, point.Stacks)
		}
	}
	if timeline.Points[0].Offset != 0 || timeline.Points[1].Offset != time.Second {
		t.Fatalf("acquisition-derived offsets = %v/%v", timeline.Points[0].Offset, timeline.Points[1].Offset)
	}
}

func TestReadEnhancedFunctionalGroupsAndDimensionIndices(t *testing.T) {
	file := enhancedDynamicTestFile(t)
	timeline, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if !timeline.Dynamic || len(timeline.Points) != 2 || !timeline.MultipleStacks {
		t.Fatalf("parsed timeline = %+v", timeline)
	}
	if got := timeline.Points[1].Offset; got != 1500*time.Millisecond {
		t.Fatalf("second offset = %v, want 1.5s", got)
	}
	if got := timeline.Points[1].Duration; got != 750*time.Millisecond {
		t.Fatalf("second duration = %v, want 750ms", got)
	}
	if timeline.Frames[2].TemporalPosition != 2 || timeline.Frames[2].InStackPosition != 1 {
		t.Fatalf("dimension indices = %+v", timeline.Frames[2])
	}
}

func TestReadMissingPerFrameIndexDoesNotReuseAnotherFramesMetadata(t *testing.T) {
	file := missingIndexDynamicTestFile()
	timeline, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if !timeline.Dynamic || len(timeline.Points) != 2 {
		t.Fatalf("missing per-frame index timeline = %+v", timeline)
	}
	if !timeline.Frames[0].HasTemporalPosition || timeline.Frames[1].HasTemporalPosition {
		t.Fatalf("per-frame temporal metadata leaked between frames: %+v", timeline.Frames)
	}
}

func TestReadFrameTimeVectorUsesCumulativeIncrements(t *testing.T) {
	file := &object.File{
		Dataset: object.FromElements([]core.Element{
			dynamicStrings(tagNumberOfFrames, core.VRIS, "3"),
			dynamicStrings(tagFrameTimeVector, core.VRDS, "0", "1000", "1500"),
		}, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}
	timeline, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	wantOffsets := []time.Duration{0, time.Second, 2500 * time.Millisecond}
	wantDurations := []time.Duration{time.Second, 1500 * time.Millisecond, 1500 * time.Millisecond}
	for index := range timeline.Frames {
		frame := timeline.Frames[index]
		if !frame.HasOffset || frame.Offset != wantOffsets[index] ||
			!frame.HasDuration || frame.Duration != wantDurations[index] {
			t.Fatalf("frame %d timing = offset %v duration %v", index, frame.Offset, frame.Duration)
		}
	}
	if !timeline.Dynamic || !timeline.Irregular {
		t.Fatalf("frame-time-vector timeline = %+v", timeline)
	}
}

func TestReadRejectsInvalidFrameTimeVector(t *testing.T) {
	for name, values := range map[string][]string{
		"wrong length":  {"0", "10"},
		"nonzero first": {"10", "10", "10"},
		"negative":      {"0", "-10", "10"},
	} {
		t.Run(name, func(t *testing.T) {
			file := &object.File{
				Dataset: object.FromElements([]core.Element{
					dynamicStrings(tagNumberOfFrames, core.VRIS, "3"),
					dynamicStrings(tagFrameTimeVector, core.VRDS, values...),
				}, std.Dictionary),
				TransferSyntax: transfer.ImplicitVRLittleEndian,
			}
			if _, err := Read(file); err == nil {
				t.Fatal("invalid FrameTimeVector was accepted")
			}
		})
	}
}

func TestReadFrameMetadataAllowsPartialMultiInstanceAcquisition(t *testing.T) {
	file := &object.File{
		Dataset: object.FromElements([]core.Element{
			dynamicStrings(tagNumberOfTemporalPositions, core.VRIS, "2"),
			dynamicStrings(tagTemporalPositionIdentifier, core.VRIS, "1"),
		}, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}
	if _, err := Read(file); err == nil {
		t.Fatal("Read accepted one instance as a complete two-position acquisition")
	}
	frames, err := ReadFrameMetadata(file)
	if err != nil {
		t.Fatalf("ReadFrameMetadata() error = %v", err)
	}
	if len(frames) != 1 || !frames[0].HasTemporalPosition || frames[0].TemporalPosition != 1 {
		t.Fatalf("ReadFrameMetadata() = %+v, want partial position 1", frames)
	}
}

func TestReadFrameMetadataPreservesDeclaredNumberOfTemporalPositions(t *testing.T) {
	for _, test := range []struct {
		name     string
		elements []core.Element
		want     int
		wantHas  bool
	}{
		{
			name: "declared",
			elements: []core.Element{
				dynamicStrings(tagNumberOfTemporalPositions, core.VRIS, "3"),
				dynamicStrings(tagTemporalPositionIdentifier, core.VRIS, "2"),
			},
			want: 3, wantHas: true,
		},
		{
			name: "absent",
			elements: []core.Element{
				dynamicStrings(tagTemporalPositionIdentifier, core.VRIS, "1"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := &object.File{
				Dataset:        object.FromElements(test.elements, std.Dictionary),
				TransferSyntax: transfer.ImplicitVRLittleEndian,
			}
			frames, err := ReadFrameMetadata(file)
			if err != nil {
				t.Fatalf("ReadFrameMetadata() error = %v", err)
			}
			if len(frames) != 1 {
				t.Fatalf("frame count = %d, want 1", len(frames))
			}
			if frames[0].NumberOfTemporalPositions != test.want || frames[0].HasNumberOfTemporalPositions != test.wantHas {
				t.Fatalf("declared temporal positions = %d/%v, want %d/%v", frames[0].NumberOfTemporalPositions, frames[0].HasNumberOfTemporalPositions, test.want, test.wantHas)
			}
		})
	}
}

func TestBuildMemoryRemainsLinearAndBounded(t *testing.T) {
	frames := make([]FrameMetadata, 400)
	for index := range frames {
		frames[index] = FrameMetadata{
			FrameIndex: index, TemporalPosition: index/20 + 1, HasTemporalPosition: true,
			SpatialPosition: float64(index % 20), HasSpatialPosition: true,
		}
	}
	allocs := testing.AllocsPerRun(20, func() {
		timeline := Build(frames)
		if len(timeline.Points) != 20 {
			panic("unexpected point count")
		}
	})
	if allocs > 2500 {
		t.Fatalf("Build allocations = %.0f, want bounded linear work", allocs)
	}
}

func BenchmarkBuildDynamicTimeline400Frames(b *testing.B) {
	frames := make([]FrameMetadata, 400)
	for index := range frames {
		frames[index] = FrameMetadata{
			FrameIndex: index, TemporalPosition: index/20 + 1, HasTemporalPosition: true,
			SpatialPosition: float64(index % 20), HasSpatialPosition: true,
		}
	}
	b.ReportAllocs()
	for range b.N {
		_ = Build(frames)
	}
}

func enhancedDynamicTestFile(t *testing.T) *object.File {
	t.Helper()
	pointers := dynamicSequence(tagDimensionIndexSequence,
		core.DataSet{Elements: []core.Element{dynamicAT(tagDimensionIndexPointer, tagTemporalPositionIndex)}},
		core.DataSet{Elements: []core.Element{dynamicAT(tagDimensionIndexPointer, tagInStackPositionNumber)}},
	)
	shared := dynamicSequence(tagSharedFunctionalGroups, core.DataSet{Elements: []core.Element{
		dynamicSequence(core.NewTag(0x0020, 0x9116), core.DataSet{Elements: []core.Element{
			dynamicStrings(core.NewTag(0x0020, 0x0037), core.VRDS, "1", "0", "0", "0", "1", "0"),
		}}),
		dynamicSequence(core.NewTag(0x0028, 0x9110), core.DataSet{Elements: []core.Element{
			dynamicStrings(core.NewTag(0x0028, 0x0030), core.VRDS, "1", "1"),
		}}),
	}})
	var perFrame []core.DataSet
	for index := 0; index < 4; index++ {
		temporal := index/2 + 1
		stack := "A"
		if index%2 == 1 {
			stack = "B"
		}
		offset := "0"
		if temporal == 2 {
			offset = "1500"
		}
		perFrame = append(perFrame, core.DataSet{Elements: []core.Element{
			dynamicSequence(tagFrameContentSequence, core.DataSet{Elements: []core.Element{
				dynamicUL(tagDimensionIndexValues, uint32(temporal), 1),
				dynamicStrings(tagStackID, core.VRSH, stack),
			}}),
			dynamicSequence(core.NewTag(0x0020, 0x9113), core.DataSet{Elements: []core.Element{
				dynamicStrings(core.NewTag(0x0020, 0x0032), core.VRDS, "0", "0", "0"),
			}}),
			dynamicStrings(tagFrameReferenceTime, core.VRDS, offset),
			dynamicStrings(tagActualFrameDuration, core.VRIS, "750"),
		}})
	}
	elements := []core.Element{
		dynamicStrings(core.NewTag(0x0008, 0x0016), core.VRUI, "1.2.840.10008.5.1.4.1.1.128"),
		dynamicStrings(core.NewTag(0x0008, 0x0018), core.VRUI, "1.2.dynamic"),
		dynamicStrings(tagNumberOfFrames, core.VRIS, "4"),
		pointers, shared, dynamicSequence(tagPerFrameFunctionalGroups, perFrame...),
	}
	return &object.File{
		Dataset:        object.FromElements(elements, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}
}

func missingIndexDynamicTestFile() *object.File {
	shared := dynamicSequence(tagSharedFunctionalGroups, core.DataSet{Elements: []core.Element{
		dynamicSequence(core.NewTag(0x0020, 0x9116), core.DataSet{Elements: []core.Element{
			dynamicStrings(core.NewTag(0x0020, 0x0037), core.VRDS, "1", "0", "0", "0", "1", "0"),
		}}),
	}})
	perFrame := dynamicSequence(tagPerFrameFunctionalGroups,
		core.DataSet{Elements: []core.Element{
			dynamicSequence(tagFrameContentSequence, core.DataSet{Elements: []core.Element{
				dynamicUL(tagTemporalPositionIndex, 1),
				dynamicUL(tagInStackPositionNumber, 1),
			}}),
			dynamicSequence(core.NewTag(0x0020, 0x9113), core.DataSet{Elements: []core.Element{
				dynamicStrings(core.NewTag(0x0020, 0x0032), core.VRDS, "0", "0", "0"),
			}}),
		}},
		core.DataSet{Elements: []core.Element{
			dynamicSequence(tagFrameContentSequence, core.DataSet{Elements: []core.Element{
				dynamicUL(tagInStackPositionNumber, 1),
			}}),
			dynamicSequence(core.NewTag(0x0020, 0x9113), core.DataSet{Elements: []core.Element{
				dynamicStrings(core.NewTag(0x0020, 0x0032), core.VRDS, "0", "0", "0"),
			}}),
		}},
	)
	return &object.File{
		Dataset: object.FromElements([]core.Element{
			dynamicStrings(tagNumberOfFrames, core.VRIS, "2"),
			dynamicStrings(tagNumberOfTemporalPositions, core.VRIS, "2"),
			shared, perFrame,
		}, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}
}

func dynamicSequence(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRSQ}, Value: core.SequenceValue{Items: items}}
}

func dynamicStrings(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(values)}
}

func dynamicAT(tag, value core.Tag) core.Element {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint16(raw, value.Group)
	binary.LittleEndian.PutUint16(raw[2:], value.Element)
	return core.NewRawElement(tag, core.VRAT, raw)
}

func dynamicUL(tag core.Tag, values ...uint32) core.Element {
	raw := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(raw[index*4:], value)
	}
	return core.NewRawElement(tag, core.VRUL, raw)
}

func ExampleBuild() {
	timeline := Build([]FrameMetadata{
		{FrameIndex: 0, TemporalPosition: 1, HasTemporalPosition: true},
		{FrameIndex: 1, TemporalPosition: 2, HasTemporalPosition: true},
	})
	fmt.Println(timeline.Dynamic, len(timeline.Points))
	// Output: true 2
}
