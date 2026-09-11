package codifycli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func inputFixture(t *testing.T) string {
	t.Helper()
	ds := object.FromElements([]core.Element{
		core.NewRawElement(core.NewTag(8, 0x16), core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.7")),
		core.NewRawElement(core.NewTag(8, 0x18), core.VRUI, []byte("1.2.3.4.5")),
		core.NewRawElement(core.NewTag(0x20, 0x0d), core.VRUI, []byte("1.2.3")),
		core.NewRawElement(core.NewTag(0x20, 0x0e), core.VRUI, []byte("1.2.3.4")),
		core.NewRawElement(core.NewTag(0x10, 0x10), core.VRPN, []byte("CLINICAL_CANARY")),
		core.NewRawElement(core.NewTag(0x7777, 0x10), core.VRLO, []byte("PRIVATE_CANARY")),
		core.NewRawElement(core.NewTag(0x7777, 0x1010), core.VRUN, []byte("OPAQUE_CANARY")),
		{Header: core.ElementHeader{Tag: core.NewTag(8, 0x1115), VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{core.NewRawElement(core.NewTag(0x10, 0x10), core.VRPN, []byte("NESTED_CANARY"))}}}}},
		core.NewRawElement(core.NewTag(0x7fe0, 0x10), core.VROB, []byte("PIXELS_CANARY")),
	}, std.Dictionary)
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, &object.File{Dataset: ds, TransferSyntax: transfer.ExplicitVRLittleEndian}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "INPUT_PATH_CANARY.dcm")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunStructuralFaithfulAndDeidPolicy(t *testing.T) {
	input := inputFixture(t)
	for _, tc := range []struct {
		name     string
		args     []string
		faithful bool
	}{{"default", nil, false}, {"faithful", []string{"-faithful"}, true}, {"deid", []string{"-deid-basic"}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			args := append(append([]string{}, tc.args...), input)
			code := Run(context.Background(), args, &out, &diag)
			if code != 0 {
				t.Fatalf("code %d: %s", code, diag.Bytes())
			}
			if tc.faithful {
				if !strings.Contains(out.String(), "CLINICAL_CANARY") || !strings.Contains(diag.String(), "may contain PHI") {
					t.Fatal("faithful policy missing")
				}
				if strings.Contains(out.String(), "PIXELS_CANARY") {
					t.Fatal("binary included implicitly")
				}
			} else if strings.Contains(out.String(), "CANARY") || strings.Contains(diag.String(), "CANARY") {
				t.Fatal("default/deid disclosure")
			}
			if strings.Contains(diag.String(), "INPUT_PATH_CANARY") {
				t.Fatal("path disclosed")
			}
			if tc.name == "deid" && !strings.Contains(diag.String(), "residual risks") {
				t.Fatal("deid report missing")
			}
		})
	}
}

func TestRunNeverOverwritesAndErrorsDoNotPublish(t *testing.T) {
	input := inputFixture(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "repro.go")
	var out, diag bytes.Buffer
	args := []string{"-output", output, input}
	if code := Run(context.Background(), args, &out, &diag); code != 0 {
		t.Fatalf("publish: %d %s", code, diag.Bytes())
	}
	original, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diag.Reset()
	if code := Run(context.Background(), []string{"-faithful", "-inline-binary", "-output", output, input}, &out, &diag); code == 0 {
		t.Fatal("existing output replaced")
	}
	after, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("destination changed")
	}
	for _, args := range [][]string{{"-function", "bad;CANARY", "-output", filepath.Join(dir, "invalid.go"), input}, {"-max-output-bytes", "50", "-output", filepath.Join(dir, "small.go"), input}, {"-faithful", "-inline-binary", "-max-binary-bytes", "1", "-output", filepath.Join(dir, "large.go"), input}} {
		out.Reset()
		diag.Reset()
		if Run(context.Background(), args, &out, &diag) == 0 {
			t.Fatal("bad input accepted")
		}
		if strings.Contains(diag.String(), "CANARY") {
			t.Fatal("error disclosed values")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed generation left artifacts: %v %v", entries, err)
	}
}

func TestRunZeroLimitsAreFiniteAndCancellationDoesNotPublish(t *testing.T) {
	input := inputFixture(t)
	var out, diag bytes.Buffer
	if code := Run(context.Background(), []string{"-max-elements", "0", "-max-depth", "0", "-max-value-bytes", "0", input}, &out, &diag); code != 0 {
		t.Fatalf("zero defaults: %d %s", code, diag.Bytes())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "canceled.go")
	if code := Run(ctx, []string{"-output", path, input}, &out, &diag); code == 0 {
		t.Fatal("canceled run succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled run published")
	}
	if code := Run(context.Background(), []string{"-help"}, &out, &diag); code != 0 || !strings.Contains(diag.String(), "Usage:") {
		t.Fatal("help")
	}
}
