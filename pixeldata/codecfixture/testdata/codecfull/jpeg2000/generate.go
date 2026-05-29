//go:build ignore

// Command generate creates externally encoded JPEG 2000 Part 2-subset and
// HTJ2K RPCL fixtures for the codecfull release corpus. The inputs are
// deterministic, synthetic, and contain no patient data.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	width  = 64
	height = 64
)

func main() {
	openJPEG := requiredEnv("DICOM_GO_OPENJPEG_COMPRESS")
	openJPH := requiredEnv("DICOM_GO_OPENJPH_COMPRESS")
	outputDir, err := os.Getwd()
	check(err)
	tempDir, err := os.MkdirTemp("", "codecfull-jpeg2000-generator-*")
	check(err)
	defer os.RemoveAll(tempDir)

	input := filepath.Join(tempDir, "source.pgm")
	check(os.WriteFile(input, syntheticPGM(), 0o600))
	run(openJPEG, "-quiet", "-i", input, "-o", filepath.Join(outputDir, "part2-lossless.j2k"), "-r", "1")
	run(openJPEG, "-quiet", "-i", input, "-o", filepath.Join(outputDir, "part2-lossy.j2k"), "-I", "-r", "2")
	run(
		openJPH,
		"-i", input,
		"-o", filepath.Join(outputDir, "htj2k-rpcl-lossless.j2c"),
		"-reversible", "true",
		"-prog_order", "RPCL",
		"-num_decomps", "2",
	)
}

func syntheticPGM() []byte {
	var output bytes.Buffer
	fmt.Fprintf(&output, "P5\n%d %d\n255\n", width, height)
	output.Write(syntheticPixels())
	return output.Bytes()
}

func syntheticPixels() []byte {
	pixels := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pixels[y*width+x] = byte((x*7 + y*13 + x*y*3) & 0xff)
		}
	}
	return pixels
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", name)
		os.Exit(2)
	}
	return value
}

func run(name string, arguments ...string) {
	command := exec.Command(name, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n%s", name, err, output)
		os.Exit(1)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
