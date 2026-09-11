package codeccost

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const ppmHeaderLimit = 4 * 1024

type ppmImage struct {
	magic  string
	width  int
	height int
	maxVal int
	raster []byte
}

func ppmToFrameBytes(data []byte, metadata pixeldata.Metadata) ([]byte, error) {
	ppm, err := parsePPM(data)
	if err != nil {
		return nil, err
	}
	if ppm.width != int(metadata.Columns) || ppm.height != int(metadata.Rows) {
		return nil, fmt.Errorf("codeccost: size=%dx%d Columns=%d Rows=%d", ppm.width, ppm.height, metadata.Columns, metadata.Rows)
	}
	components := 1
	switch ppm.magic {
	case "P5":
		components = 1
	case "P6":
		components = 3
	default:
		return nil, fmt.Errorf("codeccost: PPM magic=%s", ppm.magic)
	}
	if components != int(metadata.SamplesPerPixel) {
		return nil, fmt.Errorf("codeccost: components=%d SamplesPerPixel=%d", components, metadata.SamplesPerPixel)
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return nil, fmt.Errorf("codeccost: unsupported BitsAllocated=%d", metadata.BitsAllocated)
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated {
		return nil, fmt.Errorf("codeccost: BitsStored=%d BitsAllocated=%d", metadata.BitsStored, metadata.BitsAllocated)
	}
	wantMax := int((uint32(1) << metadata.BitsStored) - 1)
	if ppm.maxVal != wantMax {
		return nil, fmt.Errorf("codeccost: maxval=%d BitsStored=%d", ppm.maxVal, metadata.BitsStored)
	}
	bytesPerSample := 1
	if ppm.maxVal > 255 {
		bytesPerSample = 2
	}
	expectedRaster := int(metadata.Rows) * int(metadata.Columns) * components * bytesPerSample
	if len(ppm.raster) != expectedRaster {
		return nil, fmt.Errorf("codeccost: raster bytes=%d expected=%d", len(ppm.raster), expectedRaster)
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
		if sample > uint32(ppm.maxVal) {
			return nil, fmt.Errorf("codeccost: sample %d outside BitsStored range maxval=%d", sample, ppm.maxVal)
		}
		destinationOffset := sampleIndex * bytesAllocated
		if bytesAllocated == 1 {
			out[destinationOffset] = byte(sample)
		} else {
			binary.LittleEndian.PutUint16(out[destinationOffset:], uint16(sample))
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
		return nilPPMError("width", widthToken)
	}
	height, err := strconv.Atoi(heightToken)
	if err != nil || height <= 0 {
		return nilPPMError("height", heightToken)
	}
	maxVal, err := strconv.Atoi(maxValToken)
	if err != nil || maxVal <= 0 || maxVal > 65535 {
		return nilPPMError("maxval", maxValToken)
	}
	if offset >= len(data) || !isPPMWhitespace(data[offset]) {
		return ppmImage{}, fmt.Errorf("codeccost: PPM missing raster separator")
	}
	if data[offset] == '\r' && offset+1 < len(data) && data[offset+1] == '\n' {
		offset += 2
	} else {
		offset++
	}
	return ppmImage{magic: magic, width: width, height: height, maxVal: maxVal, raster: data[offset:]}, nil
}

func nilPPMError(field, token string) (ppmImage, error) {
	return ppmImage{}, fmt.Errorf("codeccost: PPM %s=%q", field, token)
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
		return "", offset, fmt.Errorf("codeccost: truncated PPM header")
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

func djxlOutputExtension(metadata pixeldata.Metadata) string {
	if metadata.SamplesPerPixel == 3 {
		return ".ppm"
	}
	return ".pgm"
}

func ppmOutputSizeLimit(metadata pixeldata.Metadata) int64 {
	rasterBytes := int64(metadata.Rows) * int64(metadata.Columns) * int64(metadata.SamplesPerPixel) * 2
	return rasterBytes + ppmHeaderLimit
}
