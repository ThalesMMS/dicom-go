// Package hangingprotocol decodes DICOM Hanging Protocol Storage objects into
// a bounded, viewer-neutral representation.
package hangingprotocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	// HangingProtocolStorage is the DICOM Hanging Protocol Storage SOP Class.
	HangingProtocolStorage = "1.2.840.10008.5.1.4.38.1"

	maxDefinitions = 64
	maxImageSets   = 64
	maxSelectors   = 64
	maxScreens     = 16
	maxDisplaySets = 128
	maxImageBoxes  = 256
	maxValues      = 32
	maxTextBytes   = 1024
)

var (
	ErrNotHangingProtocol = errors.New("dicom hanging protocol: unsupported SOP Class UID")
	ErrMissingName        = errors.New("dicom hanging protocol: missing Hanging Protocol Name")
	ErrBoundsExceeded     = errors.New("dicom hanging protocol: bounded parse limit exceeded")
)

var (
	tagSOPClassUID                       = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID                    = core.NewTag(0x0008, 0x0018)
	tagHangingProtocolName               = core.NewTag(0x0072, 0x0002)
	tagHangingProtocolDescription        = core.NewTag(0x0072, 0x0004)
	tagHangingProtocolLevel              = core.NewTag(0x0072, 0x0006)
	tagHangingProtocolCreator            = core.NewTag(0x0072, 0x0008)
	tagHangingProtocolDefinitionSeq      = core.NewTag(0x0072, 0x000C)
	tagNumberOfPriorsReferenced          = core.NewTag(0x0072, 0x0014)
	tagImageSetsSequence                 = core.NewTag(0x0072, 0x0020)
	tagImageSetSelectorSequence          = core.NewTag(0x0072, 0x0022)
	tagImageSetSelectorUsageFlag         = core.NewTag(0x0072, 0x0024)
	tagSelectorAttribute                 = core.NewTag(0x0072, 0x0026)
	tagSelectorValueNumber               = core.NewTag(0x0072, 0x0028)
	tagTimeBasedImageSetsSequence        = core.NewTag(0x0072, 0x0030)
	tagImageSetNumber                    = core.NewTag(0x0072, 0x0032)
	tagImageSetSelectorCategory          = core.NewTag(0x0072, 0x0034)
	tagRelativeTime                      = core.NewTag(0x0072, 0x0038)
	tagRelativeTimeUnits                 = core.NewTag(0x0072, 0x003A)
	tagAbstractPriorValue                = core.NewTag(0x0072, 0x003C)
	tagImageSetLabel                     = core.NewTag(0x0072, 0x0040)
	tagSelectorAttributeVR               = core.NewTag(0x0072, 0x0050)
	tagSelectorSequencePointer           = core.NewTag(0x0072, 0x0052)
	tagSelectorAttributePrivateCreator   = core.NewTag(0x0072, 0x0056)
	tagSelectorAEValue                   = core.NewTag(0x0072, 0x005E)
	tagSelectorASValue                   = core.NewTag(0x0072, 0x005F)
	tagSelectorATValue                   = core.NewTag(0x0072, 0x0060)
	tagSelectorDAValue                   = core.NewTag(0x0072, 0x0061)
	tagSelectorCSValue                   = core.NewTag(0x0072, 0x0062)
	tagSelectorDTValue                   = core.NewTag(0x0072, 0x0063)
	tagSelectorISValue                   = core.NewTag(0x0072, 0x0064)
	tagSelectorLOValue                   = core.NewTag(0x0072, 0x0066)
	tagSelectorPNValue                   = core.NewTag(0x0072, 0x006A)
	tagSelectorTMValue                   = core.NewTag(0x0072, 0x006B)
	tagSelectorSHValue                   = core.NewTag(0x0072, 0x006C)
	tagSelectorSTValue                   = core.NewTag(0x0072, 0x006E)
	tagSelectorUCValue                   = core.NewTag(0x0072, 0x006F)
	tagSelectorUTValue                   = core.NewTag(0x0072, 0x0070)
	tagSelectorURValue                   = core.NewTag(0x0072, 0x0071)
	tagSelectorDSValue                   = core.NewTag(0x0072, 0x0072)
	tagSelectorUIValue                   = core.NewTag(0x0072, 0x007F)
	tagNumberOfScreens                   = core.NewTag(0x0072, 0x0100)
	tagNominalScreenDefinitionSequence   = core.NewTag(0x0072, 0x0102)
	tagNumberOfVerticalPixels            = core.NewTag(0x0072, 0x0104)
	tagNumberOfHorizontalPixels          = core.NewTag(0x0072, 0x0106)
	tagDisplayEnvironmentSpatialPosition = core.NewTag(0x0072, 0x0108)
	tagDisplaySetsSequence               = core.NewTag(0x0072, 0x0200)
	tagDisplaySetNumber                  = core.NewTag(0x0072, 0x0202)
	tagDisplaySetLabel                   = core.NewTag(0x0072, 0x0203)
	tagDisplaySetPresentationGroup       = core.NewTag(0x0072, 0x0204)
	tagPartialDataDisplayHandling        = core.NewTag(0x0072, 0x0208)
	tagSynchronizedScrollingSequence     = core.NewTag(0x0072, 0x0210)
	tagDisplaySetScrollingGroup          = core.NewTag(0x0072, 0x0212)
	tagImageBoxesSequence                = core.NewTag(0x0072, 0x0300)
	tagImageBoxNumber                    = core.NewTag(0x0072, 0x0302)
	tagImageBoxLayoutType                = core.NewTag(0x0072, 0x0304)
	tagImageBoxTileHorizontalDimension   = core.NewTag(0x0072, 0x0306)
	tagImageBoxTileVerticalDimension     = core.NewTag(0x0072, 0x0308)
	tagFilterOperationsSequence          = core.NewTag(0x0072, 0x0400)
	tagImageBoxSynchronizationSequence   = core.NewTag(0x0072, 0x0430)
	tagSynchronizedImageBoxList          = core.NewTag(0x0072, 0x0432)
	tagTypeOfSynchronization             = core.NewTag(0x0072, 0x0434)
	tagBlendingOperationType             = core.NewTag(0x0072, 0x0500)
	tagReformattingOperationType         = core.NewTag(0x0072, 0x0510)
	tagThreeDRenderingType               = core.NewTag(0x0072, 0x0520)
	tagSortingOperationsSequence         = core.NewTag(0x0072, 0x0600)
	tagDisplaySetPatientOrientation      = core.NewTag(0x0072, 0x0700)
	tagVOIType                           = core.NewTag(0x0072, 0x0702)
	tagPseudoColorType                   = core.NewTag(0x0072, 0x0704)
	tagShowGrayscaleInverted             = core.NewTag(0x0072, 0x0706)
)

