package dicomtest

import (
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	TestSOPClassUID       = "1.2.826.0.1.3680043.9.7433.1.1"
	TestSOPInstanceUID    = "1.2.826.0.1.3680043.9.7433.1.1.1"
	TestStudyInstanceUID  = "1.2.826.0.1.3680043.9.7433.1.2.1"
	TestSeriesInstanceUID = "1.2.826.0.1.3680043.9.7433.1.2.1.1"
)

var (
	BenchmarkSequenceOuterTag       = core.NewTag(0x0008, 0x1111)
	BenchmarkSequenceInnerTag       = core.NewTag(0x0008, 0x1140)
	BenchmarkSequenceSOPClassTag    = core.NewTag(0x0008, 0x1150)
	BenchmarkSequenceSOPInstanceTag = core.NewTag(0x0008, 0x1155)
	BenchmarkSequencePatientNameTag = core.NewTag(0x0010, 0x0010)
	BenchmarkSequencePatientIDTag   = core.NewTag(0x0010, 0x0020)
	ImplicitSequenceTag             = core.NewTag(0x0008, 0x1115)
)

func MinimalDataset() []core.Element {
	return []core.Element{
		NewUIElement(tagSOPClassUID, TestSOPClassUID),
		NewPNElement(tagPatientName, "TEST^PATIENT"),
		NewStringElement(tagPatientID, core.VRLO, "TESTID001"),
		NewUIElement(tagSOPInstanceUID, TestSOPInstanceUID),
		NewUIElement(tagStudyInstanceUID, TestStudyInstanceUID),
	}
}

func DatasetWithPixelData() []core.Element {
	elements := append([]core.Element{}, MinimalDataset()...)
	elements = append(elements,
		NewUIElement(tagSeriesInstanceUID, TestSeriesInstanceUID),
		NewStringElement(tagModality, core.VRCS, "OT"),
		NewUShortElement(tagRows, 8),
		NewUShortElement(tagColumns, 8),
		NewUShortElement(tagSamplesPerPixel, 1),
		NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		NewUShortElement(tagBitsAllocated, 8),
		NewUShortElement(tagBitsStored, 8),
		NewUShortElement(tagHighBit, 7),
		NewUShortElement(tagPixelRepresentation, 0),
		NewOBElement(tagPixelData, syntheticPixelData8x8()),
	)
	return elements
}

func BenchmarkSequenceDataSet() core.DataSet {
	return core.DataSet{
		Elements: append(
			MinimalDataset(),
			NewSequenceElement(
				BenchmarkSequenceOuterTag,
				core.DataSet{
					Elements: []core.Element{
						NewPNElement(BenchmarkSequencePatientNameTag, "BENCH^ONE"),
						NewSequenceElement(
							BenchmarkSequenceInnerTag,
							core.DataSet{
								Elements: []core.Element{
									NewUIElement(BenchmarkSequenceSOPClassTag, TestSOPClassUID),
									NewUIElement(BenchmarkSequenceSOPInstanceTag, TestSOPInstanceUID),
								},
							},
						),
					},
				},
				core.DataSet{
					Elements: []core.Element{
						NewPNElement(BenchmarkSequencePatientNameTag, "BENCH^TWO"),
						NewStringElement(BenchmarkSequencePatientIDTag, core.VRLO, "SEQ-002"),
					},
				},
			),
		),
	}
}

func BenchmarkSequenceDictionaryEntries() map[core.Tag]core.VR {
	return map[core.Tag]core.VR{
		BenchmarkSequenceOuterTag:       core.VRSQ,
		BenchmarkSequenceInnerTag:       core.VRSQ,
		BenchmarkSequenceSOPClassTag:    core.VRUI,
		BenchmarkSequenceSOPInstanceTag: core.VRUI,
		BenchmarkSequencePatientNameTag: core.VRPN,
		BenchmarkSequencePatientIDTag:   core.VRLO,
	}
}

func MinimalFile() []byte {
	data, err := ExplicitVRFile()
	if err != nil {
		panic(err)
	}
	return data
}

func ExplicitVRFile() ([]byte, error) {
	return NewFileBuilder().
		WithMeta(NewFileMetaBuilder().WithTransferSyntax(transfer.ExplicitVRLittleEndian.UID)).
		AddElements(MinimalDataset()...).
		Build()
}

func ImplicitVRFile() ([]byte, error) {
	return NewFileBuilder().
		WithMeta(NewFileMetaBuilder().WithTransferSyntax(transfer.ImplicitVRLittleEndian.UID)).
		AddElements(MinimalDataset()...).
		Build()
}

func ImplicitVRSequenceDataSet() core.DataSet {
	elements := append([]core.Element{}, MinimalDataset()...)
	elements = append(elements,
		NewSequenceElement(
			ImplicitSequenceTag,
			core.DataSet{
				Elements: []core.Element{
					NewUIElement(tagSeriesInstanceUID, TestSeriesInstanceUID),
				},
			},
		),
	)
	return core.DataSet{Elements: elements}
}

func ImplicitVRSequenceFile() ([]byte, error) {
	return NewFileBuilder().
		WithMeta(NewFileMetaBuilder().WithTransferSyntax(transfer.ImplicitVRLittleEndian.UID)).
		AddElements(ImplicitVRSequenceDataSet().Elements...).
		Build()
}

func BigEndianFile() ([]byte, error) {
	return NewFileBuilder().
		WithMeta(NewFileMetaBuilder().WithTransferSyntax(transfer.ExplicitVRBigEndian.UID)).
		AddElements(MinimalDataset()...).
		Build()
}

func syntheticPixelData8x8() []byte {
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte((i * 17) % 256)
	}
	return data
}
