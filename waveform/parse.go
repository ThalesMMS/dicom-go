package waveform

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	uidTwelveLeadECG            = "1.2.840.10008.5.1.4.1.1.9.1.1"
	uidGeneralECG               = "1.2.840.10008.5.1.4.1.1.9.1.2"
	uidAmbulatoryECG            = "1.2.840.10008.5.1.4.1.1.9.1.3"
	uidGeneral32BitECG          = "1.2.840.10008.5.1.4.1.1.9.1.4"
	uidHemodynamic              = "1.2.840.10008.5.1.4.1.1.9.2.1"
	uidCardiacElectrophysiology = "1.2.840.10008.5.1.4.1.1.9.3.1"
	uidArterialPulse            = "1.2.840.10008.5.1.4.1.1.9.5.1"
	uidRespiratory              = "1.2.840.10008.5.1.4.1.1.9.6.1"
	uidMultichannelRespiratory  = "1.2.840.10008.5.1.4.1.1.9.6.2"
	uidRoutineScalpEEG          = "1.2.840.10008.5.1.4.1.1.9.7.1"
	uidElectromyogram           = "1.2.840.10008.5.1.4.1.1.9.7.2"
	uidElectrooculogram         = "1.2.840.10008.5.1.4.1.1.9.7.3"
	uidSleepEEG                 = "1.2.840.10008.5.1.4.1.1.9.7.4"
	uidBodyPosition             = "1.2.840.10008.5.1.4.1.1.9.8.1"
	uidWaveformTrial            = "1.2.840.10008.5.1.4.1.1.9.1"
	uidBasicVoiceAudio          = "1.2.840.10008.5.1.4.1.1.9.4.1"
	uidGeneralAudio             = "1.2.840.10008.5.1.4.1.1.9.4.2"
	uidUltrasoundWaveform       = "1.2.840.10008.5.1.4.1.1.601.5"
)

var supportedStorageSOPClasses = map[string]struct{}{
	uidTwelveLeadECG:            {},
	uidGeneralECG:               {},
	uidAmbulatoryECG:            {},
	uidGeneral32BitECG:          {},
	uidHemodynamic:              {},
	uidCardiacElectrophysiology: {},
	uidArterialPulse:            {},
	uidRespiratory:              {},
	uidMultichannelRespiratory:  {},
	uidRoutineScalpEEG:          {},
	uidElectromyogram:           {},
	uidElectrooculogram:         {},
	uidSleepEEG:                 {},
	uidBodyPosition:             {},
}

var unsupportedWaveformSOPClasses = map[string]string{
	uidWaveformTrial:      "retired trial waveform storage is not decoded",
	uidBasicVoiceAudio:    "G.711 audio waveform storage requires an audio decoder",
	uidGeneralAudio:       "audio waveform storage requires an audio decoder",
	uidUltrasoundWaveform: "ultrasound waveform storage is not in the calibrated physiologic waveform profile",
}

var supportedStorageUIDs = []string{
	uidTwelveLeadECG,
	uidGeneralECG,
	uidAmbulatoryECG,
	uidGeneral32BitECG,
	uidHemodynamic,
	uidCardiacElectrophysiology,
	uidArterialPulse,
	uidRespiratory,
	uidMultichannelRespiratory,
	uidRoutineScalpEEG,
	uidElectromyogram,
	uidElectrooculogram,
	uidSleepEEG,
	uidBodyPosition,
}

