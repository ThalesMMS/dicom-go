// Package microscopy models tiled DICOM whole-slide microscopy images without
// materializing their Total Pixel Matrix.
package microscopy

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	VLWholeSlideMicroscopyImageStorage      = "1.2.840.10008.5.1.4.1.1.77.1.6"
	ConfocalMicroscopyTiledPyramidalStorage = "1.2.840.10008.5.1.4.1.1.77.1.9"
	MicroscopyBulkSimpleAnnotationsStorage  = "1.2.840.10008.5.1.4.1.1.91.1"
	maxMicroscopyFrames                     = 100_000_000
	defaultOpticalPathIdentifier            = "1"
	defaultMicroscopyFocalPlaneIndex        = 0
)

var (
	ErrNotTiledMicroscopy = errors.New("dicom/microscopy: not a tiled microscopy image")
	ErrInvalidSlide       = errors.New("dicom/microscopy: invalid slide metadata")
	ErrUncalibratedSlide  = errors.New("dicom/microscopy: slide coordinates are not calibrated")
)

var (
	tagSOPClassUID                       = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID                    = core.NewTag(0x0008, 0x0018)
	tagStudyInstanceUID                  = core.NewTag(0x0020, 0x000D)
	tagSeriesInstanceUID                 = core.NewTag(0x0020, 0x000E)
	tagRows                              = core.NewTag(0x0028, 0x0010)
	tagColumns                           = core.NewTag(0x0028, 0x0011)
	tagNumberOfFrames                    = core.NewTag(0x0028, 0x0008)
	tagPixelSpacing                      = core.NewTag(0x0028, 0x0030)
	tagSharedFunctionalGroups            = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups          = core.NewTag(0x5200, 0x9230)
	tagPixelMeasuresSequence             = core.NewTag(0x0028, 0x9110)
	tagTotalPixelMatrixColumns           = core.NewTag(0x0048, 0x0006)
	tagTotalPixelMatrixRows              = core.NewTag(0x0048, 0x0007)
	tagTotalPixelMatrixOriginSequence    = core.NewTag(0x0048, 0x0008)
	tagImagedVolumeWidth                 = core.NewTag(0x0048, 0x0001)
	tagImagedVolumeHeight                = core.NewTag(0x0048, 0x0002)
	tagImagedVolumeDepth                 = core.NewTag(0x0048, 0x0003)
	tagImageOrientationSlide             = core.NewTag(0x0048, 0x0102)
	tagOpticalPathSequence               = core.NewTag(0x0048, 0x0105)
	tagOpticalPathIdentifier             = core.NewTag(0x0048, 0x0106)
	tagOpticalPathDescription            = core.NewTag(0x0048, 0x0107)
	tagOpticalPathIdentificationSequence = core.NewTag(0x0048, 0x0207)
	tagPlanePositionSlideSequence        = core.NewTag(0x0048, 0x021A)
	tagColumnPositionTotalMatrix         = core.NewTag(0x0048, 0x021E)
	tagRowPositionTotalMatrix            = core.NewTag(0x0048, 0x021F)
	tagNumberOfOpticalPaths              = core.NewTag(0x0048, 0x0302)
	tagTotalPixelMatrixFocalPlanes       = core.NewTag(0x0048, 0x0303)
	tagNumberOfFocalPlanes               = core.NewTag(0x0048, 0x0013)
	tagDistanceBetweenFocalPlanes        = core.NewTag(0x0048, 0x0014)
	tagXOffsetSlide                      = core.NewTag(0x0040, 0x072A)
	tagYOffsetSlide                      = core.NewTag(0x0040, 0x073A)
	tagZOffsetSlide                      = core.NewTag(0x0040, 0x074A)
	tagContainerIdentifier               = core.NewTag(0x0040, 0x0512)
	tagSpecimenIdentifier                = core.NewTag(0x0040, 0x0551)
	tagSpecimenDescriptionSequence       = core.NewTag(0x0040, 0x0560)
)

