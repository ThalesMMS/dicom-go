package index

import (
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

var tagSpecificCharacterSet = core.NewTag(0x0008, 0x0005)

type selectorMatch struct {
	selector int
	path     validation.Path
}

type collector struct {
	ctx           context.Context
	options       Options
	standard      map[core.Tag]core.Element
	selected      map[string][]SelectedElement
	pending       []selectorMatch
	selectedCount int
	selectErr     error
}

// Read opens source once, extracts a detached record, and closes the opened
// source before returning. No deferred reader or file ownership is transferred.
func Read(ctx context.Context, source Source, options Options) (result Result, err error) {
	if source == nil {
		return Result{}, fmt.Errorf("%w: nil source", ErrInvalidOptions)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	options, err = normalizeOptions(options)
	if err != nil {
		return Result{}, err
	}
	opened, err := openSource(ctx, source)
	if err != nil {
		return Result{}, &Error{Stage: "source", err: err}
	}
	if opened.Reader == nil || opened.Close == nil || opened.Info.Size < 0 {
		invalidErr := fmt.Errorf("%w: source returned an invalid handle", ErrInvalidOptions)
		if opened.Close != nil {
			if closeErr := closeOpenedSource(opened.Close); closeErr != nil {
				return Result{}, errors.Join(invalidErr, &Error{Stage: "close", err: ErrClose})
			}
		}
		return Result{}, invalidErr
	}
	defer func() {
		closeErr := closeOpenedSource(opened.Close)
		if closeErr == nil {
			return
		}
		closeFailure := &Error{Stage: "close", err: ErrClose}
		if err == nil {
			result = Result{}
			err = closeFailure
			return
		}
		err = errors.Join(err, closeFailure)
	}()
	return readOpened(ctx, opened, options)
}

func closeOpenedSource(closeFunc func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRead
		}
	}()
	return closeFunc()
}

func openSource(ctx context.Context, source Source) (opened OpenedSource, err error) {
	defer func() {
		if recover() != nil {
			opened = OpenedSource{}
			err = ErrRead
		}
	}()
	opened, err = source.Open(ctx)
	if err != nil {
		return OpenedSource{}, classifyExternalError(err, ErrRead)
	}
	return opened, nil
}

func ReadPath(ctx context.Context, path string, options Options) (Result, error) {
	return Read(ctx, PathSource(path), options)
}

func ReadAt(ctx context.Context, origin string, reader io.ReaderAt, size int64, options Options) (Result, error) {
	return Read(ctx, ReaderAtSource(origin, reader, size), options)
}

func ReadSeeker(ctx context.Context, origin string, reader io.ReadSeeker, size int64, options Options) (Result, error) {
	return Read(ctx, ReadSeekerSource(origin, reader, size), options)
}