// Diagnostic describes a construct that could not be represented faithfully.
// Callers must surface these diagnostics rather than silently weakening a rule.
type Diagnostic struct {
	Path    string
	Code    string
	Message string
}

// Selector is one DICOM image-set matching criterion.
type Selector struct {
	UsageFlag               string
	Category                string
	Attribute               core.Tag
	AttributeVR             core.VR
	ValueNumber             int
	Values                  []string
	SequencePointer         []core.Tag
	AttributePrivateCreator string
}

// ImageSet identifies current or prior images and their selectors.
type ImageSet struct {
	Number            int
	Label             string
	PriorIndex        int
	IsPrior           bool
	RelativeTime      []int
	RelativeTimeUnits string
	AbstractPrior     []int
	Selectors         []Selector
}

// Screen is one nominal display from the DICOM display environment.
type Screen struct {
	VerticalPixels   int
	HorizontalPixels int
	SpatialPosition  []float64
}

// ImageBox is a viewport inside a display set.
type ImageBox struct {
	Number             int
	ImageSetNumber     int
	LayoutType         string
	TileColumns        int
	TileRows           int
	SpatialPosition    []float64
	VOIType            string
	PatientOrientation []string
	ShowInverted       bool
}

// DisplaySet describes a DICOM presentation group and its image boxes.
type DisplaySet struct {
	Number              int
	Label               string
	PresentationGroup   int
	ImageSetNumber      int
	PartialDataHandling string
	VOIType             string
	PatientOrientation  []string
	ShowInverted        bool
	ImageBoxes          []ImageBox
}

