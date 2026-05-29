package dimse

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// StoreDescriptor is detached metadata used to plan one C-STORE sub-operation.
// Origin is caller-visible correlation data and can contain sensitive path
// components; StoreError never includes it.
type StoreDescriptor struct {
	SOPClassUID                string
	SOPInstanceUID             string
	TransferSyntaxUID          string
	WritableTransferSyntaxUIDs []string
	Size                       int64
	Origin                     string
	PixelDataHeader            core.ElementHeader
	PixelDataHeaderSet         bool
}

// StoreSource is replayable: Inspect does not retain a handle and Open returns
// one independently closeable payload view for an individual attempt.
type StoreSource interface {
	Inspect(context.Context) (StoreDescriptor, error)
	Open(context.Context) (OpenedStoreSource, error)
}

// OpenedStoreSource is owned by the session until Close returns. WriteDataSet
// writes dataset bytes only (no Part 10 preamble or File Meta Information).
type OpenedStoreSource struct {
	Descriptor   StoreDescriptor
	WriteDataSet CStoreDataSetWriter
	Close        func() error
}

// StorePathSourceOptions configures bounded metadata inspection and Part 10
// header parsing for a path source.
type StorePathSourceOptions struct {
	IndexOptions     index.Options
	ReadOptions      object.ReadFileOptions
	NativePreference transfer.NativeStorePreference
}

type pathStoreSource struct {
	path       string
	root       string
	relative   string
	options    StorePathSourceOptions
	descriptor *StoreDescriptor
}

// NewPathStoreSource creates a replayable Part 10 source. Path payloads are
// streamed verbatim in their declared transfer syntax, so no transcode is
// implied and only the original syntax is proposed.
func NewPathStoreSource(path string, options ...StorePathSourceOptions) StoreSource {
	var opts StorePathSourceOptions
	if len(options) > 0 {
		opts = options[0]
	}
	opts.IndexOptions = cloneStoreIndexOptions(opts.IndexOptions)
	return &pathStoreSource{path: path, options: opts}
}

// NewRootedPathStoreSource creates a path source whose relative components are
// opened beneath root without following symlinks. It is intended for bounded
// directory scanners that must keep their no-follow guarantee through send.
func NewRootedPathStoreSource(root, relative string, options ...StorePathSourceOptions) StoreSource {
	var opts StorePathSourceOptions
	if len(options) > 0 {
		opts = options[0]
	}
	opts.IndexOptions = cloneStoreIndexOptions(opts.IndexOptions)
	root = filepath.Clean(root)
	relative = filepath.Clean(relative)
	return &pathStoreSource{
		path: filepath.Join(root, relative), root: root, relative: relative, options: opts,
	}
}

// NewDescribedPathStoreSource uses caller-supplied detached metadata during
// planning while still reopening and revalidating the same path before send.
// It is intended for archives that already persist trustworthy indexing data.
func NewDescribedPathStoreSource(path string, descriptor StoreDescriptor, options ...StorePathSourceOptions) StoreSource {
	var opts StorePathSourceOptions
	if len(options) > 0 {
		opts = options[0]
	}
	opts.IndexOptions = cloneStoreIndexOptions(opts.IndexOptions)
	copy := cloneStoreDescriptor(descriptor)
	copy.WritableTransferSyntaxUIDs = transfer.ProposedStoreTransferSyntaxUIDs(copy.TransferSyntaxUID, opts.NativePreference)
	return &pathStoreSource{path: path, options: opts, descriptor: &copy}
}

func (s *pathStoreSource) Inspect(ctx context.Context) (StoreDescriptor, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return StoreDescriptor{}, ErrStoreInvalidSource
	}
	if s.descriptor != nil {
		return normalizeStoreDescriptor(cloneStoreDescriptor(*s.descriptor))
	}
	file, info, err := s.openRegularPath()
	if err != nil {
		return StoreDescriptor{}, classifyStoreSourceError(ctx, err)
	}
	result, readErr := index.ReadSeeker(ctx, s.path, file, info.Size(), s.options.IndexOptions)
	closeErr := file.Close()
	if readErr != nil {
		return StoreDescriptor{}, classifyStoreSourceError(ctx, readErr)
	}
	if closeErr != nil {
		return StoreDescriptor{}, ErrStoreInvalidSource
	}
	return descriptorFromIndex(result, s.path, s.options.NativePreference)
}