func readOpened(ctx context.Context, opened OpenedSource, options Options) (Result, error) {
	start, err := opened.Reader.Seek(0, io.SeekCurrent)
	if err != nil {
		return Result{}, &Error{Stage: "source", err: err}
	}
	headerSource := &contextReadSeeker{ctx: ctx, source: opened.Reader}
	record := Record{Source: opened.Info, Selected: make(map[string][]SelectedElement)}
	syntax := transfer.Syntax{}
	datasetOffset := start
	raw := options.RawDataSet != nil
	if !raw {
		if options.Limits.MaxTotalBytes > 0 && options.Limits.MaxTotalBytes < 132 {
			return Result{Record: record}, &Error{
				Stage: "preamble", Code: CodeLimit,
				err: errors.Join(ErrResourceLimit, parser.ErrMaxTotalBytesExceeded),
			}
		}
		header, headerErr := object.ReadPart10Header(headerSource, object.ReadFileOptions{
			Dictionary:                         options.Dictionary,
			FileMetaDictionary:                 options.FileMetaDictionary,
			MaxElementBytes:                    options.Limits.MaxSelectedValueBytes,
			MaxTotalBytes:                      options.Limits.MaxTotalBytes,
			MaxSequenceDepth:                   options.Limits.MaxSequenceDepth,
			MaxElements:                        options.Limits.MaxElements,
			StrictReservedBytes:                options.StrictReservedBytes,
			OddLengthPolicy:                    options.OddLengthPolicy,
			AllowMissingMetaElementGroupLength: options.AllowMissingMetaElementGroupLength,
			TextOptions:                        options.TextOptions,
		})
		if headerErr != nil {
			if errors.Is(headerErr, io.EOF) || errors.Is(headerErr, io.ErrUnexpectedEOF) {
				headerErr = errors.Join(object.ErrMissingPreamble, headerErr)
			}
			indexed := &Error{Stage: "file meta", err: headerErr}
			if isParserResourceLimit(headerErr) {
				indexed.Code = CodeLimit
				indexed.err = errors.Join(ErrResourceLimit, headerErr)
			}
			return Result{Record: record}, indexed
		}
		syntax = header.TransferSyntax
		datasetOffset = header.DataSetOffset
		populateFileMeta(&record, header, options)
	} else {
		syntax = options.RawDataSet.TransferSyntax
		if _, err := headerSource.Seek(start, io.SeekStart); err != nil {
			return Result{Record: record}, &Error{Stage: "source", err: err}
		}
		record.FileMeta.TransferSyntaxUID = syntax.UID
		record.FileMeta.TransferSyntaxName = syntax.Name
		appendDiagnostic(&record, options.Limits.MaxDiagnostics, Diagnostic{
			Code: CodeRawDataSet, Severity: SeverityWarning,
			Message: "input was parsed as an explicitly configured raw data set",
		})
	}

	readerSource := io.Reader(opened.Reader)
	var inflater io.ReadCloser
	if syntax.Deflated {
		inflater = flate.NewReader(readerSource)
		defer func() { _ = inflater.Close() }()
		readerSource = inflater
	}
	c := &collector{
		ctx: ctx, options: options, standard: make(map[core.Tag]core.Element),
		selected: record.Selected,
	}
	readerOptions := parser.ReaderOptions{
		Dictionary:          options.Dictionary,
		MaxElementBytes:     options.Limits.MaxSelectedValueBytes,
		MaxTotalBytes:       options.Limits.MaxTotalBytes,
		BaseOffset:          datasetOffset,
		MaxSequenceDepth:    options.Limits.MaxSequenceDepth,
		MaxElements:         options.Limits.MaxElements,
		MaxFragments:        options.Limits.MaxFragments,
		StrictReservedBytes: options.StrictReservedBytes,
		OddLengthPolicy:     options.OddLengthPolicy,
	}
	reader, err := parser.NewSelectiveReader(ctx, readerSource, syntax, readerOptions, parser.SelectiveReaderOptions{Select: c.selectHeader})
	if err != nil {
		return Result{Record: record}, &Error{Stage: "dataset", err: err}
	}
	stopped := false
	var stoppedHeader core.ElementHeader
	stoppedHeaderSet := false
	tokenCount := 0
	for {
		c.pending = nil
		c.selectErr = nil
		token, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			if c.selectErr != nil {
				return Result{Record: record, BytesScanned: reader.Position() - start, RawDataSet: raw},
					&Error{Stage: "selector", Code: CodeLimit, err: c.selectErr}
			}
			return Result{Record: record, BytesScanned: reader.Position() - start, RawDataSet: raw}, indexParserError(nextErr)
		}
		tokenCount++
		if tokenCount > options.Limits.MaxTokens {
			return Result{Record: record, BytesScanned: reader.Position() - start, RawDataSet: raw},
				&Error{Stage: "dataset", Code: CodeLimit, err: ErrResourceLimit}
		}
		if token.Kind == parser.TokenStop {
			stopped = isPixelValueTag(token.Header.Tag)
			stoppedHeader = token.Header
			stoppedHeaderSet = true
			break
		}
		if token.Kind != parser.TokenElement || token.Element.Value == nil {
			continue
		}
		if isStandardTopLevel(c.pending, token.Header.Tag) {
			c.standard[token.Header.Tag] = token.Element
		}
		if err := c.captureSelected(token); err != nil {
			return Result{Record: record, BytesScanned: reader.Position() - start, RawDataSet: raw}, err
		}
	}
	record.Selected = c.selected
	populateRecord(&record, c.standard, syntax, options)
	if err := runSelectorHandlers(ctx, options.Selectors, record.Selected); err != nil {
		return Result{Record: record, BytesScanned: reader.Position() - start, StoppedBeforePixelData: stopped, RawDataSet: raw}, err
	}
	return Result{
		Record: record, BytesScanned: reader.Position() - start,
		StoppedBeforePixelData: stopped, StoppedHeader: stoppedHeader, StoppedHeaderSet: stoppedHeaderSet, RawDataSet: raw,
	}, nil
}

type contextReadSeeker struct {
	ctx    context.Context
	source io.ReadSeeker
}

