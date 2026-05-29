package index

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	// MaxScanWorkers caps concurrent metadata reads even when callers request more.
	MaxScanWorkers = 64
	// MaxScanQueueDepth prevents an accidental unbounded channel allocation.
	MaxScanQueueDepth = 4096

	defaultScanQueueDepth     = 32
	defaultScanMaxFiles       = 100_000
	defaultScanMaxDirectories = 50_000
	defaultScanMaxDepth       = 64
	defaultScanMaxPathBytes   = 4_096
	defaultScanMaxErrors      = 1_000
	directoryReadBatch        = 128
)

// ErrScan classifies scanner failures without exposing filesystem paths or
// callback values.
var ErrScan = errors.New("dicom index scan failed")

// SymlinkPolicy controls how Scan handles symbolic links.
type SymlinkPolicy uint8

const (
	// SymlinkIgnore skips every symbolic link.
	SymlinkIgnore SymlinkPolicy = iota
	// SymlinkReject reports a bounded, value-free error for each symbolic link.
	SymlinkReject
	// SymlinkFollowFiles follows links to regular files that remain within root.
	// Directory links are never followed.
	SymlinkFollowFiles
)

// ScanErrorCode identifies a scanner failure without embedding paths, DICOM
// values, callback errors, or panic values.
type ScanErrorCode string

const (
	ScanErrorInvalidOptions ScanErrorCode = "invalid_options"
	ScanErrorLimit          ScanErrorCode = "resource_limit"
	ScanErrorFilesystem     ScanErrorCode = "filesystem"
	ScanErrorRead           ScanErrorCode = "read"
	ScanErrorSymlink        ScanErrorCode = "symlink"
	ScanErrorFilter         ScanErrorCode = "filter_callback"
	ScanErrorYield          ScanErrorCode = "yield_callback"
)

// ScanError is safe to persist. Limit contains only a fixed option name.
type ScanError struct {
	Code  ScanErrorCode
	Limit string
	cause error
}

func (e *ScanError) Error() string {
	if e == nil {
		return "dicom index scan failed"
	}
	switch e.Code {
	case ScanErrorInvalidOptions:
		return "dicom index scan options are invalid"
	case ScanErrorLimit:
		return "dicom index scan resource limit exceeded"
	case ScanErrorFilesystem:
		return "dicom index scan filesystem operation failed"
	case ScanErrorRead:
		return "dicom index scan metadata read failed"
	case ScanErrorSymlink:
		return "dicom index scan rejected symbolic link"
	case ScanErrorFilter:
		return "dicom index scan filter failed"
	case ScanErrorYield:
		return "dicom index scan result callback failed"
	default:
		return "dicom index scan failed"
	}
}

func (e *ScanError) Unwrap() error {
	if e == nil || e.cause == nil {
		return ErrScan
	}
	return errors.Join(ErrScan, redactedErrorCause{cause: e.cause})
}

// ScanFilter selects regular-file candidates. It is never called for
// directories or other special files. MaxFiles counts candidates before this
// callback so a rejecting filter cannot make traversal unbounded.
type ScanFilter func(path string, info fs.FileInfo) bool

// ScanOptions bounds directory traversal and metadata extraction. Zero numeric
// fields receive the values from DefaultScanOptions; negative values are
// invalid.
type ScanOptions struct {
	ReadOptions    Options
	Workers        int
	QueueDepth     int
	MaxFiles       int
	MaxDirectories int
	MaxDepth       int
	MaxPathBytes   int
	MaxErrors      int
	Filter         ScanFilter
	SymlinkPolicy  SymlinkPolicy
}

// ScanResult keeps the possibly sensitive source path separate from the
// detached metadata result and its value-free error.
type ScanResult struct {
	Path   string
	Result Result
	Err    error
}

// DefaultScanOptions returns finite limits suitable for scanning untrusted
// directory trees.
func DefaultScanOptions() ScanOptions {
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > MaxScanWorkers {
		workers = MaxScanWorkers
	}
	return ScanOptions{
		ReadOptions:    DefaultOptions(),
		Workers:        workers,
		QueueDepth:     defaultScanQueueDepth,
		MaxFiles:       defaultScanMaxFiles,
		MaxDirectories: defaultScanMaxDirectories,
		MaxDepth:       defaultScanMaxDepth,
		MaxPathBytes:   defaultScanMaxPathBytes,
		MaxErrors:      defaultScanMaxErrors,
		SymlinkPolicy:  SymlinkIgnore,
	}
}