var (
	tagSOPClassUID                  = core.NewTag(0x0008, 0x0016)
	tagCodeValue                    = core.NewTag(0x0008, 0x0100)
	tagCodingSchemeDesignator       = core.NewTag(0x0008, 0x0102)
	tagCodingSchemeVersion          = core.NewTag(0x0008, 0x0103)
	tagCodeMeaning                  = core.NewTag(0x0008, 0x0104)
	tagMultiplexGroupTimeOffset     = core.NewTag(0x0018, 0x1068)
	tagWaveformOriginality          = core.NewTag(0x003A, 0x0004)
	tagNumberOfWaveformChannels     = core.NewTag(0x003A, 0x0005)
	tagNumberOfWaveformSamples      = core.NewTag(0x003A, 0x0010)
	tagSamplingFrequency            = core.NewTag(0x003A, 0x001A)
	tagMultiplexGroupLabel          = core.NewTag(0x003A, 0x0020)
	tagChannelDefinitionSequence    = core.NewTag(0x003A, 0x0200)
	tagWaveformChannelNumber        = core.NewTag(0x003A, 0x0202)
	tagChannelLabel                 = core.NewTag(0x003A, 0x0203)
	tagChannelStatus                = core.NewTag(0x003A, 0x0205)
	tagChannelSourceSequence        = core.NewTag(0x003A, 0x0208)
	tagChannelSensitivity           = core.NewTag(0x003A, 0x0210)
	tagChannelSensitivityUnits      = core.NewTag(0x003A, 0x0211)
	tagChannelSensitivityCorrection = core.NewTag(0x003A, 0x0212)
	tagChannelBaseline              = core.NewTag(0x003A, 0x0213)
	tagChannelTimeSkew              = core.NewTag(0x003A, 0x0214)
	tagChannelSampleSkew            = core.NewTag(0x003A, 0x0215)
	tagChannelOffset                = core.NewTag(0x003A, 0x0218)
	tagWaveformBitsStored           = core.NewTag(0x003A, 0x021A)
	tagFilterLowFrequency           = core.NewTag(0x003A, 0x0220)
	tagFilterHighFrequency          = core.NewTag(0x003A, 0x0221)
	tagNotchFilterFrequency         = core.NewTag(0x003A, 0x0222)
	tagMultiplexGroupUID            = core.NewTag(0x003A, 0x0310)
	tagReferencedWaveformChannels   = core.NewTag(0x0040, 0xA0B0)
	tagMeasurementUnitsCodeSequence = core.NewTag(0x0040, 0x08EA)
	tagTemporalRangeType            = core.NewTag(0x0040, 0xA130)
	tagReferencedSamplePositions    = core.NewTag(0x0040, 0xA132)
	tagReferencedTimeOffsets        = core.NewTag(0x0040, 0xA138)
	tagReferencedDateTime           = core.NewTag(0x0040, 0xA13A)
	tagLegacyTextValue              = core.NewTag(0x0040, 0xA160)
	tagConceptNameCodeSequence      = core.NewTag(0x0040, 0xA043)
	tagConceptCodeSequence          = core.NewTag(0x0040, 0xA168)
	tagAnnotationGroupNumber        = core.NewTag(0x0040, 0xA180)
	tagNumericValue                 = core.NewTag(0x0040, 0xA30A)
	tagWaveformAnnotationSequence   = core.NewTag(0x0040, 0xB020)
	tagUnformattedTextValue         = core.NewTag(0x0070, 0x0006)
	tagWaveformSequence             = core.NewTag(0x5400, 0x0100)
	tagWaveformBitsAllocated        = core.NewTag(0x5400, 0x1004)
	tagWaveformSampleInterpretation = core.NewTag(0x5400, 0x1006)
	tagWaveformPaddingValue         = core.NewTag(0x5400, 0x100A)
	tagWaveformData                 = core.NewTag(0x5400, 0x1010)
)

// IsStorageSOPClass reports whether uid is one of the calibrated physiologic
// waveform storage classes supported by this package.
func IsStorageSOPClass(uid string) bool {
	_, ok := supportedStorageSOPClasses[strings.TrimSpace(strings.TrimRight(uid, "\x00"))]
	return ok
}

// IsWaveformSOPClass reports whether uid is either a decoded storage class or
// a known waveform class intentionally exposed through raw fallback.
func IsWaveformSOPClass(uid string) bool {
	uid = strings.TrimSpace(strings.TrimRight(uid, "\x00"))
	if IsStorageSOPClass(uid) {
		return true
	}
	_, ok := unsupportedWaveformSOPClasses[uid]
	return ok
}

// SupportedStorageSOPClassUIDs returns a stable copy suitable for DIMSE
// presentation-context negotiation.
func SupportedStorageSOPClassUIDs() []string {
	return append([]string(nil), supportedStorageUIDs...)
}

func open(file *object.File, opts Options) (_ *Recording, err error) {
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("waveform: nil file or dataset")
	}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	sopClassUID, ok := file.Dataset.GetUID(tagSOPClassUID)
	if !ok {
		return nil, fmt.Errorf("%w: missing SOP Class UID", ErrNotWaveform)
	}
	if !IsStorageSOPClass(sopClassUID) {
		if reason, known := unsupportedWaveformSOPClasses[sopClassUID]; known {
			return nil, &UnsupportedError{Fallback: RawFallback{
				SOPClassUID: sopClassUID,
				GroupIndex:  -1,
				RawBytes:    rawWaveformByteCount(file.Dataset),
				Reason:      reason,
			}}
		}
		return nil, fmt.Errorf("%w: %s", ErrNotWaveform, sopClassUID)
	}

	items, ok := file.Dataset.GetSequence(tagWaveformSequence)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("waveform: supported SOP class has no Waveform Sequence")
	}
	if len(items) > normalized.maxGroups {
		return nil, fmt.Errorf(
			"waveform: Waveform Sequence has %d groups, limit %d",
			len(items),
			normalized.maxGroups,
		)
	}
	recording := &Recording{
		sopClassUID: sopClassUID,
		options:     normalized,
		groups:      make([]*group, 0, len(items)),
		closeCh:     make(chan struct{}),
	}
	defer func() {
		if err != nil {
			_ = recording.Close()
		}
	}()

	for i, item := range items {
		parsed, parseErr := parseGroup(sopClassUID, i, item, normalized)
		if parseErr != nil {
			return nil, fmt.Errorf("waveform: parse multiplex group %d: %w", i+1, parseErr)
		}
		recording.groups = append(recording.groups, parsed)
	}
	if err = assignIndexBudgets(recording.groups, normalized.maxIndexEntries); err != nil {
		return nil, err
	}
	recording.annotations, err = parseAnnotations(file.Dataset, recording.groups)
	if err != nil {
		return nil, fmt.Errorf("waveform: parse annotations: %w", err)
	}
	return recording, nil
}

