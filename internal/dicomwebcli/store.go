package dicomwebcli

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

type storeOptions struct {
	common         commonOptions
	studyUID       string
	maxFiles       int
	maxUploadBytes int64
}

type storeSource struct {
	path string
	size int64
	info os.FileInfo
}

type storeSummary struct {
	StatusCode   int `json:"status_code"`
	StoredCount  int `json:"stored_count"`
	WarningCount int `json:"warning_count"`
	FailedCount  int `json:"failed_count"`
}

func runStore(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts := storeOptions{common: defaultCommonOptions(), maxFiles: 10_000, maxUploadBytes: 2 << 30}
	fs := flag.NewFlagSet("dicomweb store", flag.ContinueOnError)
	addCommonFlags(fs, &opts.common)
	fs.StringVar(&opts.studyUID, "study-uid", "", "optional study-scoped STOW-RS route UID")
	fs.IntVar(&opts.maxFiles, "max-files", opts.maxFiles, "maximum input files")
	fs.Int64Var(&opts.maxUploadBytes, "max-upload-bytes", opts.maxUploadBytes, "maximum aggregate input bytes")
	configureUsage(fs, stderr, "dicomweb store [flags] <file> [...]")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if opts.maxFiles <= 0 || opts.maxUploadBytes <= 0 {
		return invalidInput("-max-files and -max-upload-bytes must be positive")
	}
	if fs.NArg() == 0 {
		return invalidInput("store requires at least one file")
	}
	studyUID := strings.TrimSpace(opts.studyUID)
	if studyUID != "" && !core.IsValidUID(studyUID) {
		return invalidInput("-study-uid must be a valid UID")
	}
	sources, err := preflightStoreFiles(fs.Args(), opts.maxFiles, opts.maxUploadBytes)
	if err != nil {
		return err
	}
	if err := preflightJSONOutput(opts.common.output); err != nil {
		return err
	}
	client, err := opts.common.client()
	if err != nil {
		return err
	}
	instances := make([]dicomweb.StoreInstance, len(sources))
	for index := range sources {
		source := sources[index]
		instances[index] = dicomweb.StoreInstance{Open: func() (io.ReadCloser, error) {
			return source.open(ctx)
		}}
	}
	var result dicomweb.StoreResult
	if studyUID == "" {
		result, err = client.StoreInstances(ctx, instances)
	} else {
		result, err = client.StoreInstancesToStudy(ctx, studyUID, instances)
	}
	if err != nil && len(result.Stored) == 0 && len(result.Failed) == 0 {
		return err
	}
	warningCount := 0
	for _, item := range result.Stored {
		if item.WarningReason != 0 {
			warningCount++
		}
	}
	summaryErr := writeJSON(stdout, opts.common.output, storeSummary{
		StatusCode: result.StatusCode, StoredCount: len(result.Stored), WarningCount: warningCount, FailedCount: len(result.Failed),
	})
	if summaryErr != nil {
		return summaryErr
	}
	if len(result.Failed) > 0 || result.StatusCode == 202 || (err != nil && len(result.Stored) > 0) {
		return errPartialStore
	}
	return err
}

func preflightStoreFiles(paths []string, maximumFiles int, maximumBytes int64) ([]storeSource, error) {
	if len(paths) > maximumFiles {
		return nil, invalidInput("input count exceeds -max-files")
	}
	sources := make([]storeSource, 0, len(paths))
	var total int64
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 132 {
			return nil, invalidInput("each store input must be a non-empty regular Part 10 file")
		}
		if info.Size() > maximumBytes-total {
			return nil, invalidInput("aggregate input exceeds -max-upload-bytes")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, statErr
		}
		if !openedInfo.Mode().IsRegular() || !os.SameFile(openedInfo, info) || openedInfo.Size() != info.Size() {
			_ = file.Close()
			return nil, invalidInput("store input changed during preflight")
		}
		readErr := validatePart10AndRewind(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			if readErr != nil {
				return nil, readErr
			}
			return nil, closeErr
		}
		total += info.Size()
		sources = append(sources, storeSource{path: path, size: info.Size(), info: info})
	}
	return sources, nil
}

func (source storeSource) open(ctx context.Context) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(source.path)
	if err != nil {
		return nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, source.info) || pathInfo.Size() != source.size {
		return nil, invalidInput("store input changed after preflight")
	}
	file, err := os.Open(source.path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, source.info) || info.Size() != source.size {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, invalidInput("store input changed after preflight")
	}
	if err := validatePart10AndRewind(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validatePart10AndRewind(file *os.File) error {
	var prefix [132]byte
	if _, err := io.ReadFull(file, prefix[:]); err != nil {
		return err
	}
	if !bytes.Equal(prefix[128:], []byte("DICM")) {
		return invalidInput("each store input must be a DICOM Part 10 file")
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}