// InstanceRef identifies a WSI instance used as a tile source.
type InstanceRef struct {
	StudyInstanceUID  string
	SeriesInstanceUID string
	SOPInstanceUID    string
}

// SlidePoint is a point in the DICOM Slide Coordinate System, in millimeters.
type SlidePoint struct {
	X float64
	Y float64
	Z float64
}

type OpticalPath struct {
	Identifier  string
	Description string
}

type Specimen struct {
	Identifier          string
	ContainerIdentifier string
	Description         string
}

// Tile is a one-based DICOM frame positioned in the Total Pixel Matrix. Row
// and Column are zero-based pixel coordinates in that matrix.
type Tile struct {
	Source            InstanceRef
	FrameNumber       int
	Row               int
	Column            int
	Width             int
	Height            int
	OpticalPath       string
	FocalPlane        int
	ZOffsetMillimeter float64
}

func (t Tile) Bounds() image.Rectangle {
	return image.Rect(t.Column, t.Row, t.Column+t.Width, t.Row+t.Height)
}

func (t Tile) Key() TileKey {
	return TileKey{
		Source: t.Source, FrameNumber: t.FrameNumber,
		OpticalPath: t.OpticalPath, FocalPlane: t.FocalPlane,
	}
}

type TileKey struct {
	Source      InstanceRef
	FrameNumber int
	OpticalPath string
	FocalPlane  int
}

// Level describes one WSI pyramid resolution. Pixel spacing is expressed in
// millimeters per Total Pixel Matrix pixel.
type Level struct {
	ID                string
	MatrixWidth       int
	MatrixHeight      int
	TileWidth         int
	TileHeight        int
	PixelSpacingX     float64
	PixelSpacingY     float64
	Origin            SlidePoint
	Orientation       [6]float64
	ImagedVolumeDepth float64
	Tiles             []Tile
	Source            InstanceRef
	FrameCount        int
	ImplicitTiling    bool
	OpticalPathIDs    []string
	FocalPlaneOffsets []float64
}

func (l Level) MatrixBounds() image.Rectangle {
	return image.Rect(0, 0, l.MatrixWidth, l.MatrixHeight)
}

func (l Level) Calibrated() bool {
	return finitePositive(l.PixelSpacingX) && finitePositive(l.PixelSpacingY)
}

// SlideCoordinate maps a Total Pixel Matrix pixel to the DICOM slide
// coordinate system. Image Orientation (Slide) is honored when present.
func (l Level) SlideCoordinate(pixel image.Point, zMillimeter float64) (SlidePoint, error) {
	if !l.Calibrated() {
		return SlidePoint{}, ErrUncalibratedSlide
	}
	row := l.Orientation[0:3]
	column := l.Orientation[3:6]
	if zeroVector(row) || zeroVector(column) {
		row = []float64{1, 0, 0}
		column = []float64{0, 1, 0}
	}
	xDistance := float64(pixel.X) * l.PixelSpacingX
	yDistance := float64(pixel.Y) * l.PixelSpacingY
	return SlidePoint{
		X: l.Origin.X + row[0]*xDistance + column[0]*yDistance,
		Y: l.Origin.Y + row[1]*xDistance + column[1]*yDistance,
		Z: l.Origin.Z + row[2]*xDistance + column[2]*yDistance + zMillimeter,
	}, nil
}

// PixelCoordinate maps a calibrated DICOM Slide Coordinate System point back
// into floating-point Total Pixel Matrix coordinates.
func (l Level) PixelCoordinate(point SlidePoint) (x, y float64, err error) {
	if !l.Calibrated() {
		return 0, 0, ErrUncalibratedSlide
	}
	row := l.Orientation[0:3]
	column := l.Orientation[3:6]
	if zeroVector(row) || zeroVector(column) {
		row = []float64{1, 0, 0}
		column = []float64{0, 1, 0}
	}
	delta := [3]float64{
		point.X - l.Origin.X,
		point.Y - l.Origin.Y,
		point.Z - l.Origin.Z,
	}
	x = (delta[0]*row[0] + delta[1]*row[1] + delta[2]*row[2]) / l.PixelSpacingX
	y = (delta[0]*column[0] + delta[1]*column[1] + delta[2]*column[2]) / l.PixelSpacingY
	return x, y, nil
}