func normalizeOptions(opts Options) (normalizedOptions, error) {
	if opts.MaxIndexEntries < 0 {
		return normalizedOptions{}, fmt.Errorf("waveform: MaxIndexEntries must not be negative")
	}
	if opts.MaxEnvelopeWidth < 0 {
		return normalizedOptions{}, fmt.Errorf("waveform: MaxEnvelopeWidth must not be negative")
	}
	if opts.MaxGroups < 0 {
		return normalizedOptions{}, fmt.Errorf("waveform: MaxGroups must not be negative")
	}
	if opts.MaxChannelsPerGroup < 0 {
		return normalizedOptions{}, fmt.Errorf("waveform: MaxChannelsPerGroup must not be negative")
	}
	if opts.MaxIndexEntries == 0 {
		opts.MaxIndexEntries = defaultMaxIndexEntries
	}
	if opts.MaxEnvelopeWidth == 0 {
		opts.MaxEnvelopeWidth = defaultMaxWidth
	}
	if opts.MaxGroups == 0 {
		opts.MaxGroups = defaultMaxGroups
	}
	if opts.MaxChannelsPerGroup == 0 {
		opts.MaxChannelsPerGroup = defaultMaxChannels
	}
	factory := opts.SourceFactory
	if factory == nil {
		factory = rawElementSource
	}
	return normalizedOptions{
		maxIndexEntries: opts.MaxIndexEntries,
		maxWidth:        opts.MaxEnvelopeWidth,
		maxGroups:       opts.MaxGroups,
		maxChannels:     opts.MaxChannelsPerGroup,
		sourceFactory:   factory,
	}, nil
}

func assignIndexBudgets(groups []*group, maxEntries int) error {
	minimum := 0
	totalWeight := int64(0)
	supported := make([]*group, 0, len(groups))
	weights := make([]int64, 0, len(groups))
	for _, group := range groups {
		if !group.info.Supported {
			continue
		}
		channels := len(group.info.Channels)
		if channels > maxEntries-minimum {
			return fmt.Errorf(
				"waveform: MaxIndexEntries %d cannot retain one bucket for each channel across all groups",
				maxEntries,
			)
		}
		minimum += channels
		weight, overflow := multiplySize(group.info.SampleCount, int64(channels))
		if overflow {
			weight = math.MaxInt64
		}
		if weight < 1 {
			weight = 1
		}
		if totalWeight > math.MaxInt64-weight {
			totalWeight = math.MaxInt64
		} else {
			totalWeight += weight
		}
		supported = append(supported, group)
		weights = append(weights, weight)
		group.indexBudget = channels
	}
	if len(supported) == 0 {
		return nil
	}

	remainingBudget := maxEntries - minimum
	remainingWeight := totalWeight
	for i, group := range supported {
		if remainingBudget == 0 {
			break
		}
		extra := remainingBudget
		if i != len(supported)-1 && remainingWeight > 0 {
			extra = int(float64(remainingBudget) * (float64(weights[i]) / float64(remainingWeight)))
			if extra < 0 {
				extra = 0
			}
			if extra > remainingBudget {
				extra = remainingBudget
			}
		}
		group.indexBudget += extra
		remainingBudget -= extra
		if weights[i] < remainingWeight {
			remainingWeight -= weights[i]
		} else {
			remainingWeight = 0
		}
	}
	return nil
}

func validateWaveformDataVR(vr core.VR, bitsAllocated int) error {
	if vr != core.VROB && vr != core.VROW {
		return fmt.Errorf("Waveform Data has VR %s, want OB or OW", vr)
	}
	if bitsAllocated != 8 && vr != core.VROW {
		return fmt.Errorf("Waveform Data with %d bits allocated requires OW, got %s", bitsAllocated, vr)
	}
	return nil
}

