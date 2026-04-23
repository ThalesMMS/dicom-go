package dictionary_test

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

type stubDictionary struct {
	entry dictionary.Entry
}

func (s stubDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	if tag != s.entry.Tag {
		return dictionary.Entry{}, false
	}
	return s.entry, true
}

func (s stubDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

func TestLookupVR(t *testing.T) {
	tests := []struct {
		name string
		dict dictionary.DataDictionary
		tag  core.Tag
		want core.VR
	}{
		{
			name: "nil dictionary",
			dict: nil,
			tag:  core.NewTag(0x0010, 0x0010),
			want: core.VRUN,
		},
		{
			name: "unknown tag",
			dict: stubDictionary{
				entry: dictionary.Entry{
					Tag: core.NewTag(0x0010, 0x0010),
					VR:  core.VRPN,
				},
			},
			tag:  core.NewTag(0x9999, 0x0001),
			want: core.VRUN,
		},
		{
			name: "known tag",
			dict: stubDictionary{
				entry: dictionary.Entry{
					Tag: core.NewTag(0x0010, 0x0010),
					VR:  core.VRPN,
				},
			},
			tag:  core.NewTag(0x0010, 0x0010),
			want: core.VRPN,
		},
		{
			name: "entry with empty vr",
			dict: stubDictionary{
				entry: dictionary.Entry{
					Tag: core.NewTag(0x0010, 0x0010),
					VR:  "",
				},
			},
			tag:  core.NewTag(0x0010, 0x0010),
			want: core.VRUN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dictionary.LookupVR(tt.dict, tt.tag); got != tt.want {
				t.Fatalf("LookupVR(%T, %v) = %s, want %s", tt.dict, tt.tag, got, tt.want)
			}
		})
	}
}
