package seg

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/roi"
)

func TestSourceReferenceIndexMatchesLegacyLookup(t *testing.T) {
	const (
		seriesUID = "1.2.826.0.1.3680043.9.7433.859.1"
		classUID  = "1.2.840.10008.5.1.4.1.1.2.1"
	)
	refs := []ReferencedImage{
		{SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: "1.2.3.1", Frames: []int{1, 2}},
		{SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: "1.2.3.1", Frames: []int{3, 4}},
		{SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: "1.2.3.2"},
		{SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: "1.2.3.3", Frames: []int{9}},
	}
	index, err := newSourceReferenceIndex(refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []Frame{
		{ReferencedSOPInstanceUID: "1.2.3.1"},
		{ReferencedSOPInstanceUID: "1.2.3.1", ReferencedFrameNumber: 3},
		{ReferencedSOPInstanceUID: "1.2.3.2", ReferencedFrameNumber: 99},
		{ReferencedSOPInstanceUID: "1.2.3.3", ReferencedFrameNumber: 9},
	} {
		want := legacySourceReferenceForFrame(refs, frame)
		got, ok := index.lookup(frame)
		if !ok {
			t.Fatalf("lookup(%+v) unexpectedly missed", frame)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("lookup(%+v) = %+v, legacy = %+v", frame, got, want)
		}
	}
}

func TestSEGWriteRejectsMissingSourceReferences(t *testing.T) {
	const (
		seriesUID = "1.2.826.0.1.3680043.9.7433.859.20"
		classUID  = "1.2.840.10008.5.1.4.1.1.2.1"
		sopUID    = "1.2.826.0.1.3680043.9.7433.859.21"
	)
	complete := ReferencedImage{
		SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: sopUID, Frames: []int{1},
	}
	for _, test := range []struct {
		name       string
		refs       []ReferencedImage
		frame      Frame
		wantDetail string
	}{
		{name: "unlisted-sop", refs: []ReferencedImage{complete}, frame: Frame{ReferencedSOPInstanceUID: "1.2.3.unlisted", ReferencedFrameNumber: 1}, wantDetail: "unlisted source SOP Instance UID"},
		{name: "unlisted-frame", refs: []ReferencedImage{complete}, frame: Frame{ReferencedSOPInstanceUID: sopUID, ReferencedFrameNumber: 2}, wantDetail: "unlisted source SOP Instance UID"},
		{name: "frame-number-without-sop", frame: Frame{ReferencedFrameNumber: 5}, wantDetail: "without a source SOP Instance UID"},
		{name: "missing-series", refs: []ReferencedImage{{SOPClassUID: classUID, SOPInstanceUID: sopUID}}, wantDetail: "missing Series Instance UID"},
		{name: "missing-class", refs: []ReferencedImage{{SeriesInstanceUID: seriesUID, SOPInstanceUID: sopUID}}, wantDetail: "missing SOP Class UID"},
		{name: "missing-instance", refs: []ReferencedImage{{SeriesInstanceUID: seriesUID, SOPClassUID: classUID}}, wantDetail: "missing SOP Instance UID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := sourceReferenceTestDocument(test.refs)
			if test.frame.ReferencedSOPInstanceUID != "" || test.frame.ReferencedFrameNumber != 0 {
				test.frame.SegmentNumber = 1
				test.frame.Mask = roi.NewRasterMask(1, 1)
				doc.Frames = []Frame{test.frame}
			}
			_, err := Write(doc)
			if !errors.Is(err, ErrMissingReference) {
				t.Fatalf("Write error = %v, want ErrMissingReference", err)
			}
			if !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("Write error = %q, want detail %q", err, test.wantDetail)
			}
		})
	}
}

func TestSEGReadRejectsMissingSourceReferences(t *testing.T) {
	ref := ReferencedImage{
		SeriesInstanceUID: "1.2.3.10", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2.1", SOPInstanceUID: "1.2.3.11",
	}
	doc := sourceReferenceTestDocument([]ReferencedImage{ref})
	doc.Frames[0].ReferencedSOPInstanceUID = ref.SOPInstanceUID
	file, err := Write(doc)
	if err != nil {
		t.Fatal(err)
	}
	elements := file.Dataset.Elements()
	filtered := elements[:0]
	for _, element := range elements {
		if element.Header.Tag != tagReferencedSeriesSequence {
			filtered = append(filtered, element)
		}
	}
	_, err = Read(object.FromElements(filtered, std.Dictionary))
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("Read error = %v, want ErrMissingReference", err)
	}
}

