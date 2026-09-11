package spatialreg

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
)

var (
	tagDeformableRegistrationSequence     = core.NewTag(0x0064, 0x0002)
	tagSourceFrameOfReferenceUID          = core.NewTag(0x0064, 0x0003)
	tagDeformableRegistrationGridSequence = core.NewTag(0x0064, 0x0005)
	tagGridDimensions                     = core.NewTag(0x0064, 0x0007)
	tagGridResolution                     = core.NewTag(0x0064, 0x0008)
	tagVectorGridData                     = core.NewTag(0x0064, 0x0009)
	tagPreDeformationMatrixSequence       = core.NewTag(0x0064, 0x000F)
	tagPostDeformationMatrixSequence      = core.NewTag(0x0064, 0x0010)
	tagImagePositionPatient               = core.NewTag(0x0020, 0x0032)
	tagImageOrientationPatient            = core.NewTag(0x0020, 0x0037)
	tagFrameTransformationMatrix          = core.NewTag(0x3006, 0x00C6)
	tagFrameTransformationMatrixType      = core.NewTag(0x0070, 0x030C)
)

// Read validates a parsed Deformable Spatial Registration file with default
// resource limits.
func Read(file *object.File) (*Object, error) {
	return ReadWithOptions(file, Options{})
}

// ReadWithOptions validates a parsed file with explicit resource limits.
func ReadWithOptions(file *object.File, opts Options) (*Object, error) {
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	return ReadDatasetWithOptions(file.Dataset, opts)
}

// ReadDataset validates a dataset with default resource limits.
func ReadDataset(dataset *object.Object) (*Object, error) {
	return ReadDatasetWithOptions(dataset, Options{})
}

// ReadDatasetWithOptions parses the registered-to-source transformations from
// a Deformable Spatial Registration dataset. Callers reading untrusted encoded
// input should also set object.ReadFileOptions.MaxDeformableVectorGridBytes so
// the encoded OF value is rejected before parser allocation.
func ReadDatasetWithOptions(dataset *object.Object, opts Options) (*Object, error) {
	if dataset == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	if opts.MaxRegistrations < 0 {
		return nil, fmt.Errorf("%w: MaxRegistrations must not be negative", ErrInvalidObject)
	}
	opts = opts.normalized()
	sopClass := derivedio.CleanUID(dataset, derivedio.TagSOPClassUID)
	if sopClass != DeformableSpatialRegistrationStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClass)
	}

	out := &Object{
		SOPInstanceUID:                derivedio.CleanUID(dataset, derivedio.TagSOPInstanceUID),
		RegisteredFrameOfReferenceUID: derivedio.CleanUID(dataset, derivedio.TagFrameOfReferenceUID),
	}
	if out.SOPInstanceUID == "" || out.RegisteredFrameOfReferenceUID == "" {
		return nil, fmt.Errorf("%w: missing SOP Instance UID or registered Frame of Reference UID", ErrInvalidObject)
	}

	items, ok := dataset.GetSequence(tagDeformableRegistrationSequence)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%w: missing Deformable Registration Sequence", ErrInvalidObject)
	}
	if len(items) > opts.MaxRegistrations {
		return nil, &LimitError{Limit: LimitRegistrations, Value: uint64(len(items)), Maximum: uint64(opts.MaxRegistrations)}
	}

	out.Registrations = make([]Registration, 0, len(items))
	hasGrid := false
	for index, item := range items {
		registration, err := readRegistration(item, opts)
		if err != nil {
			return nil, fmt.Errorf("%w: registration item %d: %w", ErrInvalidObject, index, err)
		}
		hasGrid = hasGrid || registration.Grid != nil
		out.Registrations = append(out.Registrations, registration)
	}
	if !hasGrid {
		return nil, fmt.Errorf("%w: no registration item contains a deformation grid", ErrInvalidObject)
	}
	return out, nil
}

