package spatialreg

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
)

var (
	tagRegistrationSequence       = core.NewTag(0x0070, 0x0308)
	tagMatrixRegistrationSequence = core.NewTag(0x0070, 0x0309)
	tagMatrixSequence             = core.NewTag(0x0070, 0x030A)
	tagReferencedImageSequence    = core.NewTag(0x0008, 0x1140)
	tagReferencedSOPInstanceUID   = core.NewTag(0x0008, 0x1155)
)

// ReadAffine validates a parsed Spatial Registration Storage file.
func ReadAffine(file *object.File) (*AffineObject, error) {
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	return ReadAffineDataset(file.Dataset)
}

// ReadAffineDataset parses Source-to-Registered matrices in DICOM item order.
func ReadAffineDataset(dataset *object.Object) (*AffineObject, error) {
	if dataset == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	sopClass := derivedio.CleanUID(dataset, derivedio.TagSOPClassUID)
	if sopClass != SpatialRegistrationStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClass)
	}
	out := &AffineObject{
		SOPInstanceUID:                derivedio.CleanUID(dataset, derivedio.TagSOPInstanceUID),
		RegisteredFrameOfReferenceUID: derivedio.CleanUID(dataset, derivedio.TagFrameOfReferenceUID),
	}
	if out.SOPInstanceUID == "" || out.RegisteredFrameOfReferenceUID == "" {
		return nil, fmt.Errorf("%w: missing SOP Instance UID or registered Frame of Reference UID", ErrInvalidObject)
	}
	items, ok := dataset.GetSequence(tagRegistrationSequence)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%w: missing Registration Sequence", ErrInvalidObject)
	}
	out.Registrations = make([]AffineRegistration, 0, len(items))
	for index, item := range items {
		registration, err := readAffineRegistration(item)
		if err != nil {
			return nil, fmt.Errorf("%w: registration item %d: %w", ErrInvalidObject, index, err)
		}
		out.Registrations = append(out.Registrations, registration)
	}
	return out, nil
}

func readAffineRegistration(item *object.Object) (AffineRegistration, error) {
	if item == nil {
		return AffineRegistration{}, fmt.Errorf("nil item")
	}
	out := AffineRegistration{SourceFrameOfReferenceUID: derivedio.CleanUID(item, derivedio.TagFrameOfReferenceUID)}
	if refs, ok := item.GetSequence(tagReferencedImageSequence); ok {
		for _, ref := range refs {
			uid := derivedio.CleanUID(ref, tagReferencedSOPInstanceUID)
			if uid == "" {
				return AffineRegistration{}, fmt.Errorf("Referenced Image Sequence contains a missing SOP Instance UID")
			}
			out.ReferencedSOPInstanceUIDs = appendUniqueString(out.ReferencedSOPInstanceUIDs, uid)
		}
	}
	if out.SourceFrameOfReferenceUID == "" && len(out.ReferencedSOPInstanceUIDs) == 0 {
		return AffineRegistration{}, fmt.Errorf("missing source Frame of Reference UID and Referenced Image Sequence")
	}
	matrixRegistrationItems, ok := item.GetSequence(tagMatrixRegistrationSequence)
	if !ok || len(matrixRegistrationItems) != 1 || matrixRegistrationItems[0] == nil {
		return AffineRegistration{}, fmt.Errorf("Matrix Registration Sequence must contain one item")
	}
	matrixItems, ok := matrixRegistrationItems[0].GetSequence(tagMatrixSequence)
	if !ok || len(matrixItems) == 0 {
		return AffineRegistration{}, fmt.Errorf("Matrix Sequence must contain at least one item")
	}
	combined := identityAffine()
	out.Matrices = make([]Matrix, 0, len(matrixItems))
	for index, matrixItem := range matrixItems {
		matrix, err := readAffineMatrix(matrixItem, index)
		if err != nil {
			return AffineRegistration{}, err
		}
		out.Matrices = append(out.Matrices, matrix)
		// DICOM item order applies M1 first, then M2, so column-vector
		// composition is Mn*...*M2*M1.
		combined = multiplyAffine(matrix.Values, combined)
	}
	inverse, condition, err := inverseAffine(combined)
	if err != nil {
		return AffineRegistration{}, &MatrixError{Index: len(matrixItems) - 1, Type: mostGeneralType(out.Matrices), Condition: condition, Err: err}
	}
	out.SourceToRegistered = combined
	out.RegisteredToSource = inverse
	out.Type = mostGeneralType(out.Matrices)
	return out, nil
}

func readAffineMatrix(item *object.Object, index int) (Matrix, error) {
	if item == nil {
		return Matrix{}, &MatrixError{Index: index, Err: ErrInvalidMatrix}
	}
	typeName := TransformType(strings.ToUpper(strings.TrimSpace(derivedio.CleanString(item, tagFrameTransformationMatrixType))))
	if typeName != TransformRigid && typeName != TransformRigidScale && typeName != TransformAffine {
		return Matrix{}, &MatrixError{Index: index, Type: typeName, Err: ErrUnsupportedMatrix}
	}
	values, err := derivedio.LookupFloats(item, tagFrameTransformationMatrix)
	if err != nil || len(values) != 16 {
		return Matrix{}, &MatrixError{Index: index, Type: typeName, Err: ErrInvalidMatrix}
	}
	var affine render.GeometryAffine
	copy(affine[:], values)
	if err := validateMatrixType(affine, typeName); err != nil {
		condition := 0.0
		if _, value, inverseErr := inverseAffine(affine); inverseErr != nil {
			condition = value
		}
		return Matrix{}, &MatrixError{Index: index, Type: typeName, Condition: condition, Err: err}
	}
	return Matrix{Type: string(typeName), Values: affine}, nil
}

func mostGeneralType(matrices []Matrix) TransformType {
	kind := TransformRigid
	for _, matrix := range matrices {
		switch TransformType(matrix.Type) {
		case TransformAffine:
			return TransformAffine
		case TransformRigidScale:
			kind = TransformRigidScale
		}
	}
	return kind
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
