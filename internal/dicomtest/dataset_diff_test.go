package dicomtest

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestDiffDataSetIncludesNestedSequencePath(t *testing.T) {
	got := core.DataSet{
		Elements: []core.Element{
			NewSequenceElement(
				core.NewTag(0x0008, 0x1111),
				core.DataSet{
					Elements: []core.Element{
						NewPNElement(core.NewTag(0x0010, 0x0010), "WRONG^NAME"),
					},
				},
			),
		},
	}
	want := core.DataSet{
		Elements: []core.Element{
			NewSequenceElement(
				core.NewTag(0x0008, 0x1111),
				core.DataSet{
					Elements: []core.Element{
						NewPNElement(core.NewTag(0x0010, 0x0010), "RIGHT^NAME"),
					},
				},
			),
		},
	}

	diff := DiffDataSet(got, want)
	if !strings.Contains(diff, "dataset/(0008,1111)[0]/(0010,0010)") {
		t.Fatalf("DiffDataSet() missing nested path context: %q", diff)
	}
	if !strings.Contains(diff, `string value[0] = "WRONG^NAME", want "RIGHT^NAME"`) {
		t.Fatalf("DiffDataSet() missing nested string detail: %q", diff)
	}
}

func TestDiffDataSetIncludesOffsetTablePath(t *testing.T) {
	got := core.DataSet{
		Elements: []core.Element{
			NewFragmentSequenceElement(
				core.TagPixelData,
				[]byte{0x00, 0x00, 0x00, 0x00},
				[]byte{0x01, 0x02, 0x03, 0x04},
			),
		},
	}
	want := core.DataSet{
		Elements: []core.Element{
			NewFragmentSequenceElement(
				core.TagPixelData,
				[]byte{0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00},
				[]byte{0x01, 0x02, 0x03, 0x04},
			),
		},
	}

	diff := DiffDataSet(got, want)
	if diff != "dataset/(7FE0,0010)[offset_table]: got 4 bytes, want 8 bytes" {
		t.Fatalf("DiffDataSet() = %q", diff)
	}
}

func TestDiffDataSetIncludesFragmentIndexPath(t *testing.T) {
	got := core.DataSet{
		Elements: []core.Element{
			NewFragmentSequenceElement(
				core.TagPixelData,
				nil,
				[]byte{0x01, 0x02},
				[]byte{0x03, 0x04},
			),
		},
	}
	want := core.DataSet{
		Elements: []core.Element{
			NewFragmentSequenceElement(
				core.TagPixelData,
				nil,
				[]byte{0x01, 0x02},
				[]byte{0x05, 0x06},
			),
		},
	}

	diff := DiffDataSet(got, want)
	if diff != "dataset/(7FE0,0010)[1]: bytes = 03 04, want 05 06" {
		t.Fatalf("DiffDataSet() = %q", diff)
	}
}