// SyncGroup is a supported or inspectable synchronization declaration.
type SyncGroup struct {
	DisplaySets []int
	ImageBoxes  []int
	Type        string
}

// Protocol is the bounded, pixel-independent representation of a DICOM HP.
type Protocol struct {
	SOPInstanceUID  string
	Name            string
	Description     string
	Level           string
	Creator         string
	NumberOfPriors  int
	NumberOfScreens int
	Definitions     []Selector
	ImageSets       []ImageSet
	Screens         []Screen
	DisplaySets     []DisplaySet
	SyncGroups      []SyncGroup
	Diagnostics     []Diagnostic
}

// Read decodes a Hanging Protocol Storage data set. It never requests Pixel
// Data, recursively walks arbitrary content, or materializes deferred values.
func Read(obj *object.Object) (*Protocol, error) {
	if obj == nil {
		return nil, fmt.Errorf("dicom hanging protocol: nil dataset")
	}
	if uid := derivedio.CleanUID(obj, tagSOPClassUID); uid != HangingProtocolStorage {
		return nil, fmt.Errorf("%w: %q", ErrNotHangingProtocol, uid)
	}
	p := &Protocol{
		SOPInstanceUID:  derivedio.CleanUID(obj, tagSOPInstanceUID),
		Name:            cleanText(obj, tagHangingProtocolName),
		Description:     cleanText(obj, tagHangingProtocolDescription),
		Level:           cleanText(obj, tagHangingProtocolLevel),
		Creator:         cleanText(obj, tagHangingProtocolCreator),
		NumberOfPriors:  firstInt(obj, tagNumberOfPriorsReferenced),
		NumberOfScreens: firstInt(obj, tagNumberOfScreens),
	}
	if p.Name == "" {
		return nil, ErrMissingName
	}

	var err error
	p.Definitions, err = parseSelectorContainer(obj, tagHangingProtocolDefinitionSeq, "definitions", maxDefinitions, p)
	if err != nil {
		return nil, err
	}
	if err := parseImageSets(obj, p); err != nil {
		return nil, err
	}
	if err := parseScreens(obj, p); err != nil {
		return nil, err
	}
	if err := parseDisplaySets(obj, p); err != nil {
		return nil, err
	}
	return p, nil
}

func parseSelectorContainer(obj *object.Object, sequenceTag core.Tag, path string, limit int, p *Protocol) ([]Selector, error) {
	items, _ := obj.GetSequence(sequenceTag)
	if len(items) > limit {
		return nil, boundError(path, len(items), limit)
	}
	selectors := make([]Selector, 0, len(items))
	for i, item := range items {
		selector, diagnostics := parseSelector(item, fmt.Sprintf("%s[%d]", path, i))
		p.Diagnostics = append(p.Diagnostics, diagnostics...)
		selectors = append(selectors, selector)
	}
	return selectors, nil
}

func parseImageSets(obj *object.Object, p *Protocol) error {
	items, _ := obj.GetSequence(tagImageSetsSequence)
	if len(items) > maxImageSets {
		return boundError("imageSets", len(items), maxImageSets)
	}
	for i, item := range items {
		path := fmt.Sprintf("imageSets[%d]", i)
		selectors, err := parseSelectorContainer(item, tagImageSetSelectorSequence, path+".selectors", maxSelectors, p)
		if err != nil {
			return err
		}
		timeItems, _ := item.GetSequence(tagTimeBasedImageSetsSequence)
		if len(timeItems) > maxImageSets || len(p.ImageSets)+max(1, len(timeItems)) > maxImageSets {
			return boundError(path+".timeBased", len(timeItems), maxImageSets)
		}
		if len(timeItems) == 0 {
			if err := checkImageSetValueBounds(item, path); err != nil {
				return err
			}
			p.ImageSets = append(p.ImageSets, imageSetFrom(item, i+1, selectors))
			continue
		}
		for j, timeItem := range timeItems {
			if err := checkImageSetValueBounds(timeItem, fmt.Sprintf("%s.timeBased[%d]", path, j)); err != nil {
				return err
			}
			set := imageSetFrom(timeItem, len(p.ImageSets)+1, selectors)
			if set.Label == "" {
				set.Label = cleanText(item, tagImageSetLabel)
			}
			p.ImageSets = append(p.ImageSets, set)
			if set.Number == 0 {
				p.Diagnostics = append(p.Diagnostics, Diagnostic{
					Path:    path + ".timeBased[" + strconv.Itoa(j) + "]",
					Code:    "missing-image-set-number",
					Message: "time-based image set has no Image Set Number",
				})
			}
		}
	}
	return nil
}

