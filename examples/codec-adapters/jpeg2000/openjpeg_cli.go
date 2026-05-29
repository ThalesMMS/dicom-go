//go:build jpeg2000_openjpeg || codecfull

package jpeg2000

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const (
	openJPEGProcessOutputLimit = 16 * 1024
	openJPEGPNMHeaderLimit     = 4 * 1024
)

type pnmImage struct {
	magic  string
	width  int
	height int
	maxVal int
	raster []byte
}

func (decoder openJPEGDecoder) DecodeFrame(payload []byte, metadata pixeldata.Metadata) ([]byte, error) {
	executable, err := decoder.resolveExecutable()
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "dicom-go-openjpeg-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create temp directory: %w", ErrOpenJPEGUnavailable, err)
	}
	defer os.RemoveAll(dir)

	inputPath := filepath.Join(dir, "frame.j2k")
	outputPath := filepath.Join(dir, "frame"+openJPEGOutputExtension(metadata))
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		return nil, fmt.Errorf("%w: write input codestream: %w", ErrOpenJPEGUnavailable, err)
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if decoder.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, decoder.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, executable, "-quiet", "-i", inputPath, "-o", outputPath)
	var processOutput limitedProcessOutput
	cmd.Stdout = &processOutput
	cmd.Stderr = &processOutput
	err = cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%w: opj_decompress timed out after %s", ErrMalformedCodestream, decoder.timeout)
		}
		detail := processOutput.String()
		if detail == "" {
			detail = err.Error()
		}
		if isOpenJPEGLaunchError(err) {
			return nil, fmt.Errorf("%w: opj_decompress: %s", ErrOpenJPEGUnavailable, detail)
		}
		return nil, fmt.Errorf("%w: opj_decompress: %s", ErrMalformedCodestream, detail)
	}

	sizeLimit, err := openJPEGOutputSizeLimit(metadata)
	if err != nil {
		return nil, err
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("%w: stat OpenJPEG output: %w", ErrMalformedCodestream, err)
	}
	if outputInfo.Size() > sizeLimit {
		return nil, fmt.Errorf(
			"%w: OpenJPEG output bytes=%d limit=%d",
			pixeldata.ErrPixelDataSizeMismatch,
			outputInfo.Size(),
			sizeLimit,
		)
	}

	pnmBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read OpenJPEG output: %w", ErrMalformedCodestream, err)
	}
	return pnmToFrameBytes(pnmBytes, metadata)
}

// ValidateOpenJPEGRuntime resolves the OpenJPEG executable required by the
// clinical codecfull profile.
func ValidateOpenJPEGRuntime() error {
	decoder := newOpenJPEGDecoder()
	executable, err := decoder.resolveExecutable()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), decoder.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-h")
	var output limitedProcessOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%w: version probe timed out after %s", ErrOpenJPEGUnavailable, decoder.timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			if versionErr := validateOpenJPEGVersionOutput(output.String()); versionErr == nil {
				return nil
			}
		}
		return fmt.Errorf("%w: version probe failed: %s", ErrOpenJPEGUnavailable, outputOrError(output.String(), err))
	}
	return validateOpenJPEGVersionOutput(output.String())
}

func validateOpenJPEGVersionOutput(output string) error {
	expected := "openjp2 library v" + QualifiedOpenJPEGVersion
	if !strings.Contains(output, expected) {
		return fmt.Errorf(
			"%w: version probe did not report qualified OpenJPEG %s",
			ErrOpenJPEGUnavailable,
			QualifiedOpenJPEGVersion,
		)
	}
	return nil
}

func outputOrError(output string, err error) string {
	if output != "" {
		return output
	}
	return err.Error()
}

