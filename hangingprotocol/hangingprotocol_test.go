package hangingprotocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestReadCurrentOnlyProtocol(t *testing.T) {
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "CT current"),
		derivedio.LO(tagHangingProtocolDescription, "Current chest CT"),
		derivedio.CS(tagHangingProtocolLevel, "USER_GROUP"),
		derivedio.LO(tagHangingProtocolCreator, "Twin test"),
		derivedio.US(tagNumberOfScreens, 1),
		derivedio.Seq(tagImageSetsSequence, derivedio.DataSet(
			derivedio.Seq(tagImageSetSelectorSequence,
				selectorDataSet(core.NewTag(0x0008, 0x0060), core.VRCS, "CT"),
				selectorDataSet(core.NewTag(0x0018, 0x0015), core.VRCS, "CHEST"),
			),
			derivedio.Seq(tagTimeBasedImageSetsSequence, derivedio.DataSet(
				derivedio.US(tagImageSetNumber, 1),
				derivedio.LO(tagImageSetLabel, "Current"),
				derivedio.US(tagRelativeTime, 0, 0),
			)),
		)),
		derivedio.Seq(tagNominalScreenDefinitionSequence, derivedio.DataSet(
			derivedio.US(tagNumberOfVerticalPixels, 1080),
			derivedio.US(tagNumberOfHorizontalPixels, 1920),
			fdElement(tagDisplayEnvironmentSpatialPosition, 0, 0, 1, 1),
		)),
		derivedio.Seq(tagDisplaySetsSequence, derivedio.DataSet(
			derivedio.US(tagDisplaySetNumber, 1),
			derivedio.LO(tagDisplaySetLabel, "Lung"),
			derivedio.US(tagDisplaySetPresentationGroup, 1),
			derivedio.US(tagImageSetNumber, 1),
			derivedio.CS(tagVOIType, "LUNG"),
			derivedio.Seq(tagImageBoxesSequence, derivedio.DataSet(
				derivedio.US(tagImageBoxNumber, 1),
				derivedio.CS(tagImageBoxLayoutType, "STACK"),
				fdElement(tagDisplayEnvironmentSpatialPosition, 0, 0, 1, 1),
			)),
		)),
	)

	got, err := Read(obj)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "CT current" || got.Description != "Current chest CT" || got.Level != "USER_GROUP" || got.Creator != "Twin test" {
		t.Fatalf("metadata = %+v", got)
	}
	if len(got.ImageSets) != 1 || got.ImageSets[0].IsPrior || len(got.ImageSets[0].Selectors) != 2 {
		t.Fatalf("image sets = %+v", got.ImageSets)
	}
	if got.ImageSets[0].Selectors[0].Attribute != core.NewTag(0x0008, 0x0060) ||
		got.ImageSets[0].Selectors[0].Values[0] != "CT" {
		t.Fatalf("modality selector = %+v", got.ImageSets[0].Selectors[0])
	}
	if len(got.Screens) != 1 || got.Screens[0].HorizontalPixels != 1920 || len(got.Screens[0].SpatialPosition) != 4 {
		t.Fatalf("screens = %+v", got.Screens)
	}
	if len(got.DisplaySets) != 1 || len(got.DisplaySets[0].ImageBoxes) != 1 ||
		got.DisplaySets[0].ImageBoxes[0].ImageSetNumber != 1 || got.DisplaySets[0].ImageBoxes[0].VOIType != "LUNG" {
		t.Fatalf("display sets = %+v", got.DisplaySets)
	}
	if len(got.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", got.Diagnostics)
	}
}

