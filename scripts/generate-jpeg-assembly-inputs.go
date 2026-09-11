//go:build ignore

// Export existing non-PHI corpus codestreams for an independent decoder.
package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
)

func main() {
	output := flag.String("output", "", "candidate directory (required)")
	flag.Parse()
	if *output == "" {
		panic("output is required")
	}
	if err := os.MkdirAll(*output, 0700); err != nil {
		panic(err)
	}
	for _, c := range []codecfixture.Case{codecfixture.JPEGBaselineSmall(), codecfixture.JPEGExtendedSmall()} {
		pixel, err := c.PixelData()
		if err != nil {
			panic(err)
		}
		if len(pixel.Sequence.Fragments) != 1 {
			panic("unexpected corpus layout")
		}
		if err := os.WriteFile(filepath.Join(*output, c.Name+".jpg"), pixel.Sequence.Fragments[0], 0600); err != nil {
			panic(err)
		}
	}
}
