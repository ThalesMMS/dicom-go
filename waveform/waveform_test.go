package waveform

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestIsStorageSOPClassProfiles(t *testing.T) {
	supported := []string{
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
	for _, uid := range supported {
		if !IsStorageSOPClass(uid) {
			t.Errorf("IsStorageSOPClass(%q) = false", uid)
		}
	}
	for _, uid := range []string{uidBasicVoiceAudio, uidGeneralAudio, uidUltrasoundWaveform, "1.2.3"} {
		if IsStorageSOPClass(uid) {
			t.Errorf("IsStorageSOPClass(%q) = true", uid)
		}
	}
	if !IsWaveformSOPClass(uidGeneralAudio) || IsWaveformSOPClass("1.2.3") {
		t.Fatal("IsWaveformSOPClass did not distinguish known raw fallback")
	}
	uids := SupportedStorageSOPClassUIDs()
	if len(uids) != len(supported) {
		t.Fatalf("SupportedStorageSOPClassUIDs length = %d, want %d", len(uids), len(supported))
	}
	uids[0] = "mutated"
	if SupportedStorageSOPClassUIDs()[0] == "mutated" {
		t.Fatal("SupportedStorageSOPClassUIDs returned mutable package backing")
	}
}

func TestOpenParsesMultiplexGroupsCalibrationTimingUnitsAndAnnotations(t *testing.T) {
	sensitivity := 0.5
	correction := 2.0
	baseline := 3.0
	timeSkew := 0.001
	channelOffset := 0.002
	groupOne := testGroup{
		label:          "ECG",
		uid:            "1.2.3.1",
		frequency:      1000,
		bits:           16,
		interpretation: "SS",
		samples:        3,
		channels: []testChannel{
			{
				number:        7,
				label:         "I",
				source:        CodedConcept{Value: "2:1", Scheme: "MDC", Meaning: "Lead I"},
				sensitivity:   &sensitivity,
				correction:    &correction,
				baseline:      &baseline,
				units:         &CodedConcept{Value: "uV", Scheme: "UCUM", Meaning: "microvolt"},
				timeSkew:      &timeSkew,
				channelOffset: &channelOffset,
				bitsStored:    16,
			},
			{number: 8, label: "II", bitsStored: 16},
		},
		raw: encodeSigned(binary.LittleEndian, 16,
			10, 100,
			20, 200,
			-30, 300,
		),
	}
	groupTwo := testGroup{
		label:          "Pressure",
		uid:            "1.2.3.2",
		timeOffsetMS:   250,
		frequency:      100,
		bits:           8,
		interpretation: "UB",
		samples:        2,
		channels:       []testChannel{{number: 1, label: "P", bitsStored: 8}},
		raw:            []byte{4, 5},
	}
	annotations := sequenceElement(tagWaveformAnnotationSequence, dataSet(
		stringElement(tagTemporalRangeType, core.VRCS, "POINT"),
		uint16Element(tagReferencedWaveformChannels, 1, 1),
		uint32Element(tagReferencedSamplePositions, 2),
		uint16Element(tagAnnotationGroupNumber, 9),
		stringElement(tagUnformattedTextValue, core.VRUT, "recorded marker"),
		codeSequence(tagConceptNameCodeSequence, CodedConcept{Value: "R", Scheme: "99TEST", Meaning: "Marker"}),
		stringElement(tagNumericValue, core.VRDS, "1.25"),
		codeSequence(tagMeasurementUnitsCodeSequence, CodedConcept{Value: "s", Scheme: "UCUM", Meaning: "second"}),
	))
	file := testFile(uidTwelveLeadECG, binary.LittleEndian, []core.Element{annotations}, groupOne, groupTwo)

	recording, err := Open(file, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recording.Close() })

	groups := recording.Groups()
	if len(groups) != 2 {
		t.Fatalf("Groups length = %d, want 2", len(groups))
	}
	if groups[0].Label != "ECG" || groups[1].Label != "Pressure" {
		t.Fatalf("unexpected groups: %#v", groups)
	}
	if groups[1].TimeOffset != 250*time.Millisecond {
		t.Fatalf("group offset = %v, want 250ms", groups[1].TimeOffset)
	}
	first := groups[0].Channels[0]
	if first.Number != 7 || first.Source.Meaning != "Lead I" {
		t.Fatalf("channel metadata = %#v", first)
	}
	if first.Calibration.Status != CalibrationComplete || first.Calibration.Units.Value != "uV" {
		t.Fatalf("calibration = %#v", first.Calibration)
	}
	if groups[0].Channels[1].Calibration.Status != CalibrationMissingSensitivity {
		t.Fatalf("missing calibration status = %v", groups[0].Channels[1].Calibration.Status)
	}

	// Query the first sample at its encoded skew + offset.
	sample, err := recording.ValueAt(context.Background(), 0, 0, 3*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SampleIndex != 0 || sample.Raw != 10 || sample.Value != 13 || !sample.Calibrated {
		t.Fatalf("calibrated sample = %#v, want raw=10 value=13", sample)
	}
	rawSample, err := recording.ValueAt(context.Background(), 0, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rawSample.Raw != 100 || rawSample.Value != 100 || rawSample.Calibrated {
		t.Fatalf("raw sample = %#v", rawSample)
	}

	parsedAnnotations := recording.Annotations()
	if len(parsedAnnotations) != 1 {
		t.Fatalf("annotations length = %d, want 1", len(parsedAnnotations))
	}
	annotation := parsedAnnotations[0]
	if annotation.AnnotationGroupNumber != 9 || annotation.Text != "recorded marker" ||
		annotation.ConceptName.Meaning != "Marker" || len(annotation.NumericValues) != 1 ||
		annotation.NumericValues[0] != 1.25 || annotation.NumericUnits.Value != "s" {
		t.Fatalf("annotation = %#v", annotation)
	}
	if len(annotation.Channels) != 1 || annotation.Channels[0] != (ChannelReference{GroupNumber: 1, ChannelNumber: 1}) {
		t.Fatalf("annotation channels = %#v", annotation.Channels)
	}
	if len(annotation.ReferencedSamplePositions) != 1 || annotation.ReferencedSamplePositions[0] != 2 {
		t.Fatalf("sample positions = %#v", annotation.ReferencedSamplePositions)
	}

	// Public metadata is not an alias into the recording.
	groups[0].Channels[0].Status = append(groups[0].Channels[0].Status, "mutated")
	parsedAnnotations[0].Text = "mutated"
	if len(recording.Groups()[0].Channels[0].Status) != 0 || recording.Annotations()[0].Text != "recorded marker" {
		t.Fatal("metadata accessors leaked mutable backing")
	}
}

func TestIntegerDecoderEncodingsAndByteOrder(t *testing.T) {
	tests := []struct {
		name           string
		interpretation string
		bits           int
		order          binary.ByteOrder
		encoded        uint64
		want           float64
	}{
		{name: "signed 8", interpretation: "SB", bits: 8, order: binary.LittleEndian, encoded: uint64(uint8(0xfe)), want: -2},
		{name: "unsigned 8", interpretation: "UB", bits: 8, order: binary.LittleEndian, encoded: 254, want: 254},
		{name: "signed 16 big endian", interpretation: "SS", bits: 16, order: binary.BigEndian, encoded: uint64(uint16(0xfed4)), want: -300},
		{name: "unsigned 16", interpretation: "US", bits: 16, order: binary.LittleEndian, encoded: 65000, want: 65000},
		{name: "signed 32", interpretation: "SL", bits: 32, order: binary.LittleEndian, encoded: uint64(uint32(0xfffeee90)), want: -70000},
		{name: "unsigned 32", interpretation: "UL", bits: 32, order: binary.BigEndian, encoded: 4_000_000_000, want: 4_000_000_000},
		{name: "signed 64", interpretation: "SV", bits: 64, order: binary.LittleEndian, encoded: ^uint64(4_999_999_999), want: -5_000_000_000},
		{name: "unsigned 64", interpretation: "UV", bits: 64, order: binary.BigEndian, encoded: 9_000_000_000, want: 9_000_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoder, err := newSampleDecoder(tt.order, tt.bits, tt.interpretation)
			if err != nil {
				t.Fatal(err)
			}
			raw, _, err := decoder.decodeBytes(
				encodeUnsigned(tt.order, tt.bits, tt.encoded),
				tt.bits,
			)
			if err != nil {
				t.Fatal(err)
			}
			if raw != tt.want {
				t.Fatalf("decoded = %v, want %v", raw, tt.want)
			}
		})
	}
}

func TestBitsStoredRightJustifiedSignExtensionAndPadding(t *testing.T) {
	padding := uint64(0x0800)
	group := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        3,
		channels:       []testChannel{{bitsStored: 12}},
		raw: encodeUnsigned(binary.LittleEndian, 16,
			0x07ff,
			0x0800,
			0x0fff,
		),
		padding: &padding,
	}
	recording, err := Open(testFile(uidGeneralECG, binary.LittleEndian, nil, group), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	first, err := recording.ValueAt(context.Background(), 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Raw != 2047 || !first.Valid {
		t.Fatalf("first sample = %#v", first)
	}
	paddingSample, err := recording.ValueAt(context.Background(), 0, 0, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if paddingSample.Raw != -2048 || paddingSample.Valid {
		t.Fatalf("padding sample = %#v", paddingSample)
	}
	last, err := recording.ValueAt(context.Background(), 0, 0, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if last.Raw != -1 || !last.Valid {
		t.Fatalf("last sample = %#v", last)
	}
}

func TestMissingCalibrationParametersRemainRaw(t *testing.T) {
	sensitivity := 0.5
	units := CodedConcept{Value: "mV", Scheme: "UCUM", Meaning: "millivolt"}
	group := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        1,
		channels: []testChannel{{
			bitsStored:  16,
			sensitivity: &sensitivity,
			units:       &units,
		}},
		raw: encodeSigned(binary.LittleEndian, 16, 20),
	}
	recording, err := Open(testFile(uidGeneralECG, binary.LittleEndian, nil, group), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	if got := recording.Groups()[0].Channels[0].Calibration.Status; got != CalibrationMissingParameters {
		t.Fatalf("calibration status = %v, want %v", got, CalibrationMissingParameters)
	}
	sample, err := recording.ValueAt(context.Background(), 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Calibrated || sample.Value != sample.Raw {
		t.Fatalf("incomplete calibration was applied: %#v", sample)
	}
}

func TestEnvelopePreservesSpikesAndBoundsIndexAndViewport(t *testing.T) {
	const (
		sampleCount = 100_000
		spikeHigh   = 43_210
		spikeLow    = 76_543
		maxEntries  = 128
		width       = 31
	)
	values := make([]int64, sampleCount)
	values[spikeHigh] = 30_000
	values[spikeLow] = -30_000
	source := &countingSource{reader: bytes.NewReader(encodeSigned(binary.LittleEndian, 16, values...))}
	group := testGroup{
		frequency:      1000,
		bits:           16,
		interpretation: "SS",
		samples:        sampleCount,
		channels:       []testChannel{{bitsStored: 16}},
		raw:            make([]byte, sampleCount*2),
	}
	recording, err := Open(
		testFile(uidAmbulatoryECG, binary.LittleEndian, nil, group),
		Options{
			MaxIndexEntries: maxEntries,
			SourceFactory: func(_ int, _ core.Element) (SampleSource, error) {
				return source, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	if err := recording.BuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	readAfterBuild := source.reads.Load()
	envelopes, err := recording.Envelope(
		context.Background(),
		0,
		nil,
		0,
		100*time.Second,
		width,
	)
	if err != nil {
		t.Fatal(err)
	}
	if source.reads.Load() != readAfterBuild {
		t.Fatal("Envelope read sample data after index construction")
	}
	if len(envelopes) != 1 || len(envelopes[0].Buckets) > width {
		t.Fatalf("envelope shape = channels %d, buckets %d", len(envelopes), len(envelopes[0].Buckets))
	}
	var foundHigh, foundLow bool
	for _, bucket := range envelopes[0].Buckets {
		foundHigh = foundHigh || bucket.Max.SampleIndex == spikeHigh && bucket.Max.Raw == 30_000
		foundLow = foundLow || bucket.Min.SampleIndex == spikeLow && bucket.Min.Raw == -30_000
	}
	if !foundHigh || !foundLow {
		t.Fatalf("spikes not preserved: high=%v low=%v", foundHigh, foundLow)
	}
	entries := 0
	for _, level := range recording.groups[0].index.levels {
		for _, channel := range level.channels {
			entries += len(channel)
		}
	}
	if entries > maxEntries {
		t.Fatalf("index entries = %d, cap %d", entries, maxEntries)
	}
}

func TestEnvelopeSelectionOrderingCancellationAndRetry(t *testing.T) {
	group := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        4,
		channels: []testChannel{
			{number: 1, label: "A", bitsStored: 16},
			{number: 2, label: "B", bitsStored: 16},
			{number: 3, label: "C", bitsStored: 16},
		},
		raw: encodeSigned(binary.LittleEndian, 16,
			1, 2, 3,
			4, 5, 6,
			7, 8, 9,
			10, 11, 12,
		),
	}
	recording, err := Open(testFile(uidHemodynamic, binary.LittleEndian, nil, group), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recording.BuildIndex(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildIndex canceled error = %v", err)
	}
	if err := recording.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex retry: %v", err)
	}
	envelopes, err := recording.Envelope(context.Background(), 0, []int{2, 0, 2}, 0, 40*time.Millisecond, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 2 || envelopes[0].Channel.Label != "C" || envelopes[1].Channel.Label != "A" {
		t.Fatalf("selection order = %#v", envelopes)
	}
	if _, err := recording.Envelope(canceled, 0, nil, 0, 40*time.Millisecond, 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("Envelope canceled error = %v", err)
	}
}

func TestEnvelopeFineZoomExcludesExtremaOutsideViewport(t *testing.T) {
	values := make([]int64, 100)
	values[5] = -30_000
	values[52] = 123
	values[95] = 30_000
	recording, err := Open(
		testFile(uidGeneralECG, binary.LittleEndian, nil, testGroup{
			frequency:      1_000,
			bits:           16,
			interpretation: "SS",
			samples:        int64(len(values)),
			channels:       []testChannel{{bitsStored: 16}},
			raw:            encodeSigned(binary.LittleEndian, 16, values...),
		}),
		Options{MaxIndexEntries: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	envelopes, err := recording.Envelope(
		context.Background(),
		0,
		nil,
		50*time.Millisecond,
		5*time.Millisecond,
		5,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 1 || len(envelopes[0].Buckets) != 1 {
		t.Fatalf("fine envelope shape = %#v", envelopes)
	}
	bucket := envelopes[0].Buckets[0]
	if bucket.StartSample != 50 || bucket.EndSample != 55 {
		t.Fatalf("bucket range = [%d,%d), want [50,55)", bucket.StartSample, bucket.EndSample)
	}
	if bucket.Min.SampleIndex < 50 || bucket.Max.SampleIndex >= 55 {
		t.Fatalf("bucket contains extrema outside viewport: %#v", bucket)
	}
	if bucket.Max.SampleIndex != 52 || bucket.Max.Raw != 123 {
		t.Fatalf("in-viewport spike was not preserved: %#v", bucket.Max)
	}
}

func TestEnvelopeAppliesChannelSkewAndOffset(t *testing.T) {
	timeSkew := 0.01
	channelOffset := 0.01
	group := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        5,
		channels: []testChannel{
			{label: "shifted", bitsStored: 16, timeSkew: &timeSkew, channelOffset: &channelOffset},
			{label: "unshifted", bitsStored: 16},
		},
		raw: encodeSigned(binary.LittleEndian, 16,
			10, 100,
			11, 101,
			12, 102,
			13, 103,
			14, 104,
		),
	}
	recording, err := Open(testFile(uidGeneralECG, binary.LittleEndian, nil, group), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	envelopes, err := recording.Envelope(
		context.Background(),
		0,
		nil,
		20*time.Millisecond,
		20*time.Millisecond,
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 2 {
		t.Fatalf("channels = %d, want 2", len(envelopes))
	}
	assertEnvelopeSamplesInWindow(t, envelopes[0], 0, 2, 20*time.Millisecond, 40*time.Millisecond)
	assertEnvelopeSamplesInWindow(t, envelopes[1], 2, 4, 20*time.Millisecond, 40*time.Millisecond)
}

func TestEnvelopeAllowsNegativePresentationTimeFromChannelOffset(t *testing.T) {
	channelOffset := -0.02
	group := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        3,
		channels: []testChannel{{
			bitsStored:    16,
			channelOffset: &channelOffset,
		}},
		raw: encodeSigned(binary.LittleEndian, 16, 10, 20, 30),
	}
	recording, err := Open(testFile(uidGeneralECG, binary.LittleEndian, nil, group), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	envelopes, err := recording.Envelope(
		context.Background(),
		0,
		nil,
		-20*time.Millisecond,
		20*time.Millisecond,
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 1 || len(envelopes[0].Buckets) == 0 {
		t.Fatalf("negative-time envelope = %#v, want channel samples", envelopes)
	}
	assertEnvelopeSamplesInWindow(t, envelopes[0], 0, 2, -20*time.Millisecond, 0)
}

func TestIndexBudgetIsGlobalAcrossGroups(t *testing.T) {
	groups := []testGroup{
		ssTestGroup(200, 2),
		ssTestGroup(300, 1),
		ssTestGroup(100, 2),
	}
	const maxEntries = 17
	recording, err := Open(
		testFile(uidGeneralECG, binary.LittleEndian, nil, groups...),
		Options{MaxIndexEntries: maxEntries},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()
	if err := recording.BuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	budgetTotal, actualTotal := 0, 0
	for _, group := range recording.groups {
		budgetTotal += group.indexBudget
		for _, level := range group.index.levels {
			for _, channel := range level.channels {
				actualTotal += len(channel)
			}
		}
	}
	if budgetTotal != maxEntries {
		t.Fatalf("assigned budget = %d, want %d", budgetTotal, maxEntries)
	}
	if actualTotal > maxEntries {
		t.Fatalf("actual global entries = %d, cap %d", actualTotal, maxEntries)
	}

	_, err = Open(
		testFile(uidGeneralECG, binary.LittleEndian, nil, groups...),
		Options{MaxIndexEntries: 4},
	)
	if err == nil || !strings.Contains(err.Error(), "one bucket for each channel") {
		t.Fatalf("too-small global budget error = %v", err)
	}
}

func TestGroupAndChannelLimits(t *testing.T) {
	twoGroups := testFile(
		uidGeneralECG,
		binary.LittleEndian,
		nil,
		ssTestGroup(2, 1),
		ssTestGroup(2, 1),
	)
	if _, err := Open(twoGroups, Options{MaxGroups: 1}); err == nil {
		t.Fatal("Open accepted more groups than MaxGroups")
	}
	twoChannels := testFile(uidGeneralECG, binary.LittleEndian, nil, ssTestGroup(2, 2))
	if _, err := Open(twoChannels, Options{MaxChannelsPerGroup: 1}); err == nil {
		t.Fatal("Open accepted more channels than MaxChannelsPerGroup")
	}
}

func TestIndexReadsAlignedBlocksAndHonorsActiveCancellation(t *testing.T) {
	const sampleCount = 100_000
	raw := make([]byte, sampleCount*2)
	tracked := &trackingSource{reader: bytes.NewReader(raw)}
	recording, err := Open(
		testFile(uidGeneralECG, binary.LittleEndian, nil, testGroup{
			frequency:      500,
			bits:           16,
			interpretation: "SS",
			samples:        sampleCount,
			channels:       []testChannel{{bitsStored: 16}},
			raw:            raw,
		}),
		Options{SourceFactory: func(_ int, _ core.Element) (SampleSource, error) { return tracked, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()
	if err := recording.BuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	reads := tracked.snapshot()
	if len(reads) != 4 {
		t.Fatalf("block reads = %d, want 4 for 200000 bytes", len(reads))
	}
	for i, read := range reads {
		if read.length > waveformReadBlockBytes {
			t.Fatalf("read %d length = %d, exceeds %d", i, read.length, waveformReadBlockBytes)
		}
		if read.offset%2 != 0 {
			t.Fatalf("read %d offset = %d, not sample-aligned", i, read.offset)
		}
	}

	cancelContext, cancel := context.WithCancel(context.Background())
	canceling := &cancelingSource{reader: bytes.NewReader(raw), cancel: cancel}
	canceledRecording, err := Open(
		testFile(uidGeneralECG, binary.LittleEndian, nil, testGroup{
			frequency:      500,
			bits:           16,
			interpretation: "SS",
			samples:        sampleCount,
			channels:       []testChannel{{bitsStored: 16}},
			raw:            raw,
		}),
		Options{SourceFactory: func(_ int, _ core.Element) (SampleSource, error) { return canceling, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer canceledRecording.Close()
	if err := canceledRecording.BuildIndex(cancelContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation error = %v", err)
	}
	if got := canceling.reads.Load(); got != 1 {
		t.Fatalf("reads before cancellation = %d, want 1", got)
	}
}

func TestSOPSampleConstraintsAndWaveformVR(t *testing.T) {
	tests := []struct {
		name           string
		uid            string
		interpretation string
		bits           int
		supported      bool
	}{
		{name: "general ECG SS", uid: uidGeneralECG, interpretation: "SS", bits: 16, supported: true},
		{name: "general ECG unsigned fallback", uid: uidGeneralECG, interpretation: "US", bits: 16},
		{name: "ambulatory SB", uid: uidAmbulatoryECG, interpretation: "SB", bits: 8, supported: true},
		{name: "body position UB", uid: uidBodyPosition, interpretation: "UB", bits: 8, supported: true},
		{name: "32-bit ECG SL", uid: uidGeneral32BitECG, interpretation: "SL", bits: 32, supported: true},
		{name: "64-bit raw fallback", uid: uidGeneral32BitECG, interpretation: "SV", bits: 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recording, err := Open(
				testFile(tt.uid, binary.LittleEndian, nil, testGroup{
					frequency:      100,
					bits:           tt.bits,
					interpretation: tt.interpretation,
					samples:        1,
					channels:       []testChannel{{bitsStored: tt.bits}},
					raw:            make([]byte, tt.bits/8),
				}),
				Options{},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer recording.Close()
			if got := recording.Groups()[0].Supported; got != tt.supported {
				t.Fatalf("Supported = %v, want %v", got, tt.supported)
			}
		})
	}

	item := testGroupDataSet(ssTestGroup(1, 1))
	setElementVR(&item, tagWaveformData, core.VROB)
	if _, err := Open(testFileFromItems(uidGeneralECG, item), Options{}); err == nil {
		t.Fatal("Open accepted OB Waveform Data for 16-bit samples")
	}
	item = testGroupDataSet(ssTestGroup(1, 1))
	item.Elements = append(item.Elements, core.NewRawElement(tagWaveformPaddingValue, core.VROB, []byte{0, 0}))
	if _, err := Open(testFileFromItems(uidGeneralECG, item), Options{}); err == nil {
		t.Fatal("Open accepted padding VR different from Waveform Data")
	}
}

func TestCalibrationRequiresCompleteUnitsCode(t *testing.T) {
	sensitivity, correction, baseline := 1.0, 1.0, 0.0
	units := CodedConcept{Value: "mV", Meaning: "millivolt"}
	recording, err := Open(
		testFile(uidGeneralECG, binary.LittleEndian, nil, testGroup{
			frequency:      100,
			bits:           16,
			interpretation: "SS",
			samples:        1,
			channels: []testChannel{{
				bitsStored:  16,
				sensitivity: &sensitivity,
				correction:  &correction,
				baseline:    &baseline,
				units:       &units,
			}},
			raw: []byte{1, 0},
		}),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()
	if got := recording.Groups()[0].Channels[0].Calibration.Status; got != CalibrationMissingUnits {
		t.Fatalf("calibration status = %v, want missing units", got)
	}
}

func TestAnnotationTemporalValidation(t *testing.T) {
	valid := []struct {
		rangeType string
		positions []uint32
	}{
		{rangeType: "POINT", positions: []uint32{1}},
		{rangeType: "BEGIN", positions: []uint32{1}},
		{rangeType: "END", positions: []uint32{2}},
		{rangeType: "SEGMENT", positions: []uint32{1, 2}},
		{rangeType: "MULTIPOINT", positions: []uint32{1, 2}},
		{rangeType: "MULTISEGMENT", positions: []uint32{1, 2, 3, 4}},
	}
	for _, tt := range valid {
		t.Run("valid_"+tt.rangeType, func(t *testing.T) {
			annotation := annotationElement(tt.rangeType, []uint16{1, 1}, tt.positions, nil)
			recording, err := Open(
				testFile(uidGeneralECG, binary.LittleEndian, []core.Element{annotation}, ssTestGroup(4, 1)),
				Options{},
			)
			if err != nil {
				t.Fatal(err)
			}
			recording.Close()
		})
	}

	invalid := []struct {
		name      string
		rangeType string
		refs      []uint16
		positions []uint32
		offsets   []string
	}{
		{name: "point cardinality", rangeType: "POINT", refs: []uint16{1, 1}, positions: []uint32{1, 2}},
		{name: "segment cardinality", rangeType: "SEGMENT", refs: []uint16{1, 1}, positions: []uint32{1}},
		{name: "multipoint cardinality", rangeType: "MULTIPOINT", refs: []uint16{1, 1}, positions: []uint32{1}},
		{name: "multisegment cardinality", rangeType: "MULTISEGMENT", refs: []uint16{1, 1}, positions: []uint32{1, 2}},
		{name: "unknown type", rangeType: "OTHER", refs: []uint16{1, 1}, positions: []uint32{1}},
		{name: "two coordinate kinds", rangeType: "POINT", refs: []uint16{1, 1}, positions: []uint32{1}, offsets: []string{"0.1"}},
		{name: "offset cardinality", rangeType: "POINT", refs: []uint16{1, 1}, offsets: []string{"0.1", "0.2"}},
		{name: "missing refs", rangeType: "POINT", positions: []uint32{1}},
		{name: "bad group", rangeType: "POINT", refs: []uint16{2, 1}, positions: []uint32{1}},
		{name: "bad channel", rangeType: "POINT", refs: []uint16{1, 2}, positions: []uint32{1}},
		{name: "sample zero", rangeType: "POINT", refs: []uint16{1, 1}, positions: []uint32{0}},
		{name: "sample past end", rangeType: "POINT", refs: []uint16{1, 1}, positions: []uint32{5}},
	}
	for _, tt := range invalid {
		t.Run("invalid_"+tt.name, func(t *testing.T) {
			annotation := annotationElement(tt.rangeType, tt.refs, tt.positions, tt.offsets)
			_, err := Open(
				testFile(uidGeneralECG, binary.LittleEndian, []core.Element{annotation}, ssTestGroup(4, 1)),
				Options{},
			)
			if err == nil {
				t.Fatal("Open accepted invalid annotation")
			}
		})
	}

	crossGroup := annotationElement("POINT", []uint16{1, 1, 2, 1}, []uint32{1}, nil)
	if _, err := Open(
		testFile(
			uidGeneralECG,
			binary.LittleEndian,
			[]core.Element{crossGroup},
			ssTestGroup(4, 1),
			ssTestGroup(4, 1),
		),
		Options{},
	); err == nil {
		t.Fatal("Open accepted sample positions spanning multiplex groups")
	}

	validOffsets := annotationElement("SEGMENT", []uint16{1, 1}, nil, []string{"0.01", "0.02"})
	recording, err := Open(
		testFile(uidGeneralECG, binary.LittleEndian, []core.Element{validOffsets}, ssTestGroup(4, 1)),
		Options{},
	)
	if err != nil {
		t.Fatalf("valid time-offset annotation: %v", err)
	}
	recording.Close()
}

func TestConcurrentIndexBuildSharesOneScan(t *testing.T) {
	const sampleCount = 4096
	source := &countingSource{reader: bytes.NewReader(make([]byte, sampleCount*2))}
	group := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        sampleCount,
		channels:       []testChannel{{bitsStored: 16}},
		raw:            make([]byte, sampleCount*2),
	}
	recording, err := Open(
		testFile(uidRoutineScalpEEG, binary.LittleEndian, nil, group),
		Options{SourceFactory: func(_ int, _ core.Element) (SampleSource, error) { return source, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()

	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- recording.BuildIndex(context.Background())
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := source.reads.Load(); got != 1 {
		t.Fatalf("source reads = %d, want one block", got)
	}
}

func TestUnsupportedRawFallbacks(t *testing.T) {
	audioFile := testFile(uidGeneralAudio, binary.LittleEndian, nil, testGroup{
		frequency:      8_000,
		bits:           8,
		interpretation: "MB",
		samples:        2,
		channels:       []testChannel{{bitsStored: 8}},
		raw:            []byte{1, 2},
	})
	_, err := Open(audioFile, Options{})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Fallback.SOPClassUID != uidGeneralAudio ||
		unsupported.Fallback.RawBytes != 2 {
		t.Fatalf("audio error = %#v", err)
	}

	rawGroup := testGroup{
		frequency:      8_000,
		bits:           8,
		interpretation: "MB",
		samples:        2,
		channels:       []testChannel{{bitsStored: 8}},
		raw:            []byte{1, 2},
	}
	recording, err := Open(testFile(uidGeneralECG, binary.LittleEndian, nil, rawGroup), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()
	groups := recording.Groups()
	if groups[0].Supported || groups[0].FallbackReason == "" || groups[0].RawDataBytes != 2 {
		t.Fatalf("raw fallback group = %#v", groups[0])
	}
	_, err = recording.Envelope(context.Background(), 0, nil, 0, time.Millisecond, 1)
	if !errors.As(err, &unsupported) || unsupported.Fallback.Encoding != "MB/8-bit" {
		t.Fatalf("unsupported envelope error = %#v", err)
	}
}

func TestCloseOwnsSourcesAndPreventsReads(t *testing.T) {
	source := &countingSource{reader: bytes.NewReader([]byte{1, 0})}
	recording, err := Open(
		testFile(uidArterialPulse, binary.LittleEndian, nil, testGroup{
			frequency:      1,
			bits:           16,
			interpretation: "SS",
			samples:        1,
			channels:       []testChannel{{bitsStored: 16}},
			raw:            []byte{1, 0},
		}),
		Options{SourceFactory: func(_ int, _ core.Element) (SampleSource, error) { return source, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := recording.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recording.Close(); err != nil {
		t.Fatal(err)
	}
	if got := source.closes.Load(); got != 1 {
		t.Fatalf("source closes = %d, want 1", got)
	}
	if _, err := recording.ValueAt(context.Background(), 0, 0, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("ValueAt after close error = %v", err)
	}
}

func TestCustomSourceFactoryAcceptsDeferredWaveformData(t *testing.T) {
	group := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        2,
		channels:       []testChannel{{bitsStored: 16}},
		raw:            []byte{1, 0, 2, 0},
	}
	item := testGroupDataSet(group)
	for i := range item.Elements {
		if item.Elements[i].Tag() == tagWaveformData {
			item.Elements[i].Value = nil
			item.Elements[i].Header.Length = 4
			item.Elements[i].Header.LengthSet = true
		}
	}
	dataset := object.FromElements([]core.Element{
		stringElement(tagSOPClassUID, core.VRUI, uidGeneralECG),
		sequenceElement(tagWaveformSequence, item),
	}, std.Dictionary)
	source := &countingSource{reader: bytes.NewReader(group.raw)}
	factoryCalled := false
	recording, err := Open(
		&object.File{Dataset: dataset},
		Options{SourceFactory: func(_ int, element core.Element) (SampleSource, error) {
			factoryCalled = true
			if element.Value != nil {
				t.Fatal("deferred Waveform Data unexpectedly materialized")
			}
			return source, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()
	if !factoryCalled {
		t.Fatal("SourceFactory was not called")
	}
	sample, err := recording.ValueAt(context.Background(), 0, 0, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Raw != 2 {
		t.Fatalf("deferred sample raw = %v, want 2", sample.Raw)
	}
}

func TestOpenRejectsMalformedDataAndNonWaveform(t *testing.T) {
	nonWaveform := &object.File{Dataset: object.FromElements([]core.Element{
		stringElement(tagSOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
	}, std.Dictionary)}
	if _, err := Open(nonWaveform, Options{}); !errors.Is(err, ErrNotWaveform) {
		t.Fatalf("non-waveform error = %v", err)
	}
	malformed := testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        2,
		channels:       []testChannel{{bitsStored: 16}},
		raw:            []byte{1, 0},
	}
	if _, err := Open(testFile(uidGeneralECG, binary.LittleEndian, nil, malformed), Options{}); err == nil {
		t.Fatal("Open accepted truncated Waveform Data")
	}
}

type testGroup struct {
	label          string
	uid            string
	timeOffsetMS   float64
	frequency      float64
	bits           int
	interpretation string
	samples        int64
	channels       []testChannel
	raw            []byte
	padding        *uint64
}

type testChannel struct {
	number        int
	label         string
	source        CodedConcept
	sensitivity   *float64
	correction    *float64
	baseline      *float64
	units         *CodedConcept
	timeSkew      *float64
	sampleSkew    *float64
	channelOffset *float64
	bitsStored    int
}

func testFile(uid string, order binary.ByteOrder, extra []core.Element, groups ...testGroup) *object.File {
	items := make([]core.DataSet, len(groups))
	for i := range groups {
		items[i] = testGroupDataSet(groups[i])
	}
	elements := []core.Element{
		stringElement(tagSOPClassUID, core.VRUI, uid),
		sequenceElement(tagWaveformSequence, items...),
	}
	elements = append(elements, extra...)
	dataset := object.FromElements(elements, std.Dictionary)
	dataset.SetValueByteOrder(order)
	return &object.File{Dataset: dataset}
}

func testFileFromItems(uid string, items ...core.DataSet) *object.File {
	dataset := object.FromElements([]core.Element{
		stringElement(tagSOPClassUID, core.VRUI, uid),
		sequenceElement(tagWaveformSequence, items...),
	}, std.Dictionary)
	dataset.SetValueByteOrder(binary.LittleEndian)
	return &object.File{Dataset: dataset}
}

func ssTestGroup(sampleCount int64, channelCount int) testGroup {
	channels := make([]testChannel, channelCount)
	for i := range channels {
		channels[i] = testChannel{number: i + 1, bitsStored: 16}
	}
	return testGroup{
		frequency:      100,
		bits:           16,
		interpretation: "SS",
		samples:        sampleCount,
		channels:       channels,
		raw:            make([]byte, int(sampleCount)*channelCount*2),
	}
}

func setElementVR(dataset *core.DataSet, tag core.Tag, vr core.VR) {
	for i := range dataset.Elements {
		if dataset.Elements[i].Tag() == tag {
			dataset.Elements[i].Header.VR = vr
			return
		}
	}
}

func annotationElement(
	rangeType string,
	references []uint16,
	positions []uint32,
	offsets []string,
) core.Element {
	elements := []core.Element{stringElement(tagTemporalRangeType, core.VRCS, rangeType)}
	if references != nil {
		elements = append(elements, uint16Element(tagReferencedWaveformChannels, references...))
	}
	if positions != nil {
		elements = append(elements, uint32Element(tagReferencedSamplePositions, positions...))
	}
	if offsets != nil {
		elements = append(elements, stringElement(tagReferencedTimeOffsets, core.VRDS, offsets...))
	}
	return sequenceElement(tagWaveformAnnotationSequence, dataSet(elements...))
}

func assertEnvelopeSamplesInWindow(
	t *testing.T,
	envelope ChannelEnvelope,
	wantStart int64,
	wantEnd int64,
	windowStart time.Duration,
	windowEnd time.Duration,
) {
	t.Helper()
	if len(envelope.Buckets) == 0 {
		t.Fatal("envelope has no buckets")
	}
	if envelope.Buckets[0].StartSample != wantStart ||
		envelope.Buckets[len(envelope.Buckets)-1].EndSample != wantEnd {
		t.Fatalf(
			"envelope sample bounds = [%d,%d), want [%d,%d)",
			envelope.Buckets[0].StartSample,
			envelope.Buckets[len(envelope.Buckets)-1].EndSample,
			wantStart,
			wantEnd,
		)
	}
	for _, bucket := range envelope.Buckets {
		if !bucket.Valid {
			continue
		}
		for _, sample := range []Sample{bucket.Min, bucket.Max} {
			if sample.Time < windowStart || sample.Time >= windowEnd {
				t.Fatalf("sample time %v outside [%v,%v): %#v", sample.Time, windowStart, windowEnd, sample)
			}
		}
	}
}

func testGroupDataSet(group testGroup) core.DataSet {
	channelItems := make([]core.DataSet, len(group.channels))
	for i, channel := range group.channels {
		channelItems[i] = testChannelDataSet(channel)
	}
	raw := append([]byte(nil), group.raw...)
	expected := group.samples * int64(len(group.channels)) * int64(group.bits/8)
	if int64(len(raw)) == expected && expected%2 == 1 {
		raw = append(raw, 0)
	}
	elements := []core.Element{
		uint16Element(tagNumberOfWaveformChannels, uint16(len(group.channels))),
		uint32Element(tagNumberOfWaveformSamples, uint32(group.samples)),
		stringElement(tagSamplingFrequency, core.VRDS, formatFloat(group.frequency)),
		uint16Element(tagWaveformBitsAllocated, uint16(group.bits)),
		stringElement(tagWaveformSampleInterpretation, core.VRCS, group.interpretation),
		sequenceElement(tagChannelDefinitionSequence, channelItems...),
		core.NewRawElement(tagWaveformData, core.VROW, raw),
	}
	if group.label != "" {
		elements = append(elements, stringElement(tagMultiplexGroupLabel, core.VRSH, group.label))
	}
	if group.uid != "" {
		elements = append(elements, stringElement(tagMultiplexGroupUID, core.VRUI, group.uid))
	}
	if group.timeOffsetMS != 0 {
		elements = append(elements, stringElement(tagMultiplexGroupTimeOffset, core.VRDS, formatFloat(group.timeOffsetMS)))
	}
	if group.padding != nil {
		elements = append(elements, core.NewRawElement(
			tagWaveformPaddingValue,
			core.VROW,
			encodeUnsigned(binary.LittleEndian, group.bits, *group.padding),
		))
	}
	return dataSet(elements...)
}

func testChannelDataSet(channel testChannel) core.DataSet {
	if channel.bitsStored == 0 {
		channel.bitsStored = 16
	}
	elements := []core.Element{
		stringElement(tagWaveformChannelNumber, core.VRIS, formatInt(channel.number)),
		uint16Element(tagWaveformBitsStored, uint16(channel.bitsStored)),
	}
	if channel.label != "" {
		elements = append(elements, stringElement(tagChannelLabel, core.VRSH, channel.label))
	}
	if channel.source.Value != "" {
		elements = append(elements, codeSequence(tagChannelSourceSequence, channel.source))
	}
	if channel.sensitivity != nil {
		elements = append(elements, stringElement(tagChannelSensitivity, core.VRDS, formatFloat(*channel.sensitivity)))
	}
	if channel.correction != nil {
		elements = append(elements, stringElement(tagChannelSensitivityCorrection, core.VRDS, formatFloat(*channel.correction)))
	}
	if channel.baseline != nil {
		elements = append(elements, stringElement(tagChannelBaseline, core.VRDS, formatFloat(*channel.baseline)))
	}
	if channel.units != nil {
		elements = append(elements, codeSequence(tagChannelSensitivityUnits, *channel.units))
	}
	if channel.timeSkew != nil {
		elements = append(elements, stringElement(tagChannelTimeSkew, core.VRDS, formatFloat(*channel.timeSkew)))
	}
	if channel.sampleSkew != nil {
		elements = append(elements, stringElement(tagChannelSampleSkew, core.VRDS, formatFloat(*channel.sampleSkew)))
	}
	if channel.channelOffset != nil {
		elements = append(elements, stringElement(tagChannelOffset, core.VRDS, formatFloat(*channel.channelOffset)))
	}
	return dataSet(elements...)
}

func codeSequence(tag core.Tag, code CodedConcept) core.Element {
	return sequenceElement(tag, dataSet(
		stringElement(tagCodeValue, core.VRSH, code.Value),
		stringElement(tagCodingSchemeDesignator, core.VRSH, code.Scheme),
		stringElement(tagCodeMeaning, core.VRLO, code.Meaning),
	))
}

func dataSet(elements ...core.Element) core.DataSet {
	return core.DataSet{Elements: elements}
}

func sequenceElement(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{Items: items},
	}
}

func stringElement(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue(values),
	}
}

func uint16Element(tag core.Tag, values ...uint16) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRUS},
		Value:  core.Uint16Value(values),
	}
}

func uint32Element(tag core.Tag, values ...uint32) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRUL},
		Value:  core.Uint32Value(values),
	}
}

func encodeSigned(order binary.ByteOrder, bits int, values ...int64) []byte {
	encoded := make([]uint64, len(values))
	for i, value := range values {
		encoded[i] = uint64(value)
	}
	return encodeUnsigned(order, bits, encoded...)
}

func encodeUnsigned(order binary.ByteOrder, bits int, values ...uint64) []byte {
	width := bits / 8
	out := make([]byte, width*len(values))
	for i, value := range values {
		offset := i * width
		switch width {
		case 1:
			out[offset] = byte(value)
		case 2:
			order.PutUint16(out[offset:], uint16(value))
		case 4:
			order.PutUint32(out[offset:], uint32(value))
		case 8:
			order.PutUint64(out[offset:], value)
		default:
			panic("unsupported test sample width")
		}
	}
	return out
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'g', -1, 64) }

func formatInt(value int) string { return strconv.Itoa(value) }

type countingSource struct {
	reader *bytes.Reader
	reads  atomic.Int64
	closes atomic.Int64
	closed atomic.Bool
}

func (s *countingSource) ReadAt(p []byte, off int64) (int, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	s.reads.Add(1)
	return s.reader.ReadAt(p, off)
}

func (s *countingSource) Size() int64 {
	return s.reader.Size()
}

func (s *countingSource) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		s.closes.Add(1)
	}
	return nil
}

var _ SampleSource = (*countingSource)(nil)

type trackedRead struct {
	offset int64
	length int
}

type trackingSource struct {
	mu     sync.Mutex
	reader *bytes.Reader
	reads  []trackedRead
	closed bool
}

func (s *trackingSource) ReadAt(p []byte, off int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	s.reads = append(s.reads, trackedRead{offset: off, length: len(p)})
	return s.reader.ReadAt(p, off)
}

func (s *trackingSource) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reader.Size()
}

func (s *trackingSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *trackingSource) snapshot() []trackedRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]trackedRead(nil), s.reads...)
}

type cancelingSource struct {
	reader *bytes.Reader
	cancel context.CancelFunc
	reads  atomic.Int64
	closed atomic.Bool
}

func (s *cancelingSource) ReadAt(p []byte, off int64) (int, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	s.reads.Add(1)
	n, err := s.reader.ReadAt(p, off)
	s.cancel()
	return n, err
}

func (s *cancelingSource) Size() int64 { return s.reader.Size() }

func (s *cancelingSource) Close() error {
	s.closed.Store(true)
	return nil
}

var (
	_ SampleSource = (*trackingSource)(nil)
	_ SampleSource = (*cancelingSource)(nil)
)
