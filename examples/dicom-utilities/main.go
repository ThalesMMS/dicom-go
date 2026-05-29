package main

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/deid"
	"github.com/ThalesMMS/dicom-go/dicomdir"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

func main() {
	path := dicomdir.Resolve("/media/disc", dicomdir.Reference{FileID: []string{`DICOM\PT0\ST0\SE0\IM0`}})

	patientName := core.NewTag(0x0010, 0x0010)
	studyUID := core.NewTag(0x0020, 0x000D)
	obj := object.FromElements([]core.Element{
		core.NewRawElement(patientName, core.VRPN, []byte("DOE^JANE")),
		core.NewRawElement(studyUID, core.VRUI, []byte("1.2.3")),
	}, std.Dictionary)

	if err := deid.AnonymizeObject(obj, deid.Options{PatientName: "Demo^Anonymous"}, deid.NewUIDRemapper()); err != nil {
		panic(err)
	}
	name, _ := obj.GetString(patientName)
	uid, _ := obj.GetString(studyUID)

	fmt.Printf("%s patient=%s study=%s\n", path, strings.TrimSpace(name), strings.TrimRight(uid, "\x00"))
}
