package dictionary_test

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

type stubDictionary struct {
	entry   dictionary.Entry
	keyword string
}

type contextualStubDictionary struct {
	stubDictionary
	spec dictionary.VRSpec
}

func (s contextualStubDictionary) VRSpecByTag(tag core.Tag) (dictionary.VRSpec, bool) {
	if tag != s.entry.Tag {
		return dictionary.VRSpec{}, false
	}
	return s.spec, true
}

func (s stubDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	if tag != s.entry.Tag {
		return dictionary.Entry{}, false
	}
	return s.entry, true
}

func (s stubDictionary) ByKeyword(keyword string) (dictionary.Entry, bool) {
	if s.keyword == "" || !strings.EqualFold(keyword, s.keyword) {
		return dictionary.Entry{}, false
	}
	return s.entry, true
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

func TestChainComposesDictionariesInOrder(t *testing.T) {
	tag := core.NewTag(0x0011, 0x1001)
	private := stubDictionary{
		entry: dictionary.Entry{
			Tag:     tag,
			VR:      core.VRLO,
			Keyword: "PrivateLabel",
		},
		keyword: "PrivateLabel",
	}
	fallback := stubDictionary{
		entry: dictionary.Entry{
			Tag:     tag,
			VR:      core.VRPN,
			Keyword: "FallbackLabel",
		},
		keyword: "FallbackLabel",
	}
	chain := dictionary.Chain{nil, private, fallback}

	got, ok := chain.ByTag(tag)
	if !ok {
		t.Fatalf("ByTag(%s) did not find private overlay entry", tag)
	}
	if got.VR != core.VRLO {
		t.Fatalf("ByTag(%s) VR = %s, want %s", tag, got.VR, core.VRLO)
	}
	if got := dictionary.LookupVR(chain, tag); got != core.VRLO {
		t.Fatalf("LookupVR(chain, %s) = %s, want %s", tag, got, core.VRLO)
	}

	got, ok = chain.ByKeyword("fallbacklabel")
	if !ok {
		t.Fatal("ByKeyword did not fall through to fallback dictionary")
	}
	if got.Keyword != "FallbackLabel" {
		t.Fatalf("ByKeyword fallback keyword = %q, want FallbackLabel", got.Keyword)
	}
}

func TestLookupVRSpecPreservesContextualAlternatives(t *testing.T) {
	tag := core.NewTag(0x0028, 0x0106)
	contextual := contextualStubDictionary{
		stubDictionary: stubDictionary{entry: dictionary.Entry{Tag: tag, VR: core.VRUS}},
		spec:           dictionary.NewContextualVRSpec(dictionary.ContextualVRXS),
	}

	spec, ok := dictionary.LookupVRSpec(contextual, tag)
	if !ok {
		t.Fatal("LookupVRSpec did not find contextual entry")
	}
	if spec.Context() != dictionary.ContextualVRXS {
		t.Fatalf("context = %q, want %q", spec.Context(), dictionary.ContextualVRXS)
	}
	if !spec.Contains(core.VRUS) || !spec.Contains(core.VRSS) || spec.Contains(core.VROW) {
		t.Fatalf("xs alternatives = %v, want US and SS only", spec.Values())
	}

	values := spec.Values()
	values[0] = core.VRUN
	if !spec.Contains(core.VRUS) {
		t.Fatal("Values returned storage aliased to immutable VRSpec")
	}
}

func TestChainVRSpecUsesSameFirstWinningDictionaryAsEntry(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	overlay := stubDictionary{entry: dictionary.Entry{Tag: tag, VR: core.VRUN}}
	fallback := contextualStubDictionary{
		stubDictionary: stubDictionary{entry: dictionary.Entry{Tag: tag, VR: core.VROW}},
		spec:           dictionary.NewContextualVRSpec(dictionary.ContextualVRPX),
	}

	spec, ok := dictionary.LookupVRSpec(dictionary.Chain{overlay, fallback}, tag)
	if !ok {
		t.Fatal("LookupVRSpec did not find overlay entry")
	}
	if got := spec.Values(); len(got) != 1 || got[0] != core.VRUN {
		t.Fatalf("overlay VR spec = %v, want [UN]", got)
	}
}
