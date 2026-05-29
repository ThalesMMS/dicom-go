package main

import (
	"bytes"
	"errors"
	"flag"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.host != "localhost" || opts.port != 11112 || opts.calledAE != "ANY-SCP" || opts.callingAE != "ECHOSCU" || opts.messageID != 1 {
		t.Fatalf("parseArgs() defaults = %#v", opts)
	}
}

func TestParseArgsCustomValues(t *testing.T) {
	opts, err := parseArgs([]string{
		"-host", "127.0.0.1",
		"-port", "104",
		"-called-ae", "SCP",
		"-calling-ae", "SCU",
		"-message-id", "77",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.host != "127.0.0.1" || opts.port != 104 || opts.calledAE != "SCP" || opts.callingAE != "SCU" || opts.messageID != 77 {
		t.Fatalf("parseArgs() = %#v", opts)
	}
}

func TestParseArgsUsageError(t *testing.T) {
	_, err := parseArgs([]string{"extra"}, &bytes.Buffer{})
	if !errors.Is(err, errUsage) {
		t.Fatalf("parseArgs(extra) error = %v, want errUsage", err)
	}

	_, err = parseArgs([]string{"-h"}, &bytes.Buffer{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseArgs(-h) error = %v, want flag.ErrHelp", err)
	}
}

func TestRunNetworkErrorIsClassified(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"-host", "127.0.0.1", "-port", strconv.Itoa(port)}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("run() exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "echoscu: network:") {
		t.Fatalf("stderr = %q, want network classification", got)
	}
}