// Scan walks root without following directory links and synchronously invokes
// yield for each selected file. The bounded result queue applies backpressure
// to readers when yield is slow. Callbacks must return; a callback that waits
// should observe ctx because Go cannot preempt an arbitrary function call.
func Scan(ctx context.Context, root string, options ScanOptions, yield func(ScanResult) error) error {
	opts, err := normalizeScanOptions(options)
	if err != nil {
		return err
	}
	if strings.TrimSpace(root) == "" || yield == nil {
		return &ScanError{Code: ScanErrorInvalidOptions}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan scanTask, opts.QueueDepth)
	results := make(chan ScanResult, opts.QueueDepth)
	producerDone := make(chan error, 1)

	go func() {
		producerErr := produceScanJobs(scanCtx, root, opts, jobs, results)
		if producerErr != nil {
			cancel()
		}
		close(jobs)
		producerDone <- producerErr
	}()

	var workers sync.WaitGroup
	workers.Add(opts.Workers)
	for range opts.Workers {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-scanCtx.Done():
					return
				case task, ok := <-jobs:
					if !ok {
						return
					}
					result, readErr := readScanTask(scanCtx, task, opts.ReadOptions)
					if readErr != nil {
						var scanErr *ScanError
						if !errors.As(readErr, &scanErr) {
							code := ScanErrorRead
							limit := ""
							if errors.Is(readErr, ErrResourceLimit) {
								code = ScanErrorLimit
								limit = "ReadOptions.Limits"
							}
							readErr = &ScanError{Code: code, Limit: limit, cause: readErr}
						}
					}
					// Path is deliberately carried only by ScanResult. This also
					// avoids retaining a resolved symlink target in the record.
					result.Record.Source.Origin = ""
					if !sendScanResult(scanCtx, results, ScanResult{Path: task.path, Result: result, Err: readErr}) {
						return
					}
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(results)
	}()

	var terminalErr error
	errorCount := 0
	for result := range results {
		if terminalErr != nil {
			continue
		}
		if result.Err != nil {
			errorCount++
			if errorCount > opts.MaxErrors {
				terminalErr = &ScanError{Code: ScanErrorLimit, Limit: "MaxErrors"}
				cancel()
				continue
			}
		}
		if callbackErr := callScanYield(yield, result); callbackErr != nil {
			terminalErr = callbackErr
			cancel()
		}
	}
	producerErr := <-producerDone
	if err := ctx.Err(); err != nil {
		return err
	}
	if terminalErr != nil {
		return terminalErr
	}
	if producerErr != nil {
		return producerErr
	}
	return nil
}

type scanTask struct {
	path     string
	readPath string
	info     fs.FileInfo
}

type scanDirectory struct {
	path  string
	depth int
	info  fs.FileInfo
}

func normalizeScanOptions(options ScanOptions) (ScanOptions, error) {
	defaults := DefaultScanOptions()
	if options.Workers < 0 || options.QueueDepth < 0 || options.MaxFiles < 0 ||
		options.MaxDirectories < 0 || options.MaxDepth < 0 || options.MaxPathBytes < 0 ||
		options.MaxErrors < 0 || options.Workers > MaxScanWorkers ||
		options.QueueDepth > MaxScanQueueDepth || options.SymlinkPolicy > SymlinkFollowFiles {
		return ScanOptions{}, &ScanError{Code: ScanErrorInvalidOptions}
	}
	if options.Workers == 0 {
		options.Workers = defaults.Workers
	}
	if options.QueueDepth == 0 {
		options.QueueDepth = defaults.QueueDepth
	}
	if options.MaxFiles == 0 {
		options.MaxFiles = defaults.MaxFiles
	}
	if options.MaxDirectories == 0 {
		options.MaxDirectories = defaults.MaxDirectories
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = defaults.MaxDepth
	}
	if options.MaxPathBytes == 0 {
		options.MaxPathBytes = defaults.MaxPathBytes
	}
	if options.MaxErrors == 0 {
		options.MaxErrors = defaults.MaxErrors
	}
	return options, nil
}

