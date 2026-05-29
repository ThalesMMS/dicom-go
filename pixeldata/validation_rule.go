package pixeldata

import (
	"context"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/validation"
)

type metadataValidationRule struct{}

// MetadataValidationRule returns an opt-in adapter for the shared validation
// engine. It reuses pixeldata metadata extraction and reports only stable,
// PHI-redacted diagnostics.
func MetadataValidationRule() validation.DataSetRule { return metadataValidationRule{} }

func (metadataValidationRule) ValidateDataSet(_ context.Context, ctx validation.DataSetContext) []validation.Finding {
	pixelElement, ok := findDataSetElement(ctx.DataSet, core.TagPixelData)
	if !ok {
		return nil
	}
	obj := object.FromDataSet(ctx.DataSet, ctx.Dictionary)
	obj.SetValueByteOrder(ctx.ByteOrder)
	metadata, err := ExtractMetadata(obj)
	if err != nil {
		return []validation.Finding{pixelFinding(core.TagPixelData)}
	}
	var findings []validation.Finding
	if metadata.Rows == 0 {
		findings = append(findings, pixelFinding(tagRows))
	}
	if metadata.Columns == 0 {
		findings = append(findings, pixelFinding(tagColumns))
	}
	if metadata.SamplesPerPixel == 0 {
		findings = append(findings, pixelFinding(tagSamplesPerPixel))
	}
	if metadata.BitsAllocated != 1 && (metadata.BitsAllocated == 0 || metadata.BitsAllocated > 64 || metadata.BitsAllocated%8 != 0) {
		findings = append(findings, pixelFinding(tagBitsAllocated))
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated {
		findings = append(findings, pixelFinding(tagBitsStored))
	}
	if metadata.BitsStored > 0 && (metadata.HighBit < metadata.BitsStored-1 || metadata.HighBit >= metadata.BitsAllocated) {
		findings = append(findings, pixelFinding(tagHighBit))
	}
	if metadata.PixelRepresentation > 1 {
		findings = append(findings, pixelFinding(tagPixelRepresentation))
	}
	if metadata.SamplesPerPixel > 1 && (!metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration > 1) {
		findings = append(findings, pixelFinding(tagPlanarConfiguration))
	}
	if raw, rawOK := pixelElement.RawBytes(); rawOK {
		expected := metadata.TotalSize()
		actual := int64(len(raw))
		if expected <= 0 || actual < expected || actual > expected+1 {
			findings = append(findings, pixelFinding(core.TagPixelData))
		}
	}
	return findings
}

func findDataSetElement(dataset core.DataSet, tag core.Tag) (core.Element, bool) {
	for i := len(dataset.Elements) - 1; i >= 0; i-- {
		if dataset.Elements[i].Tag() == tag {
			return dataset.Elements[i], true
		}
	}
	return core.Element{}, false
}

func pixelFinding(tag core.Tag) validation.Finding {
	return validation.Finding{Tag: tag, Code: validation.CodePixelMetadata, Message: "Pixel Data metadata is inconsistent"}
}
