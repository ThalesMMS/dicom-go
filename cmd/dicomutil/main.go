package main

import (
	"os"

	"github.com/ThalesMMS/dicom-go/internal/dicomutil"
)

func main() {
	os.Exit(dicomutil.Run(os.Args[1:], os.Stdout, os.Stderr))
}
