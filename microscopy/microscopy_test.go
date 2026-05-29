package microscopy

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestReadModelsSparseMultiframePathsPlanesAndPyramidLevels(t *testing.T) {
	file := microscopyFixture(t, "1.2.3.high", 1024, 1024, 512, 512, []frameFixture{
		{column: 1, row: 1, path: "H&E", z: 0},
		{column: 513, row: 1, path: "H&E", z: 0},
		{column: 1, row: 513, path: "IHC", z: 0.002},
	})
	instance, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(instance.Level.Tiles) != 3 || len(instance.OpticalPaths) != 2 || len(instance.FocalPlanes) != 2 {
		t.Fatalf("instance = %#v", instance)
	}
	if got := instance.Level.Tiles[2]; got.Column != 0 || got.Row != 512 ||
		got.OpticalPath != "IHC" || got.FocalPlane != 1 || got.FrameNumber != 3 {
		t.Fatalf("third tile = %#v", got)
	}
	visible := instance.Level.TilesForViewport(image.Rect(0, 0, 1024, 1024), "H&E", 0)
	if len(visible) != 2 {
		t.Fatalf("visible H&E tiles = %d, want 2", len(visible))
	}
	if distance, err := instance.Level.Distance(image.Pt(0, 0), image.Pt(300, 400)); err != nil || math.Abs(distance-0.5) > 1e-12 {
		t.Fatalf("calibrated distance = %v err=%v, want 0.5 mm", distance, err)
	}

	low, err := Read(microscopyFixture(t, "1.2.3.low", 256, 256, 256, 256, []frameFixture{{column: 1, row: 1, path: "H&E"}}))
	if err != nil {
		t.Fatal(err)
	}
	var pyramid Pyramid
	if err := pyramid.Add(instance); err != nil {
		t.Fatal(err)
	}
	if err := pyramid.Add(low); err != nil {
		t.Fatal(err)
	}
	if len(pyramid.Levels) != 2 || pyramid.Levels[0].MatrixWidth != 1024 {
		t.Fatalf("pyramid levels = %#v", pyramid.Levels)
	}
	if selected, ok := pyramid.LevelForScale(4); !ok || selected.MatrixWidth != 256 {
		t.Fatalf("selected level = %#v ok=%v", selected, ok)
	}
}

func TestViewportUsesBoundedOutputOverviewScaleRotationAndChannelBlend(t *testing.T) {
	level := Level{
		MatrixWidth: 1_000_000, MatrixHeight: 800_000,
		TileWidth: 256, TileHeight: 256,
		PixelSpacingX: 0.0005, PixelSpacingY: 0.0005,
	}
	viewport := Viewport{
		CenterX: 500_000, CenterY: 400_000,
		MatrixPixelsPerView: 2, ScreenWidth: 200, ScreenHeight: 100,
	}
	bounds := viewport.Bounds(level)
	if bounds.Dx() != 400 || bounds.Dy() != 200 {
		t.Fatalf("viewport bounds = %v", bounds)
	}
	scale, err := viewport.ScaleBar(level)
	if err != nil || scale.Pixels < 50 || scale.Pixels > 200 || scale.Label == "" {
		t.Fatalf("scale bar = %#v err=%v", scale, err)
	}
	overview := viewport.OverviewViewport(level, image.Pt(250, 200))
	if overview.Empty() || overview.Dx() > 2 || overview.Dy() > 2 {
		t.Fatalf("overview viewport = %v", overview)
	}

	tile := Tile{Row: bounds.Min.Y, Column: bounds.Min.X, Width: 2, Height: 1}
	gray := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	gray.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	gray.SetNRGBA(1, 0, color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	composed, err := ComposeViewport(
		image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Min.X+2, bounds.Min.Y+1),
		[]TileLayer{
			{Tile: tile, Image: gray, Tint: color.NRGBA{R: 255, A: 255}, Opacity: 1},
			{Tile: tile, Image: gray, Tint: color.NRGBA{G: 255, A: 255}, Opacity: 0.5},
		},
		90,
	)
	if err != nil || composed.Bounds().Dx() != 1 || composed.Bounds().Dy() != 2 {
		t.Fatalf("composed bounds = %v err=%v", composed.Bounds(), err)
	}
	pixel := color.NRGBAModel.Convert(composed.At(0, 0)).(color.NRGBA)
	if pixel.R == 0 || pixel.G == 0 || pixel.B != 0 {
		t.Fatalf("blended channel pixel = %#v", pixel)
	}
}

