package sr

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestDocumentRoundTripsAllContentValueTypes(t *testing.T) {
	concept := func(valueType ValueType) CodedEntry {
		return CodedEntry{CodeValue: string(valueType), CodingSchemeDesignator: "99TEST", CodeMeaning: string(valueType) + " value"}
	}
	content := []ContentItem{
		{ValueType: ValueDateTime, RelationshipType: RelationshipContains, ConceptName: concept(ValueDateTime), DateTime: "20260709231530.25-0300"},
		{ValueType: ValueDate, RelationshipType: RelationshipContains, ConceptName: concept(ValueDate), Date: "20260709"},
		{ValueType: ValueTime, RelationshipType: RelationshipContains, ConceptName: concept(ValueTime), Time: "231530.25"},
		{ValueType: ValueUIDRef, RelationshipType: RelationshipContains, ConceptName: concept(ValueUIDRef), UID: "1.2.826.0.1.3680043.9.7433.324"},
		{ValueType: ValuePName, RelationshipType: RelationshipContains, ConceptName: concept(ValuePName), PersonName: "Garcia^Jose"},
		{ValueType: ValueComposite, RelationshipType: RelationshipContains, Composite: CompositeReference{SOPClassUID: "1.2.3", SOPInstanceUID: "1.2.3.4"}},
		{ValueType: ValueWaveform, RelationshipType: RelationshipContains, Waveform: WaveformReference{
			CompositeReference: CompositeReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.9.1.1", SOPInstanceUID: "1.2.3.5"},
			Channels:           []WaveformChannel{{MultiplexGroupNumber: 1, ChannelNumber: 0}, {MultiplexGroupNumber: 3, ChannelNumber: 2}},
		}},
		{ValueType: ValueSCoord, RelationshipType: RelationshipContains, Spatial: SpatialCoordinates{
			GraphicType: "POLYLINE", Coordinates: []Point2D{{X: 1.5, Y: 2.25}, {X: 8.5, Y: 9.75}},
			PixelOriginInterpretation: "FRAME", FiducialUID: "1.2.3.6",
		}, Children: []ContentItem{{
			ValueType: ValueImage, RelationshipType: RelationshipSelectedFrom,
			Image: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.7", Frames: []int{2}},
		}}},
		{ValueType: ValueSCoord3D, RelationshipType: RelationshipContains, Spatial3D: SpatialReference{
			FrameOfReferenceUID: "1.2.3.8", GraphicType: "MULTIPOINT",
			Coordinates: []Point3D{{X: -1.5, Y: 2.25, Z: 3.5}, {X: 4.75, Y: 5.5, Z: 6.25}}, FiducialUID: "1.2.3.9",
		}},
		{ValueType: ValueTCoord, RelationshipType: RelationshipContains, Temporal: TemporalCoordinates{RangeType: "SEGMENT", SamplePositions: []uint32{1, 42}}},
		{ValueType: ValueTCoord, RelationshipType: RelationshipContains, Temporal: TemporalCoordinates{RangeType: "MULTIPOINT", TimeOffsets: []float64{0.25, 1.5, 4.75}}},
		{ValueType: ValueTCoord, RelationshipType: RelationshipContains, Temporal: TemporalCoordinates{RangeType: "SEGMENT", DateTimes: []string{"20260709231530-0300", "20260709231600-0300"}}},
		{ValueType: ValueTable, RelationshipType: RelationshipContains, ValueElements: []core.Element{
			core.NewRawElement(core.NewTag(0x7777, 0x1001), core.VROB, []byte{1, 2, 3, 4}),
		}},
	}
	doc := &Document{
		SOPClassUID: BasicTextSRStorage, SOPInstanceUID: "1.2.3.324", Modality: "SR",
		Title:   CodedEntry{CodeValue: "REPORT", CodingSchemeDesignator: "99TEST", CodeMeaning: "Full value report"},
		Content: content,
	}

	first, err := ReadDocument(mustDocumentDataset(t, doc))
	if err != nil {
		t.Fatalf("ReadDocument(first) error = %v", err)
	}
	if !reflect.DeepEqual(first.Content, content) {
		t.Fatalf("first content mismatch\ngot  %#v\nwant %#v", first.Content, content)
	}
	second, err := ReadDocument(mustDocumentDataset(t, first))
	if err != nil {
		t.Fatalf("ReadDocument(second) error = %v", err)
	}
	if !reflect.DeepEqual(second.Content, content) {
		t.Fatalf("second content mismatch\ngot  %#v\nwant %#v", second.Content, content)
	}
	var encoded bytes.Buffer
	if err := object.WriteDataSet(&encoded, mustDocumentDataset(t, second), transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatalf("WriteDataSet() error = %v", err)
	}
	parsed, err := object.ReadDataSet(bytes.NewReader(encoded.Bytes()), transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReadDataSet() error = %v", err)
	}
	third, err := ReadDocument(parsed)
	if err != nil {
		t.Fatalf("ReadDocument(parsed) error = %v", err)
	}
	if !reflect.DeepEqual(third.Content, content) {
		t.Fatalf("binary round-trip content mismatch\ngot  %#v\nwant %#v", third.Content, content)
	}

	assertExtendedContentVRs(t, mustDocumentDataset(t, doc))
}

