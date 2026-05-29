package object

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

// ValidationOptions configures opt-in SOP Class validation.
type ValidationOptions struct {
	// SOPClassUID overrides the SOP Class UID discovered from (0008,0016).
	SOPClassUID string
	// Hooks contains caller-provided validation rules. An empty hook list is a no-op.
	Hooks []SOPClassValidationHook
}

// SOPClassValidationContext is passed to SOP Class validation hooks.
type SOPClassValidationContext struct {
	SOPClassUID string
	Object      *Object
}

// SOPClassValidationHook inspects an object for SOP Class-specific findings.
type SOPClassValidationHook interface {
	ValidateSOPClass(SOPClassValidationContext) []ValidationFinding
}

// SOPClassValidationHookFunc adapts a function into a validation hook.
type SOPClassValidationHookFunc func(SOPClassValidationContext) []ValidationFinding

func (f SOPClassValidationHookFunc) ValidateSOPClass(ctx SOPClassValidationContext) []ValidationFinding {
	if f == nil {
		return nil
	}
	return f(ctx)
}

// RequiredAttribute identifies an attribute a caller wants to require.
type RequiredAttribute struct {
	Tag     core.Tag
	Keyword string
}

// RequiredAttributeRule checks that a scoped list of attributes is present.
type RequiredAttributeRule struct {
	// SOPClassUID limits this rule to a SOP Class. Empty applies to any SOP Class.
	SOPClassUID string
	Attributes  []RequiredAttribute
}

func (r RequiredAttributeRule) ValidateSOPClass(ctx SOPClassValidationContext) []ValidationFinding {
	if r.SOPClassUID != "" && r.SOPClassUID != ctx.SOPClassUID {
		return nil
	}

	var findings []ValidationFinding
	for _, attr := range r.Attributes {
		if ctx.Object != nil && ctx.Object.Has(attr.Tag) {
			continue
		}
		label := attr.Keyword
		if label == "" {
			label = attr.Tag.String()
		}
		findings = append(findings, ValidationFinding{
			SOPClassUID: ctx.SOPClassUID,
			Tag:         attr.Tag,
			Keyword:     attr.Keyword,
			Message:     fmt.Sprintf("missing required attribute %s (%s)", label, attr.Tag),
		})
	}
	return findings
}

// ValidationFinding describes one opt-in validation problem.
type ValidationFinding struct {
	SOPClassUID string
	Tag         core.Tag
	Keyword     string
	Message     string
}

// ValidationError aggregates validation findings.
type ValidationError struct {
	Findings []ValidationFinding
}

func (e ValidationError) Error() string {
	if len(e.Findings) == 0 {
		return "dicom: SOP Class validation failed"
	}

	var b strings.Builder
	if len(e.Findings) == 1 {
		b.WriteString("dicom: SOP Class validation failed: ")
		writeValidationFinding(&b, e.Findings[0])
		return b.String()
	}

	fmt.Fprintf(&b, "dicom: SOP Class validation failed with %d findings: ", len(e.Findings))
	for i, finding := range e.Findings {
		if i > 0 {
			b.WriteString("; ")
		}
		writeValidationFinding(&b, finding)
	}
	return b.String()
}

func writeValidationFinding(b *strings.Builder, finding ValidationFinding) {
	if finding.SOPClassUID != "" {
		fmt.Fprintf(b, "SOP Class %s: ", finding.SOPClassUID)
	}
	if finding.Message != "" {
		b.WriteString(finding.Message)
		return
	}
	if finding.Keyword != "" {
		fmt.Fprintf(b, "%s (%s)", finding.Keyword, finding.Tag)
		return
	}
	b.WriteString(finding.Tag.String())
}

// ValidateSOPClass runs caller-provided SOP Class validation hooks.
//
// This method is intentionally opt-in and does not run during parsing or
// reading. It is a narrow extension point for applications that need targeted
// checks without treating this package as a complete conformance validator.
func (o *Object) ValidateSOPClass(opts ValidationOptions) error {
	sopClassUID := opts.SOPClassUID
	if sopClassUID == "" && o != nil {
		sopClassUID, _ = o.GetString(tagSOPClassUID)
	}

	ctx := SOPClassValidationContext{
		SOPClassUID: sopClassUID,
		Object:      o,
	}

	var findings []ValidationFinding
	for _, hook := range opts.Hooks {
		if hook == nil {
			continue
		}
		for _, finding := range hook.ValidateSOPClass(ctx) {
			if finding.SOPClassUID == "" {
				finding.SOPClassUID = sopClassUID
			}
			findings = append(findings, finding)
		}
	}
	if len(findings) == 0 {
		return nil
	}
	return ValidationError{Findings: findings}
}