func checkImageSetValueBounds(obj *object.Object, path string) error {
	for _, tag := range []core.Tag{tagRelativeTime, tagAbstractPriorValue} {
		if count := len(derivedio.Ints(obj, tag)); count > maxValues {
			return boundError(path+"."+tag.String(), count, maxValues)
		}
	}
	return nil
}

func imageSetFrom(obj *object.Object, fallback int, selectors []Selector) ImageSet {
	number := firstInt(obj, tagImageSetNumber)
	if number == 0 {
		number = fallback
	}
	selectors = append([]Selector(nil), selectors...)
	category := strings.ToUpper(cleanText(obj, tagImageSetSelectorCategory))
	for index := range selectors {
		selectors[index].Category = category
	}
	set := ImageSet{
		Number:            number,
		Label:             cleanText(obj, tagImageSetLabel),
		RelativeTime:      ints(obj, tagRelativeTime),
		RelativeTimeUnits: cleanText(obj, tagRelativeTimeUnits),
		AbstractPrior:     ints(obj, tagAbstractPriorValue),
		Selectors:         selectors,
	}
	set.IsPrior, set.PriorIndex = priorSelection(set.AbstractPrior, set.RelativeTime)
	return set
}

func priorSelection(abstract, relative []int) (bool, int) {
	best := 0
	for _, value := range abstract {
		if value != 0 {
			if value < 0 {
				value = -value
			}
			if best == 0 || value < best {
				best = value
			}
		}
	}
	if best > 0 {
		return true, best - 1
	}
	for _, value := range relative {
		if value != 0 {
			return true, 0
		}
	}
	return false, 0
}

func parseScreens(obj *object.Object, p *Protocol) error {
	items, _ := obj.GetSequence(tagNominalScreenDefinitionSequence)
	if len(items) > maxScreens {
		return boundError("screens", len(items), maxScreens)
	}
	for i, item := range items {
		screen := Screen{
			VerticalPixels:   firstInt(item, tagNumberOfVerticalPixels),
			HorizontalPixels: firstInt(item, tagNumberOfHorizontalPixels),
			SpatialPosition:  derivedio.Floats(item, tagDisplayEnvironmentSpatialPosition),
		}
		if len(screen.SpatialPosition) != 0 && len(screen.SpatialPosition) != 4 {
			p.Diagnostics = append(p.Diagnostics, Diagnostic{
				Path:    fmt.Sprintf("screens[%d].spatialPosition", i),
				Code:    "invalid-spatial-position",
				Message: "Display Environment Spatial Position must contain four values",
			})
		}
		p.Screens = append(p.Screens, screen)
	}
	return nil
}