func (l Level) Distance(a, b image.Point) (float64, error) {
	first, err := l.SlideCoordinate(a, 0)
	if err != nil {
		return 0, err
	}
	second, err := l.SlideCoordinate(b, 0)
	if err != nil {
		return 0, err
	}
	return math.Sqrt(
		(second.X-first.X)*(second.X-first.X) +
			(second.Y-first.Y)*(second.Y-first.Y) +
			(second.Z-first.Z)*(second.Z-first.Z),
	), nil
}

// TilesForViewport returns only tiles intersecting the requested Total Pixel
// Matrix rectangle and selected optical path/focal plane. Results are sorted
// center-first to prioritize the visible viewport under network contention.
func (l Level) TilesForViewport(viewport image.Rectangle, opticalPath string, focalPlane int) []Tile {
	return l.TilesForViewportLimit(viewport, opticalPath, focalPlane, 0)
}

// TilesForViewportLimit is TilesForViewport with a center-priority result cap.
// A non-positive limit returns every matching tile.
func (l Level) TilesForViewportLimit(viewport image.Rectangle, opticalPath string, focalPlane, limit int) []Tile {
	viewport = viewport.Intersect(l.MatrixBounds())
	if viewport.Empty() {
		return nil
	}
	if l.ImplicitTiling {
		return l.implicitTilesForViewport(viewport, opticalPath, focalPlane, limit)
	}
	out := make([]Tile, 0)
	for _, tile := range l.Tiles {
		if opticalPath != "" && tile.OpticalPath != opticalPath {
			continue
		}
		if focalPlane >= 0 && tile.FocalPlane != focalPlane {
			continue
		}
		if tile.Bounds().Overlaps(viewport) {
			out = append(out, tile)
		}
	}
	center := image.Pt((viewport.Min.X+viewport.Max.X)/2, (viewport.Min.Y+viewport.Max.Y)/2)
	sort.SliceStable(out, func(i, j int) bool {
		return squaredDistance(out[i].Bounds(), center) < squaredDistance(out[j].Bounds(), center)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (l Level) implicitTilesForViewport(viewport image.Rectangle, opticalPath string, focalPlane, limit int) []Tile {
	tilesAcross := ceilDiv(l.MatrixWidth, l.TileWidth)
	tilesDown := ceilDiv(l.MatrixHeight, l.TileHeight)
	tilesPerPlanePath := tilesAcross * tilesDown
	pathIDs := l.OpticalPathIDs
	if len(pathIDs) == 0 {
		pathIDs = []string{defaultOpticalPathIdentifier}
	}
	var pathIndices []int
	for index, identifier := range pathIDs {
		if opticalPath == "" || opticalPath == identifier {
			pathIndices = append(pathIndices, index)
		}
	}
	if len(pathIndices) == 0 {
		return nil
	}
	planeCount := max(1, len(l.FocalPlaneOffsets))
	firstPlane, lastPlane := 0, planeCount-1
	if focalPlane >= 0 {
		if focalPlane >= planeCount {
			return nil
		}
		firstPlane, lastPlane = focalPlane, focalPlane
	}
	firstColumn := max(0, viewport.Min.X/l.TileWidth)
	lastColumn := min(tilesAcross-1, (viewport.Max.X-1)/l.TileWidth)
	firstRow := max(0, viewport.Min.Y/l.TileHeight)
	lastRow := min(tilesDown-1, (viewport.Max.Y-1)/l.TileHeight)
	capacity := (lastColumn - firstColumn + 1) * (lastRow - firstRow + 1) * len(pathIndices) * (lastPlane - firstPlane + 1)
	if limit > 0 {
		capacity = min(capacity, limit)
	}
	out := make([]Tile, 0, capacity)
	appendCoordinate := func(rowIndex, columnIndex int) bool {
		for plane := firstPlane; plane <= lastPlane; plane++ {
			for _, pathIndex := range pathIndices {
				ordinal := rowIndex*tilesAcross + columnIndex
				frameNumber := plane*tilesPerPlanePath*len(pathIDs) + pathIndex*tilesPerPlanePath + ordinal + 1
				if frameNumber > l.FrameCount {
					continue
				}
				column := columnIndex * l.TileWidth
				row := rowIndex * l.TileHeight
				z := 0.0
				if plane < len(l.FocalPlaneOffsets) {
					z = l.FocalPlaneOffsets[plane]
				}
				out = append(out, Tile{
					Source: l.Source, FrameNumber: frameNumber,
					Row: row, Column: column,
					Width: min(l.TileWidth, l.MatrixWidth-column), Height: min(l.TileHeight, l.MatrixHeight-row),
					OpticalPath: pathIDs[pathIndex], FocalPlane: plane, ZOffsetMillimeter: z,
				})
				if limit > 0 && len(out) >= limit {
					return true
				}
			}
		}
		return false
	}
	if limit > 0 {
		centerColumn := min(lastColumn, max(firstColumn, ((viewport.Min.X+viewport.Max.X)/2)/l.TileWidth))
		centerRow := min(lastRow, max(firstRow, ((viewport.Min.Y+viewport.Max.Y)/2)/l.TileHeight))
		maxRadius := max(
			max(centerColumn-firstColumn, lastColumn-centerColumn),
			max(centerRow-firstRow, lastRow-centerRow),
		)
		for radius := 0; radius <= maxRadius; radius++ {
			for rowIndex := centerRow - radius; rowIndex <= centerRow+radius; rowIndex++ {
				if rowIndex < firstRow || rowIndex > lastRow {
					continue
				}
				for columnIndex := centerColumn - radius; columnIndex <= centerColumn+radius; columnIndex++ {
					if columnIndex < firstColumn || columnIndex > lastColumn ||
						(radius > 0 && rowIndex != centerRow-radius && rowIndex != centerRow+radius &&
							columnIndex != centerColumn-radius && columnIndex != centerColumn+radius) {
						continue
					}
					if appendCoordinate(rowIndex, columnIndex) {
						return out
					}
				}
			}
		}
		return out
	}
	for rowIndex := firstRow; rowIndex <= lastRow; rowIndex++ {
		for columnIndex := firstColumn; columnIndex <= lastColumn; columnIndex++ {
			appendCoordinate(rowIndex, columnIndex)
		}
	}
	center := image.Pt((viewport.Min.X+viewport.Max.X)/2, (viewport.Min.Y+viewport.Max.Y)/2)
	sort.SliceStable(out, func(i, j int) bool {
		return squaredDistance(out[i].Bounds(), center) < squaredDistance(out[j].Bounds(), center)
	})
	return out
}

type Instance struct {
	Level          Level
	OpticalPaths   []OpticalPath
	FocalPlanes    []float64
	Specimen       Specimen
	DimensionOrder []string
}

// Pyramid combines sparse resolution levels without inventing missing levels.
type Pyramid struct {
	Levels       []Level
	OpticalPaths []OpticalPath
	FocalPlanes  []float64
	Specimens    []Specimen
}

func (p *Pyramid) Add(instance Instance) error {
	if p == nil {
		return fmt.Errorf("%w: nil pyramid", ErrInvalidSlide)
	}
	level := instance.Level
	if level.MatrixWidth <= 0 || level.MatrixHeight <= 0 || level.TileWidth <= 0 || level.TileHeight <= 0 {
		return fmt.Errorf("%w: invalid pyramid level", ErrInvalidSlide)
	}
	p.Levels = append(p.Levels, level)
	p.OpticalPaths = appendUniqueOpticalPaths(p.OpticalPaths, instance.OpticalPaths...)
	p.FocalPlanes = appendUniqueFloats(p.FocalPlanes, instance.FocalPlanes...)
	if instance.Specimen != (Specimen{}) {
		p.Specimens = appendUniqueSpecimens(p.Specimens, instance.Specimen)
	}
	sort.SliceStable(p.Levels, func(i, j int) bool {
		left, right := p.Levels[i], p.Levels[j]
		if left.Calibrated() && right.Calibrated() && left.PixelSpacingX != right.PixelSpacingX {
			return left.PixelSpacingX < right.PixelSpacingX
		}
		return int64(left.MatrixWidth)*int64(left.MatrixHeight) > int64(right.MatrixWidth)*int64(right.MatrixHeight)
	})
	sort.Float64s(p.FocalPlanes)
	return nil
}

// LevelForScale chooses the level whose source pixels most closely match one
// screen pixel, preferring the next finer level over an undersampled one.
func (p *Pyramid) LevelForScale(totalMatrixPixelsPerScreenPixel float64) (Level, bool) {
	if p == nil || len(p.Levels) == 0 {
		return Level{}, false
	}
	if !finitePositive(totalMatrixPixelsPerScreenPixel) {
		return p.Levels[0], true
	}
	base := p.Levels[0]
	best := base
	bestError := math.Inf(1)
	for _, level := range p.Levels {
		ratio := float64(base.MatrixWidth) / float64(level.MatrixWidth)
		if !finitePositive(ratio) {
			continue
		}
		err := math.Abs(math.Log2(ratio / totalMatrixPixelsPerScreenPixel))
		if err < bestError || (err == bestError && ratio < totalMatrixPixelsPerScreenPixel) {
			best, bestError = level, err
		}
	}
	return best, true
}

// Read parses one tiled microscopy image instance. Pixel Data is intentionally
// not decoded or copied.
func Read(file *object.File) (Instance, error) {
	if file == nil || file.Dataset == nil {
		return Instance{}, fmt.Errorf("%w: nil dataset", ErrInvalidSlide)
	}
	root := file.Dataset
	sopClass := derivedio.CleanUID(root, tagSOPClassUID)
	if sopClass != VLWholeSlideMicroscopyImageStorage && sopClass != ConfocalMicroscopyTiledPyramidalStorage {
		return Instance{}, ErrNotTiledMicroscopy
	}
	matrixWidth := derivedio.Int(root, tagTotalPixelMatrixColumns)
	matrixHeight := derivedio.Int(root, tagTotalPixelMatrixRows)
	tileWidth := derivedio.Int(root, tagColumns)
	tileHeight := derivedio.Int(root, tagRows)
	frameCount := derivedio.Int(root, tagNumberOfFrames)
	if frameCount <= 0 {
		frameCount = 1
	}
	if matrixWidth <= 0 || matrixHeight <= 0 || tileWidth <= 0 || tileHeight <= 0 ||
		frameCount > maxMicroscopyFrames {
		return Instance{}, fmt.Errorf(
			"%w: matrix=%dx%d tile=%dx%d frames=%d",
			ErrInvalidSlide, matrixWidth, matrixHeight, tileWidth, tileHeight, frameCount,
		)
	}
	ref := InstanceRef{
		StudyInstanceUID:  derivedio.CleanUID(root, tagStudyInstanceUID),
		SeriesInstanceUID: derivedio.CleanUID(root, tagSeriesInstanceUID),
		SOPInstanceUID:    derivedio.CleanUID(root, tagSOPInstanceUID),
	}
	spacingX, spacingY := slidePixelSpacing(root, matrixWidth, matrixHeight)
	origin := slideOrigin(root)
	orientation := slideOrientation(root)
	opticalPaths := readOpticalPaths(root)
	if len(opticalPaths) == 0 {
		opticalPaths = []OpticalPath{{Identifier: defaultOpticalPathIdentifier}}
	}
	focalPlanes := readFocalPlanes(root, origin.Z)
	perFrame := derivedio.Sequence(root, tagPerFrameFunctionalGroups)
	var tiles []Tile
	if len(perFrame) > 0 {
		var err error
		tiles, err = readTiles(root, ref, matrixWidth, matrixHeight, tileWidth, tileHeight, frameCount, opticalPaths, focalPlanes)
		if err != nil {
			return Instance{}, err
		}
	} else {
		tilesPerPlanePath := int64(ceilDiv(matrixWidth, tileWidth)) * int64(ceilDiv(matrixHeight, tileHeight))
		expected := tilesPerPlanePath * int64(max(1, len(opticalPaths))) * int64(max(1, len(focalPlanes)))
		if int64(frameCount) > expected {
			return Instance{}, fmt.Errorf("%w: %d implicit frames exceed %d tile positions", ErrInvalidSlide, frameCount, expected)
		}
	}
	pathIDs := make([]string, len(opticalPaths))
	for index, path := range opticalPaths {
		pathIDs[index] = path.Identifier
	}
	instance := Instance{
		Level: Level{
			ID:          fmt.Sprintf("%s:%dx%d", ref.SOPInstanceUID, matrixWidth, matrixHeight),
			MatrixWidth: matrixWidth, MatrixHeight: matrixHeight,
			TileWidth: tileWidth, TileHeight: tileHeight,
			PixelSpacingX: spacingX, PixelSpacingY: spacingY,
			Origin: origin, Orientation: orientation,
			ImagedVolumeDepth: firstFloat(root, tagImagedVolumeDepth),
			Tiles:             tiles, Source: ref,
			FrameCount: frameCount, ImplicitTiling: len(perFrame) == 0,
			OpticalPathIDs: pathIDs, FocalPlaneOffsets: append([]float64(nil), focalPlanes...),
		},
		OpticalPaths: opticalPaths,
		FocalPlanes:  focalPlanes,
		Specimen:     readSpecimen(root),
	}
	return instance, nil
}

func readTiles(
	root *object.Object,
	ref InstanceRef,
	matrixWidth, matrixHeight, tileWidth, tileHeight, frameCount int,
	opticalPaths []OpticalPath,
	focalPlanes []float64,
) ([]Tile, error) {
	perFrame := derivedio.Sequence(root, tagPerFrameFunctionalGroups)
	if len(perFrame) != 0 && len(perFrame) != frameCount {
		return nil, fmt.Errorf("%w: %d per-frame groups for %d frames", ErrInvalidSlide, len(perFrame), frameCount)
	}
	tilesAcross := ceilDiv(matrixWidth, tileWidth)
	tilesDown := ceilDiv(matrixHeight, tileHeight)
	tilesPerPlanePath := tilesAcross * tilesDown
	expected := tilesPerPlanePath * max(1, len(opticalPaths)) * max(1, len(focalPlanes))
	if len(perFrame) == 0 && frameCount > expected {
		return nil, fmt.Errorf("%w: %d implicit frames exceed %d tile positions", ErrInvalidSlide, frameCount, expected)
	}
	tiles := make([]Tile, frameCount)
	for index := range tiles {
		pathIndex := (index / tilesPerPlanePath) % max(1, len(opticalPaths))
		planeIndex := (index / (tilesPerPlanePath * max(1, len(opticalPaths)))) % max(1, len(focalPlanes))
		tileOrdinal := index % tilesPerPlanePath
		row := (tileOrdinal / tilesAcross) * tileHeight
		column := (tileOrdinal % tilesAcross) * tileWidth
		pathID := opticalPaths[pathIndex].Identifier
		z := focalPlanes[planeIndex]
		if index < len(perFrame) {
			item := perFrame[index]
			position := firstNestedSequenceItem(item, tagPlanePositionSlideSequence)
			if position != nil {
				columnPosition := derivedio.Int(position, tagColumnPositionTotalMatrix)
				rowPosition := derivedio.Int(position, tagRowPositionTotalMatrix)
				if columnPosition <= 0 || rowPosition <= 0 {
					return nil, fmt.Errorf("%w: frame %d has invalid one-based tile position", ErrInvalidSlide, index+1)
				}
				column, row = columnPosition-1, rowPosition-1
				if values := derivedio.Floats(position, tagZOffsetSlide); len(values) > 0 {
					z = values[0]
					planeIndex = nearestFloatIndex(focalPlanes, z)
				}
			}
			pathItem := firstNestedSequenceItem(item, tagOpticalPathIdentificationSequence)
			if pathItem != nil {
				if identifier := derivedio.CleanString(pathItem, tagOpticalPathIdentifier); identifier != "" {
					pathID = identifier
				}
			}
		}
		if column < 0 || row < 0 || column >= matrixWidth || row >= matrixHeight {
			return nil, fmt.Errorf("%w: frame %d tile origin (%d,%d) outside matrix", ErrInvalidSlide, index+1, column, row)
		}
		tiles[index] = Tile{
			Source: ref, FrameNumber: index + 1,
			Row: row, Column: column,
			Width:       min(tileWidth, matrixWidth-column),
			Height:      min(tileHeight, matrixHeight-row),
			OpticalPath: pathID, FocalPlane: planeIndex,
			ZOffsetMillimeter: z,
		}
	}
	return tiles, nil
}

func slidePixelSpacing(root *object.Object, matrixWidth, matrixHeight int) (float64, float64) {
	var spacing []float64
	if shared := derivedio.Sequence(root, tagSharedFunctionalGroups); len(shared) > 0 {
		if measures := firstNestedSequenceItem(shared[0], tagPixelMeasuresSequence); measures != nil {
			spacing = derivedio.Floats(measures, tagPixelSpacing)
		}
	}
	if len(spacing) < 2 {
		spacing = derivedio.Floats(root, tagPixelSpacing)
	}
	if len(spacing) >= 2 && finitePositive(spacing[0]) && finitePositive(spacing[1]) {
		return spacing[1], spacing[0]
	}
	width := firstFloat(root, tagImagedVolumeWidth)
	height := firstFloat(root, tagImagedVolumeHeight)
	if finitePositive(width) && finitePositive(height) {
		return width / float64(matrixWidth), height / float64(matrixHeight)
	}
	return 0, 0
}

func slideOrigin(root *object.Object) SlidePoint {
	items := derivedio.Sequence(root, tagTotalPixelMatrixOriginSequence)
	if len(items) == 0 {
		return SlidePoint{}
	}
	return SlidePoint{
		X: firstFloat(items[0], tagXOffsetSlide),
		Y: firstFloat(items[0], tagYOffsetSlide),
		Z: firstFloat(items[0], tagZOffsetSlide),
	}
}

func slideOrientation(root *object.Object) (out [6]float64) {
	values := derivedio.Floats(root, tagImageOrientationSlide)
	if len(values) == 6 {
		copy(out[:], values)
	}
	return out
}

func readOpticalPaths(root *object.Object) []OpticalPath {
	items := derivedio.Sequence(root, tagOpticalPathSequence)
	out := make([]OpticalPath, 0, len(items))
	for _, item := range items {
		identifier := derivedio.CleanString(item, tagOpticalPathIdentifier)
		if identifier == "" {
			continue
		}
		out = append(out, OpticalPath{
			Identifier:  identifier,
			Description: derivedio.CleanString(item, tagOpticalPathDescription),
		})
	}
	return out
}

func readFocalPlanes(root *object.Object, originZ float64) []float64 {
	count := derivedio.Int(root, tagTotalPixelMatrixFocalPlanes)
	if count <= 0 {
		count = derivedio.Int(root, tagNumberOfFocalPlanes)
	}
	if count <= 0 {
		count = 1
	}
	distance := firstFloat(root, tagDistanceBetweenFocalPlanes)
	out := make([]float64, count)
	for index := range out {
		out[index] = originZ + float64(index)*distance
	}
	return out
}

func readSpecimen(root *object.Object) Specimen {
	specimen := Specimen{
		Identifier:          derivedio.CleanString(root, tagSpecimenIdentifier),
		ContainerIdentifier: derivedio.CleanString(root, tagContainerIdentifier),
	}
	if items := derivedio.Sequence(root, tagSpecimenDescriptionSequence); len(items) > 0 {
		if specimen.Identifier == "" {
			specimen.Identifier = derivedio.CleanString(items[0], tagSpecimenIdentifier)
		}
	}
	return specimen
}

func firstNestedSequenceItem(root *object.Object, target core.Tag) *object.Object {
	return findNestedSequenceItem(root, target, 0)
}

func findNestedSequenceItem(root *object.Object, target core.Tag, depth int) *object.Object {
	if root == nil || depth > 5 {
		return nil
	}
	if items := derivedio.Sequence(root, target); len(items) > 0 {
		return items[0]
	}
	for _, element := range root.Elements() {
		if element.VR() != core.VRSQ {
			continue
		}
		items, ok := root.GetSequence(element.Tag())
		if !ok {
			continue
		}
		for _, item := range items {
			if found := findNestedSequenceItem(item, target, depth+1); found != nil {
				return found
			}
		}
	}
	return nil
}

func firstFloat(root *object.Object, tag core.Tag) float64 {
	values := derivedio.Floats(root, tag)
	if len(values) == 0 || math.IsNaN(values[0]) || math.IsInf(values[0], 0) {
		return 0
	}
	return values[0]
}

func appendUniqueOpticalPaths(existing []OpticalPath, values ...OpticalPath) []OpticalPath {
	seen := make(map[string]bool, len(existing))
	for _, value := range existing {
		seen[value.Identifier] = true
	}
	for _, value := range values {
		value.Identifier = strings.TrimSpace(value.Identifier)
		if value.Identifier == "" || seen[value.Identifier] {
			continue
		}
		seen[value.Identifier] = true
		existing = append(existing, value)
	}
	return existing
}

func appendUniqueFloats(existing []float64, values ...float64) []float64 {
	for _, value := range values {
		found := false
		for _, current := range existing {
			if math.Abs(current-value) <= 1e-9 {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, value)
		}
	}
	return existing
}

func appendUniqueSpecimens(existing []Specimen, value Specimen) []Specimen {
	for _, current := range existing {
		if current.Identifier == value.Identifier && current.ContainerIdentifier == value.ContainerIdentifier {
			return existing
		}
	}
	return append(existing, value)
}

func nearestFloatIndex(values []float64, target float64) int {
	if len(values) == 0 {
		return defaultMicroscopyFocalPlaneIndex
	}
	best, distance := 0, math.Abs(values[0]-target)
	for index := 1; index < len(values); index++ {
		if current := math.Abs(values[index] - target); current < distance {
			best, distance = index, current
		}
	}
	return best
}

func squaredDistance(bounds image.Rectangle, point image.Point) int64 {
	centerX := int64(bounds.Min.X+bounds.Max.X) / 2
	centerY := int64(bounds.Min.Y+bounds.Max.Y) / 2
	dx := centerX - int64(point.X)
	dy := centerY - int64(point.Y)
	return dx*dx + dy*dy
}

func ceilDiv(value, divisor int) int {
	return (value + divisor - 1) / divisor
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func zeroVector(vector []float64) bool {
	return len(vector) != 3 || (vector[0] == 0 && vector[1] == 0 && vector[2] == 0)
}
