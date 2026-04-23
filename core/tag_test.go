package core

import (
	"sort"
	"testing"
)

func TestParseTag(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Tag
	}{
		{name: "parenthesized", input: "(0010,0010)", want: NewTag(0x0010, 0x0010)},
		{name: "comma separated", input: "0010,0010", want: NewTag(0x0010, 0x0010)},
		{name: "packed", input: "00100010", want: NewTag(0x0010, 0x0010)},
		{name: "packed lowercase", input: "7fe00010", want: NewTag(0x7FE0, 0x0010)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag, err := ParseTag(tt.input)
			if err != nil {
				t.Fatalf("ParseTag(%q) returned error: %v", tt.input, err)
			}
			if tag != tt.want {
				t.Fatalf("ParseTag(%q) = %v, want %v", tt.input, tag, tt.want)
			}
		})
	}
}

func TestParseTagRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "short packed", input: "0010"},
		{name: "wrong separator", input: "0010-0010"},
		{name: "missing comma", input: "(0010 0010)"},
		{name: "unbalanced open paren", input: "(0010,0010"},
		{name: "unbalanced close paren", input: "0010,0010)"},
		{name: "invalid group hex", input: "(ZZZZ,0010)"},
		{name: "invalid element hex", input: "0010,GGGG"},
		{name: "missing element", input: "0010,"},
		{name: "missing group", input: ",0010"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTag(tt.input); err == nil {
				t.Fatalf("ParseTag(%q) unexpectedly succeeded", tt.input)
			}
		})
	}
}

func TestTagStringFormatting(t *testing.T) {
	tag := NewTag(0x0010, 0x0020)
	if got := tag.String(); got != "(0010,0020)" {
		t.Fatalf("String() = %q, want %q", got, "(0010,0020)")
	}
	if got := tag.HexString(); got != "00100020" {
		t.Fatalf("HexString() = %q, want %q", got, "00100020")
	}
}

func TestTagIsPrivate(t *testing.T) {
	if NewTag(0x0010, 0x0010).IsPrivate() {
		t.Fatalf("expected even group tag to be public")
	}
	if !NewTag(0x0011, 0x0010).IsPrivate() {
		t.Fatalf("expected odd group tag to be private")
	}
}

func TestTagIsGroupLength(t *testing.T) {
	if !NewTag(0x0010, 0x0000).IsGroupLength() {
		t.Fatalf("expected element 0x0000 to be a group length tag")
	}
	if NewTag(0x0010, 0x0010).IsGroupLength() {
		t.Fatalf("expected non-zero element not to be a group length tag")
	}
}

func TestSequenceControlTagConstants(t *testing.T) {
	tests := []struct {
		name string
		got  Tag
		want Tag
	}{
		{name: "item", got: TagItem, want: NewTag(0xFFFE, 0xE000)},
		{name: "item delimitation", got: TagItemDelimitationItem, want: NewTag(0xFFFE, 0xE00D)},
		{name: "sequence delimitation", got: TagSequenceDelimitationItem, want: NewTag(0xFFFE, 0xE0DD)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestTagSequenceControlPredicates(t *testing.T) {
	tests := []struct {
		name                       string
		tag                        Tag
		wantSequenceDelimiting     bool
		wantItem                   bool
		wantItemDelimitationItem   bool
		wantSequenceDelimitationIt bool
	}{
		{
			name:                       "item",
			tag:                        TagItem,
			wantSequenceDelimiting:     true,
			wantItem:                   true,
			wantItemDelimitationItem:   false,
			wantSequenceDelimitationIt: false,
		},
		{
			name:                       "item delimitation",
			tag:                        TagItemDelimitationItem,
			wantSequenceDelimiting:     true,
			wantItem:                   false,
			wantItemDelimitationItem:   true,
			wantSequenceDelimitationIt: false,
		},
		{
			name:                       "sequence delimitation",
			tag:                        TagSequenceDelimitationItem,
			wantSequenceDelimiting:     true,
			wantItem:                   false,
			wantItemDelimitationItem:   false,
			wantSequenceDelimitationIt: true,
		},
		{
			name:                       "fffe boundary but unnamed",
			tag:                        NewTag(0xFFFE, 0x0000),
			wantSequenceDelimiting:     true,
			wantItem:                   false,
			wantItemDelimitationItem:   false,
			wantSequenceDelimitationIt: false,
		},
		{
			name:                       "ordinary element",
			tag:                        NewTag(0x0010, 0x0010),
			wantSequenceDelimiting:     false,
			wantItem:                   false,
			wantItemDelimitationItem:   false,
			wantSequenceDelimitationIt: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tag.IsSequenceDelimiting(); got != tt.wantSequenceDelimiting {
				t.Fatalf("IsSequenceDelimiting() = %v, want %v", got, tt.wantSequenceDelimiting)
			}
			if got := tt.tag.IsItem(); got != tt.wantItem {
				t.Fatalf("IsItem() = %v, want %v", got, tt.wantItem)
			}
			if got := tt.tag.IsItemDelimitationItem(); got != tt.wantItemDelimitationItem {
				t.Fatalf("IsItemDelimitationItem() = %v, want %v", got, tt.wantItemDelimitationItem)
			}
			if got := tt.tag.IsSequenceDelimitationItem(); got != tt.wantSequenceDelimitationIt {
				t.Fatalf("IsSequenceDelimitationItem() = %v, want %v", got, tt.wantSequenceDelimitationIt)
			}
		})
	}
}

func TestTagCompareAndLess(t *testing.T) {
	tests := []struct {
		name  string
		left  Tag
		right Tag
		want  int
		less  bool
	}{
		{
			name:  "equal tags",
			left:  NewTag(0x0010, 0x0010),
			right: NewTag(0x0010, 0x0010),
			want:  0,
			less:  false,
		},
		{
			name:  "group differs",
			left:  NewTag(0x0008, 0x0010),
			right: NewTag(0x0010, 0x0001),
			want:  -1,
			less:  true,
		},
		{
			name:  "element differs",
			left:  NewTag(0x0010, 0x0010),
			right: NewTag(0x0010, 0x0020),
			want:  -1,
			less:  true,
		},
		{
			name:  "boundary values",
			left:  NewTag(0xFFFF, 0xFFFF),
			right: NewTag(0x0000, 0x0000),
			want:  1,
			less:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.left.Compare(tt.right); got != tt.want {
				t.Fatalf("Compare() = %d, want %d", got, tt.want)
			}
			if got := tt.left.Less(tt.right); got != tt.less {
				t.Fatalf("Less() = %v, want %v", got, tt.less)
			}
		})
	}
}

func TestTagSortSliceWithLess(t *testing.T) {
	tags := []Tag{
		NewTag(0xFFFF, 0xFFFF),
		NewTag(0x0010, 0x0020),
		NewTag(0x0008, 0x1030),
		NewTag(0x0010, 0x0010),
		NewTag(0x0000, 0x0000),
	}

	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Less(tags[j])
	})

	want := []Tag{
		NewTag(0x0000, 0x0000),
		NewTag(0x0008, 0x1030),
		NewTag(0x0010, 0x0010),
		NewTag(0x0010, 0x0020),
		NewTag(0xFFFF, 0xFFFF),
	}

	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("sorted tags[%d] = %v, want %v", i, tags[i], want[i])
		}
	}
}
