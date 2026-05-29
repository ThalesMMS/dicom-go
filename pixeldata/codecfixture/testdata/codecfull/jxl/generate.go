//go:build ignore

// Command generate creates the deterministic, non-PHI JPEG XL codecfull
// fixtures. Run it from this directory with DICOM_GO_CJXL set to an audited
// libjxl cjxl executable.
package main

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	grayWidth   = 64
	grayHeight  = 64
	colorWidth  = 64
	colorHeight = 48
)

func main() {
	cjxl := os.Getenv("DICOM_GO_CJXL")
	if cjxl == "" {
		panic("DICOM_GO_CJXL is required")
	}
	temp, err := os.MkdirTemp("", "dicom-go-codecfull-jxl-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(temp)

	pgm := filepath.Join(temp, "gray16.pgm")
	ppm := filepath.Join(temp, "rgb8.ppm")
	jpg := filepath.Join(temp, "rgb8.jpg")
	must(writeGray16PGM(pgm))
	must(writeRGB8PPM(ppm))
	must(writeRGB8JPEG(jpg))

	run(cjxl, pgm, "gray16-lossless.jxl", "-d", "0", "-e", "9")
	run(cjxl, ppm, "rgb8-lossy.jxl", "-d", "1", "-e", "9")
	run(cjxl, jpg, "jpeg-recompression.jxl", "-e", "9")

	for _, name := range []string{"gray16-lossless.jxl", "rgb8-lossy.jxl", "jpeg-recompression.jxl"} {
		data, err := os.ReadFile(name)
		must(err)
		fmt.Printf("%s  %x  %d bytes\n", name, sha256.Sum256(data), len(data))
	}
}

func writeGray16PGM(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	if _, err := fmt.Fprintf(writer, "P5\n%d %d\n65535\n", grayWidth, grayHeight); err != nil {
		return err
	}
	for y := 0; y < grayHeight; y++ {
		for x := 0; x < grayWidth; x++ {
			value := uint16((x*997 + y*313 + x*y*17) & 0xffff)
			if err := writer.WriteByte(byte(value >> 8)); err != nil {
				return err
			}
			if err := writer.WriteByte(byte(value)); err != nil {
				return err
			}
		}
	}
	return writer.Flush()
}

func writeRGB8PPM(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	if _, err := fmt.Fprintf(writer, "P6\n%d %d\n255\n", colorWidth, colorHeight); err != nil {
		return err
	}
	for y := 0; y < colorHeight; y++ {
		for x := 0; x < colorWidth; x++ {
			if err := writer.WriteByte(byte(x*4 + y)); err != nil {
				return err
			}
			if err := writer.WriteByte(byte(y*5 + x/2)); err != nil {
				return err
			}
			if err := writer.WriteByte(byte((x*3 + y*7) ^ 0x5a)); err != nil {
				return err
			}
		}
	}
	return writer.Flush()
}

func writeRGB8JPEG(path string) error {
	img := image.NewRGBA(image.Rect(0, 0, colorWidth, colorHeight))
	for y := 0; y < colorHeight; y++ {
		for x := 0; x < colorWidth; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: byte(x*4 + y),
				G: byte(y*5 + x/2),
				B: byte((x*3 + y*7) ^ 0x5a),
				A: 255,
			})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return jpeg.Encode(file, img, &jpeg.Options{Quality: 90})
}

func run(cjxl, input, output string, args ...string) {
	commandArgs := append([]string{input, output}, args...)
	command := exec.Command(cjxl, commandArgs...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	must(command.Run())
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
