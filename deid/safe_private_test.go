package deid

import (
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestSafePrivateRegistryLookupAndDefensiveCopies(t *testing.T) {
	provenance := SafePrivateProvenance{
		Version:  "vendor-rules-2026-08",
		Checksum: strings.Repeat("a", 64),
	}
	rules := []SafePrivateRule{
		{Tag: core.NewTag(0x0011, 0x1010), Creator: "VENDOR_A", VR: core.VRLO, VM: 1, Action: ProfileActionKeep},
		{Tag: core.NewTag(0x0011, 0x2010), Creator: "VENDOR_B", VR: core.VRDS, VM: 2, Action: ProfileActionClean},
	}
	registry, err := NewSafePrivateRegistry(provenance, rules)
	if err != nil {
		t.Fatalf("NewSafePrivateRegistry() error = %v", err)
	}

	// Mutating caller-owned input after construction must not affect the
	// published snapshot.
	rules[0].Creator = "MUTATED"
	rules[0].Action = ProfileActionRemove
	if action, ok := registry.Lookup(core.NewTag(0x0011, 0x1010), "VENDOR_A"); !ok || action != ProfileActionKeep {
		t.Fatalf("Lookup() = %q, %v, want keep, true", action, ok)
	}
	if _, ok := registry.Lookup(core.NewTag(0x0011, 0x1010), "MUTATED"); ok {
		t.Fatal("Lookup() observed mutation of constructor input")
	}

	// Matching uses the group, relative low-byte element and normalized creator.
	// The creator block high byte is allowed to move between encoded instances.
	creatorTag := core.NewTag(0x0011, 0x0022)
	relocatedDataTag := core.NewTag(creatorTag.Group, creatorTag.Element<<8|0x10)
	if relocatedDataTag != core.NewTag(0x0011, 0x2210) {
		t.Fatalf("test setup relocated tag = %s", relocatedDataTag)
	}
	if action, ok := registry.Lookup(relocatedDataTag, "VENDOR_A"); !ok || action != ProfileActionKeep {
		t.Fatalf("relocated block Lookup() = %q, %v, want keep, true", action, ok)
	}
	if action, ok := registry.Lookup(relocatedDataTag, " VENDOR_A "); !ok || action != ProfileActionKeep {
		t.Fatalf("padded creator Lookup() = %q, %v, want keep, true", action, ok)
	}
	matchedRule, ok := registry.lookup(relocatedDataTag, "VENDOR_A")
	if !ok || matchedRule != (SafePrivateRule{
		Tag: core.NewTag(0x0011, 0x1010), Creator: "VENDOR_A", VR: core.VRLO, VM: 1, Action: ProfileActionKeep,
	}) {
		t.Fatalf("relocated block lookup() = %#v, %v", matchedRule, ok)
	}
	for _, lookup := range []struct {
		tag     core.Tag
		creator string
	}{
		{core.NewTag(0x0011, 0x1010), "vendor_a"},
		{core.NewTag(0x0011, 0x2011), "VENDOR_A"},
		{core.NewTag(0x0013, 0x1010), "VENDOR_A"},
	} {
		if _, ok := registry.Lookup(lookup.tag, lookup.creator); ok {
			t.Errorf("Lookup(%s, alternate creator) unexpectedly matched", lookup.tag)
		}
	}

	if got := registry.Provenance(); got != provenance {
		t.Fatalf("Provenance() = %#v, want %#v", got, provenance)
	}
	gotRules := registry.Rules()
	if len(gotRules) != 2 || registry.Len() != 2 {
		t.Fatalf("registry rule counts = %d, %d, want 2", len(gotRules), registry.Len())
	}
	gotRules[0].Creator = "MUTATED_OUTPUT"
	gotRules[0].Action = ProfileActionRemove
	if action, ok := registry.Lookup(core.NewTag(0x0011, 0x1010), "VENDOR_A"); !ok || action != ProfileActionKeep {
		t.Fatalf("Lookup() after Rules mutation = %q, %v, want keep, true", action, ok)
	}
}

func TestSafePrivateRegistryRejectsInvalidInputWithoutCreatorDisclosure(t *testing.T) {
	validProvenance := SafePrivateProvenance{Version: "v1", Checksum: strings.Repeat("b", 64)}
	validRule := SafePrivateRule{
		Tag: core.NewTag(0x0011, 0x1010), Creator: "TOP_SECRET_CREATOR", VR: core.VRLO, VM: 1, Action: ProfileActionKeep,
	}
	tests := []struct {
		name       string
		provenance SafePrivateProvenance
		rules      []SafePrivateRule
	}{
		{name: "empty version", provenance: SafePrivateProvenance{Checksum: validProvenance.Checksum}, rules: []SafePrivateRule{validRule}},
		{name: "whitespace version", provenance: SafePrivateProvenance{Version: " \t", Checksum: validProvenance.Checksum}, rules: []SafePrivateRule{validRule}},
		{name: "short checksum", provenance: SafePrivateProvenance{Version: "v1", Checksum: strings.Repeat("a", 63)}, rules: []SafePrivateRule{validRule}},
		{name: "non-hex checksum", provenance: SafePrivateProvenance{Version: "v1", Checksum: strings.Repeat("g", 64)}, rules: []SafePrivateRule{validRule}},
		{name: "public tag", provenance: validProvenance, rules: []SafePrivateRule{{Tag: core.NewTag(0x0010, 0x1010), Creator: validRule.Creator, Action: ProfileActionKeep}}},
		{name: "private creator tag", provenance: validProvenance, rules: []SafePrivateRule{{Tag: core.NewTag(0x0011, 0x0010), Creator: validRule.Creator, Action: ProfileActionKeep}}},
		{name: "empty creator", provenance: validProvenance, rules: []SafePrivateRule{{Tag: validRule.Tag, Action: ProfileActionKeep}}},
		{name: "whitespace creator", provenance: validProvenance, rules: []SafePrivateRule{{Tag: validRule.Tag, Creator: " \t", Action: ProfileActionKeep}}},
		{name: "missing VR", provenance: validProvenance, rules: []SafePrivateRule{{Tag: validRule.Tag, Creator: validRule.Creator, VM: 1, Action: ProfileActionKeep}}},
		{name: "missing VM", provenance: validProvenance, rules: []SafePrivateRule{{Tag: validRule.Tag, Creator: validRule.Creator, VR: core.VRLO, Action: ProfileActionKeep}}},
		{name: "remove action", provenance: validProvenance, rules: []SafePrivateRule{{Tag: validRule.Tag, Creator: validRule.Creator, Action: ProfileActionRemove}}},
		{name: "duplicate", provenance: validProvenance, rules: []SafePrivateRule{validRule, validRule}},
		{name: "duplicate relocated block", provenance: validProvenance, rules: []SafePrivateRule{validRule, {Tag: core.NewTag(0x0011, 0x2210), Creator: validRule.Creator, VR: core.VRLO, VM: 1, Action: ProfileActionClean}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSafePrivateRegistry(test.provenance, test.rules)
			if err == nil {
				t.Fatal("NewSafePrivateRegistry() error = nil")
			}
			if strings.Contains(err.Error(), validRule.Creator) {
				t.Fatalf("error disclosed private creator: %v", err)
			}
		})
	}
}

func TestSafePrivateRegistryAcceptsSHA256HexCase(t *testing.T) {
	registry, err := NewSafePrivateRegistry(SafePrivateProvenance{
		Version: "v1", Checksum: strings.Repeat("AB", 32),
	}, nil)
	if err != nil {
		t.Fatalf("NewSafePrivateRegistry() uppercase checksum error = %v", err)
	}
	if registry.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", registry.Len())
	}
}