func parseDisplaySets(obj *object.Object, p *Protocol) error {
	items, _ := obj.GetSequence(tagDisplaySetsSequence)
	if len(items) > maxDisplaySets {
		return boundError("displaySets", len(items), maxDisplaySets)
	}
	totalBoxes := 0
	for i, item := range items {
		path := fmt.Sprintf("displaySets[%d]", i)
		display := DisplaySet{
			Number:              firstInt(item, tagDisplaySetNumber),
			Label:               cleanText(item, tagDisplaySetLabel),
			PresentationGroup:   firstInt(item, tagDisplaySetPresentationGroup),
			ImageSetNumber:      firstInt(item, tagImageSetNumber),
			PartialDataHandling: cleanText(item, tagPartialDataDisplayHandling),
			VOIType:             cleanText(item, tagVOIType),
			PatientOrientation:  cleanTexts(item, tagDisplaySetPatientOrientation),
			ShowInverted:        isYes(cleanText(item, tagShowGrayscaleInverted)),
		}
		boxes, _ := item.GetSequence(tagImageBoxesSequence)
		totalBoxes += len(boxes)
		if totalBoxes > maxImageBoxes {
			return boundError("imageBoxes", totalBoxes, maxImageBoxes)
		}
		if item.Has(tagFilterOperationsSequence) {
			p.Diagnostics = append(p.Diagnostics, unsupported(path+".filterOperations", "filter operations"))
		}
		for _, tagAndName := range []struct {
			tag  core.Tag
			name string
		}{
			{tagBlendingOperationType, "blending operation"},
			{tagReformattingOperationType, "reformatting operation"},
			{tagThreeDRenderingType, "3D rendering"},
			{tagSortingOperationsSequence, "sorting operation"},
			{tagPseudoColorType, "pseudo-color presentation"},
		} {
			if item.Has(tagAndName.tag) {
				p.Diagnostics = append(p.Diagnostics, unsupported(path, tagAndName.name))
			}
		}
		for j, boxItem := range boxes {
			box := ImageBox{
				Number:             firstInt(boxItem, tagImageBoxNumber),
				ImageSetNumber:     firstInt(boxItem, tagImageSetNumber),
				LayoutType:         cleanText(boxItem, tagImageBoxLayoutType),
				TileColumns:        firstInt(boxItem, tagImageBoxTileHorizontalDimension),
				TileRows:           firstInt(boxItem, tagImageBoxTileVerticalDimension),
				SpatialPosition:    derivedio.Floats(boxItem, tagDisplayEnvironmentSpatialPosition),
				VOIType:            cleanText(boxItem, tagVOIType),
				PatientOrientation: cleanTexts(boxItem, tagDisplaySetPatientOrientation),
				ShowInverted:       isYes(cleanText(boxItem, tagShowGrayscaleInverted)),
			}
			if box.ImageSetNumber == 0 {
				box.ImageSetNumber = display.ImageSetNumber
			}
			if box.VOIType == "" {
				box.VOIType = display.VOIType
			}
			if len(box.PatientOrientation) == 0 {
				box.PatientOrientation = append([]string(nil), display.PatientOrientation...)
			}
			if !boxItem.Has(tagShowGrayscaleInverted) {
				box.ShowInverted = display.ShowInverted
			}
			if len(box.SpatialPosition) != 0 && len(box.SpatialPosition) != 4 {
				p.Diagnostics = append(p.Diagnostics, Diagnostic{
					Path:    fmt.Sprintf("%s.imageBoxes[%d].spatialPosition", path, j),
					Code:    "invalid-spatial-position",
					Message: "Display Environment Spatial Position must contain four values",
				})
			}
			display.ImageBoxes = append(display.ImageBoxes, box)
		}
		p.DisplaySets = append(p.DisplaySets, display)
	}
	if err := parseSyncGroups(obj, p); err != nil {
		return err
	}
	for i, item := range items {
		if err := parseSyncGroupsAt(item, fmt.Sprintf("displaySets[%d]", i), p); err != nil {
			return err
		}
	}
	return nil
}

func parseSyncGroups(obj *object.Object, p *Protocol) error {
	return parseSyncGroupsAt(obj, "protocol", p)
}

func parseSyncGroupsAt(obj *object.Object, path string, p *Protocol) error {
	scroll, _ := obj.GetSequence(tagSynchronizedScrollingSequence)
	boxes, _ := obj.GetSequence(tagImageBoxSynchronizationSequence)
	if len(scroll)+len(boxes) > maxDisplaySets {
		return boundError(path+".synchronization", len(scroll)+len(boxes), maxDisplaySets)
	}
	for i, item := range scroll {
		displaySets := derivedio.Ints(item, tagDisplaySetScrollingGroup)
		if len(displaySets) > maxValues {
			return boundError(fmt.Sprintf("%s.synchronizedScrolling[%d]", path, i), len(displaySets), maxValues)
		}
		group := SyncGroup{DisplaySets: ints(item, tagDisplaySetScrollingGroup), Type: "SCROLL"}
		if len(group.DisplaySets) < 2 {
			p.Diagnostics = append(p.Diagnostics, Diagnostic{
				Path:    fmt.Sprintf("%s.synchronizedScrolling[%d]", path, i),
				Code:    "incomplete-sync-group",
				Message: "synchronized scrolling group must reference at least two display sets",
			})
		}
		p.SyncGroups = append(p.SyncGroups, group)
	}
	for i, item := range boxes {
		imageBoxes := derivedio.Ints(item, tagSynchronizedImageBoxList)
		if len(imageBoxes) > maxValues {
			return boundError(fmt.Sprintf("%s.imageBoxSynchronization[%d]", path, i), len(imageBoxes), maxValues)
		}
		group := SyncGroup{
			ImageBoxes: ints(item, tagSynchronizedImageBoxList),
			Type:       strings.ToUpper(cleanText(item, tagTypeOfSynchronization)),
		}
		if len(group.ImageBoxes) < 2 || group.Type == "" {
			p.Diagnostics = append(p.Diagnostics, Diagnostic{
				Path:    fmt.Sprintf("%s.imageBoxSynchronization[%d]", path, i),
				Code:    "incomplete-sync-group",
				Message: "image-box synchronization requires a type and at least two image boxes",
			})
		}
		p.SyncGroups = append(p.SyncGroups, group)
	}
	return nil
}