func readRegistration(item *object.Object, opts Options) (Registration, error) {
	if item == nil {
		return Registration{}, fmt.Errorf("nil item")
	}
	out := Registration{
		SourceFrameOfReferenceUID: derivedio.CleanUID(item, tagSourceFrameOfReferenceUID),
		Pre:                       identityMatrix(),
		Post:                      identityMatrix(),
	}
	if out.SourceFrameOfReferenceUID == "" {
		return Registration{}, fmt.Errorf("missing Source Frame of Reference UID")
	}
	if refs, ok := item.GetSequence(tagReferencedImageSequence); ok {
		for _, ref := range refs {
			uid := derivedio.CleanUID(ref, tagReferencedSOPInstanceUID)
			if uid == "" {
				return Registration{}, fmt.Errorf("Referenced Image Sequence contains a missing SOP Instance UID")
			}
			out.ReferencedSOPInstanceUIDs = appendUniqueString(out.ReferencedSOPInstanceUIDs, uid)
		}
	}
	var err error
	if out.Pre, err = readMatrix(item, tagPreDeformationMatrixSequence); err != nil {
		return Registration{}, fmt.Errorf("pre deformation matrix: %w", err)
	}
	if out.Post, err = readMatrix(item, tagPostDeformationMatrixSequence); err != nil {
		return Registration{}, fmt.Errorf("post deformation matrix: %w", err)
	}

	gridItems, present := item.GetSequence(tagDeformableRegistrationGridSequence)
	if !present {
		return out, nil
	}
	if len(gridItems) == 0 {
		return out, nil
	}
	if len(gridItems) > 1 {
		return Registration{}, fmt.Errorf("Deformable Registration Grid Sequence has %d items, want 1", len(gridItems))
	}
	out.Grid, err = readGrid(gridItems[0], opts)
	if err != nil {
		return Registration{}, err
	}
	return out, nil
}

func readMatrix(parent *object.Object, tag core.Tag) (Matrix, error) {
	items, present := parent.GetSequence(tag)
	if !present {
		return identityMatrix(), nil
	}
	if len(items) != 1 {
		return Matrix{}, fmt.Errorf("sequence has %d items, want 1", len(items))
	}
	item := items[0]
	typeName := strings.ToUpper(strings.TrimSpace(derivedio.CleanString(item, tagFrameTransformationMatrixType)))
	switch typeName {
	case "RIGID", "RIGID_SCALE", "AFFINE":
	default:
		return Matrix{}, fmt.Errorf("unsupported matrix type %q", typeName)
	}
	values, err := derivedio.LookupFloats(item, tagFrameTransformationMatrix)
	if err != nil || len(values) != 16 {
		return Matrix{}, fmt.Errorf("Frame of Reference Transformation Matrix must contain 16 DS values")
	}
	var affine render.GeometryAffine
	copy(affine[:], values)
	if !affine.Finite() {
		return Matrix{}, fmt.Errorf("matrix is non-finite or has an invalid homogeneous row")
	}
	return Matrix{Type: typeName, Values: affine}, nil
}