func (r *contextReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func (r *contextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Seek(offset, whence)
}

func populateFileMeta(record *Record, header object.Part10Header, options Options) {
	record.FileMeta.TransferSyntaxUID = header.TransferSyntax.UID
	record.FileMeta.TransferSyntaxName = header.TransferSyntax.Name
	if header.Meta == nil {
		missing(record, "file_meta.media_storage_sop_class_uid", tags.MediaStorageSOPClassUID, options)
		missing(record, "file_meta.media_storage_sop_instance_uid", tags.MediaStorageSOPInstanceUID, options)
		return
	}
	readUID(record, header.Meta, "file_meta.media_storage_sop_class_uid", tags.MediaStorageSOPClassUID, &record.FileMeta.MediaStorageSOPClassUID, options)
	readUID(record, header.Meta, "file_meta.media_storage_sop_instance_uid", tags.MediaStorageSOPInstanceUID, &record.FileMeta.MediaStorageSOPInstanceUID, options)
	groupLengthTag := core.NewTag(0x0002, 0x0000)
	if _, ok := header.Meta.Get(groupLengthTag); !ok {
		missing(record, "file_meta.group_length", groupLengthTag, options)
	}
}

func (c *collector) selectHeader(_ context.Context, path validation.Path, header core.ElementHeader, _ int64) (parser.SelectiveDisposition, error) {
	if isPixelValueTag(header.Tag) {
		if len(path) == 1 {
			return parser.SelectiveStop, nil
		}
		return parser.SelectiveSkip, nil
	}
	if header.VR == core.VRSQ || header.Length.IsUndefined() {
		return parser.SelectiveMaterialize, nil
	}
	if len(path) == 1 && (header.Tag == tagSpecificCharacterSet || builtinTagSelected(c.options.Profile, header.Tag)) {
		c.pending = append(c.pending, selectorMatch{selector: -1, path: path.Clone()})
	}
	for i, selector := range c.options.Selectors {
		if selectorMatches(selector, path) {
			if selector.Occurrence == OccurrenceFirst && len(c.selected[selector.ID]) != 0 {
				continue
			}
			if int64(header.Length) > selector.MaxValueBytes {
				c.selectErr = ErrResourceLimit
				return 0, ErrResourceLimit
			}
			c.pending = append(c.pending, selectorMatch{selector: i, path: path.Clone()})
		}
	}
	if len(c.pending) == 0 {
		return parser.SelectiveSkip, nil
	}
	return parser.SelectiveMaterialize, nil
}

func isStandardTopLevel(matches []selectorMatch, tag core.Tag) bool {
	for _, match := range matches {
		if match.selector == -1 && len(match.path) == 1 && match.path[0].Tag == tag {
			return true
		}
	}
	return false
}

func selectorMatches(selector Selector, path validation.Path) bool {
	if len(path) != len(selector.Path)+1 || path[len(path)-1].Tag != selector.Tag {
		return false
	}
	for i, tag := range selector.Path {
		if path[i].Tag != tag || path[i].ItemIndex < 0 {
			return false
		}
	}
	return true
}

func (c *collector) captureSelected(token parser.Token) error {
	for _, match := range c.pending {
		if match.selector < 0 {
			continue
		}
		selector := c.options.Selectors[match.selector]
		values := c.selected[selector.ID]
		selected := SelectedElement{Path: match.path.Clone(), Offset: token.Offset, Element: token.Element}
		switch selector.Occurrence {
		case OccurrenceFirst:
			if len(values) != 0 {
				continue
			}
			values = append(values, selected)
		case OccurrenceLast:
			if len(values) == 0 {
				values = append(values, selected)
			} else {
				values[len(values)-1] = selected
			}
		case OccurrenceAll:
			if len(values) >= selector.MaxValues {
				return &Error{Stage: "selector", Code: CodeLimit, err: ErrResourceLimit}
			}
			values = append(values, selected)
		}
		previousCount := len(c.selected[selector.ID])
		if len(values) > previousCount {
			c.selectedCount += len(values) - previousCount
			if c.selectedCount > c.options.Limits.MaxSelectedValues {
				return &Error{Stage: "selector", Code: CodeLimit, err: ErrResourceLimit}
			}
		}
		c.selected[selector.ID] = values
	}
	return nil
}

func runSelectorHandlers(ctx context.Context, selectors []Selector, selected map[string][]SelectedElement) error {
	for _, selector := range selectors {
		if selector.Handle == nil {
			continue
		}
		for _, element := range selected[selector.ID] {
			if err := callSelectorHandler(ctx, selector.Handle, element); err != nil {
				return err
			}
		}
	}
	return nil
}

func callSelectorHandler(ctx context.Context, handler SelectorHandler, element SelectedElement) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if recover() != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			} else {
				err = &Error{Stage: "selector", err: ErrSelector}
			}
		}
	}()
	callbackErr := handler(ctx, element)
	if err := ctx.Err(); err != nil {
		return err
	}
	if callbackErr != nil {
		return &Error{Stage: "selector", err: ErrSelector}
	}
	return nil
}

