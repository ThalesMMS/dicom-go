package pixeldata

import (
	"context"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestMetadataValidationRuleIsOptInAndReportsInconsistentPixelMetadata(t *testing.T) {
	dataset := core.DataSet{Elements: append(
		pixelMetadataElements(1, 2, 1, 8, 12, 11, 0, nil, dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2")),
		dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2}),
	)}
	without, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if without.Report.Count(validation.CodePixelMetadata) != 0 {
		t.Fatalf("pixel rule ran without registration: %#v", without.Report.Findings)
	}
	with, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		DataSetRules: []validation.DataSetRuleRegistration{{Name: "pixel-metadata", Rule: MetadataValidationRule()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if with.Report.Count(validation.CodePixelMetadata) == 0 {
		t.Fatalf("registered pixel rule produced no finding: %#v", with.Report.Findings)
	}
}

func TestMetadataValidationRuleAcceptsConsistentNativeMetadata(t *testing.T) {
	dataset := core.DataSet{Elements: append(
		pixelMetadataElements(1, 2, 1, 8, 8, 7, 0, nil, dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2")),
		dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2}),
	)}
	result, err := validation.ValidateDataSet(context.Background(), dataset, validation.Options{
		DataSetRules: []validation.DataSetRuleRegistration{{Name: "pixel-metadata", Rule: MetadataValidationRule()}},
	})
	if err != nil || result.Report.Count(validation.CodePixelMetadata) != 0 {
		t.Fatalf("consistent pixel metadata: report=%#v err=%v", result.Report, err)
	}
}
