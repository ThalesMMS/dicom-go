package pixeldata

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestTranscodePathStagesDoNotPublishBeforeCommit(t *testing.T) {
	if !transcodePathSupported() {
		t.Skip("atomic transcode publication is unavailable on this platform")
	}
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.dcm")
	destinationPath := filepath.Join(dir, "destination.dcm")
	wantDestination := []byte("existing destination")
	if err := os.WriteFile(sourcePath, []byte("stable source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destinationPath, wantDestination, 0o600); err != nil {
		t.Fatal(err)
	}

	txn, err := beginTranscodePathTxn(sourcePath, destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer txn.close()
	limits := DefaultTranscodeLimits()
	staged, err := stageTranscodeFile(context.Background(), txn, stageTestPart10File(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.cleanup(txn.parent)
	assertStageDestinationBytes(t, destinationPath, wantDestination)
	if err := validateStagedTranscode(context.Background(), staged, limits); err != nil {
		t.Fatal(err)
	}
	assertStageDestinationBytes(t, destinationPath, wantDestination)
	if err := precommitTranscode(txn, staged); err != nil {
		t.Fatal(err)
	}
	assertStageDestinationBytes(t, destinationPath, wantDestination)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	published, err := publishStagedTranscode(ctx, txn, staged)
	if !errors.Is(err, context.Canceled) || published {
		t.Fatalf("publishStagedTranscode() = (%v, %v), want (false, context.Canceled)", published, err)
	}
	assertStageDestinationBytes(t, destinationPath, wantDestination)
	staged.cleanup(txn.parent)
	if _, err := os.Stat(filepath.Join(dir, staged.name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file remains after cleanup: %v", err)
	}
}

func TestPrecommitTranscodeRejectsChangedSnapshots(t *testing.T) {
	if !transcodePathSupported() {
		t.Skip("atomic transcode publication is unavailable on this platform")
	}
	tests := []struct {
		name    string
		mutate  func(string, string) error
		wantErr error
	}{
		{
			name: "source changed",
			mutate: func(sourcePath, _ string) error {
				return os.WriteFile(sourcePath, []byte("source changed to a different size"), 0o600)
			},
			wantErr: ErrTranscodeSourceChanged,
		},
		{
			name: "destination changed",
			mutate: func(_, destinationPath string) error {
				return os.WriteFile(destinationPath, []byte("destination changed externally"), 0o600)
			},
			wantErr: ErrTranscodeDestinationUnsafe,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			sourcePath := filepath.Join(dir, "source.dcm")
			destinationPath := filepath.Join(dir, "destination.dcm")
			if err := os.WriteFile(sourcePath, []byte("stable source"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destinationPath, []byte("existing destination"), 0o600); err != nil {
				t.Fatal(err)
			}
			txn, err := beginTranscodePathTxn(sourcePath, destinationPath)
			if err != nil {
				t.Fatal(err)
			}
			defer txn.close()
			staged, err := stageTranscodeFile(context.Background(), txn, stageTestPart10File(), DefaultTranscodeLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer staged.cleanup(txn.parent)
			if err := test.mutate(sourcePath, destinationPath); err != nil {
				t.Fatal(err)
			}

			if err := precommitTranscode(txn, staged); !errors.Is(err, test.wantErr) {
				t.Fatalf("precommitTranscode() error = %v, want %v", err, test.wantErr)
			}
			if _, err := os.Stat(filepath.Join(dir, staged.name)); err != nil {
				t.Fatalf("precommit failure removed staged file before owner cleanup: %v", err)
			}
		})
	}
}

func stageTestPart10File() *object.File {
	dataset := transcodeNativeObject([]byte{1, 2, 3, 4})
	dataset.Put(core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI},
		Value:  core.StringValue{"1.2.840.10008.5.1.4.1.1.7"},
	})
	return &object.File{
		Preamble:       make([]byte, 128),
		Meta:           fileMetaWithTransferSyntax(nil, transfer.ExplicitVRLittleEndian),
		Dataset:        dataset,
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
}

func assertStageDestinationBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination = %q, want unchanged %q", got, want)
	}
}
