package main

import (
	"os"

	"github.com/ThalesMMS/dicom-go/internal/dcmdump"
)

func main() {
	os.Exit(dcmdump.Run(os.Args[1:], os.Stdout, os.Stderr))
}
