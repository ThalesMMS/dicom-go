package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/ThalesMMS/dicom-go/internal/codifycli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(codifycli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
