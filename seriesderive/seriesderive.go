// Package seriesderive creates non-destructive derived copies of DICOM
// instances for series-level workflows.
package seriesderive

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	ErrMissingSeriesInstanceUID = errors.New("dicom/seriesderive: missing derived Series Instance UID")
	ErrMissingSOPInstanceUID    = errors.New("dicom/seriesderive: missing derived SOP Instance UID")
)

var (
	tagImageType             = core.NewTag(0x0008, 0x0008)
	tagDerivationDescription = core.NewTag(0x0008, 0x2111)
	tagSourceImageSequence   = core.NewTag(0x0008, 0x2112)
	tagSeriesNumber          = core.NewTag(0x0020, 0x0011)
)

// Options identifies the derived series and instance. Study Instance UID and
// all pixel data are intentionally retained from the source object.
type Options struct {
	SeriesInstanceUID     string
	SOPInstanceUID        string
	SeriesDescription     string
	DerivationDescription string
	SeriesNumber          string
}

// CloneToSeries returns an independent derived copy of src. It gives the copy
// new Series/SOP Instance UIDs, marks it DERIVED/SECONDARY, and records the
// source SOP reference. src is never modified.
func CloneToSeries(src *object.File, opts Options) (*object.File, error) {
	if src == nil || src.Dataset == nil {
		return nil, object.ErrNilFile
	}
	seriesUID := strings.TrimSpace(opts.SeriesInstanceUID)
	if seriesUID == "" {
		return nil, ErrMissingSeriesInstanceUID
	}
	sopUID := strings.TrimSpace(opts.SOPInstanceUID)
	if sopUID == "" {
		return nil, ErrMissingSOPInstanceUID
	}
	sourceClassUID, ok := src.Dataset.GetString(derivedio.TagSOPClassUID)
	if !ok || strings.TrimSpace(sourceClassUID) == "" {
		return nil, object.ErrMissingSOPClassUID
	}
	sourceSOPUID, ok := src.Dataset.GetString(derivedio.TagSOPInstanceUID)
	if !ok || strings.TrimSpace(sourceSOPUID) == "" {
		return nil, object.ErrMissingSOPInstanceUID
	}

	clone, err := object.CloneFile(src)
	if err != nil {
		return nil, fmt.Errorf("dicom/seriesderive: clone source: %w", err)
	}
	clone.Dataset.Put(derivedio.UI(derivedio.TagSeriesInstanceUID, seriesUID))
	clone.Dataset.Put(derivedio.UI(derivedio.TagSOPInstanceUID, sopUID))
	clone.Dataset.Put(derivedio.Strings(tagImageType, core.VRCS, derivedImageType(clone.Dataset)))
	clone.Dataset.Put(derivedio.Seq(tagSourceImageSequence, derivedio.DataSet(
		derivedio.UI(derivedio.TagRefSOPClassUID, strings.TrimSpace(sourceClassUID)),
		derivedio.UI(derivedio.TagRefSOPInstanceUID, strings.TrimSpace(sourceSOPUID)),
	)))
	if value := strings.TrimSpace(opts.SeriesDescription); value != "" {
		clone.Dataset.Put(derivedio.LO(derivedio.TagSeriesDescr, value))
	}
	if value := strings.TrimSpace(opts.DerivationDescription); value != "" {
		clone.Dataset.Put(derivedio.Str(tagDerivationDescription, core.VRST, value))
	}
	if value := strings.TrimSpace(opts.SeriesNumber); value != "" {
		clone.Dataset.Put(derivedio.Str(tagSeriesNumber, core.VRIS, value))
	}
	if err := clone.RebuildFileMeta(); err != nil {
		_ = clone.Close()
		return nil, fmt.Errorf("dicom/seriesderive: rebuild file meta: %w", err)
	}
	return clone, nil
}

func derivedImageType(dataset *object.Object) []string {
	values, _ := dataset.GetStrings(tagImageType)
	out := []string{"DERIVED", "SECONDARY"}
	if len(values) > 2 {
		out = append(out, values[2:]...)
	}
	return out
}