func TestReadSourcesSelectorCategoryFromTimeBasedImageSet(t *testing.T) {
	selector := selectorDataSet(core.NewTag(0x0008, 0x0060), core.VRCS, "CT")
	selector.Elements = append(selector.Elements, derivedio.CS(tagImageSetSelectorCategory, "STUDY"))
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "Category source"),
		derivedio.Seq(tagImageSetsSequence, derivedio.DataSet(
			derivedio.Seq(tagImageSetSelectorSequence, selector),
			derivedio.Seq(tagTimeBasedImageSetsSequence,
				derivedio.DataSet(
					derivedio.US(tagImageSetNumber, 1),
					derivedio.CS(tagImageSetSelectorCategory, "RELATIVE_TIME"),
					derivedio.US(tagRelativeTime, 0, 0),
				),
				derivedio.DataSet(
					derivedio.US(tagImageSetNumber, 2),
					derivedio.CS(tagImageSetSelectorCategory, "ABSTRACT_PRIOR"),
					derivedio.Raw(tagAbstractPriorValue, core.VRSS, int16Bytes(-1, -1)),
				),
			),
		)),
	)

	got, err := Read(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ImageSets) != 2 || len(got.ImageSets[0].Selectors) != 1 || len(got.ImageSets[1].Selectors) != 1 {
		t.Fatalf("image sets = %+v", got.ImageSets)
	}
	tests := []struct {
		name     string
		imageSet int
		want     string
	}{
		{name: "relative time overrides selector item", imageSet: 0, want: "RELATIVE_TIME"},
		{name: "abstract prior overrides selector item", imageSet: 1, want: "ABSTRACT_PRIOR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if category := got.ImageSets[tt.imageSet].Selectors[0].Category; category != tt.want {
				t.Fatalf("selector category = %q, want %s", category, tt.want)
			}
		})
	}
}

func TestReadImageBoxExplicitNoOverridesDisplayInversion(t *testing.T) {
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "Image box inversion"),
		derivedio.Seq(tagDisplaySetsSequence, derivedio.DataSet(
			derivedio.US(tagDisplaySetNumber, 1),
			derivedio.CS(tagShowGrayscaleInverted, "YES"),
			derivedio.Seq(tagImageBoxesSequence,
				derivedio.DataSet(
					derivedio.US(tagImageBoxNumber, 1),
					derivedio.CS(tagShowGrayscaleInverted, "NO"),
				),
				derivedio.DataSet(
					derivedio.US(tagImageBoxNumber, 2),
				),
			),
		)),
	)

	got, err := Read(obj)
	if err != nil {
		t.Fatal(err)
	}
	boxes := got.DisplaySets[0].ImageBoxes
	if len(boxes) != 2 || boxes[0].ShowInverted || !boxes[1].ShowInverted {
		t.Fatalf("image box inversion = %+v, want explicit NO then inherited YES", boxes)
	}
}

func TestReadCurrentPriorAndSynchronization(t *testing.T) {
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "CT current and prior"),
		derivedio.US(tagNumberOfPriorsReferenced, 1),
		derivedio.Seq(tagImageSetsSequence, derivedio.DataSet(
			derivedio.Seq(tagImageSetSelectorSequence,
				selectorDataSet(core.NewTag(0x0008, 0x0060), core.VRCS, "CT"),
			),
			derivedio.Seq(tagTimeBasedImageSetsSequence,
				derivedio.DataSet(
					derivedio.US(tagImageSetNumber, 1),
					derivedio.US(tagRelativeTime, 0, 0),
				),
				derivedio.DataSet(
					derivedio.US(tagImageSetNumber, 2),
					derivedio.Raw(tagAbstractPriorValue, core.VRSS, int16Bytes(-1, -1)),
				),
			),
		)),
		derivedio.Seq(tagDisplaySetsSequence,
			displaySetDataSet(1, 1, 1),
			displaySetDataSet(2, 2, 2),
		),
		derivedio.Seq(tagSynchronizedScrollingSequence, derivedio.DataSet(
			derivedio.US(tagDisplaySetScrollingGroup, 1, 2),
		)),
		derivedio.Seq(tagImageBoxSynchronizationSequence, derivedio.DataSet(
			derivedio.US(tagSynchronizedImageBoxList, 1, 2),
			derivedio.CS(tagTypeOfSynchronization, "POSITION"),
		)),
	)

	got, err := Read(obj)
	if err != nil {
		t.Fatal(err)
	}
	if got.NumberOfPriors != 1 || len(got.ImageSets) != 2 {
		t.Fatalf("priors/image sets = %d %+v", got.NumberOfPriors, got.ImageSets)
	}
	if got.ImageSets[0].IsPrior || !got.ImageSets[1].IsPrior || got.ImageSets[1].PriorIndex != 0 {
		t.Fatalf("current/prior scopes = %+v", got.ImageSets)
	}
	if len(got.SyncGroups) != 2 ||
		len(got.SyncGroups[0].DisplaySets) != 2 ||
		got.SyncGroups[1].Type != "POSITION" {
		t.Fatalf("sync groups = %+v", got.SyncGroups)
	}
}