func (s *pathStoreSource) Open(ctx context.Context) (OpenedStoreSource, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return OpenedStoreSource{}, ErrStoreInvalidSource
	}
	if err := contextError(ctx); err != nil {
		return OpenedStoreSource{}, err
	}
	file, info, err := s.openRegularPath()
	if err != nil {
		return OpenedStoreSource{}, classifyStoreSourceError(ctx, err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	indexed, err := index.ReadSeeker(ctx, s.path, file, info.Size(), s.options.IndexOptions)
	if err != nil {
		return OpenedStoreSource{}, classifyStoreSourceError(ctx, err)
	}
	descriptor, err := descriptorFromIndex(indexed, s.path, s.options.NativePreference)
	if err != nil {
		return OpenedStoreSource{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return OpenedStoreSource{}, ErrStoreInvalidSource
	}
	header, err := object.ReadPart10Header(file, s.options.ReadOptions)
	if err != nil {
		return OpenedStoreSource{}, classifyStoreSourceError(ctx, err)
	}
	datasetOffset := header.DataSetOffset
	closeOnError = false
	return OpenedStoreSource{
		Descriptor: descriptor,
		WriteDataSet: func(writeCtx context.Context, destination io.Writer, syntax transfer.Syntax) error {
			requestedUID := transfer.NormalizeUID(syntax.UID)
			if !storeSyntaxUIDPresent(descriptor.WritableTransferSyntaxUIDs, requestedUID) {
				return ErrStoreTransferSyntax
			}
			if requestedUID != descriptor.TransferSyntaxUID {
				if _, err := file.Seek(0, io.SeekStart); err != nil {
					return ErrStoreInvalidSource
				}
				readOptions := storeReencodeReadOptions(info.Size(), s.options)
				parsed, err := object.ReadFileWithOptions(file, readOptions)
				if err != nil {
					return classifyStoreSourceError(writeCtx, err)
				}
				if err := contextError(writeCtx); err != nil {
					return err
				}
				return object.WriteDataSet(destination, parsed.Dataset, syntax)
			}
			if _, err := file.Seek(datasetOffset, io.SeekStart); err != nil {
				return ErrStoreInvalidSource
			}
			_, err := copyStoreDataSet(writeCtx, destination, file)
			return err
		},
		Close: file.Close,
	}, nil
}

func (s *pathStoreSource) openRegularPath() (*os.File, os.FileInfo, error) {
	if s.root == "" {
		return openRegularStorePath(s.path)
	}
	if filepath.IsAbs(s.relative) || s.relative == "." || s.relative == ".." || strings.HasPrefix(s.relative, ".."+string(filepath.Separator)) {
		return nil, nil, ErrStoreInvalidSource
	}
	file, err := openStoreRootedPathNoFollow(s.root, s.relative)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrStoreInvalidSource
	}
	return file, info, nil
}

func storeReencodeReadOptions(size int64, options StorePathSourceOptions) object.ReadFileOptions {
	readOptions := options.ReadOptions
	if readOptions.Dictionary == nil {
		readOptions.Dictionary = options.IndexOptions.Dictionary
	}
	if readOptions.FileMetaDictionary == nil {
		readOptions.FileMetaDictionary = options.IndexOptions.FileMetaDictionary
	}
	if readOptions.MaxTotalBytes == 0 {
		if size < math.MaxInt64 {
			readOptions.MaxTotalBytes = size + 1
		}
	}
	if readOptions.MaxElementBytes == 0 {
		readOptions.MaxElementBytes = 64 << 20
	}
	if readOptions.MaxSequenceDepth == 0 {
		readOptions.MaxSequenceDepth = index.DefaultOptions().Limits.MaxSequenceDepth
	}
	if readOptions.MaxElements == 0 {
		readOptions.MaxElements = index.DefaultOptions().Limits.MaxElements
	}
	if readOptions.MaxFragments == 0 {
		readOptions.MaxFragments = index.DefaultOptions().Limits.MaxFragments
	}
	if readOptions.InlineValueBytesThreshold == 0 {
		readOptions.InlineValueBytesThreshold = 1 << 20
	}
	readOptions.DeferPixelData = true
	return readOptions
}

func storeSyntaxUIDPresent(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func openRegularStorePath(path string) (*os.File, os.FileInfo, error) {
	file, err := openStorePathNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrStoreInvalidSource
	}
	return file, info, nil
}

type objectStoreSource struct {
	descriptor StoreDescriptor
	file       *object.File
	dataset    *object.Object
	opener     func(context.Context) (*object.File, error)
	owned      bool
}

// NewFileStoreSource creates a borrowed object.File source. The caller must
// keep the file and any deferred provider open and immutable until the batch
// returns; the session never closes it.
func NewFileStoreSource(file *object.File, size int64) StoreSource {
	descriptor, _ := descriptorFromFile(file, size, "", true)
	return &objectStoreSource{descriptor: descriptor, file: file}
}

// NewDataSetStoreSource creates a borrowed in-memory/deferred dataset source.
// syntax declares the dataset's current representation and size is a planning
// hint; both are revalidated before transmission where possible.
func NewDataSetStoreSource(dataset *object.Object, syntax transfer.Syntax, size int64) StoreSource {
	file := &object.File{Dataset: dataset, TransferSyntax: syntax}
	descriptor, _ := descriptorFromFile(file, size, "", true)
	return &objectStoreSource{descriptor: descriptor, dataset: dataset, file: file}
}

// NewLazyStoreSource creates an owned replayable source from detached metadata
// and a file opener. Each successful Open result is closed by the session.
func NewLazyStoreSource(descriptor StoreDescriptor, opener func(context.Context) (*object.File, error)) StoreSource {
	copy := cloneStoreDescriptor(descriptor)
	copy.WritableTransferSyntaxUIDs = transfer.ProposedStoreTransferSyntaxUIDs(copy.TransferSyntaxUID, transfer.NativeStoreSourceFirst)
	return &objectStoreSource{descriptor: copy, opener: opener, owned: true}
}

func (s *objectStoreSource) Inspect(ctx context.Context) (StoreDescriptor, error) {
	if err := contextError(ctx); err != nil {
		return StoreDescriptor{}, err
	}
	if s == nil {
		return StoreDescriptor{}, ErrStoreInvalidSource
	}
	if s.file != nil {
		return descriptorFromFile(s.file, s.descriptor.Size, s.descriptor.Origin, true)
	}
	return normalizeStoreDescriptor(s.descriptor)
}

func (s *objectStoreSource) Open(ctx context.Context) (OpenedStoreSource, error) {
	if err := contextError(ctx); err != nil {
		return OpenedStoreSource{}, err
	}
	if s == nil {
		return OpenedStoreSource{}, ErrStoreInvalidSource
	}
	file := s.file
	closeFunc := func() error { return nil }
	if s.opener != nil {
		var err error
		file, err = s.opener(ctx)
		if err != nil {
			if file != nil {
				_ = file.Close()
			}
			return OpenedStoreSource{}, classifyStoreSourceError(ctx, err)
		}
		if file == nil {
			return OpenedStoreSource{}, ErrStoreInvalidSource
		}
		closeFunc = file.Close
		if err := contextError(ctx); err != nil {
			_ = closeFunc()
			return OpenedStoreSource{}, err
		}
	}
	descriptor, err := descriptorFromFile(file, s.descriptor.Size, s.descriptor.Origin, true)
	if err != nil {
		if s.owned {
			_ = closeFunc()
		}
		return OpenedStoreSource{}, err
	}
	return OpenedStoreSource{
		Descriptor: descriptor,
		WriteDataSet: func(_ context.Context, destination io.Writer, syntax transfer.Syntax) error {
			return object.WriteDataSet(destination, file.Dataset, syntax)
		},
		Close: closeFunc,
	}, nil
}

func descriptorFromIndex(result index.Result, origin string, preference transfer.NativeStorePreference) (StoreDescriptor, error) {
	record := result.Record
	sopClassUID := strings.TrimSpace(record.Instance.SOPClassUID)
	sopInstanceUID := strings.TrimSpace(record.Instance.SOPInstanceUID)
	transferSyntaxUID := transfer.NormalizeUID(record.FileMeta.TransferSyntaxUID)
	if media := strings.TrimSpace(record.FileMeta.MediaStorageSOPClassUID); media != "" && media != sopClassUID {
		return StoreDescriptor{}, ErrStoreInvalidSource
	}
	if media := strings.TrimSpace(record.FileMeta.MediaStorageSOPInstanceUID); media != "" && media != sopInstanceUID {
		return StoreDescriptor{}, ErrStoreInvalidSource
	}
	descriptor, err := normalizeStoreDescriptor(StoreDescriptor{
		SOPClassUID:                sopClassUID,
		SOPInstanceUID:             sopInstanceUID,
		TransferSyntaxUID:          transferSyntaxUID,
		WritableTransferSyntaxUIDs: transfer.ProposedStoreTransferSyntaxUIDs(transferSyntaxUID, preference),
		Size:                       record.Source.Size,
		Origin:                     origin,
		PixelDataHeader:            result.StoppedHeader,
		PixelDataHeaderSet:         result.StoppedHeaderSet,
	})
	if err != nil {
		return StoreDescriptor{}, err
	}
	if err := validateStorePixelHeader(descriptor); err != nil {
		return StoreDescriptor{}, err
	}
	return descriptor, nil
}

func descriptorFromFile(file *object.File, size int64, origin string, nativeFallbacks bool) (StoreDescriptor, error) {
	if file == nil || file.Dataset == nil {
		return StoreDescriptor{}, ErrStoreInvalidSource
	}
	sopClassUID, ok := file.Dataset.GetUID(tags.SOPClassUID)
	if !ok || strings.TrimSpace(sopClassUID) == "" {
		return StoreDescriptor{}, object.ErrMissingSOPClassUID
	}
	sopInstanceUID, ok := file.Dataset.GetUID(tags.SOPInstanceUID)
	if !ok || strings.TrimSpace(sopInstanceUID) == "" {
		return StoreDescriptor{}, object.ErrMissingSOPInstanceUID
	}
	transferSyntaxUID := transfer.NormalizeUID(file.TransferSyntax.UID)
	if transferSyntaxUID == "" && file.Meta != nil {
		if value, ok := file.Meta.GetUID(tags.TransferSyntaxUID); ok {
			transferSyntaxUID = transfer.NormalizeUID(value)
		}
	}
	if transferSyntaxUID == "" {
		return StoreDescriptor{}, object.ErrMissingTransferSyntax
	}
	if file.Meta != nil {
		if value, ok := file.Meta.GetUID(tags.MediaStorageSOPClassUID); ok && strings.TrimSpace(value) != "" && strings.TrimSpace(value) != strings.TrimSpace(sopClassUID) {
			return StoreDescriptor{}, ErrStoreInvalidSource
		}
		if value, ok := file.Meta.GetUID(tags.MediaStorageSOPInstanceUID); ok && strings.TrimSpace(value) != "" && strings.TrimSpace(value) != strings.TrimSpace(sopInstanceUID) {
			return StoreDescriptor{}, ErrStoreInvalidSource
		}
		if value, ok := file.Meta.GetUID(tags.TransferSyntaxUID); ok && transfer.NormalizeUID(value) != "" && transfer.NormalizeUID(value) != transferSyntaxUID {
			return StoreDescriptor{}, ErrStoreInvalidSource
		}
	}
	writable := []string{transferSyntaxUID}
	if nativeFallbacks {
		writable = transfer.ProposedStoreTransferSyntaxUIDs(transferSyntaxUID, transfer.NativeStoreSourceFirst)
	}
	pixelHeader, pixelHeaderSet, err := storePixelDataHeader(file.Dataset)
	if err != nil {
		return StoreDescriptor{}, err
	}
	return normalizeStoreDescriptor(StoreDescriptor{
		SOPClassUID:                sopClassUID,
		SOPInstanceUID:             sopInstanceUID,
		TransferSyntaxUID:          transferSyntaxUID,
		WritableTransferSyntaxUIDs: writable,
		Size:                       size,
		Origin:                     origin,
		PixelDataHeader:            pixelHeader,
		PixelDataHeaderSet:         pixelHeaderSet,
	})
}

func storePixelDataHeader(dataset *object.Object) (core.ElementHeader, bool, error) {
	if dataset == nil {
		return core.ElementHeader{}, false, ErrStoreInvalidSource
	}
	pixelTags := [...]core.Tag{
		core.TagPixelData,
		core.NewTag(0x7FE0, 0x0008),
		core.NewTag(0x7FE0, 0x0009),
	}
	var header core.ElementHeader
	found := false
	for _, tag := range pixelTags {
		element, ok := dataset.Get(tag)
		if !ok {
			continue
		}
		if found {
			return core.ElementHeader{}, false, ErrStoreInvalidSource
		}
		header = element.Header
		if !header.LengthSet {
			header.Length = element.Length()
			header.LengthSet = true
		}
		found = true
	}
	return header, found, nil
}

func normalizeStoreDescriptor(descriptor StoreDescriptor) (StoreDescriptor, error) {
	descriptor.SOPClassUID = strings.TrimSpace(descriptor.SOPClassUID)
	descriptor.SOPInstanceUID = strings.TrimSpace(descriptor.SOPInstanceUID)
	descriptor.TransferSyntaxUID = transfer.NormalizeUID(descriptor.TransferSyntaxUID)
	if !validStoreUID(descriptor.SOPClassUID) || !validStoreUID(descriptor.SOPInstanceUID) || !validStoreUID(descriptor.TransferSyntaxUID) || descriptor.Size < 0 {
		return StoreDescriptor{}, ErrStoreInvalidSource
	}
	if _, ok := transfer.DefaultRegistry.Get(descriptor.TransferSyntaxUID); !ok {
		return StoreDescriptor{}, ErrStoreTransferSyntax
	}
	seen := make(map[string]bool, len(descriptor.WritableTransferSyntaxUIDs)+1)
	writable := make([]string, 0, len(descriptor.WritableTransferSyntaxUIDs)+1)
	for _, value := range descriptor.WritableTransferSyntaxUIDs {
		uid := transfer.NormalizeUID(value)
		if uid == "" || seen[uid] {
			continue
		}
		if _, ok := transfer.DefaultRegistry.Get(uid); !ok {
			return StoreDescriptor{}, ErrStoreTransferSyntax
		}
		seen[uid] = true
		writable = append(writable, uid)
	}
	if len(writable) == 0 {
		writable = append(writable, descriptor.TransferSyntaxUID)
	}
	descriptor.WritableTransferSyntaxUIDs = writable
	if err := validateStorePixelHeader(descriptor); err != nil {
		return StoreDescriptor{}, err
	}
	return descriptor, nil
}

func validStoreUID(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '.' || value[len(value)-1] == '.' {
		return false
	}
	components := strings.Split(value, ".")
	if len(components) < 2 || components[0] != "0" && components[0] != "1" && components[0] != "2" {
		return false
	}
	for _, component := range components {
		if component == "" || len(component) > 1 && component[0] == '0' {
			return false
		}
		for i := range component {
			if component[i] < '0' || component[i] > '9' {
				return false
			}
		}
	}
	if components[0] != "2" && (len(components[1]) > 2 || len(components[1]) == 2 && components[1] > "39") {
		return false
	}
	return true
}

func cloneStoreDescriptor(descriptor StoreDescriptor) StoreDescriptor {
	descriptor.WritableTransferSyntaxUIDs = append([]string(nil), descriptor.WritableTransferSyntaxUIDs...)
	return descriptor
}

func validateStorePixelHeader(descriptor StoreDescriptor) error {
	if !descriptor.PixelDataHeaderSet {
		return nil
	}
	header := descriptor.PixelDataHeader
	syntax, ok := transfer.DefaultRegistry.Get(descriptor.TransferSyntaxUID)
	if !ok {
		return ErrStoreTransferSyntax
	}
	integerPixel := header.Tag == core.TagPixelData
	floatPixel := header.Tag == core.NewTag(0x7FE0, 0x0008) || header.Tag == core.NewTag(0x7FE0, 0x0009)
	if !integerPixel && !floatPixel {
		return ErrStoreInvalidSource
	}
	if floatPixel {
		if header.Length.IsUndefined() || syntax.Encapsulated || syntax.MediaPayload {
			return ErrStoreInvalidSource
		}
		return nil
	}
	if syntax.Encapsulated || syntax.MediaPayload {
		if !header.Length.IsUndefined() || (header.VR != core.VROB && header.VR != core.VROW) {
			return ErrStoreInvalidSource
		}
		return nil
	}
	if header.Length.IsUndefined() {
		return ErrStoreInvalidSource
	}
	return nil
}

func cloneStoreIndexOptions(options index.Options) index.Options {
	options.Selectors = append([]index.Selector(nil), options.Selectors...)
	for i := range options.Selectors {
		options.Selectors[i].Path = append([]core.Tag(nil), options.Selectors[i].Path...)
	}
	if options.RawDataSet != nil {
		raw := *options.RawDataSet
		options.RawDataSet = &raw
	}
	return options
}

func copyStoreDataSet(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	buffer := make([]byte, 64<<10)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			wn, writeErr := destination.Write(buffer[:n])
			written += int64(wn)
			if writeErr != nil {
				return written, writeErr
			}
			if wn != n {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

func classifyStoreSourceError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrStoreInvalidSource
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