func (decoder openJPEGDecoder) resolveExecutable() (string, error) {
	if decoder.executable != "" {
		if filepath.Base(decoder.executable) == decoder.executable {
			resolved, err := exec.LookPath(decoder.executable)
			if err != nil {
				return "", fmt.Errorf("%w: %s not found in PATH", ErrOpenJPEGUnavailable, decoder.executable)
			}
			return resolved, nil
		}
		info, err := os.Stat(decoder.executable)
		if err != nil {
			return "", fmt.Errorf("%w: %s: %w", ErrOpenJPEGUnavailable, decoder.executable, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: %s is not a regular executable file", ErrOpenJPEGUnavailable, decoder.executable)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%w: %s is not executable", ErrOpenJPEGUnavailable, decoder.executable)
		}
		return decoder.executable, nil
	}
	name := "opj_decompress"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		for _, candidate := range []string{
			filepath.Join(dir, name),
			filepath.Join(dir, "codec", name),
			filepath.Clean(filepath.Join(dir, "..", "Resources", "codec", name)),
		} {
			info, statErr := os.Stat(candidate)
			if statErr == nil && info.Mode().IsRegular() &&
				(runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
				return candidate, nil
			}
		}
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found in bundled runtime or PATH", ErrOpenJPEGUnavailable, name)
	}
	return resolved, nil
}

func openJPEGOutputExtension(metadata pixeldata.Metadata) string {
	if metadata.SamplesPerPixel == 3 {
		return ".ppm"
	}
	return ".pgm"
}

func pnmToFrameBytes(data []byte, metadata pixeldata.Metadata) ([]byte, error) {
	pnm, err := parsePNM(data)
	if err != nil {
		return nil, err
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedMetadata, metadata.BitsAllocated)
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated {
		return nil, fmt.Errorf(
			"%w: BitsStored=%d BitsAllocated=%d",
			pixeldata.ErrInvalidMetadata,
			metadata.BitsStored,
			metadata.BitsAllocated,
		)
	}
	if pnm.width != int(metadata.Columns) || pnm.height != int(metadata.Rows) {
		return nil, fmt.Errorf(
			"%w: OpenJPEG size=%dx%d Columns=%d Rows=%d",
			ErrImageSizeMismatch,
			pnm.width,
			pnm.height,
			metadata.Columns,
			metadata.Rows,
		)
	}
	components := 1
	switch pnm.magic {
	case "P5":
		components = 1
	case "P6":
		components = 3
	default:
		return nil, fmt.Errorf("%w: OpenJPEG PNM magic=%s", ErrUnsupportedMetadata, pnm.magic)
	}
	if components != int(metadata.SamplesPerPixel) {
		return nil, fmt.Errorf(
			"%w: OpenJPEG components=%d SamplesPerPixel=%d",
			ErrImageSizeMismatch,
			components,
			metadata.SamplesPerPixel,
		)
	}
	precision, ok := precisionFromPNMMaxVal(pnm.maxVal)
	if !ok || precision > metadata.BitsAllocated {
		return nil, fmt.Errorf(
			"%w: OpenJPEG precision=%d exceeds BitsAllocated=%d maxval=%d",
			ErrUnsupportedMetadata,
			precision,
			metadata.BitsAllocated,
			pnm.maxVal,
		)
	}
	bytesPerSample := 1
	if pnm.maxVal > 255 {
		bytesPerSample = 2
	}
	expectedRaster := int(metadata.Rows) * int(metadata.Columns) * components * bytesPerSample
	if len(pnm.raster) != expectedRaster {
		return nil, fmt.Errorf("%w: OpenJPEG raster bytes=%d expected=%d", pixeldata.ErrPixelDataSizeMismatch, len(pnm.raster), expectedRaster)
	}
	bytesAllocated := int(metadata.BitsAllocated / 8)
	out := make([]byte, int(metadata.Rows)*int(metadata.Columns)*components*bytesAllocated)
	for sampleIndex := 0; sampleIndex < len(out)/bytesAllocated; sampleIndex++ {
		sourceOffset := sampleIndex * bytesPerSample
		var sample uint32
		if bytesPerSample == 1 {
			sample = uint32(pnm.raster[sourceOffset])
		} else {
			sample = uint32(binary.BigEndian.Uint16(pnm.raster[sourceOffset:]))
		}
		stored := uint16(sample)
		if metadata.PixelRepresentation == 1 {
			signBit := uint32(1) << (precision - 1)
			signed := int32(sample)
			if uint32(sample)&signBit == 0 {
				signed -= int32(signBit)
			} else {
				signed = int32(uint32(sample) - signBit)
			}
			stored = uint16(signed)
		}
		destinationOffset := sampleIndex * bytesAllocated
		if bytesAllocated == 1 {
			out[destinationOffset] = byte(stored)
		} else {
			binary.LittleEndian.PutUint16(out[destinationOffset:], stored)
		}
	}
	return out, nil
}