func readGrid(item *object.Object, opts Options) (*Grid, error) {
	if item == nil {
		return nil, fmt.Errorf("nil deformation grid item")
	}
	dimensionValues, err := derivedio.LookupInts(item, tagGridDimensions)
	if err != nil || len(dimensionValues) != 3 {
		return nil, fmt.Errorf("Grid Dimensions must contain three UL values")
	}
	var dimensions [3]uint32
	for index, value := range dimensionValues {
		if value <= 0 || value > math.MaxUint32 {
			return nil, fmt.Errorf("Grid Dimensions[%d] is invalid: %d", index, value)
		}
		dimensions[index] = uint32(value)
	}
	voxelCount, ok := checkedProduct64(uint64(dimensions[0]), uint64(dimensions[1]), uint64(dimensions[2]))
	if !ok {
		return nil, fmt.Errorf("Grid Dimensions multiplication overflows uint64")
	}
	if voxelCount > opts.MaxGridVoxels {
		return nil, &LimitError{Limit: LimitGridVoxels, Value: voxelCount, Maximum: opts.MaxGridVoxels}
	}
	encodedBytes, ok := checkedProduct64(voxelCount, 3, 4)
	if !ok {
		return nil, fmt.Errorf("Vector Grid Data byte length overflows uint64")
	}
	if encodedBytes > opts.MaxGridBytes {
		return nil, &LimitError{Limit: LimitGridBytes, Value: encodedBytes, Maximum: opts.MaxGridBytes}
	}
	if voxelCount > uint64(maxInt()) {
		return nil, &LimitError{Limit: LimitGridVoxels, Value: voxelCount, Maximum: uint64(maxInt())}
	}

	position, positionErr := derivedio.LookupFloats(item, tagImagePositionPatient)
	orientation, orientationErr := derivedio.LookupFloats(item, tagImageOrientationPatient)
	resolution, resolutionErr := derivedio.LookupFloats(item, tagGridResolution)
	if positionErr != nil || len(position) != 3 || orientationErr != nil || len(orientation) != 6 ||
		resolutionErr != nil || len(resolution) != 3 {
		return nil, fmt.Errorf("incomplete deformation grid geometry")
	}
	if !allFinite(position) || !allFinite(orientation) ||
		!finitePositive(resolution[0]) || !finitePositive(resolution[1]) || !finitePositive(resolution[2]) {
		return nil, fmt.Errorf("non-finite or non-positive deformation grid geometry")
	}
	row := render.Vec3{X: orientation[0], Y: orientation[1], Z: orientation[2]}
	column := render.Vec3{X: orientation[3], Y: orientation[4], Z: orientation[5]}
	if math.Abs(row.Length()-1) > 1e-4 || math.Abs(column.Length()-1) > 1e-4 || math.Abs(row.Dot(column)) > 1e-4 {
		return nil, fmt.Errorf("Image Orientation Patient must contain orthonormal directions")
	}
	row = row.Normalize()
	column = column.Normalize()
	normal := row.Cross(column).Normalize()
	if row == (render.Vec3{}) || column == (render.Vec3{}) || normal == (render.Vec3{}) {
		return nil, fmt.Errorf("Image Orientation Patient is degenerate")
	}

	element, ok := item.Get(tagVectorGridData)
	if !ok || element.VR() != core.VROF {
		return nil, fmt.Errorf("missing Vector Grid Data with VR OF")
	}
	raw, ok := element.RawBytes()
	if !ok {
		return nil, fmt.Errorf("Vector Grid Data is not materialized raw OF data")
	}
	if uint64(len(raw)) != encodedBytes || uint64(element.Length()) != encodedBytes {
		return nil, fmt.Errorf("Vector Grid Data has %d bytes, want %d", len(raw), encodedBytes)
	}

	vectors := make([]vector3f, int(voxelCount))
	if err := decodeVectors(vectors, raw, item.ValueByteOrder()); err != nil {
		return nil, err
	}
	return &Grid{
		Origin:          render.Vec3{X: position[0], Y: position[1], Z: position[2]},
		RowDirection:    row,
		ColumnDirection: column,
		NormalDirection: normal,
		Spacing:         render.Vec3{X: resolution[0], Y: resolution[1], Z: resolution[2]},
		Dimensions:      dimensions,
		vectors:         vectors,
	}, nil
}

func decodeVectors(destination []vector3f, raw []byte, order binary.ByteOrder) error {
	if order == nil {
		order = binary.LittleEndian
	}
	for index := range destination {
		offset := index * 12
		value := vector3f{
			X: math.Float32frombits(order.Uint32(raw[offset:])),
			Y: math.Float32frombits(order.Uint32(raw[offset+4:])),
			Z: math.Float32frombits(order.Uint32(raw[offset+8:])),
		}
		allNaN := undefinedVector(value)
		allFinite := finite(float64(value.X)) && finite(float64(value.Y)) && finite(float64(value.Z))
		if !allNaN && !allFinite {
			return fmt.Errorf("Vector Grid Data vector %d is partially undefined or non-finite", index)
		}
		destination[index] = value
	}
	return nil
}

func checkedProduct64(values ...uint64) (uint64, bool) {
	product := uint64(1)
	for _, value := range values {
		if value == 0 || product > math.MaxUint64/value {
			return 0, false
		}
		product *= value
	}
	return product, true
}

func maxInt() int { return int(^uint(0) >> 1) }

func allFinite(values []float64) bool {
	for _, value := range values {
		if !finite(value) {
			return false
		}
	}
	return true
}

func finitePositive(value float64) bool { return value > 0 && finite(value) }
