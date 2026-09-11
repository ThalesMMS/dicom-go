//go:build ignore

// Explicit local update of the synthetic section of the existing corpus.
// Run from this directory: go run generate.go. Review manifest changes before
// approval. This command neither downloads nor decodes independent goldens.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
)

func main() {
	data, err := os.ReadFile("manifest.json")
	must(err)
	var manifest map[string]any
	must(json.Unmarshal(data, &manifest))
	fixtures := manifest["fixtures"].([]any)
	for _, c := range codecfixture.CorpusCases() {
		id := c.Name
		if id == "jpeg-baseline-small" {
			id = "jpeg-baseline-sof0"
		}
		if id == "jpeg-extended-small" {
			id = "jpeg-extended-sof1"
		}
		var f map[string]any
		for _, entry := range fixtures {
			candidate := entry.(map[string]any)
			if candidate["id"] == id {
				f = candidate
				break
			}
		}
		if f == nil {
			f = map[string]any{"id": id}
			fixtures = append(fixtures, f)
		}
		meta, err := pixeldata.ExtractMetadata(c.Object())
		must(err)
		encoded, err := c.Part10Bytes()
		must(err)
		family := "native"
		switch c.Syntax.UID {
		case "1.2.840.10008.1.2.5":
			family = "rle"
		case "1.2.840.10008.1.2.4.50":
			family = "jpeg-baseline"
		case "1.2.840.10008.1.2.4.51":
			family = "jpeg-extended"
		case "1.2.840.10008.1.2.4.70":
			family = "jpeg-lossless"
		case "1.2.840.10008.1.2.1.98":
			family = "encapsulated-uncompressed"
		}
		f["family"], f["caseName"], f["sha256"] = family, c.Name, fmt.Sprintf("%x", sha256.Sum256(encoded))
		f["provenance"] = c.Provenance
		f["referenceSha256"] = fmt.Sprintf("%x", sha256.Sum256(bytes.Join(c.ExpectedFrames, nil)))
		f["test"], f["generator"] = "TestSyntheticCorpusManifest", "codecfixture.CorpusCases v1; testdata/codecfull/generate.go; serialized Part10 and full expected samples SHA256"
		f["modality"], f["bitsAllocated"], f["signed"], f["color"], f["multiframe"] = "OT", meta.BitsAllocated, meta.PixelRepresentation == 1, meta.SamplesPerPixel > 1, meta.NumberOfFrames > 1
		f["lossy"] = family == "jpeg-baseline" || family == "jpeg-extended"
		f["comparison"], f["expectation"] = "exact", "decode-success"
		if c.Tolerance != 0 {
			f["comparison"] = "absolute-tolerance"
		}
		if c.SamplePolicy != nil {
			f["comparison"], f["samplePolicy"] = "sample-metrics", c.SamplePolicy
		}
		if c.ExpectedError != codecfixture.ErrorNone {
			f["expectation"], f["comparison"] = string(c.ExpectedError), "expected-rejection"
		}
		f["input"] = codecfixture.CorpusInput{TransferSyntax: c.Syntax.UID, Rows: int(meta.Rows), Columns: int(meta.Columns), Components: int(meta.SamplesPerPixel), BitsAllocated: int(meta.BitsAllocated), BitsStored: int(meta.BitsStored), HighBit: int(meta.HighBit), Signed: meta.PixelRepresentation == 1, Photometric: meta.PhotometricInterpretation, PlanarConfiguration: int(meta.PlanarConfiguration), Frames: meta.NumberOfFrames}
	}
	manifest["fixtures"] = fixtures
	data, err = json.MarshalIndent(manifest, "", "  ")
	must(err)
	must(os.WriteFile("manifest.json", append(data, '\n'), 0644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
