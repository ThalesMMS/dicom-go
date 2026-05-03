package main

import "testing"

func TestSortFramesUsesPositionThenFallbacks(t *testing.T) {
	frames := []Frame{
		{SourceName: "name-2", Sort: sortKey{rank: 3, sourceName: "name-2"}},
		{SourceName: "position-2", Sort: sortKey{rank: 0, value: 2, sourceName: "position-2"}},
		{SourceName: "instance-3", Sort: sortKey{rank: 2, instance: 3, sourceName: "instance-3"}},
		{SourceName: "position-1", Sort: sortKey{rank: 0, value: 1, sourceName: "position-1"}},
		{SourceName: "slice-location", Sort: sortKey{rank: 1, value: -4, sourceName: "slice-location"}},
	}

	sortFrames(frames)

	got := make([]string, len(frames))
	for i := range frames {
		got[i] = frames[i].SourceName
	}
	want := []string{"position-1", "position-2", "slice-location", "instance-3", "name-2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted frame %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestShouldSkipZipEntryIgnoresFinderMetadata(t *testing.T) {
	for _, name := range []string{"__MACOSX/a.dcm", "series/._IM-1.dcm", "series/.DS_Store"} {
		if !shouldSkipZipEntry(name) {
			t.Fatalf("shouldSkipZipEntry(%q) = false, want true", name)
		}
	}
	if shouldSkipZipEntry("series/IM-1.dcm") {
		t.Fatal("shouldSkipZipEntry(valid DICOM) = true, want false")
	}
}
