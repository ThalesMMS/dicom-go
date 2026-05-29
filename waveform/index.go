package waveform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

type rawPoint struct {
	sample int64
	raw    float64
	bits   uint64
}

type rawBucket struct {
	min   rawPoint
	max   rawPoint
	valid bool
}

type indexLevel struct {
	sampleSpan int64
	channels   [][]rawBucket
}

type multiIndex struct {
	levels []indexLevel
}

const waveformReadBlockBytes = 64 << 10

// Envelope returns at most width aligned min/max buckets for every selected
// channel. start is relative to the multiplex-group start. Nil channelIndices
// selects every channel in encoded order; otherwise the supplied order is
// preserved. Boundary buckets are refined to the requested interval and expose
// their exact inclusive/exclusive StartSample/EndSample bounds.
func (r *Recording) Envelope(
	ctx context.Context,
	groupIndex int,
	channelIndices []int,
	start time.Duration,
	duration time.Duration,
	width int,
) ([]ChannelEnvelope, error) {
	if r == nil {
		return nil, fmt.Errorf("waveform: nil recording")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrClosed
	}
	if err := operationErr(ctx, r.closeCh); err != nil {
		return nil, err
	}
	if groupIndex < 0 || groupIndex >= len(r.groups) {
		return nil, fmt.Errorf("waveform: group index %d out of range", groupIndex)
	}
	if width <= 0 || width > r.options.maxWidth {
		return nil, fmt.Errorf("waveform: envelope width %d must be in [1,%d]", width, r.options.maxWidth)
	}
	if duration <= 0 {
		return nil, fmt.Errorf("waveform: duration must be positive")
	}
	group := r.groups[groupIndex]
	if !group.info.Supported {
		return nil, group.unsupportedError(r.sopClassUID)
	}
	channels, err := selectedChannels(channelIndices, len(group.info.Channels))
	if err != nil {
		return nil, err
	}
	if err := group.ensureIndex(ctx, r.closeCh); err != nil {
		return nil, err
	}
	result := make([]ChannelEnvelope, 0, len(channels))
	for _, channelIndex := range channels {
		if err := operationErr(ctx, r.closeCh); err != nil {
			return nil, err
		}
		envelope := ChannelEnvelope{
			Channel: cloneGroupInfo(group.info).Channels[channelIndex],
		}
		startSample, endSample, ok, rangeErr := group.channelSampleRange(channelIndex, start, duration)
		if rangeErr != nil {
			return nil, rangeErr
		}
		if ok {
			envelope.Buckets, err = group.envelopeBuckets(
				ctx,
				r.closeCh,
				channelIndex,
				startSample,
				endSample,
				width,
			)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, envelope)
	}
	return result, nil
}

// ValueAt returns the nearest channel sample at the requested time. at is
// relative to the multiplex-group start; encoded channel skew and offset are
// applied when locating the sample.
func (r *Recording) ValueAt(
	ctx context.Context,
	groupIndex int,
	channelIndex int,
	at time.Duration,
) (Sample, error) {
	if r == nil {
		return Sample{}, fmt.Errorf("waveform: nil recording")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return Sample{}, ErrClosed
	}
	if err := operationErr(ctx, r.closeCh); err != nil {
		return Sample{}, err
	}
	if groupIndex < 0 || groupIndex >= len(r.groups) {
		return Sample{}, fmt.Errorf("waveform: group index %d out of range", groupIndex)
	}
	group := r.groups[groupIndex]
	if !group.info.Supported {
		return Sample{}, group.unsupportedError(r.sopClassUID)
	}
	if channelIndex < 0 || channelIndex >= len(group.info.Channels) {
		return Sample{}, fmt.Errorf("waveform: channel index %d out of range", channelIndex)
	}
	channel := group.info.Channels[channelIndex]
	adjusted := at - channel.TimeSkew - channel.ChannelOffset
	if adjusted < 0 {
		return Sample{}, fmt.Errorf("waveform: cursor time precedes channel start")
	}
	sampleIndex := int64(math.Round(float64(adjusted) / float64(time.Second) * group.info.SamplingFrequencyHz))
	if sampleIndex < 0 || sampleIndex >= group.info.SampleCount {
		return Sample{}, fmt.Errorf("waveform: cursor time is outside group duration")
	}
	raw, bits, err := group.readSample(channelIndex, sampleIndex)
	if err != nil {
		return Sample{}, err
	}
	return group.publicSample(channelIndex, rawPoint{sample: sampleIndex, raw: raw, bits: bits}), nil
}

