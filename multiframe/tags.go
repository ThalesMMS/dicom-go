package multiframe

import (
	"github.com/ThalesMMS/dicom-go/core"
)

var (
	tagSOPClassUID    = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID = core.NewTag(0x0008, 0x0018)
	tagModality       = core.NewTag(0x0008, 0x0060)
	tagImageType      = core.NewTag(0x0008, 0x0008)
	tagFrameType      = core.NewTag(0x0008, 0x9007)

	tagManufacturer             = core.NewTag(0x0008, 0x0070)
	tagInstitutionName          = core.NewTag(0x0008, 0x0080)
	tagCodeValue                = core.NewTag(0x0008, 0x0100)
	tagCodingSchemeDesignator   = core.NewTag(0x0008, 0x0102)
	tagCodeMeaning              = core.NewTag(0x0008, 0x0104)
	tagStationName              = core.NewTag(0x0008, 0x1010)
	tagManufacturerModelName    = core.NewTag(0x0008, 0x1090)
	tagReferencedSOPClassUID    = core.NewTag(0x0008, 0x1150)
	tagReferencedSOPInstanceUID = core.NewTag(0x0008, 0x1155)
	tagReferencedFrameNumber    = core.NewTag(0x0008, 0x1160)
	tagDerivationDescription    = core.NewTag(0x0008, 0x2111)
	tagSourceImageSequence      = core.NewTag(0x0008, 0x2112)

	tagDeviceSerialNumber            = core.NewTag(0x0018, 0x1000)
	tagSoftwareVersions              = core.NewTag(0x0018, 0x1020)
	tagContributingEquipmentSequence = core.NewTag(0x0018, 0xA001)
	tagContributionDateTime          = core.NewTag(0x0018, 0xA002)
	tagContributionDescription       = core.NewTag(0x0018, 0xA003)

	tagStudyInstanceUID        = core.NewTag(0x0020, 0x000D)
	tagSeriesInstanceUID       = core.NewTag(0x0020, 0x000E)
	tagInstanceNumber          = core.NewTag(0x0020, 0x0013)
	tagFrameOfReferenceUID     = core.NewTag(0x0020, 0x0052)
	tagImagePositionPatient    = core.NewTag(0x0020, 0x0032)
	tagImageOrientationPatient = core.NewTag(0x0020, 0x0037)

	tagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	tagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	tagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	tagFrameIncrementPointer     = core.NewTag(0x0028, 0x0009)
	tagRows                      = core.NewTag(0x0028, 0x0010)
	tagColumns                   = core.NewTag(0x0028, 0x0011)
	tagPixelSpacing              = core.NewTag(0x0028, 0x0030)
	tagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	tagBitsStored                = core.NewTag(0x0028, 0x0101)
	tagHighBit                   = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
	tagRepresentativeFrameNumber = core.NewTag(0x0028, 0x6010)
	tagStereoPairsPresent        = core.NewTag(0x0022, 0x0028)

	tagPurposeOfReferenceCodeSequence      = core.NewTag(0x0040, 0xA170)
	tagConversionSourceAttributesSequence  = core.NewTag(0x0020, 0x9172)
	tagDimensionOrganizationSequence       = core.NewTag(0x0020, 0x9221)
	tagDimensionIndexSequence              = core.NewTag(0x0020, 0x9222)
	tagDimensionOrganizationType           = core.NewTag(0x0020, 0x9311)
	tagConcatenationUID                    = core.NewTag(0x0020, 0x9161)
	tagInConcatenationNumber               = core.NewTag(0x0020, 0x9162)
	tagInConcatenationTotalNumber          = core.NewTag(0x0020, 0x9163)
	tagConcatenationFrameOffsetNumber      = core.NewTag(0x0020, 0x9228)
	tagSOPInstanceUIDOfConcatenationSource = core.NewTag(0x0020, 0x0242)
	tagSharedFunctionalGroupsSequence      = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroupsSequence    = core.NewTag(0x5200, 0x9230)
	tagPixelData                           = core.NewTag(0x7FE0, 0x0010)
	tagFloatPixelData                      = core.NewTag(0x7FE0, 0x0008)
	tagDoubleFloatPixelData                = core.NewTag(0x7FE0, 0x0009)

	tagScanningSequence  = core.NewTag(0x0018, 0x0020)
	tagSequenceVariant   = core.NewTag(0x0018, 0x0021)
	tagScanOptions       = core.NewTag(0x0018, 0x0022)
	tagMRAcquisitionType = core.NewTag(0x0018, 0x0023)
	tagEchoTime          = core.NewTag(0x0018, 0x0081)
	tagEchoTrainLength   = core.NewTag(0x0018, 0x0091)
	tagKVP               = core.NewTag(0x0018, 0x0060)
	tagAcquisitionNumber = core.NewTag(0x0020, 0x0012)
	tagRescaleIntercept  = core.NewTag(0x0028, 0x1052)
	tagRescaleSlope      = core.NewTag(0x0028, 0x1053)

	tagPixelMeasuresSequence            = core.NewTag(0x0028, 0x9110)
	tagPlanePositionSequence            = core.NewTag(0x0020, 0x9113)
	tagPlaneOrientationSequence         = core.NewTag(0x0020, 0x9116)
	tagPixelValueTransformationSequence = core.NewTag(0x0028, 0x9145)
	tagCTImageFrameTypeSequence         = core.NewTag(0x0018, 0x9329)
	tagMRImageFrameTypeSequence         = core.NewTag(0x0018, 0x9226)
)

var multiFrameOnlyTags = map[core.Tag]struct{}{
	tagNumberOfFrames: {}, tagFrameIncrementPointer: {}, tagRepresentativeFrameNumber: {},
	tagStereoPairsPresent:             {},
	tagSharedFunctionalGroupsSequence: {}, tagPerFrameFunctionalGroupsSequence: {},
	tagConversionSourceAttributesSequence: {}, tagDimensionOrganizationSequence: {},
	tagDimensionIndexSequence: {}, tagDimensionOrganizationType: {}, tagConcatenationUID: {},
	tagInConcatenationNumber: {}, tagInConcatenationTotalNumber: {},
	tagConcatenationFrameOffsetNumber: {}, tagSOPInstanceUIDOfConcatenationSource: {},
	tagPixelData: {}, tagFloatPixelData: {}, tagDoubleFloatPixelData: {},
}

func str(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue{value}}
}

func stringsElement(tag core.Tag, vr core.VR, values []string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(append([]string(nil), values...))}
}

func ui(tag core.Tag, value string) core.Element { return str(tag, core.VRUI, value) }

func seq(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true}, Value: core.SequenceValue{Items: items}}
}

func dataSet(elements ...core.Element) core.DataSet { return core.DataSet{Elements: elements} }

func codeItem(value, scheme, meaning string) core.DataSet {
	return dataSet(str(tagCodeValue, core.VRSH, value), str(tagCodingSchemeDesignator, core.VRSH, scheme), str(tagCodeMeaning, core.VRLO, meaning))
}
