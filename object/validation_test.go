package object

import (
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

const validationCTImageStorageUID = "1.2.840.10008.5.1.4.1.1.2"

var (
	validationPatientNameTag = core.NewTag(0x0010, 0x0010)
	validationPatientIDTag   = core.NewTag(0x0010, 0x0020)
)

func TestRequiredAttributeRuleReportsMissingAttributesForMatchingSOPClass(t *testing.T) {
	obj := validationObject(
		dicomtest.NewUIElement(tagSOPClassUID, validationCTImageStorageUID),
		dicomtest.NewPNElement(validationPatientNameTag, "TEST^PATIENT"),
	)
	rule := RequiredAttributeRule{
		SOPClassUID: validationCTImageStorageUID,
		Attributes: []RequiredAttribute{
			{Tag: validationPatientNameTag, Keyword: "PatientName"},
			{Tag: validationPatientIDTag, Keyword: "PatientID"},
		},
	}

	err := obj.ValidateSOPClass(ValidationOptions{
		Hooks: []SOPClassValidationHook{rule},
	})
	if err == nil {
		t.Fatal("ValidateSOPClass() error = nil, want missing PatientID")
	}

	var validationErr ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error type = %T, want ValidationError", err)
	}
	if len(validationErr.Findings) != 1 {
		t.Fatalf("finding count = %d, want 1: %#v", len(validationErr.Findings), validationErr.Findings)
	}
	finding := validationErr.Findings[0]
	if finding.SOPClassUID != validationCTImageStorageUID {
		t.Fatalf("finding SOPClassUID = %q, want %q", finding.SOPClassUID, validationCTImageStorageUID)
	}
	if finding.Tag != validationPatientIDTag {
		t.Fatalf("finding tag = %s, want %s", finding.Tag, validationPatientIDTag)
	}
	if finding.Keyword != "PatientID" {
		t.Fatalf("finding keyword = %q, want PatientID", finding.Keyword)
	}
	if !strings.Contains(err.Error(), "PatientID") || !strings.Contains(err.Error(), validationPatientIDTag.String()) {
		t.Fatalf("error = %q, want PatientID and tag", err)
	}
}

func TestRequiredAttributeRulePassesWhenAttributesArePresent(t *testing.T) {
	obj := validationObject(
		dicomtest.NewUIElement(tagSOPClassUID, validationCTImageStorageUID),
		dicomtest.NewPNElement(validationPatientNameTag, "TEST^PATIENT"),
		dicomtest.NewStringElement(validationPatientIDTag, core.VRLO, "TESTID001"),
	)
	rule := RequiredAttributeRule{
		SOPClassUID: validationCTImageStorageUID,
		Attributes: []RequiredAttribute{
			{Tag: validationPatientNameTag, Keyword: "PatientName"},
			{Tag: validationPatientIDTag, Keyword: "PatientID"},
		},
	}

	if err := obj.ValidateSOPClass(ValidationOptions{Hooks: []SOPClassValidationHook{rule}}); err != nil {
		t.Fatalf("ValidateSOPClass() = %v, want nil", err)
	}
}

func TestRequiredAttributeRuleSkipsDifferentSOPClass(t *testing.T) {
	obj := validationObject(
		dicomtest.NewUIElement(tagSOPClassUID, dicomtest.TestSOPClassUID),
	)
	rule := RequiredAttributeRule{
		SOPClassUID: validationCTImageStorageUID,
		Attributes: []RequiredAttribute{
			{Tag: validationPatientIDTag, Keyword: "PatientID"},
		},
	}

	if err := obj.ValidateSOPClass(ValidationOptions{Hooks: []SOPClassValidationHook{rule}}); err != nil {
		t.Fatalf("ValidateSOPClass() = %v, want nil for non-matching SOP Class", err)
	}
}

func TestValidateSOPClassInvokesCustomHook(t *testing.T) {
	obj := validationObject()
	var gotSOPClass string
	hook := SOPClassValidationHookFunc(func(ctx SOPClassValidationContext) []ValidationFinding {
		gotSOPClass = ctx.SOPClassUID
		if ctx.Object != obj {
			t.Fatalf("hook object = %p, want %p", ctx.Object, obj)
		}
		return []ValidationFinding{{
			SOPClassUID: ctx.SOPClassUID,
			Tag:         validationPatientIDTag,
			Keyword:     "PatientID",
			Message:     "custom rule failed",
		}}
	})

	err := obj.ValidateSOPClass(ValidationOptions{
		SOPClassUID: validationCTImageStorageUID,
		Hooks:       []SOPClassValidationHook{hook},
	})
	if gotSOPClass != validationCTImageStorageUID {
		t.Fatalf("hook SOPClassUID = %q, want %q", gotSOPClass, validationCTImageStorageUID)
	}
	var validationErr ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error type = %T, want ValidationError", err)
	}
	if len(validationErr.Findings) != 1 || validationErr.Findings[0].Message != "custom rule failed" {
		t.Fatalf("findings = %#v, want custom failure", validationErr.Findings)
	}
}

func validationObject(elements ...core.Element) *Object {
	return FromElements(elements, std.Dictionary)
}
