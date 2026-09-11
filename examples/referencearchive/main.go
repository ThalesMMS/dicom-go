// Reference archive connects existing dicom-go services for local development.
// It is not a clinical PACS and does not advertise Storage Commitment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

type destinationFlags map[string]string

func (d destinationFlags) String() string { return "configured C-MOVE allowlist" }
func (d destinationFlags) Set(value string) error {
	ae, address, ok := strings.Cut(value, "=")
	if !ok || len(ae) == 0 || len(ae) > 16 || strings.TrimSpace(ae) != ae || strings.ContainsAny(ae, "\\\r\n\x00") {
		return errQuery
	}
	for _, r := range ae {
		if r < 32 || r > 126 {
			return errQuery
		}
	}
	host, port, err := net.SplitHostPort(address)
	n, numberErr := strconv.Atoi(port)
	if err != nil || host == "" || numberErr != nil || n < 1 || n > 65535 {
		return errQuery
	}
	if _, exists := d[ae]; exists {
		return errQuery
	}
	if len(d) >= 16 {
		return errQuota
	}
	d[ae] = address
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(runArchive(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func runArchive(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	limits := defaultArchiveLimits()
	destinations := destinationFlags{}
	fs := flag.NewFlagSet("referencearchive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "./reference-archive", "trusted directory for this reference archive")
	address := fs.String("listen", "127.0.0.1:11113", "DIMSE listen address (loopback by default)")
	ae := fs.String("aetitle", "REFERENCEARCHIVE", "archive AE title")
	fs.IntVar(&limits.instances, "max-instances", limits.instances, "maximum stored instances (up to 10000)")
	fs.IntVar(&limits.results, "max-results", limits.results, "maximum instances selected by one query/retrieve")
	fs.Int64Var(&limits.instanceBytes, "max-instance-bytes", limits.instanceBytes, "maximum Part 10 bytes per instance (up to 64 MiB)")
	fs.Int64Var(&limits.totalBytes, "max-archive-bytes", limits.totalBytes, "maximum committed Part 10 bytes")
	fs.Var(destinations, "move-destination", "explicit AE=host:port destination; repeat for allowlist")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || !limits.valid() || strings.TrimSpace(*root) == "" {
		fmt.Fprintln(stderr, "reference archive: invalid options")
		return 2
	}
	a, err := openArchive(ctx, *root, limits)
	if err != nil {
		fmt.Fprintln(stderr, "reference archive: open failed")
		return 1
	}
	defer a.Close()
	listener, err := ul.Listen(ul.ListenOptions{Address: *address, Context: ctx})
	if err != nil {
		fmt.Fprintln(stderr, "reference archive: listen failed")
		return 1
	}
	defer listener.Close()
	server, err := newArchiveServer(listener, a, *ae, destinations)
	if err != nil {
		fmt.Fprintln(stderr, "reference archive: server configuration failed")
		return 1
	}
	fmt.Fprintf(stdout, "reference archive listening on %s; Storage Commitment disabled\n", listener.Addr())
	if err := server.Serve(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(stderr, "reference archive: server failed")
		return 1
	}
	return 0
}
