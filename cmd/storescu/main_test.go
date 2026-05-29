package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs([]string{"127.0.0.1:104", "file.dcm"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.address != "127.0.0.1:104" || opts.calledAE != "ANY-SCP" || opts.callingAE != "STORESCU" || opts.timeout != defaultDialTimeout || len(opts.files) != 1 || opts.files[0] != "file.dcm" {
		t.Fatalf("parseArgs() defaults = %#v", opts)
	}
}

func TestParseArgsCustomValues(t *testing.T) {
	opts, err := parseArgs([]string{
		"-called", "SCP",
		"-calling", "SCU",
		"-timeout", "2s",
		"127.0.0.1:104",
		"file.dcm",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.address != "127.0.0.1:104" || opts.calledAE != "SCP" || opts.callingAE != "SCU" || opts.timeout != 2*time.Second || len(opts.files) != 1 || opts.files[0] != "file.dcm" {
		t.Fatalf("parseArgs() = %#v", opts)
	}
}

func TestParseArgsUsageError(t *testing.T) {
	_, err := parseArgs(nil, &bytes.Buffer{})
	if !errors.Is(err, errUsage) {
		t.Fatalf("parseArgs(nil) error = %v, want errUsage", err)
	}

	_, err = parseArgs([]string{"-h"}, &bytes.Buffer{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseArgs(-h) error = %v, want flag.ErrHelp", err)
	}
}

func TestRunMissingFileErrorIsClassified(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.dcm")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"127.0.0.1:1", missingPath}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("run() exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "storescu: error: file inspection failed") || strings.Contains(got, missingPath) {
		t.Fatalf("stderr = %q, want redacted file classification", got)
	}
}

func TestRunStoreSendsFixtureToLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	path := tempDICOMFile(t, data)

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                 "STORESCP",
			Context:                 ctx,
			AcceptAnyAbstractSyntax: true,
			SupportedTransferSyntaxes: []string{
				transfer.ExplicitVRLittleEndian.UID,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()

		pc, err := dimse.AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := dimse.ReceiveCStoreRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := dimse.ReceiveDataSet(assoc, pc.ID, transfer.ExplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		if err := dimse.SendCStoreResponse(assoc, pc.ID, dimse.CStoreResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
			Status:                    dimse.StatusSuccess,
		}); err != nil {
			serverDone <- err
			return
		}
		pdu, err := assoc.ReadPDU()
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := pdu.(*ul.ReleaseRQ); !ok {
			serverDone <- errors.New("server expected A-RELEASE-RQ")
			return
		}
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()

	var stdout bytes.Buffer
	err = runStore(options{
		address:   listener.Addr().String(),
		calledAE:  "STORESCP",
		callingAE: "STORESCU",
		timeout:   2 * time.Second,
		files:     []string{path},
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runStore() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("outcome=success")) {
		t.Fatalf("stdout = %q, want success", stdout.String())
	}
}

func TestParseArgsAcceptsMultipleInputs(t *testing.T) {
	opts, err := parseArgs([]string{"127.0.0.1:104", "one.dcm", "two.dcm", "directory"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if got := len(opts.files); got != 3 {
		t.Fatalf("files = %v, want 3", opts.files)
	}
}

func TestExpandStoreInputsWalksDirectoriesDeterministicallyAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "A")
	if err := os.Mkdir(firstDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	first := filepath.Join(firstDir, "ONE.DCM")
	second := filepath.Join(root, "TWO.DCM")
	if err := os.WriteFile(first, []byte("one"), 0o600); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil {
		t.Fatalf("WriteFile(second) error = %v", err)
	}

	paths, err := expandStoreInputs(context.Background(), []string{root}, 2)
	if err != nil {
		t.Fatalf("expandStoreInputs() error = %v", err)
	}
	want := []string{first, second}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}

	link := filepath.Join(root, "LINK.DCM")
	if err := os.Symlink(first, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := expandStoreInputs(context.Background(), []string{root}, 3); err == nil {
		t.Fatal("expandStoreInputs(symlink) error = nil, want rejection")
	}
}

func TestExpandStoreInputsEnforcesFiniteTraversalLimits(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "A")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(child, "ONE.DCM")
	if err := os.WriteFile(file, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	exact := storeInputLimits{maxFiles: 1, maxDirs: 2, maxDepth: 1, maxPath: len([]byte(file))}
	paths, err := expandStoreInputsWithLimits(context.Background(), []string{root}, exact)
	if err != nil || len(paths) != 1 || paths[0] != file {
		t.Fatalf("exact limits paths=%v error=%v", paths, err)
	}

	tests := []struct {
		name   string
		limits storeInputLimits
	}{
		{name: "files", limits: storeInputLimits{maxFiles: 0, maxDirs: 2, maxDepth: 1, maxPath: len([]byte(file))}},
		{name: "directories", limits: storeInputLimits{maxFiles: 1, maxDirs: 1, maxDepth: 1, maxPath: len([]byte(file))}},
		{name: "depth", limits: storeInputLimits{maxFiles: 1, maxDirs: 2, maxDepth: 0, maxPath: len([]byte(file))}},
		{name: "path", limits: storeInputLimits{maxFiles: 1, maxDirs: 2, maxDepth: 1, maxPath: len([]byte(file)) - 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := expandStoreInputsWithLimits(context.Background(), []string{root}, test.limits); !errors.Is(err, dimse.ErrStoreResourceLimit) {
				t.Fatalf("error = %v, want ErrStoreResourceLimit", err)
			}
		})
	}
}

func TestExpandedStoreSourceRejectsAncestorSymlinkSwap(t *testing.T) {
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "source.dcm"
	if err := os.WriteFile(filepath.Join(sub, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	expanded, err := expandStoreInputSpecs(context.Background(), []string{root}, storeInputLimits{
		maxFiles: 1, maxDirs: 2, maxDepth: 1, maxPath: 4096,
	})
	if err != nil || len(expanded) != 1 {
		t.Fatalf("expandStoreInputSpecs() = %v, %v", expanded, err)
	}
	original := filepath.Join(root, "original-sub")
	if err := os.Rename(sub, original); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, sub); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	source := dimse.NewRootedPathStoreSource(expanded[0].root, expanded[0].relative)
	if _, err := source.Inspect(context.Background()); !errors.Is(err, dimse.ErrStoreInvalidSource) {
		t.Fatalf("Inspect() error = %v, want ErrStoreInvalidSource after ancestor swap", err)
	}
}

func TestExpandedStoreSourceRejectsRootParentSymlinkSwap(t *testing.T) {
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	sandbox := t.TempDir()
	parent := filepath.Join(sandbox, "parent")
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "source.dcm"
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	expanded, err := expandStoreInputSpecs(context.Background(), []string{root}, storeInputLimits{
		maxFiles: 1, maxDirs: 1, maxDepth: 0, maxPath: 4096,
	})
	if err != nil || len(expanded) != 1 {
		t.Fatalf("expandStoreInputSpecs() = %v, %v", expanded, err)
	}
	originalParent := filepath.Join(sandbox, "original-parent")
	if err := os.Rename(parent, originalParent); err != nil {
		t.Fatal(err)
	}
	externalParent := t.TempDir()
	externalRoot := filepath.Join(externalParent, "root")
	if err := os.Mkdir(externalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalRoot, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalParent, parent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	source := dimse.NewRootedPathStoreSource(expanded[0].root, expanded[0].relative)
	if _, err := source.Inspect(context.Background()); !errors.Is(err, dimse.ErrStoreInvalidSource) {
		t.Fatalf("Inspect() error = %v, want ErrStoreInvalidSource after root parent swap", err)
	}
}

func tempDICOMFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.dcm")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return f.Name()
}
