package dicomwebcli

import (
	"context"
	"flag"
	"io"
)

type verifyOutput struct {
	StatusCode int   `json:"status_code"`
	DurationMS int64 `json:"duration_ms"`
}

func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts := defaultCommonOptions()
	fs := flag.NewFlagSet("dicomweb verify", flag.ContinueOnError)
	addCommonFlags(fs, &opts)
	configureUsage(fs, stderr, "dicomweb verify [flags]")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return invalidInput("verify accepts no positional arguments")
	}
	client, err := opts.client()
	if err != nil {
		return err
	}
	result, err := client.Verify(ctx)
	if err != nil {
		return err
	}
	return writeJSON(stdout, opts.output, verifyOutput{StatusCode: result.StatusCode, DurationMS: result.Duration.Milliseconds()})
}