func parseSelector(obj *object.Object, path string) (Selector, []Diagnostic) {
	selector := Selector{
		UsageFlag:               strings.ToUpper(cleanText(obj, tagImageSetSelectorUsageFlag)),
		ValueNumber:             firstInt(obj, tagSelectorValueNumber),
		AttributeVR:             core.VR(strings.ToUpper(cleanText(obj, tagSelectorAttributeVR))),
		AttributePrivateCreator: cleanText(obj, tagSelectorAttributePrivateCreator),
	}
	selectorTags := tagValues(obj, tagSelectorAttribute)
	if len(selectorTags) > 0 {
		selector.Attribute = selectorTags[0]
	}
	selector.SequencePointer = tagValues(obj, tagSelectorSequencePointer)
	selector.Values = selectorValues(obj, selector.AttributeVR)
	var diagnostics []Diagnostic
	if selector.Attribute == (core.Tag{}) {
		diagnostics = append(diagnostics, Diagnostic{Path: path, Code: "missing-selector-attribute", Message: "selector has no valid Selector Attribute"})
	}
	if selector.AttributeVR == "" {
		diagnostics = append(diagnostics, Diagnostic{Path: path, Code: "missing-selector-vr", Message: "selector has no Selector Attribute VR"})
	}
	if len(selector.Values) == 0 {
		diagnostics = append(diagnostics, Diagnostic{Path: path, Code: "missing-selector-value", Message: "selector has no decodable value"})
	}
	if len(selector.SequencePointer) > 0 {
		diagnostics = append(diagnostics, unsupported(path+".sequencePointer", "nested selector sequence pointer"))
	}
	if selector.AttributePrivateCreator != "" || selector.Attribute.IsPrivate() {
		diagnostics = append(diagnostics, unsupported(path, "private selector attribute"))
	}
	if selector.UsageFlag != "" && selector.UsageFlag != "MATCH" {
		diagnostics = append(diagnostics, unsupported(path+".usageFlag", "selector usage "+selector.UsageFlag))
	}
	if selector.ValueNumber > len(selector.Values) && len(selector.Values) > 0 {
		diagnostics = append(diagnostics, Diagnostic{Path: path + ".valueNumber", Code: "invalid-selector-value-number", Message: "Selector Value Number exceeds available values"})
	}
	if message := selectorValueBoundsMessage(obj, selector.AttributeVR); message != "" {
		diagnostics = append(diagnostics, Diagnostic{Path: path + ".values", Code: "selector-value-bounds", Message: message})
	}
	return selector, diagnostics
}

func selectorValues(obj *object.Object, vr core.VR) []string {
	tag := selectorValueTag(vr)
	if tag == (core.Tag{}) {
		return nil
	}
	if vr == core.VRAT {
		values := tagValues(obj, tag)
		out := make([]string, 0, min(len(values), maxValues))
		for _, value := range values {
			if len(out) == maxValues {
				break
			}
			out = append(out, value.String())
		}
		return out
	}
	values := cleanTexts(obj, tag)
	if len(values) > maxValues {
		values = values[:maxValues]
	}
	return values
}