func indexParserError(err error) error {
	var parseErr *parser.ParseError
	if errors.As(err, &parseErr) {
		indexed := &Error{
			Stage: "dataset", Tag: parseErr.Tag, Offset: parseErr.Offset,
			OffsetSet: true, err: err,
		}
		if isParserResourceLimit(err) {
			indexed.Code = CodeLimit
			indexed.err = errors.Join(ErrResourceLimit, err)
		}
		return indexed
	}
	indexed := &Error{Stage: "dataset", err: err}
	if isParserResourceLimit(err) {
		indexed.Code = CodeLimit
		indexed.err = errors.Join(ErrResourceLimit, err)
	}
	return indexed
}

func isParserResourceLimit(err error) bool {
	return errors.Is(err, parser.ErrMaxTotalBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementsExceeded) ||
		errors.Is(err, parser.ErrMaxDepthExceeded) ||
		errors.Is(err, parser.ErrMaxFragmentsExceeded)
}

func isPixelValueTag(tag core.Tag) bool {
	return tag == tags.PixelData || tag == core.NewTag(0x7FE0, 0x0008) || tag == core.NewTag(0x7FE0, 0x0009)
}

func builtinTagSelected(profile Profile, tag core.Tag) bool {
	if profile&ProfileCore != 0 {
		switch tag {
		case tags.StudyInstanceUID, tags.SeriesInstanceUID, tags.Modality, tags.SeriesNumber,
			tags.SOPClassUID, tags.SOPInstanceUID, tags.InstanceNumber, tags.NumberOfFrames:
			return true
		}
	}
	if profile&ProfilePatient != 0 {
		switch tag {
		case tags.PatientName, tags.PatientID, tags.PatientBirthDate, tags.PatientSex:
			return true
		}
	}
	if profile&ProfileDescriptions != 0 {
		switch tag {
		case tags.AccessionNumber, tags.StudyDate, tags.StudyTime, tags.StudyDescription,
			tags.SeriesDate, tags.SeriesTime, tags.SeriesDescription:
			return true
		}
	}
	if profile&ProfileGeometry != 0 {
		switch tag {
		case tags.Rows, tags.Columns, tags.PixelSpacing, tags.ImagePositionPatient,
			tags.ImageOrientationPatient, tags.SliceThickness:
			return true
		}
	}
	return false
}

func populateRecord(record *Record, elements map[core.Tag]core.Element, syntax transfer.Syntax, options Options) {
	list := make([]core.Element, 0, len(elements))
	for _, element := range elements {
		list = append(list, element)
	}
	dataset := object.FromElementsWithTextOptions(list, options.Dictionary, options.TextOptions)
	dataset.SetValueByteOrder(syntax.ByteOrder)
	if options.Profile&ProfilePatient != 0 {
		record.Patient = &Patient{}
		readString(record, dataset, "patient.name", tags.PatientName, &record.Patient.Name, options)
		readString(record, dataset, "patient.id", tags.PatientID, &record.Patient.ID, options)
		readString(record, dataset, "patient.birth_date", tags.PatientBirthDate, &record.Patient.BirthDate, options)
		readString(record, dataset, "patient.sex", tags.PatientSex, &record.Patient.Sex, options)
	}
	if options.Profile&ProfileCore != 0 {
		readUID(record, dataset, "study.instance_uid", tags.StudyInstanceUID, &record.Study.InstanceUID, options)
		readUID(record, dataset, "series.instance_uid", tags.SeriesInstanceUID, &record.Series.InstanceUID, options)
		readString(record, dataset, "series.modality", tags.Modality, &record.Series.Modality, options)
		readString(record, dataset, "series.number", tags.SeriesNumber, &record.Series.Number, options)
		readUID(record, dataset, "instance.sop_class_uid", tags.SOPClassUID, &record.Instance.SOPClassUID, options)
		readUID(record, dataset, "instance.sop_instance_uid", tags.SOPInstanceUID, &record.Instance.SOPInstanceUID, options)
		readString(record, dataset, "instance.number", tags.InstanceNumber, &record.Instance.Number, options)
		readIS(record, dataset, "instance.number_of_frames", tags.NumberOfFrames, &record.Instance.NumberOfFrames, options)
	}
	if options.Profile&ProfileDescriptions != 0 {
		readString(record, dataset, "study.accession_number", tags.AccessionNumber, &record.Study.AccessionNumber, options)
		readString(record, dataset, "study.date", tags.StudyDate, &record.Study.Date, options)
		readString(record, dataset, "study.time", tags.StudyTime, &record.Study.Time, options)
		readString(record, dataset, "study.description", tags.StudyDescription, &record.Study.Description, options)
		readString(record, dataset, "series.date", tags.SeriesDate, &record.Series.Date, options)
		readString(record, dataset, "series.time", tags.SeriesTime, &record.Series.Time, options)
		readString(record, dataset, "series.description", tags.SeriesDescription, &record.Series.Description, options)
	}
	if options.Profile&ProfileGeometry != 0 {
		record.Geometry = &Geometry{}
		readUS(record, elements, syntax, "geometry.rows", tags.Rows, &record.Geometry.Rows, options)
		readUS(record, elements, syntax, "geometry.columns", tags.Columns, &record.Geometry.Columns, options)
		readDS(record, dataset, "geometry.pixel_spacing", tags.PixelSpacing, &record.Geometry.PixelSpacing, options)
		readDS(record, dataset, "geometry.image_position_patient", tags.ImagePositionPatient, &record.Geometry.ImagePositionPatient, options)
		readDS(record, dataset, "geometry.image_orientation_patient", tags.ImageOrientationPatient, &record.Geometry.ImageOrientationPatient, options)
		var thickness []float64
		readDS(record, dataset, "geometry.slice_thickness", tags.SliceThickness, &thickness, options)
		if len(thickness) > 0 {
			record.Geometry.SliceThickness = thickness[0]
		}
	}
}

