package dynamic

import (
	"math/rand"
	"testing"
	"time"
)

func TestTemporalOffsetIndexPreservesToleranceAndTieBreaking(t *testing.T) {
	for _, test := range []struct {
		name      string
		offsets   map[int]time.Duration
		target    time.Duration
		want      int
		wantFound bool
	}{
		{name: "positive-tolerance-boundary", offsets: map[int]time.Duration{4: 0}, target: time.Microsecond, want: 4, wantFound: true},
		{name: "negative-tolerance-boundary", offsets: map[int]time.Duration{4: 0}, target: -time.Microsecond, want: 4, wantFound: true},
		{name: "positive-outside-tolerance", offsets: map[int]time.Duration{4: 0}, target: time.Microsecond + time.Nanosecond},
		{name: "negative-outside-tolerance", offsets: map[int]time.Duration{4: 0}, target: -time.Microsecond - time.Nanosecond},
		{name: "tie-prefers-lower-position-on-left", offsets: map[int]time.Duration{1: 0, 2: 2 * time.Microsecond}, target: time.Microsecond, want: 1, wantFound: true},
		{name: "tie-prefers-lower-position-on-right", offsets: map[int]time.Duration{2: 0, 1: 2 * time.Microsecond}, target: time.Microsecond, want: 1, wantFound: true},
		{name: "duplicate-offset-prefers-lower-position", offsets: map[int]time.Duration{7: time.Second, 3: time.Second, 5: time.Second}, target: time.Second, want: 3, wantFound: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			position, found := newTemporalOffsetIndex(test.offsets).nearest(test.target)
			if position != test.want || found != test.wantFound {
				t.Fatalf("nearest = %d/%v, want %d/%v", position, found, test.want, test.wantFound)
			}
		})
	}
}

func TestTemporalOffsetIndexHandlesDurationExtremesWithoutOverflow(t *testing.T) {
	index := newTemporalOffsetIndex(map[int]time.Duration{
		1: time.Duration(-1 << 63),
		2: time.Duration(1<<63 - 1),
	})
	if position, found := index.nearest(0); found || position != 0 {
		t.Fatalf("extreme nearest to zero = %d/%v, want no match", position, found)
	}
	if position, found := index.nearest(time.Duration(-1<<63) + time.Microsecond); !found || position != 1 {
		t.Fatalf("minimum boundary nearest = %d/%v, want 1/true", position, found)
	}
	if got := durationDistance(time.Duration(-1<<63), time.Duration(1<<63-1)); got != ^uint64(0) {
		t.Fatalf("extreme duration distance = %d, want %d", got, ^uint64(0))
	}
}

func TestTemporalOffsetIndexMatchesLinearReference(t *testing.T) {
	random := rand.New(rand.NewSource(860))
	offsets := make(map[int]time.Duration, 500)
	for position := 1; position <= 500; position++ {
		// A narrow domain intentionally creates duplicate offsets and dense ties.
		offsets[position] = time.Duration(random.Intn(2_001)-1_000) * time.Nanosecond
	}
	index := newTemporalOffsetIndex(offsets)
	for query := 0; query < 2_000; query++ {
		target := time.Duration(random.Intn(6_001)-3_000) * time.Nanosecond
		wantPosition, wantFound := linearNearestTemporalPosition(offsets, target)
		gotPosition, gotFound := index.nearest(target)
		if gotPosition != wantPosition || gotFound != wantFound {
			t.Fatalf("query %d target %v: indexed = %d/%v, linear = %d/%v", query, target, gotPosition, gotFound, wantPosition, wantFound)
		}
	}
}

func TestBuildExplicitOffsetUsesLastAnchorForRepeatedPosition(t *testing.T) {
	for _, test := range []struct {
		name        string
		firstOffset time.Duration
		lastOffset  time.Duration
		queryOffset time.Duration
		wantPoints  int
	}{
		{name: "later-anchor-wins", firstOffset: 10 * time.Millisecond, lastOffset: 20 * time.Millisecond, queryOffset: 20*time.Millisecond + 500*time.Nanosecond, wantPoints: 1},
		{name: "reversed-anchor-wins", firstOffset: 20 * time.Millisecond, lastOffset: 10 * time.Millisecond, queryOffset: 20*time.Millisecond + 500*time.Nanosecond, wantPoints: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			frames := []FrameMetadata{
				{FrameIndex: 0, TemporalPosition: 7, HasTemporalPosition: true, Offset: test.firstOffset, HasOffset: true, SpatialPosition: 0, HasSpatialPosition: true},
				{FrameIndex: 1, TemporalPosition: 7, HasTemporalPosition: true, Offset: test.lastOffset, HasOffset: true, SpatialPosition: 1, HasSpatialPosition: true},
				{FrameIndex: 2, Offset: test.queryOffset, HasOffset: true, SpatialPosition: 2, HasSpatialPosition: true},
			}
			timeline := Build(frames)
			if len(timeline.Points) != test.wantPoints {
				t.Fatalf("timeline points = %d, want %d: %+v", len(timeline.Points), test.wantPoints, timeline.Points)
			}
		})
	}
}

func linearNearestTemporalPosition(offsets map[int]time.Duration, target time.Duration) (int, bool) {
	bestPosition := 0
	bestDelta := uint64(0)
	found := false
	for position, offset := range offsets {
		delta := durationDistance(target, offset)
		if delta > uint64(time.Microsecond) {
			continue
		}
		if !found || delta < bestDelta || (delta == bestDelta && position < bestPosition) {
			bestPosition, bestDelta, found = position, delta, true
		}
	}
	return bestPosition, found
}