func TestReadWaveformReferenceRejectsInvalidChannelPairs(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values []int
	}{
		{name: "odd count", values: []int{1}},
		{name: "negative value", values: []int{-1, 1}},
		{name: "value above US", values: []int{1 << 16, 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := derivedio.Object(derivedio.Seq(tagRefSOPSequence, derivedio.DataSet(
				derivedio.IS(tagReferencedWaveformCh, tt.values...),
			)))
			dec := &decoder{}
			ref := readWaveformReference(dec, item)
			if dec.err == nil {
				t.Fatalf("readWaveformReference() = %#v without error", ref)
			}
			if len(ref.Channels) != 0 {
				t.Fatalf("Channels = %#v, want none after invalid input", ref.Channels)
			}
		})
	}
}

func assertExtendedContentVRs(t *testing.T, dataset *object.Object) {
	t.Helper()
	items, ok := dataset.GetSequence(tagContentSequence)
	if !ok {
		t.Fatal("Content Sequence missing")
	}
	for _, item := range items {
		valueType := ValueType(cleanString(item, tagValueType))
		switch valueType {
		case ValueDateTime:
			assertContentVR(t, item, tagDateTimeValue, core.VRDT)
		case ValueDate:
			assertContentVR(t, item, tagDateValue, core.VRDA)
		case ValueTime:
			assertContentVR(t, item, tagTimeValue, core.VRTM)
		case ValueUIDRef:
			assertContentVR(t, item, tagUID, core.VRUI)
		case ValuePName:
			assertContentVR(t, item, tagPersonNameValue, core.VRPN)
		case ValueWaveform:
			refs, _ := item.GetSequence(tagRefSOPSequence)
			assertContentVR(t, refs[0], tagReferencedWaveformCh, core.VRUS)
		case ValueSCoord, ValueSCoord3D:
			assertContentVR(t, item, tagGraphicData, core.VRFL)
		case ValueTCoord:
			for _, check := range []struct {
				tag core.Tag
				vr  core.VR
			}{{tagReferencedSamples, core.VRUL}, {tagReferencedTimeOffset, core.VRDS}, {tagReferencedDateTime, core.VRDT}} {
				if item.Has(check.tag) {
					assertContentVR(t, item, check.tag, check.vr)
				}
			}
		}
	}
}

func assertContentVR(t *testing.T, item *object.Object, tag core.Tag, want core.VR) {
	t.Helper()
	element, ok := item.Get(tag)
	if !ok {
		t.Fatalf("%s missing from content item", tag)
	}
	if element.VR() != want {
		t.Fatalf("%s VR = %s, want %s", tag, element.VR(), want)
	}
}

func TestGeneratedSRDSElementsRespect16ByteLimit(t *testing.T) {
	longValue := 1.0 / 300000.0
	measured := measuredValueSequence(Measurement{Value: longValue})
	sequence, ok := measured.Value.(core.SequenceValue)
	if !ok || len(sequence.Items) != 1 || len(sequence.Items[0].Elements) == 0 {
		t.Fatalf("measured value sequence = %#v", measured.Value)
	}

	assertDSLength := func(element core.Element) {
		t.Helper()
		for _, value := range element.StringValues() {
			if len(value) > 16 {
				t.Errorf("%s DS value %q is %d bytes, want at most 16", element.Tag(), value, len(value))
			}
		}
	}
	assertDSLength(sequence.Items[0].Elements[0])

	for _, element := range temporalCoordinateElements(TemporalCoordinates{TimeOffsets: []float64{longValue, -longValue}}) {
		if element.VR() == core.VRDS {
			assertDSLength(element)
		}
	}
}

func TestReadDocumentRejectsIncompleteSpatialCoordinateTuples(t *testing.T) {
	for _, test := range []struct {
		name      string
		valueType ValueType
		values    []float64
	}{
		{name: "SCOORD pair", valueType: ValueSCoord, values: []float64{1, 2, 3}},
		{name: "SCOORD3D triple", valueType: ValueSCoord3D, values: []float64{1, 2, 3, 4}},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := core.DataSet{Elements: []core.Element{
				strElem(tagValueType, core.VRCS, string(test.valueType)),
				float32Element(tagGraphicData, test.values...),
			}}
			obj := object.FromElements([]core.Element{seqElement(tagContentSequence, item)}, nil)

			_, err := ReadDocument(obj)
			if !errors.Is(err, ErrInvalidGraphicData) {
				t.Fatalf("ReadDocument() error = %v, want ErrInvalidGraphicData", err)
			}
		})
	}
}
