//go:build jpegxl_djxl || codecfull

package jpegxladapter

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const (
	djxlProcessOutputLimit = 16 * 1024
	djxlPPMHeaderLimit     = 4 * 1024
)

type ppmImage struct {
	magic  string
	width  int
	height int
	maxVal int
	raster []byte
}

func (decoder djxlDecoder) DecodeFrame(fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	executable, err := decoder.resolveExecutable()
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "dicom-go-djxl-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create temp directory: %w", ErrDjxlUnavailable, err)
	}
	defer os.RemoveAll(dir)

	inputPath := filepath.Join(dir, "frame.jxl")
	outputPath := filepath.Join(dir, "frame"+djxlOutputExtension(metadata))
	if err := os.WriteFile(inputPath, fragment, 0o600); err != nil {
		return nil, fmt.Errorf("%w: write input codestream: %w", ErrDjxlUnavailable, err)
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if decoder.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, decoder.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, executable, inputPath, outputPath, "--quiet")
	var processOutput limitedProcessOutput
	cmd.Stdout = &processOutput
	cmd.Stderr = &processOutput
	err = cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%w: djxl timed out after %s", ErrMalformedCodestream, decoder.timeout)
		}
		detail := processOutput.String()
		if detail == "" {
			detail = err.Error()
		}
		if isDjxlLaunchError(err) {
			return nil, fmt.Errorf("%w: djxl: %s", ErrDjxlUnavailable, detail)
		}
		return nil, fmt.Errorf("%w: djxl: %s", ErrMalformedCodestream, detail)
	}

	sizeLimit, err := djxlOutputSizeLimit(metadata)
	if err != nil {
		return nil, err
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("%w: stat djxl output: %w", ErrMalformedCodestream, err)
	}
	if outputInfo.Size() > sizeLimit {
		return nil, fmt.Errorf(
			"%w: djxl output bytes=%d limit=%d",
			pixeldata.ErrPixelDataSizeMismatch,
			outputInfo.Size(),
			sizeLimit,
		)
	}

	ppmBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read djxl output: %w", ErrMalformedCodestream, err)
	}
	return ppmToFrameBytes(ppmBytes, metadata)
}

// ValidateRuntime resolves the djxl executable before a codecfull application
// starts accepting studies.
func ValidateRuntime() error {
	decoder := newDjxlDecoder()
	executable, err := decoder.resolveExecutable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), decoder.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--version")
	var output limitedProcessOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%w: version probe timed out after %s", ErrDjxlUnavailable, decoder.timeout)
		}
		detail := output.String()
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%w: version probe failed: %s", ErrDjxlUnavailable, detail)
	}
	return validateDjxlVersionOutput(output.String())
}

func validateDjxlVersionOutput(output string) error {
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "djxl" || fields[1] != "v"+QualifiedDjxlVersion {
		return fmt.Errorf(
			"%w: version probe did not report qualified djxl %s",
			ErrDjxlUnavailable,
			QualifiedDjxlVersion,
		)
	}
	return nil
}

func ppmToFrameBytes(data []byte, metadata pixeldata.Metadata) ([]byte, error) {
	ppm, err := parsePPM(data)
	if err != nil {
		return nil, err
	}
	if ppm.width != int(metadata.Columns) || ppm.height != int(metadata.Rows) {
		return nil, fmt.Errorf(
			"%w: djxl size=%dx%d Columns=%d Rows=%d",
			ErrImageSizeMismatch,
			ppm.width,
			ppm.height,
			metadata.Columns,
			metadata.Rows,
		)
	}
	components := 1
	switch ppm.magic {
	case "P5":
		components = 1
	case "P6":
		components = 3
	default:
		return nil, fmt.Errorf("%w: djxl PPM magic=%s", ErrUnsupportedMetadata, ppm.magic)
	}
	if components != int(metadata.SamplesPerPixel) {
		return nil, fmt.Errorf(
			"%w: djxl components=%d SamplesPerPixel=%d",
			ErrImageSizeMismatch,
			components,
			metadata.SamplesPerPixel,
		)
	}

	precision := bits.Len(uint(ppm.maxVal))
	if precision == 0 || precision > int(metadata.BitsAllocated) {
		return nil, fmt.Errorf(
			"%w: djxl precision=%d BitsAllocated=%d BitsStored=%d maxval=%d",
			ErrUnsupportedMetadata,
			precision,
			metadata.BitsAllocated,
			metadata.BitsStored,
			ppm.maxVal,
		)
	}

	bytesPerSample := 1
	if ppm.maxVal > 255 {
		bytesPerSample = 2
	}
	expectedRaster := int(metadata.Rows) * int(metadata.Columns) * components * bytesPerSample
	if len(ppm.raster) != expectedRaster {
		return nil, fmt.Errorf("%w: djxl raster bytes=%d expected=%d", pixeldata.ErrPixelDataSizeMismatch, len(ppm.raster), expectedRaster)
	}

	bytesAllocated := int(metadata.BitsAllocated / 8)
	out := make([]byte, int(metadata.Rows)*int(metadata.Columns)*components*bytesAllocated)
	for sampleIndex := 0; sampleIndex < len(out)/bytesAllocated; sampleIndex++ {
		sourceOffset := sampleIndex * bytesPerSample
		var sample uint32
		if bytesPerSample == 1 {
			sample = uint32(ppm.raster[sourceOffset])
		} else {
			sample = uint32(binary.BigEndian.Uint16(ppm.raster[sourceOffset:]))
		}
		stored := uint16(sample)
		destinationOffset := sampleIndex * bytesAllocated
		if bytesAllocated == 1 {
			out[destinationOffset] = byte(stored)
		} else {
			binary.LittleEndian.PutUint16(out[destinationOffset:], stored)
		}
	}
	return out, nil
}