func (g *group) ensureIndex(ctx context.Context, closeCh <-chan struct{}) error {
	for {
		g.indexMu.Lock()
		if g.index != nil {
			g.indexMu.Unlock()
			return nil
		}
		if g.indexErr != nil {
			err := g.indexErr
			g.indexMu.Unlock()
			return err
		}
		if g.indexBuilding {
			ready := g.indexReady
			g.indexMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-closeCh:
				return ErrClosed
			case <-ready:
				continue
			}
		}
		g.indexBuilding = true
		g.indexReady = make(chan struct{})
		ready := g.indexReady
		g.indexMu.Unlock()

		index, err := g.buildIndex(ctx, closeCh, g.indexBudget)

		g.indexMu.Lock()
		g.indexBuilding = false
		if err == nil {
			g.index = index
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			g.indexErr = err
		}
		close(ready)
		g.indexMu.Unlock()
		return err
	}
}

func (g *group) buildIndex(ctx context.Context, closeCh <-chan struct{}, maxEntries int) (*multiIndex, error) {
	channelCount := len(g.info.Channels)
	baseSpan, err := boundedBaseSpan(g.info.SampleCount, channelCount, maxEntries)
	if err != nil {
		return nil, err
	}
	leafCount := divideRoundUp(g.info.SampleCount, baseSpan)
	leaf := indexLevel{
		sampleSpan: baseSpan,
		channels:   make([][]rawBucket, channelCount),
	}
	for channel := range leaf.channels {
		leaf.channels[channel] = make([]rawBucket, leafCount)
	}
	err = g.scanSamples(ctx, closeCh, 0, g.info.SampleCount, nil, func(
		sampleIndex int64,
		channelIndex int,
		raw float64,
		bits uint64,
	) {
		bucketIndex := sampleIndex / baseSpan
		if g.hasPadding && bits == g.paddingBits[channelIndex] {
			return
		}
		updateRawBucket(&leaf.channels[channelIndex][bucketIndex], rawPoint{
			sample: sampleIndex,
			raw:    raw,
			bits:   bits,
		})
	})
	if err != nil {
		return nil, err
	}
	levels := []indexLevel{leaf}
	for len(levels[len(levels)-1].channels[0]) > 1 {
		if err := operationErr(ctx, closeCh); err != nil {
			return nil, err
		}
		previous := levels[len(levels)-1]
		nextCount := divideRoundUp(int64(len(previous.channels[0])), 2)
		next := indexLevel{
			sampleSpan: previous.sampleSpan * 2,
			channels:   make([][]rawBucket, channelCount),
		}
		for channelIndex := range next.channels {
			next.channels[channelIndex] = make([]rawBucket, nextCount)
			for bucketIndex := int64(0); bucketIndex < nextCount; bucketIndex++ {
				left := previous.channels[channelIndex][bucketIndex*2]
				merged := left
				if rightIndex := bucketIndex*2 + 1; rightIndex < int64(len(previous.channels[channelIndex])) {
					merged = mergeRawBuckets(left, previous.channels[channelIndex][rightIndex])
				}
				next.channels[channelIndex][bucketIndex] = merged
			}
		}
		levels = append(levels, next)
	}
	return &multiIndex{levels: levels}, nil
}

func (g *group) scanSamples(
	ctx context.Context,
	closeCh <-chan struct{},
	startSample int64,
	endSample int64,
	channelIndices []int,
	visit func(sampleIndex int64, channelIndex int, raw float64, bits uint64),
) error {
	if startSample < 0 || endSample < startSample || endSample > g.info.SampleCount {
		return fmt.Errorf("waveform: invalid sample scan [%d,%d)", startSample, endSample)
	}
	if startSample == endSample {
		return nil
	}
	channelCount := len(g.info.Channels)
	sampleStride := channelCount * g.decoder.width
	samplesPerBlock := waveformReadBlockBytes / sampleStride
	if samplesPerBlock < 1 {
		samplesPerBlock = 1
	}
	blockBytes := samplesPerBlock * sampleStride
	scratch := make([]byte, blockBytes)
	channels := channelIndices
	if channels == nil {
		channels = make([]int, channelCount)
		for i := range channels {
			channels[i] = i
		}
	}
	for blockStart := startSample; blockStart < endSample; {
		if err := operationErr(ctx, closeCh); err != nil {
			return err
		}
		blockEnd := blockStart + int64(samplesPerBlock)
		if blockEnd > endSample {
			blockEnd = endSample
		}
		samplesInBlock := int(blockEnd - blockStart)
		bytesInBlock := samplesInBlock * sampleStride
		offset, overflow := multiplySize(blockStart, int64(sampleStride))
		if overflow {
			return fmt.Errorf("waveform: sample offset overflow")
		}
		n, readErr := g.source.ReadAt(scratch[:bytesInBlock], offset)
		if readErr == nil && n != bytesInBlock {
			readErr = io.ErrUnexpectedEOF
		}
		if readErr != nil {
			return fmt.Errorf(
				"read waveform samples [%d,%d) at byte %d: %w",
				blockStart,
				blockEnd,
				offset,
				readErr,
			)
		}
		if err := operationErr(ctx, closeCh); err != nil {
			return err
		}
		for sampleOffset := 0; sampleOffset < samplesInBlock; sampleOffset++ {
			if sampleOffset&1023 == 0 {
				if err := operationErr(ctx, closeCh); err != nil {
					return err
				}
			}
			sampleIndex := blockStart + int64(sampleOffset)
			base := sampleOffset * sampleStride
			for _, channelIndex := range channels {
				channelOffset := base + channelIndex*g.decoder.width
				raw, bits, err := g.decoder.decodeBytes(
					scratch[channelOffset:channelOffset+g.decoder.width],
					g.info.Channels[channelIndex].BitsStored,
				)
				if err != nil {
					return err
				}
				visit(sampleIndex, channelIndex, raw, bits)
			}
		}
		blockStart = blockEnd
	}
	return nil
}

