package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestReferenceArchiveIndependentStoreFindGetMoveRestart(t *testing.T) {
	if os.Getenv("DICOMGO_PYNETDICOM_INTEGRATION") == "" {
		t.Skip("opt-in: DICOMGO_PYNETDICOM_INTEGRATION=1 and DICOMGO_PYTHON; uses the #903 pinned peer profile")
	}
	python := os.Getenv("DICOMGO_PYTHON")
	if python == "" {
		python = "python3"
	}
	dir := t.TempDir()
	for phase := 0; phase < 2; phase++ {
		t.Run(strconv.Itoa(phase), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			a, err := openArchive(ctx, dir, defaultArchiveLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			command := exec.CommandContext(ctx, python, "-u", "testdata/peer.py", dir, strconv.Itoa(phase))
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			// Peer fixtures are synthetic; collect diagnostics after process exit.
			var stderr strings.Builder
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill() }()
			scanner := bufio.NewScanner(output)
			if !scanner.Scan() {
				_ = command.Wait()
				t.Fatalf("peer start: %s", stderr.String())
			}
			fields := strings.Fields(scanner.Text())
			if len(fields) != 2 || fields[0] != "PORT" {
				t.Fatal("invalid peer readiness")
			}
			port, err := strconv.Atoi(fields[1])
			if err != nil || port < 1 || port > 65535 {
				t.Fatal("invalid peer port")
			}
			listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serviceErrors := make(chan error, 4)
			server, err := newArchiveServer(listener, a, "REFERENCEARCHIVE", map[string]string{"MOVEDEST": net.JoinHostPort("127.0.0.1", fields[1])}, func(err error) {
				if err != nil {
					select {
					case serviceErrors <- err:
					default:
					}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx) }()
			defer func() {
				cancel()
				if err := <-done; err != nil && ctx.Err() == nil {
					t.Error(err)
				}
			}()
			_, archivePort, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Fprintln(input, archivePort); err != nil {
				t.Fatal(err)
			}
			_ = input.Close()
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if err := command.Wait(); err != nil {
				select {
				case serviceErr := <-serviceErrors:
					t.Logf("service error: %v", serviceErr)
				default:
				}
				t.Fatalf("independent peer failed: %v\n%s\n%s", err, strings.Join(lines, "\n"), stderr.String())
			}
			if err := scanner.Err(); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(lines, "\n"), "VERIFIED") {
				t.Fatal("missing independent evidence")
			}
			t.Log(strings.Join(lines, "\n"))
		})
	}
}
