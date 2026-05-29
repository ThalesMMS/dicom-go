//go:build codecfull

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
	openJPHProcessOutputLimit = 16 * 1024
	openJPHBinarySizeLimit    = 128 * 1024 * 1024
)

func (decoder openJPHDecoder) DecodeFrame(payload []byte, metadata pixeldata.Metadata) ([]byte, error) {
	if metadata.PixelRepresentation != 0 {
		return nil, fmt.Errorf(
			"%w: signed HTJ2K output is not qualified for the OpenJPH PNM backend",
			ErrUnsupportedMetadata,
		)
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return nil, fmt.Errorf(
			"%w: OpenJPH PNM backend requires BitsAllocated 8 or 16, got %d",
			ErrUnsupportedMetadata,
			metadata.BitsAllocated,
		)
	}
	executable, err := decoder.resolveExecutable()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "dicom-go-openjph-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create temp directory: %w", ErrOpenJPHUnavailable, err)
	}
	defer os.RemoveAll(dir)

	input := filepath.Join(dir, "frame.j2c")
	output := filepath.Join(dir, "frame"+openJPHOutputExtension(metadata))
	if err := os.WriteFile(input, payload, 0o600); err != nil {
		return nil, fmt.Errorf("%w: write input codestream: %w", ErrOpenJPHUnavailable, err)
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if decoder.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, decoder.timeout)
		defer cancel()
	}
	command := exec.CommandContext(ctx, executable, "-i", input, "-o", output)
	var processOutput openJPHLimitedOutput
	command.Stdout = &processOutput
	command.Stderr = &processOutput
	if err := command.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%w: ojph_expand timed out after %s", ErrMalformedCodestream, decoder.timeout)
		}
		detail := processOutput.String()
		if detail == "" {
			detail = err.Error()
		}
		var execErr *exec.Error
		var pathErr *os.PathError
		if errors.As(err, &execErr) || errors.As(err, &pathErr) {
			return nil, fmt.Errorf("%w: ojph_expand: %s", ErrOpenJPHUnavailable, detail)
		}
		return nil, fmt.Errorf("%w: ojph_expand: %s", ErrMalformedCodestream, detail)
	}

	info, err := os.Stat(output)
	if err != nil {
		return nil, fmt.Errorf("%w: stat ojph_expand output: %w", ErrMalformedCodestream, err)
	}
	limit := metadata.FrameSize()*2 + 4096
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: OpenJPH output bytes=%d limit=%d", pixeldata.ErrPixelDataSizeMismatch, info.Size(), limit)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, fmt.Errorf("%w: read ojph_expand output: %w", ErrMalformedCodestream, err)
	}
	return openJPHPNMToFrame(data, metadata)
}

// ValidateClinicalRuntime resolves the OpenJPH executable required by
// codecfull.
func ValidateClinicalRuntime() error {
	executable, err := newOpenJPHDecoder().resolveExecutable()
	if err != nil {
		return err
	}
	return validateOpenJPHBinary(executable)
}

func validateOpenJPHBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect runtime: %w", ErrOpenJPHUnavailable, err)
	}
	if info.Size() <= 0 || info.Size() > openJPHBinarySizeLimit {
		return fmt.Errorf(
			"%w: runtime size=%d is outside the qualified inspection limit",
			ErrOpenJPHUnavailable,
			info.Size(),
		)
	}
	if _, err := os.ReadFile(path); err != nil {
		return fmt.Errorf("%w: read runtime: %w", ErrOpenJPHUnavailable, err)
	}
	markerPath := path + QualifiedOpenJPHMarkerSuffix
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("%w: read runtime qualification marker %q: %w", ErrOpenJPHUnavailable, markerPath, err)
	}
	if strings.TrimSpace(string(data)) != QualifiedOpenJPHMarker {
		return fmt.Errorf(
			"%w: runtime qualification marker does not identify OpenJPH %s commit %s",
			ErrOpenJPHUnavailable,
			QualifiedOpenJPHVersion,
			QualifiedOpenJPHCommit,
		)
	}
	return nil
}

func (decoder openJPHDecoder) resolveExecutable() (string, error) {
	executable := decoder.executable
	if executable == "" {
		executable = os.Getenv("DICOM_GO_OPENJPH_EXPAND")
	}
	if executable != "" {
		return validateOpenJPHExecutable(executable)
	}
	name := "ojph_expand"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if current, err := os.Executable(); err == nil {
		dir := filepath.Dir(current)
		for _, candidate := range []string{
			filepath.Join(dir, name),
			filepath.Join(dir, "codec", name),
			filepath.Clean(filepath.Join(dir, "..", "Resources", "codec", name)),
		} {
			if resolved, err := validateOpenJPHExecutable(candidate); err == nil {
				return resolved, nil
			}
		}
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found in bundled runtime or PATH", ErrOpenJPHUnavailable, name)
	}
	return resolved, nil
}

func validateOpenJPHExecutable(path string) (string, error) {
	if filepath.Base(path) == path {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return "", fmt.Errorf("%w: %s not found in PATH", ErrOpenJPHUnavailable, path)
		}
		return resolved, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrOpenJPHUnavailable, path, err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return "", fmt.Errorf("%w: %s is not an executable file", ErrOpenJPHUnavailable, path)
	}
	return path, nil
}

func openJPHOutputExtension(metadata pixeldata.Metadata) string {
	if metadata.SamplesPerPixel == 3 {
		return ".ppm"
	}
	return ".pgm"
}