func produceScanJobs(ctx context.Context, root string, options ScanOptions, jobs chan<- scanTask, results chan<- ScanResult) error {
	if exceedsPathLimit(root, options.MaxPathBytes) {
		return &ScanError{Code: ScanErrorLimit, Limit: "MaxPathBytes"}
	}
	info, err := os.Lstat(root)
	if err != nil {
		sendScanResult(ctx, results, ScanResult{Path: root, Err: &ScanError{Code: ScanErrorFilesystem}})
		return nil
	}

	fileCount := 0
	countFile := func() error {
		fileCount++
		if fileCount > options.MaxFiles {
			return &ScanError{Code: ScanErrorLimit, Limit: "MaxFiles"}
		}
		return nil
	}
	queueFile := func(path, readPath string, info fs.FileInfo) error {
		if exceedsPathLimit(path, options.MaxPathBytes) || exceedsPathLimit(readPath, options.MaxPathBytes) {
			return &ScanError{Code: ScanErrorLimit, Limit: "MaxPathBytes"}
		}
		selected, filterErr := callScanFilter(options.Filter, path, info)
		if filterErr != nil {
			return filterErr
		}
		if !selected {
			return nil
		}
		select {
		case jobs <- scanTask{path: path, readPath: readPath, info: info}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if info.Mode()&os.ModeSymlink != 0 {
		if err := countFile(); err != nil {
			return err
		}
		switch options.SymlinkPolicy {
		case SymlinkIgnore:
			return nil
		case SymlinkReject:
			sendScanResult(ctx, results, ScanResult{Path: root, Err: &ScanError{Code: ScanErrorSymlink}})
			return nil
		case SymlinkFollowFiles:
			resolved, targetInfo, resolveErr := resolveRootFileLink(root)
			if resolveErr != nil {
				sendScanResult(ctx, results, ScanResult{Path: root, Err: resolveErr})
				return nil
			}
			return queueFile(root, resolved, targetInfo)
		}
	}
	if info.Mode().IsRegular() {
		if err := countFile(); err != nil {
			return err
		}
		return queueFile(root, root, info)
	}
	if !info.IsDir() {
		if err := countFile(); err != nil {
			return err
		}
		return nil
	}

	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		sendScanResult(ctx, results, ScanResult{Path: root, Err: &ScanError{Code: ScanErrorFilesystem}})
		return nil
	}
	rootInfo, err := os.Stat(canonicalRoot)
	if err != nil {
		sendScanResult(ctx, results, ScanResult{Path: root, Err: &ScanError{Code: ScanErrorFilesystem}})
		return nil
	}

	directoryCount := 1
	directories := []scanDirectory{{path: root, depth: 0, info: info}}
	for len(directories) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		last := len(directories) - 1
		directory := directories[last]
		directories = directories[:last]

		handle, openErr := os.Open(directory.path)
		if openErr != nil {
			if !sendScanResult(ctx, results, ScanResult{Path: directory.path, Err: &ScanError{Code: ScanErrorFilesystem}}) {
				return ctx.Err()
			}
			continue
		}
		openedInfo, statErr := handle.Stat()
		if statErr != nil || !openedInfo.IsDir() || !os.SameFile(openedInfo, directory.info) {
			_ = handle.Close()
			if !sendScanResult(ctx, results, ScanResult{Path: directory.path, Err: &ScanError{Code: ScanErrorFilesystem}}) {
				return ctx.Err()
			}
			continue
		}
		for {
			if err := ctx.Err(); err != nil {
				_ = handle.Close()
				return err
			}
			entries, readErr := handle.ReadDir(directoryReadBatch)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					_ = handle.Close()
					return err
				}
				path := filepath.Join(directory.path, entry.Name())
				if exceedsPathLimit(path, options.MaxPathBytes) {
					_ = handle.Close()
					return &ScanError{Code: ScanErrorLimit, Limit: "MaxPathBytes"}
				}
				entryInfo, infoErr := entry.Info()
				if infoErr != nil {
					if !sendScanResult(ctx, results, ScanResult{Path: path, Err: &ScanError{Code: ScanErrorFilesystem}}) {
						_ = handle.Close()
						return ctx.Err()
					}
					continue
				}
				if !entryInfo.IsDir() {
					if err := countFile(); err != nil {
						_ = handle.Close()
						return err
					}
				}
				if entryInfo.Mode()&os.ModeSymlink != 0 {
					switch options.SymlinkPolicy {
					case SymlinkIgnore:
						continue
					case SymlinkReject:
						if !sendScanResult(ctx, results, ScanResult{Path: path, Err: &ScanError{Code: ScanErrorSymlink}}) {
							_ = handle.Close()
							return ctx.Err()
						}
					case SymlinkFollowFiles:
						resolved, targetInfo, resolveErr := resolveContainedFileLink(path, rootInfo)
						if resolveErr != nil {
							if !sendScanResult(ctx, results, ScanResult{Path: path, Err: resolveErr}) {
								_ = handle.Close()
								return ctx.Err()
							}
							continue
						}
						if err := queueFile(path, resolved, targetInfo); err != nil {
							_ = handle.Close()
							return err
						}
					}
					continue
				}
				if entryInfo.IsDir() {
					nextDepth := directory.depth + 1
					if nextDepth > options.MaxDepth {
						_ = handle.Close()
						return &ScanError{Code: ScanErrorLimit, Limit: "MaxDepth"}
					}
					directoryCount++
					if directoryCount > options.MaxDirectories {
						_ = handle.Close()
						return &ScanError{Code: ScanErrorLimit, Limit: "MaxDirectories"}
					}
					directories = append(directories, scanDirectory{path: path, depth: nextDepth, info: entryInfo})
					continue
				}
				if entryInfo.Mode().IsRegular() {
					if err := queueFile(path, path, entryInfo); err != nil {
						_ = handle.Close()
						return err
					}
				}
			}
			if readErr != nil {
				closeErr := handle.Close()
				if readErr != io.EOF || closeErr != nil {
					if !sendScanResult(ctx, results, ScanResult{Path: directory.path, Err: &ScanError{Code: ScanErrorFilesystem}}) {
						return ctx.Err()
					}
				}
				break
			}
		}
	}
	return nil
}