func TestImplicitHugeSlideGeneratesOnlyViewportTiles(t *testing.T) {
	const (
		matrixWidth  = 1_000_000
		matrixHeight = 800_000
		tileSize     = 256
	)
	frameCount := ceilDiv(matrixWidth, tileSize) * ceilDiv(matrixHeight, tileSize)
	file := &object.File{Dataset: derivedio.Object(
		derivedio.UI(tagSOPClassUID, VLWholeSlideMicroscopyImageStorage),
		derivedio.UI(tagSOPInstanceUID, "1.2.3.huge"),
		derivedio.UL(tagTotalPixelMatrixColumns, matrixWidth),
		derivedio.UL(tagTotalPixelMatrixRows, matrixHeight),
		derivedio.US(tagColumns, tileSize),
		derivedio.US(tagRows, tileSize),
		derivedio.IS(tagNumberOfFrames, frameCount),
		derivedio.FL(tagImagedVolumeWidth, 500),
		derivedio.FL(tagImagedVolumeHeight, 400),
	)}
	instance, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if !instance.Level.ImplicitTiling || len(instance.Level.Tiles) != 0 || instance.Level.FrameCount != frameCount {
		t.Fatalf("huge level materialized tile metadata: %#v", instance.Level)
	}
	visible := instance.Level.TilesForViewport(image.Rect(500_000, 400_000, 500_512, 400_512), "1", 0)
	if len(visible) < 4 || len(visible) > 9 {
		t.Fatalf("visible tile count = %d, want a viewport-bounded set", len(visible))
	}
	for _, tile := range visible {
		if tile.FrameNumber <= 0 || !tile.Bounds().Overlaps(image.Rect(500_000, 400_000, 500_512, 400_512)) {
			t.Fatalf("invalid visible tile %#v", tile)
		}
	}
	bounded := instance.Level.TilesForViewportLimit(instance.Level.MatrixBounds(), "1", 0, 64)
	if len(bounded) != 64 {
		t.Fatalf("bounded overview tile count = %d, want 64", len(bounded))
	}
	center := image.Pt(matrixWidth/2, matrixHeight/2)
	if distance := squaredDistance(bounded[0].Bounds(), center); distance > int64(tileSize*tileSize*2) {
		t.Fatalf("first bounded tile is not center-priority: %#v distance=%d", bounded[0], distance)
	}
}