func openJPHPNMToFrame(data []byte, metadata pixeldata.Metadata) ([]byte, error) {
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return nil, fmt.Errorf(
			"%w: OpenJPH PNM backend requires BitsAllocated 8 or 16, got %d",
			ErrUnsupportedMetadata,
			metadata.BitsAllocated,
		)
	}
	image, err := parseOpenJPHPNM(data)
	if err != nil {
		return nil, err
	}
	if image.width != int(metadata.Columns) || image.height != int(metadata.Rows) {
		return nil, fmt.Errorf("%w: OpenJPH size=%dx%d Columns=%d Rows=%d", ErrImageSizeMismatch, image.width, image.height, metadata.Columns, metadata.Rows)
	}
	components := 1
	if image.magic == "P6" {
		components = 3
	} else if image.magic != "P5" {
		return nil, fmt.Errorf("%w: OpenJPH PNM magic=%s", ErrUnsupportedMetadata, image.magic)
	}
	if components != int(metadata.SamplesPerPixel) {
		return nil, fmt.Errorf("%w: OpenJPH components=%d SamplesPerPixel=%d", ErrImageSizeMismatch, components, metadata.SamplesPerPixel)
	}
	precision, ok := precisionFromPNMMaxVal(image.maxVal)
	if !ok || precision != metadata.BitsStored || precision > metadata.BitsAllocated {
		return nil, fmt.Errorf(
			"%w: OpenJPH precision=%d BitsStored=%d BitsAllocated=%d maxval=%d",
			ErrUnsupportedMetadata,
			precision,
			metadata.BitsStored,
			metadata.BitsAllocated,
			image.maxVal,
		)
	}
	sourceBytes := 1
	if image.maxVal > 255 {
		sourceBytes = 2
	}
	expected := int(metadata.Rows) * int(metadata.Columns) * components * sourceBytes
	if len(image.raster) != expected {
		return nil, fmt.Errorf("%w: OpenJPH raster bytes=%d expected=%d", pixeldata.ErrPixelDataSizeMismatch, len(image.raster), expected)
	}
	destinationBytes := int(metadata.BitsAllocated / 8)
	out := make([]byte, int(metadata.Rows)*int(metadata.Columns)*components*destinationBytes)
	for sample := 0; sample < len(out)/destinationBytes; sample++ {
		var value uint16
		if sourceBytes == 1 {
			value = uint16(image.raster[sample])
		} else {
			value = binary.BigEndian.Uint16(image.raster[sample*2:])
		}
		if destinationBytes == 1 {
			out[sample] = byte(value)
		} else {
			binary.LittleEndian.PutUint16(out[sample*2:], value)
		}
	}
	return out, nil
}

type openJPHPNM struct {
	magic, widthToken, heightToken, maxValToken string
	width, height, maxVal                       int
	raster                                      []byte
}

func parseOpenJPHPNM(data []byte) (openJPHPNM, error) {
	var result openJPHPNM
	offset := 0
	tokens := []*string{&result.magic, &result.widthToken, &result.heightToken, &result.maxValToken}
	for _, target := range tokens {
		token, next, err := nextOpenJPHPNMToken(data, offset)
		if err != nil {
			return result, err
		}
		*target = token
		offset = next
	}
	var err error
	result.width, err = strconv.Atoi(result.widthToken)
	if err != nil || result.width <= 0 {
		return result, fmt.Errorf("%w: OpenJPH PNM width=%q", ErrMalformedCodestream, result.widthToken)
	}
	result.height, err = strconv.Atoi(result.heightToken)
	if err != nil || result.height <= 0 {
		return result, fmt.Errorf("%w: OpenJPH PNM height=%q", ErrMalformedCodestream, result.heightToken)
	}
	result.maxVal, err = strconv.Atoi(result.maxValToken)
	if err != nil || result.maxVal <= 0 || result.maxVal > 65535 {
		return result, fmt.Errorf("%w: OpenJPH PNM maxval=%q", ErrMalformedCodestream, result.maxValToken)
	}
	if offset >= len(data) || !openJPHPNMWhitespace(data[offset]) {
		return result, fmt.Errorf("%w: OpenJPH PNM missing raster separator", ErrMalformedCodestream)
	}
	if data[offset] == '\r' && offset+1 < len(data) && data[offset+1] == '\n' {
		offset += 2
	} else {
		offset++
	}
	result.raster = data[offset:]
	return result, nil
}

func nextOpenJPHPNMToken(data []byte, offset int) (string, int, error) {
	for {
		for offset < len(data) && openJPHPNMWhitespace(data[offset]) {
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
		return "", offset, fmt.Errorf("%w: truncated OpenJPH PNM header", ErrMalformedCodestream)
	}
	start := offset
	for offset < len(data) && !openJPHPNMWhitespace(data[offset]) && data[offset] != '#' {
		offset++
	}
	return string(data[start:offset]), offset, nil
}

func openJPHPNMWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

type openJPHLimitedOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (output *openJPHLimitedOutput) Write(data []byte) (int, error) {
	remaining := openJPHProcessOutputLimit - output.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			output.buffer.Write(data[:remaining])
			output.truncated = true
		} else {
			output.buffer.Write(data)
		}
	} else if len(data) > 0 {
		output.truncated = true
	}
	return len(data), nil
}

func (output *openJPHLimitedOutput) String() string {
	detail := strings.TrimSpace(output.buffer.String())
	if output.truncated {
		detail += " (truncated)"
	}
	return detail
}
