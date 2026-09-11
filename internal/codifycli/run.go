// Package codifycli contains the thin, local-only codify command.
package codifycli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ThalesMMS/dicom-go/codify"
	"github.com/ThalesMMS/dicom-go/deid"
	"github.com/ThalesMMS/dicom-go/internal/nofollow"
	"github.com/ThalesMMS/dicom-go/object"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dicom-go-codify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := codify.Options{Limits: codify.DefaultLimits()}
	fs.StringVar(&o.Package, "package", "main", "generated package; main includes a complete entry point")
	fs.StringVar(&o.Function, "function", "BuildObject", "generated object builder identifier")
	faithful := fs.Bool("faithful", false, "local opt-in: source may contain PHI")
	fs.BoolVar(&o.InlineBinary, "inline-binary", false, "faithful only: embed bounded binary payloads")
	profile := fs.Bool("deid-basic", false, "apply existing deid Basic Profile before generation; review residual risks")
	output := fs.String("output", "", "new output file; default stdout; existing files never replaced")
	maxInput := fs.Int64("max-input-bytes", 64<<20, "maximum input bytes (up to 256 MiB)")
	fs.IntVar(&o.Limits.MaxElements, "max-elements", o.Limits.MaxElements, "maximum elements")
	fs.IntVar(&o.Limits.MaxDepth, "max-depth", o.Limits.MaxDepth, "maximum sequence depth")
	fs.IntVar(&o.Limits.MaxItems, "max-items", o.Limits.MaxItems, "maximum sequence items")
	fs.IntVar(&o.Limits.MaxValues, "max-values", o.Limits.MaxValues, "maximum scalar values/fragments")
	fs.IntVar(&o.Limits.MaxBinaryBytes, "max-binary-bytes", o.Limits.MaxBinaryBytes, "maximum total embedded binary bytes")
	fs.IntVar(&o.Limits.MaxValueBytes, "max-value-bytes", o.Limits.MaxValueBytes, "maximum retained text/raw bytes")
	fs.IntVar(&o.Limits.MaxOutputBytes, "max-output-bytes", o.Limits.MaxOutputBytes, "maximum formatted Go source bytes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "Usage: dicom-go-codify [flags] input.dcm")
			fs.SetOutput(stderr)
			fs.PrintDefaults()
			return 0
		}
		fmt.Fprintln(stderr, "codify: invalid arguments")
		return 2
	}
	if fs.NArg() != 1 || *maxInput <= 0 || *maxInput > 256<<20 {
		fmt.Fprintln(stderr, "codify: invalid arguments")
		return 2
	}
	if *faithful {
		o.Mode = codify.Faithful
	}
	// Validate options before opening input or allocating parser buffers.
	normalized, optionsErr := codify.NormalizeOptions(o)
	if optionsErr != nil || ctx == nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "codify: invalid options or canceled")
		return 2
	}
	o = normalized
	input, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "codify: input unavailable")
		return 1
	}
	f, err := nofollow.OpenFile(input)
	if err != nil {
		fmt.Fprintln(stderr, "codify: input unavailable")
		return 1
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > *maxInput {
		fmt.Fprintln(stderr, "codify: input limit or type rejected")
		return 1
	}
	pixelLimit := *maxInput
	if o.InlineBinary {
		pixelLimit = min(pixelLimit, int64(o.Limits.MaxBinaryBytes))
	}
	file, err := object.ReadFileWithOptions(contextReader{ctx, io.LimitReader(f, *maxInput+1)}, object.ReadFileOptions{MaxTotalBytes: *maxInput, MaxElementBytes: int64(o.Limits.MaxValueBytes), MaxPixelDataBytes: pixelLimit, MaxElements: o.Limits.MaxElements, MaxSequenceDepth: o.Limits.MaxDepth, MaxFragments: o.Limits.MaxValues, SkipPixelData: !o.InlineBinary})
	if err != nil {
		fmt.Fprintln(stderr, "codify: input parse failed")
		return 1
	}
	defer file.Close()
	if *profile {
		p := deid.DefaultBasicProfileOptions()
		p.Limits.MaxDepth = o.Limits.MaxDepth
		p.Limits.MaxElements = o.Limits.MaxElements
		p.Limits.MaxItems = o.Limits.MaxItems
		p.Limits.MaxValueBytes = *maxInput
		report, err := deid.ApplyBasicProfile(ctx, file.Dataset, p, deid.NewUIDRemapper())
		if err != nil {
			fmt.Fprintln(stderr, "codify: deid profile failed")
			return 1
		}
		fmt.Fprintln(stderr, "codify: deid profile applied; residual risks require review; output is not guaranteed anonymous")
		if err := json.NewEncoder(stderr).Encode(report); err != nil {
			return 1
		}
	}
	result, err := codify.Generate(ctx, file.Dataset, o)
	if err != nil {
		fmt.Fprintln(stderr, "codify: generation failed (options, value, resource limit or cancellation)")
		return 1
	}
	if *faithful {
		fmt.Fprintln(stderr, "codify: faithful output may contain PHI, including private/nested data and URIs")
	}
	if err := json.NewEncoder(stderr).Encode(result.Report); err != nil {
		return 1
	}
	if *output == "" {
		if _, err := stdout.Write(result.Source); err != nil {
			return 1
		}
		return 0
	}
	if err := publish(ctx, *output, result.Source); err != nil {
		fmt.Fprintln(stderr, "codify: output failed; existing destinations are never replaced")
		return 1
	}
	return 0
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Publish only reviewed, fully generated source to a new name in a trusted
// local directory. Reuse the descriptor-anchored no-follow publication helper.
func publish(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parent, err := nofollow.OpenDirectory(filepath.Dir(abs))
	if err != nil {
		return err
	}
	defer parent.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := ".codify-" + hex.EncodeToString(random[:]) + ".partial"
	f, err := nofollow.CreateAt(parent, name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = nofollow.RemoveAt(parent, name) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	_, err = nofollow.PublishClosedAt(parent, name, filepath.Base(abs), info)
	return err
}
