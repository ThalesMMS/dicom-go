package main

import (
	"fmt"
	"os"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagPatientName             = core.NewTag(0x0010, 0x0010)
	tagPatientID               = core.NewTag(0x0010, 0x0020)
	tagReferencedStudySequence = core.NewTag(0x0008, 0x1110)
	tagReferencedSOPClassUID   = core.NewTag(0x0008, 0x1150)
	tagReferencedSOPInstanceID = core.NewTag(0x0008, 0x1155)
	tagEncapsulatedDocument    = core.NewTag(0x0042, 0x0011)
)

func main() {
	obj := object.New(std.Dictionary)
	obj.Put(stringElement(tagPatientName, core.VRPN, "Public^Patient=Ideographic^Name=Phonetic^Name"))
	obj.Put(stringElement(tagPatientID, core.VRLO, "JSON-001"))
	obj.Put(core.Element{
		Header: core.ElementHeader{Tag: tagReferencedStudySequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{
			Elements: []core.Element{
				stringElement(tagReferencedSOPClassUID, core.VRUI, "1.2.840.10008.5.1.4.1.1.2"),
				stringElement(tagReferencedSOPInstanceID, core.VRUI, "1.2.826.0.1.3680043.10.543.200.1"),
			},
		}}},
	})
	obj.Put(core.NewRawElement(tagEncapsulatedDocument, core.VROB, []byte("large binary payload placeholder")))

	pretty, err := dicomjson.MarshalPretty(obj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(pretty))

	roundTrip, err := dicomjson.Unmarshal(pretty, std.Dictionary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unmarshal JSON: %v\n", err)
		os.Exit(1)
	}
	if name, ok := roundTrip.GetString(tagPatientName); ok {
		fmt.Printf("\nround-trip patient name: %s\n", name)
	}

	withBulkURI, err := dicomjson.MarshalObjectWithOptions(obj, dicomjson.Options{
		Pretty:          true,
		OmitGroupLength: true,
		BulkDataURIFunc: func(tag core.Tag, vr core.VR, data []byte) string {
			if len(data) > 16 {
				return "file://bulk/" + tag.HexString()
			}
			return ""
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal JSON with BulkDataURI: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nwith BulkDataURI:\n%s\n", withBulkURI)
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue{value},
	}
}