func TestSourceReferenceIndexRejectsDuplicatesAndConflictsDeterministically(t *testing.T) {
	const (
		seriesUID = "1.2.826.0.1.3680043.9.7433.859.1"
		classUID  = "1.2.840.10008.5.1.4.1.1.2.1"
		sopUID    = "1.2.826.0.1.3680043.9.7433.859.2"
	)
	ref := func() ReferencedImage {
		return ReferencedImage{SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: sopUID}
	}
	for _, test := range []struct {
		name       string
		refs       []ReferencedImage
		wantDetail string
	}{
		{
			name: "repeated-frame-in-one-entry",
			refs: func() []ReferencedImage {
				item := ref()
				item.Frames = []int{4, 4}
				return []ReferencedImage{item}
			}(),
			wantDetail: "duplicate",
		},
		{
			name: "overlapping-explicit-entries",
			refs: func() []ReferencedImage {
				first, second := ref(), ref()
				first.Frames, second.Frames = []int{3, 4}, []int{4, 5}
				return []ReferencedImage{first, second}
			}(),
			wantDetail: "duplicate",
		},
		{
			name:       "duplicate-wildcards",
			refs:       []ReferencedImage{ref(), ref()},
			wantDetail: "duplicate",
		},
		{
			name: "wildcard-before-explicit",
			refs: func() []ReferencedImage {
				explicit := ref()
				explicit.Frames = []int{1}
				return []ReferencedImage{ref(), explicit}
			}(),
			wantDetail: "overlap wildcard and explicit",
		},
		{
			name: "explicit-before-wildcard",
			refs: func() []ReferencedImage {
				explicit := ref()
				explicit.Frames = []int{1}
				return []ReferencedImage{explicit, ref()}
			}(),
			wantDetail: "overlap wildcard and explicit",
		},
		{
			name: "conflicting-sop-class",
			refs: func() []ReferencedImage {
				first, second := ref(), ref()
				first.Frames, second.Frames = []int{1}, []int{2}
				second.SOPClassUID = "1.2.840.10008.5.1.4.1.1.4"
				return []ReferencedImage{first, second}
			}(),
			wantDetail: "conflict",
		},
		{
			name: "conflicting-series",
			refs: func() []ReferencedImage {
				first, second := ref(), ref()
				first.Frames, second.Frames = []int{1}, []int{2}
				second.SeriesInstanceUID = "1.2.3.other-series"
				return []ReferencedImage{first, second}
			}(),
			wantDetail: "conflict",
		},
		{
			name: "zero-frame",
			refs: func() []ReferencedImage {
				item := ref()
				item.Frames = []int{0}
				return []ReferencedImage{item}
			}(),
			wantDetail: "non-positive frame number 0",
		},
		{
			name: "negative-frame",
			refs: func() []ReferencedImage {
				item := ref()
				item.Frames = []int{-1}
				return []ReferencedImage{item}
			}(),
			wantDetail: "non-positive frame number -1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			firstError := writeSEGWithSourceReferences(test.refs)
			secondError := writeSEGWithSourceReferences(test.refs)
			if !errors.Is(firstError, ErrInvalidObject) {
				t.Fatalf("Write error = %v, want ErrInvalidObject", firstError)
			}
			if firstError.Error() != secondError.Error() {
				t.Fatalf("nondeterministic errors: %q != %q", firstError, secondError)
			}
			if !strings.Contains(firstError.Error(), test.wantDetail) {
				t.Fatalf("Write error = %q, want detail %q", firstError, test.wantDetail)
			}
		})
	}
}

func TestSEGWriteAllowsDisjointFrameReferencesForSameSOP(t *testing.T) {
	const (
		seriesUID = "1.2.826.0.1.3680043.9.7433.859.10"
		classUID  = "1.2.840.10008.5.1.4.1.1.2.1"
		sopUID    = "1.2.826.0.1.3680043.9.7433.859.11"
	)
	refs := []ReferencedImage{
		{SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: sopUID, Frames: []int{1, 2}},
		{SeriesInstanceUID: seriesUID, SOPClassUID: classUID, SOPInstanceUID: sopUID, Frames: []int{3, 4}},
	}
	doc := sourceReferenceTestDocument(refs)
	doc.Frames = []Frame{
		{SegmentNumber: 1, ReferencedSOPInstanceUID: sopUID, ReferencedFrameNumber: 2, Mask: roi.NewRasterMask(1, 1)},
		{SegmentNumber: 1, ReferencedSOPInstanceUID: sopUID, ReferencedFrameNumber: 4, Mask: roi.NewRasterMask(1, 1)},
	}
	file, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	items, ok := file.Dataset.GetSequence(tagPerFrameFunctionalGroups)
	if !ok || len(items) != 2 {
		t.Fatalf("per-frame groups = %d, ok=%v, want 2", len(items), ok)
	}
}

func writeSEGWithSourceReferences(refs []ReferencedImage) error {
	_, err := Write(sourceReferenceTestDocument(refs))
	return err
}

func sourceReferenceTestDocument(refs []ReferencedImage) *Document {
	return &Document{
		SOPClassUID: SegmentationStorage, SOPInstanceUID: "1.2.3.100",
		StudyInstanceUID: "1.2.3.101", SeriesInstanceUID: "1.2.3.102",
		Rows: 1, Columns: 1,
		Segments:         []Segment{{Number: 1, Label: "Target", AlgorithmType: AlgorithmManual}},
		ReferencedImages: refs,
		Frames:           []Frame{{SegmentNumber: 1, Mask: roi.NewRasterMask(1, 1)}},
	}
}