func TestReadMultiModalityValuesAndBigEndianAttribute(t *testing.T) {
	selector := selectorDataSet(core.NewTag(0x0008, 0x0060), core.VRCS, "CT", "MR")
	selector.Elements[1] = atElement(binary.BigEndian, tagSelectorAttribute, core.NewTag(0x0008, 0x0060))
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "CT or MR"),
		derivedio.Seq(tagImageSetsSequence, derivedio.DataSet(
			derivedio.Seq(tagImageSetSelectorSequence, selector),
			derivedio.US(tagImageSetNumber, 1),
		)),
	)
	obj.SetValueByteOrder(binary.BigEndian)
	items, _ := obj.GetSequence(tagImageSetsSequence)
	items[0].SetValueByteOrder(binary.BigEndian)
	selectors, _ := items[0].GetSequence(tagImageSetSelectorSequence)
	selectors[0].SetValueByteOrder(binary.BigEndian)

	got, err := Read(obj)
	if err != nil {
		t.Fatal(err)
	}
	values := got.ImageSets[0].Selectors[0].Values
	if got.ImageSets[0].Selectors[0].Attribute != core.NewTag(0x0008, 0x0060) ||
		len(values) != 2 || values[0] != "CT" || values[1] != "MR" {
		t.Fatalf("selector = %+v", got.ImageSets[0].Selectors[0])
	}
}

func TestReadReportsUnsupportedConstructs(t *testing.T) {
	privateTag := core.NewTag(0x0011, 0x1010)
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "Unsupported"),
		derivedio.Seq(tagImageSetsSequence, derivedio.DataSet(
			derivedio.Seq(tagImageSetSelectorSequence, derivedio.DataSet(
				derivedio.CS(tagImageSetSelectorUsageFlag, "NO_MATCH"),
				atElement(binary.LittleEndian, tagSelectorAttribute, privateTag),
				derivedio.US(tagSelectorValueNumber, 1),
				derivedio.CS(tagSelectorAttributeVR, "CS"),
				atElement(binary.LittleEndian, tagSelectorSequencePointer, core.NewTag(0x0040, 0x0275)),
				derivedio.LO(tagSelectorAttributePrivateCreator, "VENDOR"),
				derivedio.CS(tagSelectorCSValue, "SECRET"),
			)),
			derivedio.US(tagImageSetNumber, 1),
		)),
		derivedio.Seq(tagDisplaySetsSequence, derivedio.DataSet(
			derivedio.US(tagDisplaySetNumber, 1),
			derivedio.CS(tagBlendingOperationType, "BLEND"),
			derivedio.Seq(tagFilterOperationsSequence, derivedio.DataSet()),
			derivedio.Seq(tagImageBoxesSequence, derivedio.DataSet(
				derivedio.US(tagImageBoxNumber, 1),
				fdElement(tagDisplayEnvironmentSpatialPosition, 0, 1, 2),
			)),
		)),
	)

	got, err := Read(obj)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]int{}
	for _, diagnostic := range got.Diagnostics {
		codes[diagnostic.Code]++
	}
	if codes["unsupported-construct"] < 4 || codes["invalid-spatial-position"] != 1 {
		t.Fatalf("diagnostics = %+v", got.Diagnostics)
	}
}

func TestReadReportsSelectorValueBoundsInsteadOfSilentlyWeakening(t *testing.T) {
	values := make([]string, maxValues+1)
	for i := range values {
		values[i] = "VALUE"
	}
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "Too many values"),
		derivedio.Seq(tagImageSetsSequence, derivedio.DataSet(
			derivedio.Seq(tagImageSetSelectorSequence,
				selectorDataSet(core.NewTag(0x0008, 0x0060), core.VRCS, values...),
			),
			derivedio.US(tagImageSetNumber, 1),
		)),
	)
	got, err := Read(obj)
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range got.Diagnostics {
		if diagnostic.Code == "selector-value-bounds" {
			return
		}
	}
	t.Fatalf("diagnostics = %+v, want selector-value-bounds", got.Diagnostics)
}

