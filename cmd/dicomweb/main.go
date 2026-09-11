package main

import (
	"context"
	"io"
	"os"

	"github.com/ThalesMMS/dicom-go/internal/clisignal"
	"github.com/ThalesMMS/dicom-go/internal/dicomwebcli"
)

func main() {
	ctx, stop := clisignal.NotifyInterruptContext(context.Background())
	defer stop()
	os.Exit(runWithContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func runWithContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return dicomwebcli.RunWithContext(ctx, args, stdout, stderr)
}
