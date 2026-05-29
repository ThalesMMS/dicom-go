package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		clidiag.Fprintln(stderr, "storescu", err)
		return 1
	}
	if err := runStore(opts, stdout, stderr); err != nil {
		clidiag.Fprintln(stderr, "storescu", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	address         string
	calledAE        string
	callingAE       string
	timeout         time.Duration
	files           []string
	continueOnError bool
}

const (
	defaultDialTimeout    = 10 * time.Second
	defaultReleaseTimeout = 5 * time.Second
	defaultMaxStoreFiles  = 100_000
	defaultMaxStoreDirs   = 50_000
	defaultMaxStoreDepth  = 64
	defaultMaxStorePath   = 4_096
)

func parseArgs(args []string, stderr io.Writer) (options, error) {
	opts := options{
		calledAE:        "ANY-SCP",
		callingAE:       "STORESCU",
		timeout:         defaultDialTimeout,
		continueOnError: true,
	}

	fs := flag.NewFlagSet("storescu", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.calledAE, "called", opts.calledAE, "called AE title")
	fs.StringVar(&opts.callingAE, "calling", opts.callingAE, "calling AE title")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "connection timeout")
	fs.BoolVar(&opts.continueOnError, "continue-on-error", opts.continueOnError, "continue after per-file failures")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags] address file-or-directory [...]\n\nSend DICOM Part 10 files with a reusable C-STORE session.\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, errUsage
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return opts, errUsage
	}
	opts.address = fs.Arg(0)
	opts.files = append([]string(nil), fs.Args()[1:]...)
	if opts.timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "-timeout must be positive")
		fs.Usage()
		return opts, errUsage
	}
	return opts, nil
}

func runStore(opts options, stdout, stderr io.Writer) error {
	_ = stderr
	inputs, err := expandStoreInputSpecs(context.Background(), opts.files, storeInputLimits{
		maxFiles: defaultMaxStoreFiles,
		maxDirs:  defaultMaxStoreDirs,
		maxDepth: defaultMaxStoreDepth,
		maxPath:  defaultMaxStorePath,
	})
	if err != nil {
		return err
	}
	sources := make([]dimse.StoreSource, len(inputs))
	for i := range inputs {
		sources[i] = dimse.NewRootedPathStoreSource(inputs[i].root, inputs[i].relative)
	}
	session, err := dimse.NewStoreSession(opts.address, dimse.StoreSessionOptions{
		DialOptions: ul.DialOptions{
			CalledAETitle:        opts.calledAE,
			CallingAETitle:       opts.callingAE,
			NegotiationTimeout:   opts.timeout,
			ReadProgressTimeout:  opts.timeout,
			WriteProgressTimeout: opts.timeout,
		},
		ContinueOnError: opts.continueOnError,
		ReleaseTimeout:  defaultReleaseTimeout,
	})
	if err != nil {
		return err
	}
	defer func() { _ = session.Abort(context.Background()) }()
	result, err := session.StoreBatch(context.Background(), sources)
	for _, item := range result.Items {
		status := ""
		if item.StatusSet {
			status = fmt.Sprintf(" status=0x%04X", item.Status)
		}
		_, _ = fmt.Fprintf(stdout, "C-STORE source=%d outcome=%s%s\n", item.SourceIndex+1, item.Outcome, status)
	}
	if err != nil {
		return err
	}
	if result.Failed > 0 || result.Unknown > 0 {
		return fmt.Errorf("C-STORE batch completed with %d failed and %d uncertain", result.Failed, result.Unknown)
	}
	return session.Close(context.Background())
}

func expandStoreInputs(ctx context.Context, inputs []string, maxFiles int) ([]string, error) {
	return expandStoreInputsWithLimits(ctx, inputs, storeInputLimits{
		maxFiles: maxFiles,
		maxDirs:  defaultMaxStoreDirs,
		maxDepth: defaultMaxStoreDepth,
		maxPath:  defaultMaxStorePath,
	})
}

type storeInputLimits struct {
	maxFiles int
	maxDirs  int
	maxDepth int
	maxPath  int
}

type storePendingInput struct {
	path      string
	depth     int
	directory bool
	identity  fs.FileInfo
}

type storeExpandedInput struct {
	path     string
	root     string
	relative string
}

func expandStoreInputsWithLimits(ctx context.Context, inputs []string, limits storeInputLimits) ([]string, error) {
	expanded, err := expandStoreInputSpecs(ctx, inputs, limits)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(expanded))
	for i := range expanded {
		paths[i] = expanded[i].path
	}
	return paths, nil
}