func parsePPM(data []byte) (ppmImage, error) {
	offset := 0
	magic, next, err := nextPPMToken(data, offset)
	if err != nil {
		return ppmImage{}, err
	}
	offset = next
	widthToken, next, err := nextPPMToken(data, offset)
	if err != nil {
		return ppmImage{}, err
	}
	offset = next
	heightToken, next, err := nextPPMToken(data, offset)
	if err != nil {
		return ppmImage{}, err
	}
	offset = next
	maxValToken, next, err := nextPPMToken(data, offset)
	if err != nil {
		return ppmImage{}, err
	}
	offset = next

	width, err := strconv.Atoi(widthToken)
	if err != nil || width <= 0 {
		return ppmImage{}, fmt.Errorf("%w: djxl PPM width=%q", ErrMalformedCodestream, widthToken)
	}
	height, err := strconv.Atoi(heightToken)
	if err != nil || height <= 0 {
		return ppmImage{}, fmt.Errorf("%w: djxl PPM height=%q", ErrMalformedCodestream, heightToken)
	}
	maxVal, err := strconv.Atoi(maxValToken)
	if err != nil || maxVal <= 0 || maxVal > 65535 {
		return ppmImage{}, fmt.Errorf("%w: djxl PPM maxval=%q", ErrMalformedCodestream, maxValToken)
	}
	if offset >= len(data) || !isPPMWhitespace(data[offset]) {
		return ppmImage{}, fmt.Errorf("%w: djxl PPM missing raster separator", ErrMalformedCodestream)
	}
	if data[offset] == '\r' && offset+1 < len(data) && data[offset+1] == '\n' {
		offset += 2
	} else {
		offset++
	}

	return ppmImage{
		magic:  magic,
		width:  width,
		height: height,
		maxVal: maxVal,
		raster: data[offset:],
	}, nil
}

func nextPPMToken(data []byte, offset int) (string, int, error) {
	for {
		for offset < len(data) && isPPMWhitespace(data[offset]) {
			offset++
		}
		if offset < len(data) && data[offset] == '#' {
			for offset < len(data) && data[offset] != '\n' {
				offset++
			}
			continue
		}
		break
	}
	if offset >= len(data) {
		return "", offset, fmt.Errorf("%w: truncated djxl PPM header", ErrMalformedCodestream)
	}
	start := offset
	for offset < len(data) && !isPPMWhitespace(data[offset]) && data[offset] != '#' {
		offset++
	}
	return string(data[start:offset]), offset, nil
}

func isPPMWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

type limitedProcessOutput struct {
	buf       bytes.Buffer
	truncated bool
}

func (output *limitedProcessOutput) Write(p []byte) (int, error) {
	remaining := djxlProcessOutputLimit - output.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			output.buf.Write(p[:remaining])
			output.truncated = true
		} else {
			output.buf.Write(p)
		}
	} else if len(p) > 0 {
		output.truncated = true
	}
	return len(p), nil
}

func (output *limitedProcessOutput) String() string {
	detail := strings.TrimSpace(output.buf.String())
	if output.truncated {
		if detail != "" {
			detail += " "
		}
		detail += "(truncated)"
	}
	return detail
}

func isDjxlLaunchError(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}

func djxlOutputSizeLimit(metadata pixeldata.Metadata) (int64, error) {
	rasterBytes := int64(metadata.Rows) * int64(metadata.Columns) * int64(metadata.SamplesPerPixel) * 2
	return rasterBytes + djxlPPMHeaderLimit, nil
}

func djxlOutputExtension(metadata pixeldata.Metadata) string {
	if metadata.SamplesPerPixel == 3 {
		return ".ppm"
	}
	return ".pgm"
}