func TestLoaderCancelsStaleViewportAndBoundsSharedCache(t *testing.T) {
	started := make(chan int, 8)
	source := TileSourceFunc(func(ctx context.Context, tile Tile) (image.Image, error) {
		started <- tile.FrameNumber
		if tile.FrameNumber == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
	})
	loader := NewLoader(source, 300, 2)
	defer loader.Close()
	first := Tile{Source: InstanceRef{SOPInstanceUID: "1"}, FrameNumber: 1, Width: 8, Height: 8}
	second := Tile{Source: InstanceRef{SOPInstanceUID: "1"}, FrameNumber: 2, Width: 8, Height: 8}
	third := Tile{Source: InstanceRef{SOPInstanceUID: "1"}, FrameNumber: 3, Width: 8, Height: 8}

	var mu sync.Mutex
	var published []TileResult
	done, err := loader.Request(context.Background(), []Tile{first}, func(result TileResult) {
		mu.Lock()
		published = append(published, result)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != 1 {
		t.Fatalf("first started frame = %d", got)
	}
	secondDone, err := loader.Request(context.Background(), []Tile{second, third}, func(result TileResult) {
		mu.Lock()
		published = append(published, result)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	waitLoader(t, done)
	waitLoader(t, secondDone)
	mu.Lock()
	defer mu.Unlock()
	if len(published) != 2 {
		t.Fatalf("published results = %#v, want only current viewport", published)
	}
	for _, result := range published {
		if result.Generation != 2 || result.Tile.FrameNumber == 1 {
			t.Fatalf("stale result published: %#v", result)
		}
	}
	stats := loader.CacheStats()
	if stats.Bytes > stats.MaxBytes || stats.Entries != 1 {
		t.Fatalf("bounded cache stats = %#v", stats)
	}
	loader.Close()
	if _, err := loader.Request(context.Background(), []Tile{second}, nil); !errors.Is(err, ErrLoaderClosed) {
		t.Fatalf("request after close = %v", err)
	}
}

func TestReadBulkAnnotationsPreservesGroupsMeasurementsAndSlideCoordinates(t *testing.T) {
	coordinates := []float32{
		0, 0, 10, 0, 10, 10, 0, 10,
		20, 20, 30, 20, 30, 30, 20, 30,
	}
	group := derivedio.DataSet(
		derivedio.UI(tagAnnotationGroupUID, "1.2.3.4"),
		derivedio.LO(tagAnnotationGroupLabel, "Tumor boundaries"),
		derivedio.CS(tagAnnotationGroupGenerationType, "MANUAL"),
		derivedio.UL(tagNumberOfAnnotations, 2),
		derivedio.CS(tagGraphicType, "POLYGON"),
		rawFloat32s(tagPointCoordinatesData, core.VROF, coordinates...),
		rawUint32s(tagLongPrimitivePointIndexList, 1, 5),
		derivedio.CS(tagAppliesAllOpticalPaths, "NO"),
		derivedio.Strings(tagReferencedOpticalPath, core.VRSH, []string{"H&E"}),
		derivedio.CS(tagAppliesAllZPlanes, "YES"),
		codeSequence(tagAnnotationPropertyCategoryCode, "49755003", "SCT", "Morphologically Abnormal Structure"),
		codeSequence(tagAnnotationPropertyTypeCode, "108369006", "SCT", "Neoplasm"),
		derivedio.Seq(tagMeasurementsSequence, derivedio.DataSet(
			codeSequence(tagConceptNameCodeSequence, "42798000", "SCT", "Area"),
			codeSequence(tagMeasurementUnitsCodeSequence, "{pixels}", "UCUM", "pixels"),
			derivedio.Seq(tagMeasurementValuesSequence, derivedio.DataSet(
				rawFloat32s(tagFloatingPointValues, core.VROF, 100, 100),
			)),
		)),
	)
	file := &object.File{Dataset: derivedio.Object(
		derivedio.UI(tagSOPClassUID, MicroscopyBulkSimpleAnnotationsStorage),
		derivedio.CS(tagAnnotationCoordinateType, "2D"),
		derivedio.CS(tagPixelOriginInterpretation, "VOLUME"),
		derivedio.Seq(tagAnnotationGroupSequence, group),
	)}
	groups, err := ReadAnnotations(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Primitives) != 2 || len(groups[0].Measurements) != 1 ||
		groups[0].Type.CodeMeaning != "Neoplasm" || groups[0].OpticalPaths[0] != "H&E" {
		t.Fatalf("annotation groups = %#v", groups)
	}
	level := Level{
		MatrixWidth: 100, MatrixHeight: 100,
		PixelSpacingX: 0.001, PixelSpacingY: 0.002,
		Origin:      SlidePoint{X: 1, Y: 2, Z: 3},
		Orientation: [6]float64{1, 0, 0, 0, 1, 0},
	}
	slide, err := groups[0].SlidePrimitives(level)
	if err != nil {
		t.Fatal(err)
	}
	if point := slide[0].Points[2]; point != (AnnotationPoint{X: 1.01, Y: 2.02, Z: 3}) {
		t.Fatalf("calibrated annotation point = %#v", point)
	}
	x, y, err := level.PixelCoordinate(SlidePoint{X: 1.01, Y: 2.02, Z: 3})
	if err != nil || math.Abs(x-10) > 1e-9 || math.Abs(y-10) > 1e-9 {
		t.Fatalf("inverse calibrated point = (%v,%v) err=%v", x, y, err)
	}

	target := image.NewRGBA(image.Rect(0, 0, 100, 100))
	if err := RenderAnnotations(
		target, image.Rect(0, 0, 100, 100), level, groups,
		AnnotationRenderOptions{OpticalPath: "H&E", Color: color.NRGBA{R: 255, A: 255}},
	); err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(target.At(10, 0)).(color.NRGBA); got.R == 0 {
		t.Fatalf("annotation edge was not rendered: %#v", got)
	}
	filtered := image.NewRGBA(image.Rect(0, 0, 100, 100))
	if err := RenderAnnotations(
		filtered, image.Rect(0, 0, 100, 100), level, groups,
		AnnotationRenderOptions{OpticalPath: "IHC"},
	); err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(filtered.At(10, 0)).(color.NRGBA); got.A != 0 {
		t.Fatalf("annotation leaked into unrelated optical path: %#v", got)
	}
}

func waitLoader(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err, ok := <-done:
		if ok && err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tile loader did not finish")
	}
}

type frameFixture struct {
	column int
	row    int
	path   string
	z      float64
}

func microscopyFixture(t *testing.T, sopUID string, matrixWidth, matrixHeight, tileWidth, tileHeight int, frames []frameFixture) *object.File {
	t.Helper()
	var perFrame []core.DataSet
	for _, frame := range frames {
		perFrame = append(perFrame, derivedio.DataSet(
			derivedio.Seq(tagPlanePositionSlideSequence, derivedio.DataSet(
				derivedio.SL(tagColumnPositionTotalMatrix, int32(frame.column)),
				derivedio.SL(tagRowPositionTotalMatrix, int32(frame.row)),
				derivedio.FD(tagZOffsetSlide, frame.z),
			)),
			derivedio.Seq(tagOpticalPathIdentificationSequence, derivedio.DataSet(
				derivedio.SH(tagOpticalPathIdentifier, frame.path),
			)),
		))
	}
	root := derivedio.Object(
		derivedio.UI(tagSOPClassUID, VLWholeSlideMicroscopyImageStorage),
		derivedio.UI(tagSOPInstanceUID, sopUID),
		derivedio.UI(tagStudyInstanceUID, "1.2.study"),
		derivedio.UI(tagSeriesInstanceUID, "1.2.series"),
		derivedio.UL(tagTotalPixelMatrixColumns, uint32(matrixWidth)),
		derivedio.UL(tagTotalPixelMatrixRows, uint32(matrixHeight)),
		derivedio.US(tagColumns, uint16(tileWidth)),
		derivedio.US(tagRows, uint16(tileHeight)),
		derivedio.IS(tagNumberOfFrames, len(frames)),
		derivedio.FL(tagImagedVolumeWidth, float64(matrixWidth)*0.001),
		derivedio.FL(tagImagedVolumeHeight, float64(matrixHeight)*0.001),
		derivedio.DS(tagImageOrientationSlide, 1, 0, 0, 0, 1, 0),
		derivedio.Seq(tagTotalPixelMatrixOriginSequence, derivedio.DataSet(
			derivedio.DS(tagXOffsetSlide, 1),
			derivedio.DS(tagYOffsetSlide, 2),
			derivedio.DS(tagZOffsetSlide, 0),
		)),
		derivedio.UL(tagTotalPixelMatrixFocalPlanes, 2),
		derivedio.FL(tagDistanceBetweenFocalPlanes, 0.002),
		derivedio.Seq(tagOpticalPathSequence,
			derivedio.DataSet(derivedio.SH(tagOpticalPathIdentifier, "H&E"), derivedio.Str(tagOpticalPathDescription, core.VRST, "Hematoxylin and eosin")),
			derivedio.DataSet(derivedio.SH(tagOpticalPathIdentifier, "IHC")),
		),
		derivedio.Seq(tagSharedFunctionalGroups, derivedio.DataSet(
			derivedio.Seq(tagPixelMeasuresSequence, derivedio.DataSet(
				derivedio.DS(tagPixelSpacing, 0.001, 0.001),
			)),
		)),
		derivedio.Seq(tagPerFrameFunctionalGroups, perFrame...),
	)
	return &object.File{Dataset: root}
}

func rawFloat32s(tag core.Tag, vr core.VR, values ...float32) core.Element {
	raw := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(raw[index*4:], math.Float32bits(value))
	}
	return derivedio.Raw(tag, vr, raw)
}

func rawUint32s(tag core.Tag, values ...uint32) core.Element {
	raw := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(raw[index*4:], value)
	}
	return derivedio.Raw(tag, core.VROL, raw)
}

func codeSequence(tag core.Tag, value, scheme, meaning string) core.Element {
	return derivedio.Seq(tag, derivedio.DataSet(
		derivedio.SH(tagCodeValue, value),
		derivedio.SH(tagCodingSchemeDesignator, scheme),
		derivedio.LO(tagCodeMeaning, meaning),
	))
}
