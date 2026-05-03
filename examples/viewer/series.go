package main

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	tagImagePositionPatient = core.NewTag(0x0020, 0x0032)
	tagSliceLocation        = core.NewTag(0x0020, 0x1041)
	tagInstanceNumber       = core.NewTag(0x0020, 0x0013)
	tagWindowCenter         = core.NewTag(0x0028, 0x1050)
	tagWindowWidth          = core.NewTag(0x0028, 0x1051)
	tagRescaleIntercept     = core.NewTag(0x0028, 0x1052)
	tagRescaleSlope         = core.NewTag(0x0028, 0x1053)
)

const (
	defaultWindowCenter = 40.0
	defaultWindowWidth  = 400.0
)

type Series struct {
	Input      string
	Frames     []Frame
	LoadErrors []error
}

type Frame struct {
	SourceName         string
	FrameIndex         int
	Metadata           pixeldata.Metadata
	TransferSyntaxUID  string
	TransferSyntaxName string
	ByteOrder          binary.ByteOrder
	PixelBytes         []byte
	DefaultWindow      Window
	Rescale            Rescale
	Sort               sortKey
	DecodeErr          error
}

type Window struct {
	Center float64
	Width  float64
}

type Rescale struct {
	Slope     float64
	Intercept float64
}

type sortKey struct {
	rank       int
	value      float64
	instance   int64
	sourceName string
	frameIndex int
}

func loadSeries(input string) (*Series, error) {
	if strings.TrimSpace(input) == "" {
		return nil, errors.New("viewer: missing DICOM input")
	}
	if err := rle.RegisterDefault(); err != nil {
		return nil, fmt.Errorf("register RLE codec: %w", err)
	}

	info, err := os.Stat(input)
	if err != nil {
		return nil, err
	}

	series := &Series{Input: input}
	switch {
	case info.IsDir():
		err = loadDirectory(input, series)
	case strings.EqualFold(filepath.Ext(input), ".zip"):
		err = loadZip(input, series)
	default:
		err = loadPath(input, filepath.Base(input), series)
	}
	if err != nil {
		return nil, err
	}
	if len(series.Frames) == 0 {
		if len(series.LoadErrors) > 0 {
			return nil, fmt.Errorf("viewer: no displayable DICOM frames found: %w", series.LoadErrors[0])
		}
		return nil, errors.New("viewer: no displayable DICOM frames found")
	}

	sortFrames(series.Frames)
	return series, nil
}

func loadDirectory(root string, series *Series) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			series.LoadErrors = append(series.LoadErrors, fmt.Errorf("%s: %w", name, err))
			return nil
		}
		base := filepath.Base(name)
		if entry.IsDir() {
			if shouldSkipSidecar(base) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkipSidecar(base) || !isDICOMName(base) {
			return nil
		}
		displayName, relErr := filepath.Rel(root, name)
		if relErr != nil {
			displayName = name
		}
		return loadPath(name, displayName, series)
	})
}

func loadZip(name string, series *Series) error {
	reader, err := zip.OpenReader(name)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		if file.FileInfo().IsDir() || shouldSkipZipEntry(file.Name) || !isDICOMName(file.Name) {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			series.LoadErrors = append(series.LoadErrors, fmt.Errorf("%s: %w", file.Name, err))
			continue
		}
		if err := loadReader(file.Name, rc, series); err != nil {
			series.LoadErrors = append(series.LoadErrors, err)
		}
		if err := rc.Close(); err != nil {
			series.LoadErrors = append(series.LoadErrors, fmt.Errorf("%s: close: %w", file.Name, err))
		}
	}
	return nil
}

func loadPath(name, displayName string, series *Series) error {
	file, err := os.Open(name)
	if err != nil {
		series.LoadErrors = append(series.LoadErrors, fmt.Errorf("%s: %w", displayName, err))
		return nil
	}
	defer file.Close()

	if err := loadReader(displayName, file, series); err != nil {
		series.LoadErrors = append(series.LoadErrors, err)
	}
	return nil
}

func loadReader(displayName string, reader io.Reader, series *Series) error {
	file, err := object.ReadFile(reader)
	if err != nil {
		return fmt.Errorf("%s: read DICOM: %w", displayName, err)
	}
	frames := framesFromFile(displayName, file)
	series.Frames = append(series.Frames, frames...)
	return nil
}

func framesFromFile(displayName string, file *object.File) []Frame {
	base := baseFrame(displayName, file)
	metadata, data, err := decodeFrames(file)
	if err != nil {
		base.DecodeErr = err
		if metadata.Rows != 0 && metadata.Columns != 0 {
			base.Metadata = metadata
		}
		return []Frame{base}
	}

	frames := make([]Frame, 0, len(data))
	for i, frameData := range data {
		frame := base
		frame.FrameIndex = i
		frame.Metadata = metadata
		frame.PixelBytes = frameData
		frame.Sort.frameIndex = i
		frames = append(frames, frame)
	}
	return frames
}

