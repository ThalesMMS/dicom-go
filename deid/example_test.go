package deid_test

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/deid"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func ExampleCloneFileWithBasicProfile() {
	patientName := core.NewTag(0x0010, 0x0010)
	studyInstanceUID := core.NewTag(0x0020, 0x000D)
	source := &object.File{
		Dataset: object.FromElements([]core.Element{
			core.NewRawElement(core.NewTag(0x0008, 0x0016), core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.2")),
			core.NewRawElement(core.NewTag(0x0008, 0x0018), core.VRUI, []byte("1.2.3.4.5.1")),
			core.NewRawElement(studyInstanceUID, core.VRUI, []byte("1.2.3.4.5.2")),
			core.NewRawElement(core.NewTag(0x0020, 0x000E), core.VRUI, []byte("1.2.3.4.5.3")),
			core.NewRawElement(patientName, core.VRPN, []byte("EXAMPLE^PATIENT")),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	if err := source.RebuildFileMeta(); err != nil {
		panic(err)
	}

	originalName, _ := source.Dataset.GetString(patientName)
	originalStudyUID, _ := source.Dataset.GetUID(studyInstanceUID)
	clone, report, err := deid.CloneFileWithBasicProfile(
		context.Background(),
		source,
		deid.DefaultBasicProfileOptions(),
		deid.NewUIDRemapper(),
	)
	if err != nil {
		panic(err)
	}

	sourceName, _ := source.Dataset.GetString(patientName)
	sourceStudyUID, _ := source.Dataset.GetUID(studyInstanceUID)
	cloneStudyUID, _ := clone.Dataset.GetUID(studyInstanceUID)
	fmt.Println(report.Complete)
	fmt.Println(sourceName == originalName && sourceStudyUID == originalStudyUID)
	fmt.Println(cloneStudyUID != originalStudyUID)

	// Output:
	// true
	// true
	// true
}