func validateSOPSampleInterpretation(sopClassUID, interpretation string) error {
	allowed := map[string]map[string]struct{}{
		uidTwelveLeadECG:            {"SS": {}},
		uidGeneralECG:               {"SS": {}},
		uidAmbulatoryECG:            {"SB": {}, "SS": {}},
		uidGeneral32BitECG:          {"SS": {}, "SL": {}},
		uidHemodynamic:              {"SS": {}},
		uidCardiacElectrophysiology: {"SS": {}},
		uidArterialPulse:            {"SB": {}, "SS": {}},
		uidRespiratory:              {"SB": {}, "SS": {}},
		uidMultichannelRespiratory:  {"SS": {}, "SL": {}},
		uidRoutineScalpEEG:          {"SS": {}, "SL": {}},
		uidElectromyogram:           {"SS": {}, "SL": {}},
		uidElectrooculogram:         {"SS": {}, "SL": {}},
		uidSleepEEG:                 {"SS": {}, "SL": {}},
		uidBodyPosition:             {"UB": {}, "SS": {}},
	}
	profile := allowed[sopClassUID]
	if _, ok := profile[interpretation]; ok {
		return nil
	}
	terms := make([]string, 0, len(profile))
	for term := range profile {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return fmt.Errorf(
		"SOP Class %s permits sample interpretation %s, got %s",
		sopClassUID,
		strings.Join(terms, " or "),
		interpretation,
	)
}

func parseGroup(sopClassUID string, index int, item *object.Object, opts normalizedOptions) (*group, error) {
	channelCount, err := requiredPositiveInt(item, tagNumberOfWaveformChannels, "Number of Waveform Channels")
	if err != nil {
		return nil, err
	}
	if channelCount > opts.maxChannels {
		return nil, fmt.Errorf(
			"Number of Waveform Channels %d exceeds limit %d",
			channelCount,
			opts.maxChannels,
		)
	}
	sampleCount, err := requiredPositiveInt64(item, tagNumberOfWaveformSamples, "Number of Waveform Samples")
	if err != nil {
		return nil, err
	}
	frequency, err := requiredPositiveFloat(item, tagSamplingFrequency, "Sampling Frequency")
	if err != nil {
		return nil, err
	}
	bitsAllocated, err := requiredPositiveInt(item, tagWaveformBitsAllocated, "Waveform Bits Allocated")
	if err != nil {
		return nil, err
	}
	interpretation, ok := item.GetString(tagWaveformSampleInterpretation)
	if !ok || strings.TrimSpace(interpretation) == "" {
		return nil, fmt.Errorf("missing Waveform Sample Interpretation")
	}
	interpretation = strings.ToUpper(strings.TrimSpace(interpretation))

	channelItems, ok := item.GetSequence(tagChannelDefinitionSequence)
	if !ok || len(channelItems) != channelCount {
		return nil, fmt.Errorf("Channel Definition Sequence has %d items, want %d", len(channelItems), channelCount)
	}
	channels := make([]ChannelInfo, channelCount)
	for i, channelItem := range channelItems {
		channel, channelErr := parseChannel(i, channelItem, frequency, bitsAllocated)
		if channelErr != nil {
			return nil, fmt.Errorf("channel %d: %w", i+1, channelErr)
		}
		channels[i] = channel
	}

	dataElement, ok := item.Get(tagWaveformData)
	if !ok {
		return nil, fmt.Errorf("missing Waveform Data")
	}
	if err := validateWaveformDataVR(dataElement.VR(), bitsAllocated); err != nil {
		return nil, err
	}
	source, err := opts.sourceFactory(index, dataElement)
	if err != nil {
		return nil, fmt.Errorf("open Waveform Data source: %w", err)
	}
	if source == nil {
		return nil, fmt.Errorf("Waveform Data source factory returned nil")
	}

	decoder, decoderErr := newSampleDecoder(item.ValueByteOrder(), bitsAllocated, interpretation)
	if decoderErr == nil {
		decoderErr = validateSOPSampleInterpretation(sopClassUID, interpretation)
	}
	info := GroupInfo{
		Index:                index,
		Label:                optionalString(item, tagMultiplexGroupLabel),
		UID:                  optionalString(item, tagMultiplexGroupUID),
		Originality:          optionalString(item, tagWaveformOriginality),
		TimeOffset:           durationFromFloat(optionalFloat(item, tagMultiplexGroupTimeOffset)/1000, time.Second),
		SamplingFrequencyHz:  frequency,
		SampleCount:          sampleCount,
		Duration:             durationFromFloat(float64(sampleCount)/frequency, time.Second),
		BitsAllocated:        bitsAllocated,
		SampleInterpretation: interpretation,
		Channels:             channels,
		Supported:            decoderErr == nil,
		RawDataBytes:         source.Size(),
	}
	parsed := &group{info: info, source: source, decoder: decoder}
	paddingElement, hasPadding := item.Get(tagWaveformPaddingValue)
	if hasPadding {
		if paddingElement.VR() != dataElement.VR() {
			_ = source.Close()
			return nil, fmt.Errorf(
				"Waveform Padding Value VR %s differs from Waveform Data VR %s",
				paddingElement.VR(),
				dataElement.VR(),
			)
		}
		raw, rawOK := paddingElement.RawBytes()
		if !rawOK {
			_ = source.Close()
			return nil, fmt.Errorf("Waveform Padding Value is not a raw sample")
		}
		width := bitsAllocated / 8
		maxLength := width
		if width == 1 {
			maxLength = 2
		}
		if width <= 0 || len(raw) < width || len(raw) > maxLength {
			_ = source.Close()
			return nil, fmt.Errorf(
				"Waveform Padding Value has %d bytes, want %d",
				len(raw),
				width,
			)
		}
	}
	if decoderErr != nil {
		parsed.info.FallbackReason = decoderErr.Error()
		return parsed, nil
	}
	expected, overflow := multiplySize(sampleCount, int64(channelCount), int64(decoder.width))
	if overflow {
		_ = source.Close()
		return nil, fmt.Errorf("Waveform Data size overflows int64")
	}
	if source.Size() < expected || source.Size() > expected+expectedPadding(expected) {
		_ = source.Close()
		return nil, fmt.Errorf("Waveform Data has %d bytes, want %d data bytes plus optional value padding", source.Size(), expected)
	}
	if hasPadding {
		raw, _ := paddingElement.RawBytes()
		parsed.paddingBits = make([]uint64, len(channels))
		for channelIndex, channel := range channels {
			_, bits, readErr := decoder.decodeBytes(raw[:decoder.width], channel.BitsStored)
			if readErr != nil {
				_ = source.Close()
				return nil, fmt.Errorf("Waveform Padding Value for channel %d: %w", channelIndex+1, readErr)
			}
			parsed.paddingBits[channelIndex] = bits
		}
		parsed.hasPadding = true
	}
	return parsed, nil
}

func parseChannel(index int, item *object.Object, frequency float64, bitsAllocated int) (ChannelInfo, error) {
	number := index + 1
	if encoded, err := optionalInt(item, tagWaveformChannelNumber); err != nil {
		return ChannelInfo{}, err
	} else if encoded != 0 {
		number = encoded
	}
	bitsStored, err := requiredPositiveInt(item, tagWaveformBitsStored, "Waveform Bits Stored")
	if err != nil {
		return ChannelInfo{}, err
	}
	if bitsStored > bitsAllocated {
		return ChannelInfo{}, fmt.Errorf("Waveform Bits Stored %d exceeds Bits Allocated %d", bitsStored, bitsAllocated)
	}
	calibration := Calibration{
		Status:           CalibrationMissingSensitivity,
		CorrectionFactor: 1,
	}
	if sensitivity, ok, floatErr := lookupOptionalFloat(item, tagChannelSensitivity); floatErr != nil {
		return ChannelInfo{}, floatErr
	} else if ok {
		calibration.Sensitivity = sensitivity
		calibration.HasSensitivity = true
		calibration.Status = CalibrationMissingUnits
	}
	if correction, ok, floatErr := lookupOptionalFloat(item, tagChannelSensitivityCorrection); floatErr != nil {
		return ChannelInfo{}, floatErr
	} else if ok {
		calibration.CorrectionFactor = correction
		calibration.HasCorrectionFactor = true
	}
	if baseline, ok, floatErr := lookupOptionalFloat(item, tagChannelBaseline); floatErr != nil {
		return ChannelInfo{}, floatErr
	} else if ok {
		calibration.Baseline = baseline
		calibration.HasBaseline = true
	}
	if units, ok, codeErr := singleCode(item, tagChannelSensitivityUnits); codeErr != nil {
		return ChannelInfo{}, codeErr
	} else if ok {
		calibration.Units = units
	}
	switch {
	case !calibration.HasSensitivity:
		calibration.Status = CalibrationMissingSensitivity
	case calibration.Units.Value == "" ||
		calibration.Units.Scheme == "" ||
		calibration.Units.Meaning == "":
		calibration.Status = CalibrationMissingUnits
	case !calibration.HasCorrectionFactor || !calibration.HasBaseline:
		calibration.Status = CalibrationMissingParameters
	default:
		calibration.Status = CalibrationComplete
	}
	timeSkew, hasTimeSkew, err := lookupOptionalFloat(item, tagChannelTimeSkew)
	if err != nil {
		return ChannelInfo{}, err
	}
	sampleSkew, _, err := lookupOptionalFloat(item, tagChannelSampleSkew)
	if err != nil {
		return ChannelInfo{}, err
	}
	if !hasTimeSkew {
		timeSkew = sampleSkew / frequency
	}
	channelOffset, _, err := lookupOptionalFloat(item, tagChannelOffset)
	if err != nil {
		return ChannelInfo{}, err
	}
	source, _ := firstCode(item, tagChannelSourceSequence)
	status, _ := item.GetStrings(tagChannelStatus)
	return ChannelInfo{
		Index:          index,
		Number:         number,
		Label:          optionalString(item, tagChannelLabel),
		Status:         status,
		Source:         source,
		Calibration:    calibration,
		TimeSkew:       durationFromFloat(timeSkew, time.Second),
		SampleSkew:     sampleSkew,
		ChannelOffset:  durationFromFloat(channelOffset, time.Second),
		BitsStored:     bitsStored,
		LowFrequency:   optionalFloat(item, tagFilterLowFrequency),
		HighFrequency:  optionalFloat(item, tagFilterHighFrequency),
		NotchFrequency: optionalFloat(item, tagNotchFilterFrequency),
	}, nil
}

func parseAnnotations(dataset *object.Object, groups []*group) ([]Annotation, error) {
	items, ok := dataset.GetSequence(tagWaveformAnnotationSequence)
	if !ok {
		return nil, nil
	}
	out := make([]Annotation, 0, len(items))
	for i, item := range items {
		annotation := Annotation{
			TemporalRangeType: strings.ToUpper(optionalString(item, tagTemporalRangeType)),
			Text:              optionalString(item, tagUnformattedTextValue),
		}
		if annotation.Text == "" {
			// Older producers sometimes used Text Value from the SR content
			// item macro. Preserve it as a compatibility fallback while
			// preferring the standard Waveform Annotation attribute.
			annotation.Text = optionalString(item, tagLegacyTextValue)
		}
		if groupNumber, err := optionalInt(item, tagAnnotationGroupNumber); err != nil {
			return nil, fmt.Errorf("annotation %d group number: %w", i+1, err)
		} else {
			annotation.AnnotationGroupNumber = groupNumber
		}
		if refs, ok, err := optionalUintValues(item, tagReferencedWaveformChannels); err != nil {
			return nil, fmt.Errorf("annotation %d channel references: %w", i+1, err)
		} else if ok {
			if len(refs)%2 != 0 {
				return nil, fmt.Errorf("annotation %d has odd Referenced Waveform Channels multiplicity", i+1)
			}
			for ref := 0; ref < len(refs); ref += 2 {
				annotation.Channels = append(annotation.Channels, ChannelReference{
					GroupNumber: int(refs[ref]), ChannelNumber: int(refs[ref+1]),
				})
			}
		}
		if positions, ok, err := optionalUintValues(item, tagReferencedSamplePositions); err != nil {
			return nil, fmt.Errorf("annotation %d sample positions: %w", i+1, err)
		} else if ok {
			annotation.ReferencedSamplePositions = positions
		}
		if offsets, ok := item.GetStrings(tagReferencedTimeOffsets); ok {
			for _, encoded := range offsets {
				seconds, parseErr := strconv.ParseFloat(encoded, 64)
				if parseErr != nil || !isFinite(seconds) {
					return nil, fmt.Errorf("annotation %d invalid time offset %q", i+1, encoded)
				}
				annotation.ReferencedTimeOffsets = append(annotation.ReferencedTimeOffsets, durationFromFloat(seconds, time.Second))
			}
		}
		annotation.ReferencedDateTimes, _ = item.GetStrings(tagReferencedDateTime)
		annotation.ConceptName, _ = firstCode(item, tagConceptNameCodeSequence)
		annotation.ConceptValue, _ = firstCode(item, tagConceptCodeSequence)
		annotation.NumericUnits, _ = firstCode(item, tagMeasurementUnitsCodeSequence)
		if numericValues, ok := item.GetStrings(tagNumericValue); ok {
			for _, encoded := range numericValues {
				value, parseErr := strconv.ParseFloat(encoded, 64)
				if parseErr != nil || !isFinite(value) {
					return nil, fmt.Errorf("annotation %d invalid numeric value %q", i+1, encoded)
				}
				annotation.NumericValues = append(annotation.NumericValues, value)
			}
		}
		if err := validateAnnotation(i, &annotation, groups); err != nil {
			return nil, err
		}
		out = append(out, annotation)
	}
	return out, nil
}

func validateAnnotation(index int, annotation *Annotation, groups []*group) error {
	if len(annotation.Channels) == 0 {
		return fmt.Errorf("annotation %d has no Referenced Waveform Channels", index+1)
	}
	referencedGroups := make(map[int]struct{})
	for _, reference := range annotation.Channels {
		if reference.GroupNumber < 1 || reference.GroupNumber > len(groups) {
			return fmt.Errorf(
				"annotation %d references multiplex group %d, valid range is [1,%d]",
				index+1,
				reference.GroupNumber,
				len(groups),
			)
		}
		channelCount := len(groups[reference.GroupNumber-1].info.Channels)
		if reference.ChannelNumber < 0 || reference.ChannelNumber > channelCount {
			return fmt.Errorf(
				"annotation %d references channel %d in group %d, valid range is [0,%d]",
				index+1,
				reference.ChannelNumber,
				reference.GroupNumber,
				channelCount,
			)
		}
		referencedGroups[reference.GroupNumber] = struct{}{}
	}

	positionCount := len(annotation.ReferencedSamplePositions)
	offsetCount := len(annotation.ReferencedTimeOffsets)
	dateTimeCount := len(annotation.ReferencedDateTimes)
	coordinateKinds := boolInt(positionCount > 0) + boolInt(offsetCount > 0) + boolInt(dateTimeCount > 0)
	if annotation.TemporalRangeType == "" {
		if coordinateKinds != 0 {
			return fmt.Errorf("annotation %d has temporal coordinates without Temporal Range Type", index+1)
		}
		return nil
	}
	if coordinateKinds != 1 {
		return fmt.Errorf(
			"annotation %d Temporal Range Type requires exactly one temporal coordinate kind",
			index+1,
		)
	}
	count := positionCount + offsetCount + dateTimeCount
	validCount := false
	switch annotation.TemporalRangeType {
	case "POINT", "BEGIN", "END":
		validCount = count == 1
	case "SEGMENT":
		validCount = count == 2
	case "MULTIPOINT":
		validCount = count >= 2
	case "MULTISEGMENT":
		validCount = count >= 4 && count%2 == 0
	default:
		return fmt.Errorf(
			"annotation %d has unsupported Temporal Range Type %q",
			index+1,
			annotation.TemporalRangeType,
		)
	}
	if !validCount {
		return fmt.Errorf(
			"annotation %d Temporal Range Type %s has invalid coordinate cardinality %d",
			index+1,
			annotation.TemporalRangeType,
			count,
		)
	}
	if positionCount > 0 {
		if len(referencedGroups) != 1 {
			return fmt.Errorf(
				"annotation %d sample positions reference more than one multiplex group",
				index+1,
			)
		}
		groupNumber := annotation.Channels[0].GroupNumber
		sampleCount := uint64(groups[groupNumber-1].info.SampleCount)
		for _, position := range annotation.ReferencedSamplePositions {
			if position < 1 || position > sampleCount {
				return fmt.Errorf(
					"annotation %d sample position %d is outside [1,%d]",
					index+1,
					position,
					sampleCount,
				)
			}
		}
	}
	for _, offset := range annotation.ReferencedTimeOffsets {
		if offset < 0 {
			return fmt.Errorf("annotation %d has negative referenced time offset", index+1)
		}
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firstCode(obj *object.Object, tag core.Tag) (CodedConcept, bool) {
	items, ok := obj.GetSequence(tag)
	if !ok || len(items) == 0 {
		return CodedConcept{}, false
	}
	item := items[0]
	return CodedConcept{
		Value:   optionalString(item, tagCodeValue),
		Scheme:  optionalString(item, tagCodingSchemeDesignator),
		Version: optionalString(item, tagCodingSchemeVersion),
		Meaning: optionalString(item, tagCodeMeaning),
	}, true
}

func singleCode(obj *object.Object, tag core.Tag) (CodedConcept, bool, error) {
	items, ok := obj.GetSequence(tag)
	if !ok {
		return CodedConcept{}, false, nil
	}
	if len(items) != 1 {
		return CodedConcept{}, false, fmt.Errorf("%s must contain exactly one coded item", tag)
	}
	item := items[0]
	return CodedConcept{
		Value:   optionalString(item, tagCodeValue),
		Scheme:  optionalString(item, tagCodingSchemeDesignator),
		Version: optionalString(item, tagCodingSchemeVersion),
		Meaning: optionalString(item, tagCodeMeaning),
	}, true, nil
}

func requiredPositiveInt(obj *object.Object, tag core.Tag, name string) (int, error) {
	value, err := requiredPositiveInt64(obj, tag, name)
	if err != nil {
		return 0, err
	}
	if value > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("%s exceeds int", name)
	}
	return int(value), nil
}

func requiredPositiveInt64(obj *object.Object, tag core.Tag, name string) (int64, error) {
	values, ok, err := optionalUintValues(obj, tag)
	if err != nil {
		return 0, err
	}
	if !ok || len(values) != 1 || values[0] == 0 || values[0] > math.MaxInt64 {
		return 0, fmt.Errorf("%s must contain one positive integer", name)
	}
	return int64(values[0]), nil
}

func requiredPositiveFloat(obj *object.Object, tag core.Tag, name string) (float64, error) {
	value, ok, err := lookupOptionalFloat(obj, tag)
	if err != nil || !ok || value <= 0 {
		if err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("%s must contain one positive finite number", name)
	}
	return value, nil
}

func optionalInt(obj *object.Object, tag core.Tag) (int, error) {
	element, ok := obj.Get(tag)
	if !ok {
		return 0, nil
	}
	if element.VR() == core.VRIS {
		value, err := obj.GetInt(tag)
		return int(value), err
	}
	values, _, err := optionalUintValues(obj, tag)
	if err != nil || len(values) == 0 {
		return 0, err
	}
	return int(values[0]), nil
}

func optionalString(obj *object.Object, tag core.Tag) string {
	value, _ := obj.GetString(tag)
	return value
}

func optionalFloat(obj *object.Object, tag core.Tag) float64 {
	value, _, _ := lookupOptionalFloat(obj, tag)
	return value
}

func lookupOptionalFloat(obj *object.Object, tag core.Tag) (float64, bool, error) {
	element, ok := obj.Get(tag)
	if !ok {
		return 0, false, nil
	}
	var value float64
	switch values := element.Value.(type) {
	case core.Float32Value:
		if len(values) != 1 {
			return 0, false, fmt.Errorf("%s must contain one value", tag)
		}
		value = float64(values[0])
	case core.Float64Value:
		if len(values) != 1 {
			return 0, false, fmt.Errorf("%s must contain one value", tag)
		}
		value = values[0]
	default:
		encoded, err := obj.LookupString(tag)
		if err != nil {
			return 0, false, err
		}
		value, err = strconv.ParseFloat(encoded, 64)
		if err != nil {
			return 0, false, fmt.Errorf("parse %s value %q: %w", tag, encoded, err)
		}
	}
	if !isFinite(value) {
		return 0, false, fmt.Errorf("%s must be finite", tag)
	}
	return value, true, nil
}

func optionalUintValues(obj *object.Object, tag core.Tag) ([]uint64, bool, error) {
	element, ok := obj.Get(tag)
	if !ok {
		return nil, false, nil
	}
	switch values := element.Value.(type) {
	case core.Uint16Value:
		out := make([]uint64, len(values))
		for i, value := range values {
			out[i] = uint64(value)
		}
		return out, true, nil
	case core.Uint32Value:
		out := make([]uint64, len(values))
		for i, value := range values {
			out[i] = uint64(value)
		}
		return out, true, nil
	case core.Uint64Value:
		return append([]uint64(nil), values...), true, nil
	case core.RawValue:
		width := 0
		switch element.VR() {
		case core.VRUS:
			width = 2
		case core.VRUL:
			width = 4
		case core.VRUV:
			width = 8
		default:
			return nil, false, fmt.Errorf("%s has unsupported integer VR %s", tag, element.VR())
		}
		raw := values.Bytes()
		if len(raw)%width != 0 {
			return nil, false, fmt.Errorf("%s byte length %d is not divisible by %d", tag, len(raw), width)
		}
		order := obj.ValueByteOrder()
		out := make([]uint64, len(raw)/width)
		for i := range out {
			offset := i * width
			switch width {
			case 2:
				out[i] = uint64(order.Uint16(raw[offset:]))
			case 4:
				out[i] = uint64(order.Uint32(raw[offset:]))
			case 8:
				out[i] = order.Uint64(raw[offset:])
			}
		}
		return out, true, nil
	default:
		return nil, false, fmt.Errorf("%s has unsupported value type %T", tag, element.Value)
	}
}

type sampleDecoder struct {
	order  binary.ByteOrder
	width  int
	bits   int
	signed bool
}

func newSampleDecoder(order binary.ByteOrder, bits int, interpretation string) (sampleDecoder, error) {
	if order == nil {
		order = binary.LittleEndian
	}
	wantBits, signed := 0, false
	switch interpretation {
	case "SB":
		wantBits, signed = 8, true
	case "UB":
		wantBits = 8
	case "SS":
		wantBits, signed = 16, true
	case "US":
		wantBits = 16
	case "SL":
		wantBits, signed = 32, true
	case "UL":
		wantBits = 32
	case "SV":
		wantBits, signed = 64, true
	case "UV":
		wantBits = 64
	case "MB", "AB":
		return sampleDecoder{}, fmt.Errorf("%s audio sample interpretation is raw fallback only", interpretation)
	default:
		return sampleDecoder{}, fmt.Errorf("sample interpretation %q is not a supported integer encoding", interpretation)
	}
	if bits != wantBits {
		return sampleDecoder{}, fmt.Errorf("%s requires %d bits allocated, got %d", interpretation, wantBits, bits)
	}
	return sampleDecoder{order: order, width: bits / 8, bits: bits, signed: signed}, nil
}

func (d sampleDecoder) read(source SampleSource, offset int64, bitsStored int) (float64, uint64, error) {
	var buf [8]byte
	if _, err := source.ReadAt(buf[:d.width], offset); err != nil {
		return 0, 0, fmt.Errorf("read sample at byte %d: %w", offset, err)
	}
	return d.decodeBytes(buf[:d.width], bitsStored)
}

func (d sampleDecoder) decodeBytes(raw []byte, bitsStored int) (float64, uint64, error) {
	if bitsStored <= 0 || bitsStored > d.bits {
		return 0, 0, fmt.Errorf("invalid bits stored %d for %d-bit sample", bitsStored, d.bits)
	}
	var bits uint64
	switch d.width {
	case 1:
		bits = uint64(raw[0])
	case 2:
		bits = uint64(d.order.Uint16(raw))
	case 4:
		bits = uint64(d.order.Uint32(raw))
	case 8:
		bits = d.order.Uint64(raw)
	default:
		return 0, 0, fmt.Errorf("unsupported sample width %d", d.width)
	}
	if bitsStored < 64 {
		mask := uint64(1)<<bitsStored - 1
		bits &= mask
		if d.signed && bits&(uint64(1)<<(bitsStored-1)) != 0 {
			bits |= ^mask
		}
	}
	if d.signed {
		return float64(int64(bits)), bits, nil
	}
	return float64(bits), bits, nil
}

type byteSource struct {
	mu     sync.RWMutex
	reader *bytes.Reader
	size   int64
	closed bool
}

func rawElementSource(_ int, element core.Element) (SampleSource, error) {
	raw, ok := element.RawBytes()
	if !ok {
		return nil, fmt.Errorf("Waveform Data is deferred; configure Options.SourceFactory")
	}
	return &byteSource{reader: bytes.NewReader(raw), size: int64(len(raw))}, nil
}

func (s *byteSource) ReadAt(p []byte, off int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0, ErrClosed
	}
	return s.reader.ReadAt(p, off)
}

func (s *byteSource) Size() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.size
}

func (s *byteSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func rawWaveformByteCount(dataset *object.Object) int64 {
	items, _ := dataset.GetSequence(tagWaveformSequence)
	var total int64
	for _, item := range items {
		element, ok := item.Get(tagWaveformData)
		if !ok {
			continue
		}
		if raw, ok := element.RawBytes(); ok {
			total += int64(len(raw))
		} else if element.Header.HasLength() && element.EncodedLength() != core.UndefinedLength {
			total += int64(element.EncodedLength())
		}
	}
	return total
}

func expectedPadding(size int64) int64 {
	if size%2 == 1 {
		return 1
	}
	return 0
}

func multiplySize(values ...int64) (int64, bool) {
	result := int64(1)
	for _, value := range values {
		if value < 0 || value != 0 && result > math.MaxInt64/value {
			return 0, true
		}
		result *= value
	}
	return result, false
}

func durationFromFloat(value float64, unit time.Duration) time.Duration {
	if !isFinite(value) {
		return 0
	}
	scaled := value * float64(unit)
	if scaled > math.MaxInt64 {
		return time.Duration(math.MaxInt64)
	}
	if scaled < math.MinInt64 {
		return time.Duration(math.MinInt64)
	}
	return time.Duration(math.Round(scaled))
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
