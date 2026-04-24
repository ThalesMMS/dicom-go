package main

import (
	"fmt"
	"os"

	"github.com/ThalesMMS/dicom-go"
	"github.com/ThalesMMS/dicom-go/core"
)

var (
	tagPatientName = core.NewTag(0x0010, 0x0010)
	tagPatientID   = core.NewTag(0x0010, 0x0020)
	tagStudyDate   = core.NewTag(0x0008, 0x0020)
)

func main() {
	args := normalizeArgs(os.Args[1:])
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: go run ./examples/readfile -- <file.dcm>\n")
		os.Exit(2)
	}

	file, err := dicom.OpenFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "open DICOM file: %v\n", err)
		os.Exit(1)
	}

	// A Part 10 file is split into File.Meta, which always uses Explicit VR
	// Little Endian, and File.Dataset, which uses File.TransferSyntax.
	fmt.Printf("transfer syntax: %s (%s)\n", file.TransferSyntax.UID, file.TransferSyntax.Name)
	if patientName, ok := file.GetString(tagPatientName); ok {
		fmt.Printf("patient name: %s\n", patientName)
	}
	if patientID, ok := file.GetString(tagPatientID); ok {
		fmt.Printf("patient id: %s\n", patientID)
	}
	studyDate, err := file.LookupString(tagStudyDate)
	if err != nil {
		fmt.Printf("study date: <missing: %v>\n", err)
	} else {
		fmt.Printf("study date: %s\n", studyDate)
	}

	if file.Dataset == nil {
		fmt.Println("dataset: <nil>")
		return
	}
	fmt.Println("\ndataset elements:")
	for _, elem := range file.Dataset.SortedElements() {
		fmt.Printf("  %s %s length=%s\n", elem.Tag(), elem.VR(), elem.Length())
	}
}

func normalizeArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}
