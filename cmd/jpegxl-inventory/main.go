package main

import (
	"os"

	"github.com/ThalesMMS/dicom-go/internal/jpegxlinventory"
)

func main() {
	os.Exit(jpegxlinventory.Run(os.Args[1:], os.Stdout, os.Stderr))
}