func readString(record *Record, dataset *object.Object, field string, tag core.Tag, target *string, options Options) {
	value, ok := dataset.GetString(tag)
	if !ok {
		if _, present := dataset.Get(tag); present {
			invalid(record, field, tag, options)
		} else {
			missing(record, field, tag, options)
		}
		return
	}
	*target = value
}

func readUID(record *Record, dataset *object.Object, field string, tag core.Tag, target *string, options Options) {
	value, ok := dataset.GetUID(tag)
	if !ok {
		if _, present := dataset.Get(tag); present {
			invalid(record, field, tag, options)
		} else {
			missing(record, field, tag, options)
		}
		return
	}
	*target = value
}

func readIS(record *Record, dataset *object.Object, field string, tag core.Tag, target *int64, options Options) {
	if _, ok := dataset.Get(tag); !ok {
		missing(record, field, tag, options)
		return
	}
	value, err := dataset.GetInt(tag)
	if err != nil {
		invalid(record, field, tag, options)
		return
	}
	*target = value
}

func readUS(record *Record, elements map[core.Tag]core.Element, syntax transfer.Syntax, field string, tag core.Tag, target *int64, options Options) {
	element, ok := elements[tag]
	if !ok {
		missing(record, field, tag, options)
		return
	}
	if element.VR() != core.VRUS {
		invalid(record, field, tag, options)
		return
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw) != 2 {
		invalid(record, field, tag, options)
		return
	}
	*target = int64(syntax.ByteOrder.Uint16(raw))
}

func readDS(record *Record, dataset *object.Object, field string, tag core.Tag, target *[]float64, options Options) {
	if _, ok := dataset.Get(tag); !ok {
		missing(record, field, tag, options)
		return
	}
	values, err := dataset.GetFloats(tag)
	if err != nil {
		invalid(record, field, tag, options)
		return
	}
	*target = append([]float64(nil), values...)
}

func missing(record *Record, field string, tag core.Tag, options Options) {
	record.Missing = append(record.Missing, MissingField{Field: field, Tag: tag})
	appendDiagnostic(record, options.Limits.MaxDiagnostics, Diagnostic{
		Code: CodeMissingField, Severity: SeverityInfo, Field: field, Tag: tag,
		Message: "requested metadata field is missing",
	})
}

func invalid(record *Record, field string, tag core.Tag, options Options) {
	appendDiagnostic(record, options.Limits.MaxDiagnostics, Diagnostic{
		Code: CodeInvalidValue, Severity: SeverityWarning, Field: field, Tag: tag,
		Message: "requested metadata field has an invalid encoded value",
	})
}

func appendDiagnostic(record *Record, limit int, diagnostic Diagnostic) {
	if limit > 0 && len(record.Diagnostics) >= limit {
		return
	}
	record.Diagnostics = append(record.Diagnostics, diagnostic)
}
