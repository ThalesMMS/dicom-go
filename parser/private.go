package parser

import (
	"errors"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

// PrivateDiagnostic identifies a structural resolution issue without embedding
// creator strings or attribute values. Offset is the parser position where the
// issue was detected; ItemOffset identifies the enclosing Item when present.
type PrivateDiagnostic struct {
	Tag           core.Tag
	Offset        int64
	ItemOffset    int64
	ItemOffsetSet bool
	Err           error
}

// PrivateDiagnostics returns a detached, bounded snapshot and whether further
// issues were omitted. Unknown definitions are diagnostics, never guessed VRs.
func (r *Reader) PrivateDiagnostics() ([]PrivateDiagnostic, bool) {
	if r == nil {
		return nil, false
	}
	return append([]PrivateDiagnostic(nil), r.privateDiagnostics...), r.privateDiagnosticsTruncated
}

func (r *Reader) resetPrivateScopes() {
	if !dictionary.HasPrivateDictionary(r.dict) {
		return
	}
	r.privateRoot, r.privateInitError = dictionary.NewPrivateReservations(r.maxPrivateCreators)
	if r.maxPrivateDiagnostics < 0 {
		r.privateInitError = dictionary.ErrPrivateResourceLimit
	}
	if r.maxPrivateDiagnostics == 0 {
		r.maxPrivateDiagnostics = dictionary.DefaultMaxPrivateIssues
	}
	r.privateDiagnostics = nil
	r.privateDiagnosticsTruncated = false
}
func (r *Reader) privateScope() *dictionary.PrivateReservations {
	for i := len(r.seqDelimiters) - 1; i >= 0; i-- {
		if r.seqDelimiters[i].typ == seqTokenTypeItem {
			return r.seqDelimiters[i].privateScope
		}
	}
	return r.privateRoot
}
func (r *Reader) privateIssue(tag core.Tag, err error) {
	if len(r.privateDiagnostics) >= r.maxPrivateDiagnostics {
		r.privateDiagnosticsTruncated = true
		return
	}
	d := PrivateDiagnostic{Tag: tag, Offset: r.Position(), Err: err}
	for i := len(r.seqDelimiters) - 1; i >= 0; i-- {
		f := r.seqDelimiters[i]
		if f.typ == seqTokenTypeItem && f.baseOffset >= 8 {
			d.ItemOffset = int64(f.baseOffset - 8)
			d.ItemOffsetSet = true
			break
		}
	}
	r.privateDiagnostics = append(r.privateDiagnostics, d)
}
func (r *Reader) capturePrivateReservation(e core.Element) error {
	if r.privateRoot == nil || !dictionary.IsPrivateCreatorTag(e.Tag()) {
		return nil
	}
	if err := r.privateScope().Observe(e); err != nil {
		r.privateIssue(e.Tag(), err)
		if errors.Is(err, dictionary.ErrPrivateResourceLimit) || r.rejectInvalidPrivateCreators {
			return err
		}
	}
	return nil
}
func (r *Reader) lookupPrivateVR(tag core.Tag) core.VR {
	if dictionary.IsPrivateCreatorTag(tag) {
		// Explicit-VR headers never enter this path. The normative creator VR
		// is LO; legacy absolute-tag overlays still win when supplied first.
		if e, ok := dictionary.LookupScopedEntry(r.dict, tag, ""); ok && e.VR != "" {
			return e.VR
		}
		return core.VRLO
	}
	creator, scopeErr := r.privateScope().Creator(tag)
	if scopeErr != nil {
		creator = ""
	}
	if e, ok := dictionary.LookupScopedEntry(r.dict, tag, creator); ok {
		if e.VR != "" {
			return e.VR
		}
		return core.VRUN
	}
	if scopeErr != nil {
		r.privateIssue(tag, scopeErr)
	} else {
		r.privateIssue(tag, dictionary.ErrPrivateDefinitionMissing)
	}
	return core.VRUN
}