func TestSafePrivateRegistryConcurrentReads(t *testing.T) {
	registry, err := NewSafePrivateRegistry(SafePrivateProvenance{
		Version: "v1", Checksum: strings.Repeat("c", 64),
	}, []SafePrivateRule{{
		Tag: core.NewTag(0x0011, 0x1010), Creator: "VENDOR", VR: core.VRLO, VM: 1, Action: ProfileActionKeep,
	}})
	if err != nil {
		t.Fatalf("NewSafePrivateRegistry() error = %v", err)
	}

	var wait sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 1_000; iteration++ {
				if action, ok := registry.Lookup(core.NewTag(0x0011, 0x1010), "VENDOR"); !ok || action != ProfileActionKeep {
					t.Errorf("Lookup() = %q, %v", action, ok)
					return
				}
				_ = registry.Provenance()
				_ = registry.Rules()
			}
		}()
	}
	wait.Wait()
}

func TestNilSafePrivateRegistryReadsAsEmpty(t *testing.T) {
	var registry *SafePrivateRegistry
	if _, ok := registry.Lookup(core.NewTag(0x0011, 0x1010), "VENDOR"); ok {
		t.Fatal("nil registry lookup matched")
	}
	if registry.Len() != 0 || registry.Provenance() != (SafePrivateProvenance{}) || registry.Rules() != nil {
		t.Fatal("nil registry did not behave as empty")
	}
}