func readScanTask(ctx context.Context, task scanTask, options Options) (Result, error) {
	info, err := os.Lstat(task.readPath)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, task.info) {
		return Result{}, &ScanError{Code: ScanErrorFilesystem}
	}
	return ReadPath(ctx, task.readPath, options)
}

func resolveRootFileLink(path string) (string, fs.FileInfo, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, &ScanError{Code: ScanErrorSymlink}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, &ScanError{Code: ScanErrorSymlink}
	}
	return resolved, info, nil
}

func resolveContainedFileLink(path string, rootInfo fs.FileInfo) (string, fs.FileInfo, error) {
	resolved, info, err := resolveRootFileLink(path)
	if err != nil {
		return "", nil, err
	}
	contained, err := directoryContains(filepath.Dir(resolved), rootInfo)
	if err != nil || !contained {
		return "", nil, &ScanError{Code: ScanErrorSymlink}
	}
	return resolved, info, nil
}

func directoryContains(directory string, rootInfo fs.FileInfo) (bool, error) {
	for {
		info, err := os.Stat(directory)
		if err != nil {
			return false, err
		}
		if os.SameFile(info, rootInfo) {
			return true, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false, nil
		}
		directory = parent
	}
}

func callScanFilter(filter ScanFilter, path string, info fs.FileInfo) (selected bool, err error) {
	if filter == nil {
		return true, nil
	}
	defer func() {
		if recover() != nil {
			selected = false
			err = &ScanError{Code: ScanErrorFilter}
		}
	}()
	return filter(path, info), nil
}

func callScanYield(yield func(ScanResult) error, result ScanResult) (err error) {
	defer func() {
		if recover() != nil {
			err = &ScanError{Code: ScanErrorYield}
		}
	}()
	if callbackErr := yield(result); callbackErr != nil {
		return &ScanError{Code: ScanErrorYield}
	}
	return nil
}

func sendScanResult(ctx context.Context, results chan<- ScanResult, result ScanResult) bool {
	select {
	case results <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

func exceedsPathLimit(path string, maxBytes int) bool { return len([]byte(path)) > maxBytes }