func (g *group) envelopeBuckets(
	ctx context.Context,
	closeCh <-chan struct{},
	channelIndex int,
	startSample int64,
	endSample int64,
	width int,
) ([]EnvelopeBucket, error) {
	level := chooseLevel(g.index.levels, startSample, endSample, int64(width))
	firstBucket := startSample / level.sampleSpan
	lastBucket := (endSample - 1) / level.sampleSpan
	buckets := make([]EnvelopeBucket, 0, lastBucket-firstBucket+1)
	indexed := level.channels[channelIndex]
	for bucketIndex := firstBucket; bucketIndex <= lastBucket; bucketIndex++ {
		if err := operationErr(ctx, closeCh); err != nil {
			return nil, err
		}
		alignedStart := bucketIndex * level.sampleSpan
		alignedEnd := alignedStart + level.sampleSpan
		if alignedEnd > g.info.SampleCount {
			alignedEnd = g.info.SampleCount
		}
		bucketStart := alignedStart
		if bucketStart < startSample {
			bucketStart = startSample
		}
		bucketEnd := alignedEnd
		if bucketEnd > endSample {
			bucketEnd = endSample
		}
		raw := indexed[bucketIndex]
		if bucketStart != alignedStart || bucketEnd != alignedEnd {
			var err error
			raw, err = g.scanRawBucket(ctx, closeCh, channelIndex, bucketStart, bucketEnd)
			if err != nil {
				return nil, err
			}
		}
		bucket := EnvelopeBucket{
			StartSample: bucketStart,
			EndSample:   bucketEnd,
			Valid:       raw.valid,
		}
		if raw.valid {
			bucket.Min = g.publicSample(channelIndex, raw.min)
			bucket.Max = g.publicSample(channelIndex, raw.max)
			if bucket.Min.Value > bucket.Max.Value {
				bucket.Min, bucket.Max = bucket.Max, bucket.Min
			}
		}
		buckets = append(buckets, bucket)
	}
	return buckets, nil
}

func (g *group) scanRawBucket(
	ctx context.Context,
	closeCh <-chan struct{},
	channelIndex int,
	startSample int64,
	endSample int64,
) (rawBucket, error) {
	var bucket rawBucket
	err := g.scanSamples(
		ctx,
		closeCh,
		startSample,
		endSample,
		[]int{channelIndex},
		func(sampleIndex int64, _ int, raw float64, bits uint64) {
			if g.hasPadding && bits == g.paddingBits[channelIndex] {
				return
			}
			updateRawBucket(&bucket, rawPoint{sample: sampleIndex, raw: raw, bits: bits})
		},
	)
	return bucket, err
}

func (g *group) readSample(channelIndex int, sampleIndex int64) (float64, uint64, error) {
	channelCount := int64(len(g.info.Channels))
	sampleOrdinal, overflow := multiplySize(sampleIndex, channelCount)
	if overflow || sampleOrdinal > math.MaxInt64-int64(channelIndex) {
		return 0, 0, fmt.Errorf("waveform: sample offset overflow")
	}
	sampleOrdinal += int64(channelIndex)
	offset, overflow := multiplySize(sampleOrdinal, int64(g.decoder.width))
	if overflow {
		return 0, 0, fmt.Errorf("waveform: sample offset overflow")
	}
	return g.decoder.read(g.source, offset, g.info.Channels[channelIndex].BitsStored)
}

func (g *group) publicSample(channelIndex int, point rawPoint) Sample {
	channel := g.info.Channels[channelIndex]
	value := point.raw
	calibrated := channel.Calibration.Status == CalibrationComplete
	if calibrated {
		value = point.raw*channel.Calibration.Sensitivity*channel.Calibration.CorrectionFactor +
			channel.Calibration.Baseline
	}
	valid := !g.hasPadding || point.bits != g.paddingBits[channelIndex]
	return Sample{
		SampleIndex: point.sample,
		Time: durationFromFloat(
			float64(point.sample)/g.info.SamplingFrequencyHz,
			time.Second,
		) + channel.TimeSkew + channel.ChannelOffset,
		Raw:        point.raw,
		Value:      value,
		Valid:      valid,
		Calibrated: calibrated,
	}
}

