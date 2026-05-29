package deid

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

// ErrInvalidSafePrivateRegistry reports invalid provenance or an invalid,
// ambiguous safe-private rule. Error details never contain private creators.
var ErrInvalidSafePrivateRegistry = errors.New("deid: invalid safe-private registry")

// SafePrivateProvenance identifies one externally reviewed set of safe-private
// rules. Checksum is a hexadecimal SHA-256 digest of the reviewed source.
type SafePrivateProvenance struct {
	Version  string
	Checksum string
}

// SafePrivateRule permits keeping or cleaning one private data element for an
// exact private creator. Tag is a physical representative; lookup follows the
// creator if its block is relocated in another encoded instance.
type SafePrivateRule struct {
	Tag     core.Tag
	Creator string
	VR      core.VR
	VM      int
	Action  ProfileAction
}

type safePrivateKey struct {
	group    uint16
	relative uint8
	creator  string
}

// SafePrivateRegistry is an immutable, concurrently readable snapshot of
// verified private-creator rules.
type SafePrivateRegistry struct {
	provenance SafePrivateProvenance
	rules      []SafePrivateRule
	byKey      map[safePrivateKey]SafePrivateRule
}

// NewSafePrivateRegistry validates and defensively copies verified
// safe-private rules. Only exact private data elements with Keep or Clean
// actions are accepted.
func NewSafePrivateRegistry(provenance SafePrivateProvenance, rules []SafePrivateRule) (*SafePrivateRegistry, error) {
	if strings.TrimSpace(provenance.Version) == "" {
		return nil, fmt.Errorf("%w: provenance version is required", ErrInvalidSafePrivateRegistry)
	}
	if !validSHA256Hex(provenance.Checksum) {
		return nil, fmt.Errorf("%w: provenance checksum must be a SHA-256 hexadecimal digest", ErrInvalidSafePrivateRegistry)
	}

	registry := &SafePrivateRegistry{
		provenance: provenance,
		rules:      append([]SafePrivateRule(nil), rules...),
		byKey:      make(map[safePrivateKey]SafePrivateRule, len(rules)),
	}
	for index, rule := range registry.rules {
		if !rule.Tag.IsPrivate() || rule.Tag.Element < 0x1000 {
			return nil, fmt.Errorf("%w: rule %d must identify a private data element", ErrInvalidSafePrivateRegistry, index+1)
		}
		if strings.TrimSpace(rule.Creator) == "" {
			return nil, fmt.Errorf("%w: rule %d requires a private creator", ErrInvalidSafePrivateRegistry, index+1)
		}
		if _, err := core.ParseVR(string(rule.VR)); err != nil || rule.VM <= 0 {
			return nil, fmt.Errorf("%w: rule %d requires an exact VR and positive VM", ErrInvalidSafePrivateRegistry, index+1)
		}
		if rule.Action != ProfileActionKeep && rule.Action != ProfileActionClean {
			return nil, fmt.Errorf("%w: rule %d action must be keep or clean", ErrInvalidSafePrivateRegistry, index+1)
		}
		key := safePrivateLookupKey(rule.Tag, rule.Creator)
		if _, duplicate := registry.byKey[key]; duplicate {
			return nil, fmt.Errorf("%w: rule %d duplicates an earlier group, relative element, and creator", ErrInvalidSafePrivateRegistry, index+1)
		}
		registry.byKey[key] = rule
	}
	return registry, nil
}

func validSHA256Hex(checksum string) bool {
	if len(checksum) != sha256HexLength {
		return false
	}
	_, err := hex.DecodeString(checksum)
	return err == nil
}

const sha256HexLength = 64

func safePrivateLookupKey(tag core.Tag, creator string) safePrivateKey {
	return safePrivateKey{group: tag.Group, relative: uint8(tag.Element), creator: strings.TrimSpace(creator)}
}

// lookup returns the verified rule for tag and creator. A private creator's
// block occupies the high byte of a private data element and may be reassigned
// between instances, so only the low-byte relative element participates in the
// key. The group and creator remain exact.
func (registry *SafePrivateRegistry) lookup(tag core.Tag, creator string) (SafePrivateRule, bool) {
	if registry == nil || !tag.IsPrivate() || tag.Element < 0x1000 {
		return SafePrivateRule{}, false
	}
	rule, ok := registry.byKey[safePrivateLookupKey(tag, creator)]
	return rule, ok
}

// Lookup reports the verified action for tag and normalized creator. It follows
// safe private data across creator-block relocation within the same group.
func (registry *SafePrivateRegistry) Lookup(tag core.Tag, creator string) (ProfileAction, bool) {
	rule, ok := registry.lookup(tag, creator)
	if !ok {
		return "", false
	}
	return rule.Action, true
}

// Provenance returns the registry's immutable provenance value.
func (registry *SafePrivateRegistry) Provenance() SafePrivateProvenance {
	if registry == nil {
		return SafePrivateProvenance{}
	}
	return registry.provenance
}

// Rules returns a detached copy of the reviewed rules in input order.
func (registry *SafePrivateRegistry) Rules() []SafePrivateRule {
	if registry == nil {
		return nil
	}
	return append([]SafePrivateRule(nil), registry.rules...)
}

// Len returns the number of reviewed rules in the registry.
func (registry *SafePrivateRegistry) Len() int {
	if registry == nil {
		return 0
	}
	return len(registry.rules)
}