func TestReadIsBoundedAndPixelDataIndependent(t *testing.T) {
	items := make([]core.DataSet, maxImageSets+1)
	for i := range items {
		items[i] = derivedio.DataSet(derivedio.US(tagImageSetNumber, uint16(i+1)))
	}
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "Bounded"),
		derivedio.Seq(tagImageSetsSequence, items...),
		core.Element{
			Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
			Value:  nil,
		},
	)
	if _, err := Read(obj); !errors.Is(err, ErrBoundsExceeded) {
		t.Fatalf("Read error = %v, want ErrBoundsExceeded", err)
	}

	obj = hpObject(
		derivedio.SH(tagHangingProtocolName, "No pixels"),
		core.Element{
			Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
			Value:  nil,
		},
	)
	if _, err := Read(obj); err != nil {
		t.Fatalf("pixel-independent Read: %v", err)
	}
}

func TestReadPart10RoundTrip(t *testing.T) {
	obj := hpObject(
		derivedio.SH(tagHangingProtocolName, "Round trip"),
		derivedio.Seq(tagImageSetsSequence, derivedio.DataSet(
			derivedio.Seq(tagImageSetSelectorSequence,
				selectorDataSet(core.NewTag(0x0008, 0x0060), core.VRCS, "MR"),
			),
			derivedio.US(tagImageSetNumber, 1),
		)),
		derivedio.Seq(tagDisplaySetsSequence, displaySetDataSet(1, 1, 1)),
	)
	file, err := derivedio.File(HangingProtocolStorage, "1.2.826.0.1.3680043.9.7433.563.1", obj)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(roundTrip.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Round trip" || got.SOPInstanceUID != "1.2.826.0.1.3680043.9.7433.563.1" ||
		len(got.ImageSets) != 1 || got.ImageSets[0].Selectors[0].Values[0] != "MR" ||
		len(got.DisplaySets) != 1 {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestReadRejectsWrongSOPClassAndMissingName(t *testing.T) {
	wrong := derivedio.Object(
		derivedio.UI(tagSOPClassUID, "1.2.3"),
		derivedio.SH(tagHangingProtocolName, "Wrong"),
	)
	if _, err := Read(wrong); !errors.Is(err, ErrNotHangingProtocol) {
		t.Fatalf("wrong SOP error = %v", err)
	}
	if _, err := Read(hpObject()); !errors.Is(err, ErrMissingName) {
		t.Fatalf("missing name error = %v", err)
	}
}

func hpObject(elements ...core.Element) *object.Object {
	base := []core.Element{
		derivedio.UI(tagSOPClassUID, HangingProtocolStorage),
		derivedio.UI(tagSOPInstanceUID, "1.2.826.0.1.3680043.9.7433.563.1"),
	}
	return derivedio.Object(append(base, elements...)...)
}

func selectorDataSet(attribute core.Tag, vr core.VR, values ...string) core.DataSet {
	valueTag := map[core.VR]core.Tag{
		core.VRCS: tagSelectorCSValue,
		core.VRLO: tagSelectorLOValue,
		core.VRSH: tagSelectorSHValue,
	}[vr]
	return derivedio.DataSet(
		derivedio.CS(tagImageSetSelectorUsageFlag, "MATCH"),
		atElement(binary.LittleEndian, tagSelectorAttribute, attribute),
		derivedio.US(tagSelectorValueNumber, 1),
		derivedio.CS(tagSelectorAttributeVR, string(vr)),
		derivedio.Strings(valueTag, vr, values),
	)
}

func displaySetDataSet(displayNumber, imageSetNumber, boxNumber uint16) core.DataSet {
	return derivedio.DataSet(
		derivedio.US(tagDisplaySetNumber, displayNumber),
		derivedio.US(tagImageSetNumber, imageSetNumber),
		derivedio.Seq(tagImageBoxesSequence, derivedio.DataSet(
			derivedio.US(tagImageBoxNumber, boxNumber),
			derivedio.CS(tagImageBoxLayoutType, "STACK"),
		)),
	)
}

func atElement(order binary.ByteOrder, tag core.Tag, values ...core.Tag) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		order.PutUint16(raw[i*4:], value.Group)
		order.PutUint16(raw[i*4+2:], value.Element)
	}
	return derivedio.Raw(tag, core.VRAT, raw)
}

func int16Bytes(values ...int16) []byte {
	raw := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(value))
	}
	return raw
}

func fdElement(tag core.Tag, values ...float64) core.Element {
	raw := make([]byte, len(values)*8)
	for i, value := range values {
		binary.LittleEndian.PutUint64(raw[i*8:], math.Float64bits(value))
	}
	return derivedio.Raw(tag, core.VRFD, raw)
}