func expandStoreInputSpecs(ctx context.Context, inputs []string, limits storeInputLimits) ([]storeExpandedInput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limits.maxFiles <= 0 || limits.maxDirs <= 0 || limits.maxDepth < 0 || limits.maxPath <= 0 {
		return nil, dimse.ErrStoreResourceLimit
	}
	seen := make(map[string]bool)
	seenDirs := make(map[string]bool)
	expanded := make([]storeExpandedInput, 0, len(inputs))
	add := func(path, lexicalRoot, physicalRoot string) error {
		path = filepath.Clean(path)
		if len([]byte(path)) > limits.maxPath {
			return dimse.ErrStoreResourceLimit
		}
		if seen[path] {
			return nil
		}
		if len(expanded) >= limits.maxFiles {
			return dimse.ErrStoreResourceLimit
		}
		relative, err := filepath.Rel(lexicalRoot, path)
		if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("input escaped scan root")
		}
		seen[path] = true
		expanded = append(expanded, storeExpandedInput{path: path, root: physicalRoot, relative: relative})
		return nil
	}
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len([]byte(filepath.Clean(input))) > limits.maxPath {
			return nil, dimse.ErrStoreResourceLimit
		}
		info, err := os.Lstat(input)
		if err != nil {
			return nil, errors.New("file inspection failed")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symbolic links are not accepted")
		}
		if info.Mode().IsRegular() {
			cleanInput := filepath.Clean(input)
			lexicalRoot := filepath.Dir(cleanInput)
			physicalRoot, err := storePhysicalRoot(lexicalRoot)
			if err != nil {
				return nil, errors.New("file inspection failed")
			}
			if err := add(cleanInput, lexicalRoot, physicalRoot); err != nil {
				return nil, err
			}
			continue
		}
		if !info.IsDir() {
			return nil, errors.New("input is not a regular file or directory")
		}
		scanRoot := filepath.Clean(input)
		physicalScanRoot, err := storePhysicalRoot(scanRoot)
		if err != nil {
			return nil, errors.New("directory scan failed")
		}
		pending := []storePendingInput{{path: scanRoot, directory: true, identity: info}}
		for len(pending) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			current := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if !current.directory {
				if err := add(current.path, scanRoot, physicalScanRoot); err != nil {
					return nil, err
				}
				continue
			}
			if seenDirs[current.path] {
				continue
			}
			if len(seenDirs) >= limits.maxDirs {
				return nil, dimse.ErrStoreResourceLimit
			}
			seenDirs[current.path] = true
			before, err := os.Lstat(current.path)
			if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() || current.identity == nil || !os.SameFile(current.identity, before) {
				return nil, errors.New("directory scan failed")
			}
			handle, err := os.Open(current.path)
			if err != nil {
				return nil, errors.New("directory scan failed")
			}
			openedInfo, statErr := handle.Stat()
			if statErr != nil || !openedInfo.IsDir() || !os.SameFile(before, openedInfo) {
				_ = handle.Close()
				return nil, errors.New("directory scan failed")
			}
			entries := make([]fs.DirEntry, 0, 128)
			for {
				if err := ctx.Err(); err != nil {
					_ = handle.Close()
					return nil, err
				}
				batch, readErr := handle.ReadDir(128)
				for _, entry := range batch {
					if entry.Type()&os.ModeSymlink != 0 {
						_ = handle.Close()
						return nil, errors.New("symbolic links are not accepted")
					}
					if entry.IsDir() || entry.Type().IsRegular() {
						entries = append(entries, entry)
						if len(entries) > limits.maxFiles+limits.maxDirs {
							_ = handle.Close()
							return nil, dimse.ErrStoreResourceLimit
						}
					}
				}
				if errors.Is(readErr, io.EOF) {
					break
				}
				if readErr != nil {
					_ = handle.Close()
					return nil, errors.New("directory scan failed")
				}
			}
			if err := handle.Close(); err != nil {
				return nil, errors.New("directory scan failed")
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			children := make([]storePendingInput, 0, len(entries))
			for _, entry := range entries {
				path := filepath.Join(current.path, entry.Name())
				if len([]byte(path)) > limits.maxPath {
					return nil, dimse.ErrStoreResourceLimit
				}
				if entry.IsDir() {
					entryInfo, infoErr := entry.Info()
					if infoErr != nil || entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.IsDir() {
						return nil, errors.New("directory scan failed")
					}
					depth := current.depth + 1
					if depth > limits.maxDepth {
						return nil, dimse.ErrStoreResourceLimit
					}
					children = append(children, storePendingInput{path: path, depth: depth, directory: true, identity: entryInfo})
				} else {
					children = append(children, storePendingInput{path: path, depth: current.depth})
				}
			}
			for i := len(children) - 1; i >= 0; i-- {
				pending = append(pending, children[i])
			}
		}
	}
	if len(expanded) == 0 {
		return nil, errors.New("no regular files to send")
	}
	return expanded, nil
}

func storePhysicalRoot(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absRoot)
}
