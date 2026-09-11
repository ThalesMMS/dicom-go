//go:build ignore

// Run from the module root: go run ./fuzz/testdata/generate_upstream.go
// These structural seeds are generated locally and contain no upstream data.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

func main() {
	names := []string{"upstream-349-ob", "upstream-349-ow", "upstream-huge-length", "upstream-355-short", "upstream-360-charset", "upstream-376-private-un", "upstream-356-nondicom"}
	seeds := dicomtest.UpstreamParserRegressionSeeds()
	if len(seeds) != len(names) {
		panic("seed names require review")
	}
	dir := filepath.Join("fuzz", "testdata", "fuzz", "FuzzDICOMParse")
	if err := os.MkdirAll(dir, 0755); err != nil {
		panic(err)
	}
	for i, seed := range seeds {
		data := []byte(fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", seed))
		if err := os.WriteFile(filepath.Join(dir, names[i]), data, 0644); err != nil {
			panic(err)
		}
	}
}
