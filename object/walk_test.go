package object

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

var (
	walkOuterSeqTag    = core.NewTag(0x0008, 0x1111)
	walkInnerSeqTag    = core.NewTag(0x0008, 0x1115)
	walkSOPClassTag    = core.NewTag(0x0008, 0x1150)
	walkPatientNameTag = core.NewTag(0x0010, 0x0010)
	walkPatientIDTag   = core.NewTag(0x0010, 0x0020)
	walkStudyUIDTag    = core.NewTag(0x0020, 0x000D)
)

func TestObjectWalkVisitsSequencesAndChildrenPreOrder(t *testing.T) {
	obj := walkFixtureObject()

	var got []string
	err := obj.Walk(func(path []core.Tag, elem core.Element) error {
		got = append(got, compactTagPath(path))
		return nil
	})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}

	want := []string{
		"(0008,1111)",
		"(0008,1111)/(0010,0010)",
		"(0008,1111)/(0008,1115)",
		"(0008,1111)/(0008,1115)/(0008,1150)",
		"(0008,1111)/(0010,0020)",
		"(0020,000D)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Walk() paths = %#v, want %#v", got, want)
	}
}

func TestObjectWalkPathReportsSequenceItemIndexes(t *testing.T) {
	obj := walkFixtureObject()

	var got []string
	err := obj.WalkPath(func(path WalkPath, elem core.Element) error {
		got = append(got, path.String())
		return nil
	})
	if err != nil {
		t.Fatalf("WalkPath() error = %v", err)
	}

	want := []string{
		"(0008,1111)",
		"(0008,1111)[0]/(0010,0010)",
		"(0008,1111)[0]/(0008,1115)",
		"(0008,1111)[0]/(0008,1115)[0]/(0008,1150)",
		"(0008,1111)[1]/(0010,0020)",
		"(0020,000D)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WalkPath() paths = %#v, want %#v", got, want)
	}
}

func TestObjectWalkRetainedPathsAreIndependent(t *testing.T) {
	obj := walkFixtureObject()

	var paths [][]core.Tag
	err := obj.Walk(func(path []core.Tag, elem core.Element) error {
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}

	got := make([]string, len(paths))
	for i, path := range paths {
		got[i] = compactTagPath(path)
	}
	want := []string{
		"(0008,1111)",
		"(0008,1111)/(0010,0010)",
		"(0008,1111)/(0008,1115)",
		"(0008,1111)/(0008,1115)/(0008,1150)",
		"(0008,1111)/(0010,0020)",
		"(0020,000D)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retained Walk() paths = %#v, want %#v", got, want)
	}
}

func TestObjectWalkPathRetainedPathsAreIndependent(t *testing.T) {
	obj := walkFixtureObject()

	var paths []WalkPath
	err := obj.WalkPath(func(path WalkPath, elem core.Element) error {
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkPath() error = %v", err)
	}

	got := make([]string, len(paths))
	for i, path := range paths {
		got[i] = path.String()
	}
	want := []string{
		"(0008,1111)",
		"(0008,1111)[0]/(0010,0010)",
		"(0008,1111)[0]/(0008,1115)",
		"(0008,1111)[0]/(0008,1115)[0]/(0008,1150)",
		"(0008,1111)[1]/(0010,0020)",
		"(0020,000D)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retained WalkPath() paths = %#v, want %#v", got, want)
	}
}

func TestWalkPathCloneReturnsIndependentCopy(t *testing.T) {
	path := WalkPath{
		{Tag: walkOuterSeqTag, ItemIndex: 0},
		{Tag: walkPatientNameTag, ItemIndex: WalkPathNoItem},
	}

	clone := path.Clone()
	path[0].ItemIndex = 7
	path[1].Tag = walkPatientIDTag

	want := WalkPath{
		{Tag: walkOuterSeqTag, ItemIndex: 0},
		{Tag: walkPatientNameTag, ItemIndex: WalkPathNoItem},
	}
	if !reflect.DeepEqual(clone, want) {
		t.Fatalf("Clone() = %#v, want %#v", clone, want)
	}
}

func TestObjectWalkStopsOnCallbackError(t *testing.T) {
	obj := walkFixtureObject()
	stop := errors.New("stop walk")

	var visited []core.Tag
	err := obj.Walk(func(path []core.Tag, elem core.Element) error {
		visited = append(visited, elem.Tag())
		if elem.Tag() == walkInnerSeqTag {
			return stop
		}
		return nil
	})

	if !errors.Is(err, stop) {
		t.Fatalf("Walk() error = %v, want stop", err)
	}
	want := []core.Tag{walkOuterSeqTag, walkPatientNameTag, walkInnerSeqTag}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited tags = %v, want %v", visited, want)
	}
}

func TestObjectWalkHandlesNilReceiverAndNilCallbacks(t *testing.T) {
	var obj *Object
	called := false
	if err := obj.Walk(func(path []core.Tag, elem core.Element) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("Walk() on nil object error = %v", err)
	}
	if called {
		t.Fatal("Walk() on nil object called callback")
	}

	obj = walkFixtureObject()
	if err := obj.Walk(nil); err != nil {
		t.Fatalf("Walk(nil) error = %v", err)
	}
	if err := obj.WalkPath(nil); err != nil {
		t.Fatalf("WalkPath(nil) error = %v", err)
	}
}

func walkFixtureObject() *Object {
	return FromElements([]core.Element{
		dicomtest.NewSequenceElement(
			walkOuterSeqTag,
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewPNElement(walkPatientNameTag, "NEST^ONE"),
					dicomtest.NewSequenceElement(
						walkInnerSeqTag,
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewUIElement(walkSOPClassTag, dicomtest.TestSOPClassUID),
							},
						},
					),
				},
			},
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewStringElement(walkPatientIDTag, core.VRLO, "NEST-TWO"),
				},
			},
		),
		dicomtest.NewUIElement(walkStudyUIDTag, dicomtest.TestStudyInstanceUID),
	}, std.Dictionary)
}

func walkWideSequenceFixtureObject(items int) *Object {
	dataSets := make([]core.DataSet, items)
	for i := range dataSets {
		dataSets[i] = core.DataSet{
			Elements: []core.Element{
				dicomtest.NewStringElement(walkPatientIDTag, core.VRLO, "PATIENT-ID"),
			},
		}
	}
	return FromElements([]core.Element{
		dicomtest.NewSequenceElement(walkOuterSeqTag, dataSets...),
	}, std.Dictionary)
}

func BenchmarkObjectWalkWideSequence(b *testing.B) {
	obj := walkWideSequenceFixtureObject(128)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := obj.Walk(func(path []core.Tag, elem core.Element) error {
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkObjectWalkPathWideSequence(b *testing.B) {
	obj := walkWideSequenceFixtureObject(128)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := obj.WalkPath(func(path WalkPath, elem core.Element) error {
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func compactTagPath(path []core.Tag) string {
	parts := make([]string, len(path))
	for i, tag := range path {
		parts[i] = tag.String()
	}
	return strings.Join(parts, "/")
}
