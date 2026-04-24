package main

import (
	"fmt"
	"os"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	secondaryCaptureImageStorage = "1.2.840.10008.5.1.4.1.1.7"
	exampleSOPInstanceUID        = "1.2.826.0.1.3680043.10.543.100.1"
)

var (
	tagMediaStorageSOPClassUID    = core.NewTag(0x0002, 0x0002)
	tagMediaStorageSOPInstanceUID = core.NewTag(0x0002, 0x0003)
	tagTransferSyntaxUID          = core.NewTag(0x0002, 0x0010)
	tagSOPClassUID                = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID             = core.NewTag(0x0008, 0x0018)
	tagStudyDate                  = core.NewTag(0x0008, 0x0020)
	tagModality                   = core.NewTag(0x0008, 0x0060)
	tagPatientName                = core.NewTag(0x0010, 0x0010)
	tagPatientID                  = core.NewTag(0x0010, 0x0020)
)

func main() {
	args := normalizeArgs(os.Args[1:])
	if len(args) != 1 && len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: go run ./examples/writefile -- <output.dcm> [input.dcm]\n")
		os.Exit(2)
	}

	outPath := args[0]
	file := newExampleFile()
	if len(args) == 2 {
		input, err := object.OpenFile(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "open input DICOM file: %v\n", err)
			os.Exit(1)
		}
		file = input
		if file.Dataset == nil {
			file.Dataset = object.New(std.Dictionary)
		}
		file.Dataset.Put(stringElement(tagPatientName, core.VRPN, "Modified^Example"))
		file.Dataset.Put(stringElement(tagPatientID, core.VRLO, "EXAMPLE-ID"))
	}

	out, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create output file: %v\n", err)
		os.Exit(1)
	}

	if err := object.WriteFile(out, file); err != nil {
		if closeErr := out.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "close output file after write failure: %v\n", closeErr)
		}
		fmt.Fprintf(os.Stderr, "write DICOM file: %v\n", err)
		os.Exit(1)
	}
	if errClose := out.Close(); errClose != nil {
		fmt.Fprintf(os.Stderr, "close output file: %v\n", errClose)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", outPath)
}

func newExampleFile() *object.File {
	meta := object.New(std.Dictionary)
	meta.Put(stringElement(tagMediaStorageSOPClassUID, core.VRUI, secondaryCaptureImageStorage))
	meta.Put(stringElement(tagMediaStorageSOPInstanceUID, core.VRUI, exampleSOPInstanceUID))
	meta.Put(stringElement(tagTransferSyntaxUID, core.VRUI, transfer.ExplicitVRLittleEndian.UID))

	ds := object.New(std.Dictionary)
	ds.Put(stringElement(tagSOPClassUID, core.VRUI, secondaryCaptureImageStorage))
	ds.Put(stringElement(tagSOPInstanceUID, core.VRUI, exampleSOPInstanceUID))
	ds.Put(stringElement(tagStudyDate, core.VRDA, "20260424"))
	ds.Put(stringElement(tagModality, core.VRCS, "OT"))
	ds.Put(stringElement(tagPatientName, core.VRPN, "Example^Patient"))
	ds.Put(stringElement(tagPatientID, core.VRLO, "EXAMPLE-001"))

	return &object.File{
		Meta:           meta,
		Dataset:        ds,
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue{value},
	}
}

func normalizeArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}