func selectorValueTag(vr core.VR) core.Tag {
	return map[core.VR]core.Tag{
		core.VRAE: tagSelectorAEValue,
		core.VRAS: tagSelectorASValue,
		core.VRAT: tagSelectorATValue,
		core.VRDA: tagSelectorDAValue,
		core.VRCS: tagSelectorCSValue,
		core.VRDT: tagSelectorDTValue,
		core.VRIS: tagSelectorISValue,
		core.VRLO: tagSelectorLOValue,
		core.VRPN: tagSelectorPNValue,
		core.VRTM: tagSelectorTMValue,
		core.VRSH: tagSelectorSHValue,
		core.VRST: tagSelectorSTValue,
		core.VRUC: tagSelectorUCValue,
		core.VRUT: tagSelectorUTValue,
		core.VRUR: tagSelectorURValue,
		core.VRDS: tagSelectorDSValue,
		core.VRUI: tagSelectorUIValue,
	}[vr]
}

func selectorValueBoundsMessage(obj *object.Object, vr core.VR) string {
	tag := selectorValueTag(vr)
	if tag == (core.Tag{}) {
		return ""
	}
	if vr == core.VRAT {
		elem, ok := obj.Get(tag)
		if !ok {
			return ""
		}
		count := 0
		if values, ok := elem.Value.(core.TagValue); ok {
			count = len(values)
		} else if raw, ok := elem.RawBytes(); ok && len(raw)%4 == 0 {
			count = len(raw) / 4
		}
		if count > maxValues {
			return fmt.Sprintf("selector contains %d values (limit %d)", count, maxValues)
		}
		return ""
	}
	values, ok := obj.GetStrings(tag)
	if !ok {
		return ""
	}
	if len(values) > maxValues {
		return fmt.Sprintf("selector contains %d values (limit %d)", len(values), maxValues)
	}
	for _, value := range values {
		if len(value) > maxTextBytes {
			return fmt.Sprintf("selector value exceeds %d bytes", maxTextBytes)
		}
	}
	return ""
}

func cleanText(obj *object.Object, tag core.Tag) string {
	value := derivedio.CleanString(obj, tag)
	if len(value) > maxTextBytes {
		value = value[:maxTextBytes]
	}
	return value
}

func cleanTexts(obj *object.Object, tag core.Tag) []string {
	values, ok := obj.GetStrings(tag)
	if !ok {
		return nil
	}
	if len(values) > maxValues {
		values = values[:maxValues]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Trim(value, " \x00")
		if len(value) > maxTextBytes {
			value = value[:maxTextBytes]
		}
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func firstInt(obj *object.Object, tag core.Tag) int {
	values := ints(obj, tag)
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func ints(obj *object.Object, tag core.Tag) []int {
	values := derivedio.Ints(obj, tag)
	if len(values) > maxValues {
		values = values[:maxValues]
	}
	out := make([]int, len(values))
	for i, value := range values {
		out[i] = int(value)
	}
	return out
}

func tagValues(obj *object.Object, tag core.Tag) []core.Tag {
	if obj == nil {
		return nil
	}
	elem, ok := obj.Get(tag)
	if !ok || elem.VR() != core.VRAT {
		return nil
	}
	if values, ok := elem.Value.(core.TagValue); ok {
		if len(values) > maxValues {
			values = values[:maxValues]
		}
		return append([]core.Tag(nil), values...)
	}
	raw, ok := elem.RawBytes()
	if !ok || len(raw)%4 != 0 {
		return nil
	}
	count := len(raw) / 4
	if count > maxValues {
		count = maxValues
	}
	order := obj.ValueByteOrder()
	if order == nil {
		order = binary.LittleEndian
	}
	out := make([]core.Tag, count)
	for i := range out {
		offset := i * 4
		out[i] = core.NewTag(order.Uint16(raw[offset:]), order.Uint16(raw[offset+2:]))
	}
	return out
}

func isYes(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "YES", "Y", "TRUE":
		return true
	default:
		return false
	}
}

func unsupported(path, construct string) Diagnostic {
	return Diagnostic{Path: path, Code: "unsupported-construct", Message: construct + " is not representable by the Twin 2D hanging model"}
}

func boundError(path string, got, limit int) error {
	return fmt.Errorf("%w: %s has %d items (limit %d)", ErrBoundsExceeded, path, got, limit)
}