func parsePNM(data []byte) (pnmImage, error) {
	offset := 0
	magic, next, err := nextPNMToken(data, offset)
	if err != nil {
		return pnmImage{}, err
	}
	offset = next
	widthToken, next, err := nextPNMToken(data, offset)
	if err != nil {
		return pnmImage{}, err
	}
	offset = next
	heightToken, next, err := nextPNMToken(data, offset)
	if err != nil {
		return pnmImage{}, err
	}
	offset = next
	maxValToken, next, err := nextPNMToken(data, offset)
	if err != nil {
		return pnmImage{}, err
	}
	offset = next

	width, err := strconv.Atoi(widthToken)
	if err != nil || width <= 0 {
		return pnmImage{}, fmt.Errorf("%w: OpenJPEG PNM width=%q", ErrMalformedCodestream, widthToken)
	}
	height, err := strconv.Atoi(heightToken)
	if err != nil || height <= 0 {
		return pnmImage{}, fmt.Errorf("%w: OpenJPEG PNM height=%q", ErrMalformedCodestream, heightToken)
	}
	maxVal, err := strconv.Atoi(maxValToken)
	if err != nil || maxVal <= 0 || maxVal > 65535 {
		return pnmImage{}, fmt.Errorf("%w: OpenJPEG PNM maxval=%q", ErrMalformedCodestream, maxValToken)
	}
	if offset >= len(data) || !isPNMWhitespace(data[offset]) {
		return pnmImage{}, fmt.Errorf("%w: OpenJPEG PNM missing raster separator", ErrMalformedCodestream)
	}
	if data[offset] == '\r' && offset+1 < len(data) && data[offset+1] == '\n' {
		offset += 2
	} else {
		offset++
	}

	return pnmImage{
		magic:  magic,
		width:  width,
		height: height,
		maxVal: maxVal,
		raster: data[offset:],
	}, nil
}

func nextPNMToken(data []byte, offset int) (string, int, error) {
	for {
		for offset < len(data) && isPNMWhitespace(data[offset]) {
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
		return "", offset, fmt.Errorf("%w: truncated OpenJPEG PNM header", ErrMalformedCodestream)
	}
	start := offset
	for offset < len(data) && !isPNMWhitespace(data[offset]) && data[offset] != '#' {
		offset++
	}
	return string(data[start:offset]), offset, nil
}

func isPNMWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

type limitedProcessOutput struct {
	buf       bytes.Buffer
	truncated bool
}

func (output *limitedProcessOutput) Write(p []byte) (int, error) {
	remaining := openJPEGProcessOutputLimit - output.buf.Len()
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

func isOpenJPEGLaunchError(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}

func openJPEGOutputSizeLimit(metadata pixeldata.Metadata) (int64, error) {
	bytesPerSample := int64(1)
	if metadata.BitsStored > 8 {
		bytesPerSample = 2
	}
	rasterBytes := int64(metadata.Rows) * int64(metadata.Columns) * int64(metadata.SamplesPerPixel) * bytesPerSample
	return rasterBytes + openJPEGPNMHeaderLimit, nil
}

func precisionFromPNMMaxVal(maxVal int) (uint16, bool) {
	for precision := uint16(1); precision <= 16; precision++ {
		if maxVal == int((uint32(1)<<precision)-1) {
			return precision, true
		}
	}
	return 0, false
}