func baseFrame(displayName string, file *object.File) Frame {
	syntax := transfer.Syntax{}
	if file != nil {
		syntax = file.TransferSyntax
	}
	order := syntax.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	frame := Frame{
		SourceName:         displayName,
		TransferSyntaxUID:  syntax.UID,
		TransferSyntaxName: syntax.Name,
		ByteOrder:          order,
		DefaultWindow:      Window{Center: defaultWindowCenter, Width: defaultWindowWidth},
		Rescale:            Rescale{Slope: 1},
		Sort:               sortKey{rank: 3, sourceName: displayName},
	}
	if file != nil && file.Dataset != nil {
		frame.DefaultWindow = defaultWindow(file.Dataset)
		frame.Rescale = rescale(file.Dataset)
		frame.Sort = sortKeyFor(file.Dataset, displayName)
	}
	return frame
}

func decodeFrames(file *object.File) (pixeldata.Metadata, [][]byte, error) {
	if file == nil || file.Dataset == nil {
		return pixeldata.Metadata{}, nil, errors.New("viewer: file has no dataset")
	}

	native, err := pixeldata.ExtractNativeFrames(file.Dataset)
	if err == nil {
		return native.Metadata, native.Data, nil
	}
	if !errors.Is(err, pixeldata.ErrEncapsulatedPixelData) {
		metadata, metaErr := pixeldata.ExtractMetadata(file.Dataset)
		if metaErr != nil {
			return pixeldata.Metadata{}, nil, err
		}
		return metadata, nil, err
	}

	metadata, metaErr := pixeldata.ExtractMetadata(file.Dataset)
	if metaErr != nil {
		return pixeldata.Metadata{}, nil, metaErr
	}
	pixels, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		return metadata, nil, err
	}
	frames, err := pixeldata.DecodeFrames(file.TransferSyntax.UID, pixels, file.Dataset)
	if err != nil {
		if errors.Is(err, pixeldata.ErrCodecNotFound) {
			return metadata, nil, fmt.Errorf("unsupported pixel codec for %s (%s): %w", file.TransferSyntax.Name, file.TransferSyntax.UID, err)
		}
		return metadata, nil, err
	}
	return metadata, frames.Data, nil
}

func defaultWindow(obj *object.Object) Window {
	centers, centerErr := obj.GetFloats(tagWindowCenter)
	widths, widthErr := obj.GetFloats(tagWindowWidth)
	if centerErr == nil && widthErr == nil && len(centers) > 0 && len(widths) > 0 && widths[0] > 0 {
		return Window{Center: centers[0], Width: widths[0]}
	}
	return Window{Center: defaultWindowCenter, Width: defaultWindowWidth}
}

func rescale(obj *object.Object) Rescale {
	out := Rescale{Slope: 1}
	if slope, err := obj.GetFloat(tagRescaleSlope); err == nil && slope != 0 {
		out.Slope = slope
	}
	if intercept, err := obj.GetFloat(tagRescaleIntercept); err == nil {
		out.Intercept = intercept
	}
	return out
}

func sortKeyFor(obj *object.Object, displayName string) sortKey {
	key := sortKey{rank: 3, sourceName: displayName}
	if values, err := obj.GetFloats(tagImagePositionPatient); err == nil && len(values) >= 3 {
		key.rank = 0
		key.value = values[2]
		return key
	}
	if value, err := obj.GetFloat(tagSliceLocation); err == nil {
		key.rank = 1
		key.value = value
		return key
	}
	if value, err := obj.GetInt(tagInstanceNumber); err == nil {
		key.rank = 2
		key.instance = value
		return key
	}
	return key
}

func sortFrames(frames []Frame) {
	sort.SliceStable(frames, func(i, j int) bool {
		left := frames[i].Sort
		right := frames[j].Sort
		if left.rank != right.rank {
			return left.rank < right.rank
		}
		switch left.rank {
		case 0, 1:
			if left.value != right.value {
				return left.value < right.value
			}
		case 2:
			if left.instance != right.instance {
				return left.instance < right.instance
			}
		}
		if left.sourceName != right.sourceName {
			return left.sourceName < right.sourceName
		}
		return left.frameIndex < right.frameIndex
	})
}

func isDICOMName(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".dcm")
}

func shouldSkipZipEntry(name string) bool {
	clean := path.Clean(name)
	if clean == "." {
		return true
	}
	for _, part := range strings.Split(clean, "/") {
		if shouldSkipSidecar(part) {
			return true
		}
	}
	return false
}

func shouldSkipSidecar(name string) bool {
	return name == "" || name == "__MACOSX" || name == ".DS_Store" || strings.HasPrefix(name, "._")
}