func (g *group) channelSampleRange(
	channelIndex int,
	start time.Duration,
	duration time.Duration,
) (int64, int64, bool, error) {
	end := start + duration
	if end < start {
		return 0, 0, false, fmt.Errorf("waveform: requested interval overflows duration")
	}
	channel := g.info.Channels[channelIndex]
	shift := channel.TimeSkew + channel.ChannelOffset
	startFloat := (float64(start) - float64(shift)) / float64(time.Second) * g.info.SamplingFrequencyHz
	endFloat := (float64(end) - float64(shift)) /
		float64(time.Second) * g.info.SamplingFrequencyHz
	if startFloat >= float64(g.info.SampleCount) || endFloat <= 0 {
		return 0, 0, false, nil
	}
	startSample := int64(0)
	if startFloat > 0 {
		startSample = int64(math.Ceil(startFloat))
	}
	endSample := g.info.SampleCount
	if endFloat < float64(g.info.SampleCount) {
		endSample = int64(math.Ceil(endFloat))
	}
	if startSample < 0 {
		startSample = 0
	}
	if endSample > g.info.SampleCount {
		endSample = g.info.SampleCount
	}
	if endSample <= startSample {
		return 0, 0, false, nil
	}
	return startSample, endSample, true, nil
}

func (g *group) unsupportedError(sopClassUID string) error {
	return &UnsupportedError{Fallback: RawFallback{
		SOPClassUID: sopClassUID,
		GroupIndex:  g.info.Index,
		Encoding: fmt.Sprintf(
			"%s/%d-bit",
			g.info.SampleInterpretation,
			g.info.BitsAllocated,
		),
		RawBytes: g.info.RawDataBytes,
		Reason:   g.info.FallbackReason,
	}}
}

func selectedChannels(selected []int, count int) ([]int, error) {
	if selected == nil {
		out := make([]int, count)
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	out := make([]int, 0, len(selected))
	seen := make(map[int]struct{}, len(selected))
	for _, index := range selected {
		if index < 0 || index >= count {
			return nil, fmt.Errorf("waveform: channel index %d out of range", index)
		}
		if _, exists := seen[index]; exists {
			continue
		}
		seen[index] = struct{}{}
		out = append(out, index)
	}
	return out, nil
}

func chooseLevel(levels []indexLevel, startSample, endSample, width int64) indexLevel {
	for _, level := range levels {
		count := (endSample-1)/level.sampleSpan - startSample/level.sampleSpan + 1
		if count <= width {
			return level
		}
	}
	return levels[len(levels)-1]
}

func boundedBaseSpan(sampleCount int64, channelCount, maxEntries int) (int64, error) {
	if maxEntries < channelCount {
		return 0, fmt.Errorf(
			"waveform: MaxIndexEntries %d cannot retain one bucket for each of %d channels",
			maxEntries,
			channelCount,
		)
	}
	low, high := int64(1), sampleCount
	for low < high {
		middle := low + (high-low)/2
		if indexEntryCount(sampleCount, middle, channelCount, maxEntries) <= maxEntries {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low, nil
}

func indexEntryCount(sampleCount, baseSpan int64, channelCount, stopAfter int) int {
	count := divideRoundUp(sampleCount, baseSpan)
	total := int64(0)
	for {
		if count > (int64(stopAfter)-total)/int64(channelCount) {
			return stopAfter + 1
		}
		total += count * int64(channelCount)
		if count == 1 {
			return int(total)
		}
		count = divideRoundUp(count, 2)
	}
}

func divideRoundUp(value, divisor int64) int64 {
	return value/divisor + boolInt64(value%divisor != 0)
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func updateRawBucket(bucket *rawBucket, point rawPoint) {
	if !bucket.valid {
		bucket.min = point
		bucket.max = point
		bucket.valid = true
		return
	}
	if point.raw < bucket.min.raw || point.raw == bucket.min.raw && point.sample < bucket.min.sample {
		bucket.min = point
	}
	if point.raw > bucket.max.raw || point.raw == bucket.max.raw && point.sample < bucket.max.sample {
		bucket.max = point
	}
}

func mergeRawBuckets(left, right rawBucket) rawBucket {
	if !left.valid {
		return right
	}
	if !right.valid {
		return left
	}
	merged := left
	updateRawBucket(&merged, right.min)
	updateRawBucket(&merged, right.max)
	return merged
}

func operationErr(ctx context.Context, closeCh <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-closeCh:
		return ErrClosed
	default:
		return nil
	}
}
