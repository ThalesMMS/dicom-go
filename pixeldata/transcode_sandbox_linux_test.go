//go:build linux

package pixeldata

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestTranscodeSandboxRootReadDenied(t *testing.T) {
	if os.Getenv("DICOM_GO_SANDBOX_CHILD") == "1" {
		if os.Geteuid() == 0 {
			t.Fatal("sandbox helper must be unprivileged")
		}
		for _, path := range []string{".", "source.dcm"} {
			file, err := os.Open(path)
			if err != nil {
				t.Fatalf("ordinary open %s must work: %v", path, err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile("write-probe", []byte("probe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove("write-probe"); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open("/")
		if !errors.Is(err, os.ErrPermission) {
			if file != nil {
				_ = file.Close()
			}
			t.Fatalf("ordinary root open error=%v, want permission denial", err)
		}
		file, err = openTranscodeFileNoFollow("source.dcm")
		if err != nil {
			t.Fatalf("descriptor-root fallback failed: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if file, err := openTranscodeDirectoryNoFollow("/"); !errors.Is(err, os.ErrPermission) {
			if file != nil {
				_ = file.Close()
			}
			t.Fatalf("final directory must remain readable for fsync: %v", err)
		}
		for _, link := range []struct{ name, target, path string }{
			{"source-link", "source.dcm", "source-link"},
			{"ancestor-link", ".", "ancestor-link/source.dcm"},
		} {
			if err := os.Symlink(link.target, link.name); err != nil {
				t.Fatal(err)
			}
			file, err := openTranscodeFileNoFollow(link.path)
			if file != nil {
				_ = file.Close()
			}
			if err == nil {
				t.Fatalf("fallback accepted symlink: %s", link.path)
			}
			if err := os.Remove(link.name); err != nil {
				t.Fatal(err)
			}
		}
		_, err = TranscodePath(context.Background(), "source.dcm", "destination.dcm", transfer.Syntax{UID: "1.2.3"}, TranscodeOptions{})
		if err == nil {
			t.Fatal("invalid conversion unexpectedly succeeded")
		}
		assertStageDestinationBytes(t, "destination.dcm", []byte("KEEP"))
		_, err = TranscodePath(context.Background(), "source.dcm", "destination.dcm", transfer.ImplicitVRLittleEndian, TranscodeOptions{})
		if err != nil {
			t.Fatalf("TranscodePath fallback: %v", err)
		}
		t.Setenv("TMPDIR", "/allowed")
		t.Run("fallback directory replacement", TestTranscodePrecommitRejectsDirectoryReplacement)
		t.Log("root 0711 denied O_RDONLY for uid 65534; O_PATH anchor allowed safe transcode; symlinks and failed conversion remained rejected")
		return
	}
	if os.Getenv("DICOM_GO_TEST_CHROOT") != "1" || os.Geteuid() != 0 {
		t.Skip("opt-in Linux chroot reproduction requires DICOM_GO_TEST_CHROOT=1, root, and a static test executable")
	}
	jail := t.TempDir()
	allowed := filepath.Join(jail, "allowed")
	if err := os.Mkdir(allowed, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(allowed, 0o777); err != nil {
		t.Fatal(err)
	}
	var sourceBytes bytes.Buffer
	if err := object.WriteFile(&sourceBytes, stageTestPart10File()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowed, "source.dcm"), sourceBytes.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowed, "destination.dcm"), []byte("KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	helper, err := os.OpenFile(filepath.Join(jail, "test-helper"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(helper, source)
	closeErr := helper.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy helper: %v, close: %v", copyErr, closeErr)
	}
	if err := os.Chmod(jail, 0o711); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/test-helper", "-test.run=^TestTranscodeSandboxRootReadDenied$", "-test.v")
	cmd.Dir = "/allowed"
	cmd.Env = append(os.Environ(), "DICOM_GO_SANDBOX_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Chroot: jail, Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox helper: %v\n%s", err, output)
	}
	t.Log(string(output))
	assertStageDestinationBytes(t, filepath.Join(allowed, "source.dcm"), sourceBytes.Bytes())
	result, err := os.Open(filepath.Join(allowed, "destination.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	outputFile, err := object.ReadFile(result)
	if err != nil || outputFile.TransferSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("output did not roundtrip as target syntax: %v", err)
	}
	entries, err := os.ReadDir(allowed)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected files after refusal: %v, %v", entries, err)
	}
}

func TestTranscodeNoFollowRejectsSymlinkAncestors(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "canonical")
	if err := os.Mkdir(canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "source"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	file, err := openTranscodeFileNoFollow(filepath.Join(alias, "source"))
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("accepted symlink ancestor in descriptor traversal")
	}
	// Explicit canonical paths outside the working directory remain supported.
	file, err = openTranscodeFileNoFollow(filepath.Join(canonical, "source"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTranscodePrecommitRejectsDirectoryReplacement(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		name := "directory"
		if symlink {
			name = "symlink"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			ancestor := filepath.Join(root, "ancestor")
			parent := filepath.Join(ancestor, "output")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(parent, "destination")
			if err := os.WriteFile(destination, []byte("KEEP"), 0o600); err != nil {
				t.Fatal(err)
			}
			txn, err := beginTranscodePathTxn(source, destination)
			if err != nil {
				t.Fatal(err)
			}
			defer txn.close()
			staged, err := stageTranscodeFile(context.Background(), txn, stageTestPart10File(), DefaultTranscodeLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer staged.cleanup(txn.parent)
			moved := filepath.Join(root, "moved")
			if err := os.Rename(ancestor, moved); err != nil {
				t.Fatal(err)
			}
			replacement := ancestor
			if symlink {
				replacement = filepath.Join(root, "outside")
			}
			if err := os.MkdirAll(filepath.Join(replacement, "output"), 0o700); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(replacement, ancestor); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(destination, []byte("OUTSIDE"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := precommitTranscode(txn, staged); !errors.Is(err, ErrTranscodeDestinationUnsafe) {
				t.Fatalf("precommit=%v, want unsafe destination", err)
			}
			staged.cleanup(txn.parent)
			assertStageDestinationBytes(t, filepath.Join(moved, "output", "destination"), []byte("KEEP"))
			assertStageDestinationBytes(t, destination, []byte("OUTSIDE"))
			if _, err := os.Stat(filepath.Join(moved, "output", staged.name)); !os.IsNotExist(err) {
				t.Fatalf("staged file remains in moved directory: %v", err)
			}
		})
	}
}
