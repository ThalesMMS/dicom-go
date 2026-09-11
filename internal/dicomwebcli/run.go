package dicomwebcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

const (
	ExitOK           = 0
	ExitFailure      = 1
	ExitInput        = 2
	ExitAuth         = 3
	ExitHTTP         = 4
	ExitDecode       = 5
	ExitPartialStore = 6
	ExitCanceled     = 130
)

var errPartialStore = errors.New("partial STOW-RS result")

// RunWithContext executes one DICOMweb CLI invocation without terminating the process.
func RunWithContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		printRootUsage(stderr)
		return ExitInput
	}
	command := args[0]
	var err error
	switch command {
	case "verify":
		err = runVerify(ctx, args[1:], stdout, stderr)
	case "query":
		err = runQuery(ctx, args[1:], stdout, stderr)
	case "retrieve":
		err = runRetrieve(ctx, args[1:], stdout, stderr)
	case "frames":
		err = runFrames(ctx, args[1:], stdout, stderr)
	case "store":
		err = runStore(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printRootUsage(stdout)
		return ExitOK
	default:
		_, _ = fmt.Fprintln(stderr, "unknown subcommand")
		printRootUsage(stderr)
		return ExitInput
	}
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	if err == nil {
		return ExitOK
	}
	exit := classifyExit(ctx, err)
	writeDiagnostic(stderr, command, err, exit)
	return exit
}

func printRootUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: dicomweb <verify|query|retrieve|frames|store> [flags]")
}
