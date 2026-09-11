//go:build jpeg2000_openjpeg || codecfull

package jpeg2000

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestOpenJPEGLosslessEncoderIndependentFFmpeg(t *testing.T) {
	if os.Getenv("DICOMGO_J2K_ENCODER_INTEROP") != "1" {
		t.Skip("set DICOMGO_J2K_ENCODER_INTEROP=1 for qualified runtime profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	encoder, err := NewOpenJPEGLosslessEncoder(ctx, OpenJPEGEncoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg := os.Getenv("DICOMGO_FFMPEG")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	command := func(t *testing.T, args ...string) *exec.Cmd {
		if distro := os.Getenv("DICOMGO_FFMPEG_WSL"); distro != "" && runtime.GOOS == "windows" {
			for i, arg := range args {
				if filepath.IsAbs(arg) {
					converted, err := exec.CommandContext(ctx, "wsl", "-d", distro, "--", "wslpath", "-u", filepath.ToSlash(arg)).Output()
					if err != nil {
						t.Fatal(err)
					}
					args[i] = strings.TrimSpace(string(converted))
				}
			}
			return exec.CommandContext(ctx, "wsl", append([]string{"-d", distro, "--", ffmpeg}, args...)...)
		}
		return exec.CommandContext(ctx, ffmpeg, args...)
	}
	version, err := command(t, "-version").CombinedOutput()
	if err != nil {
		t.Fatal("independent FFmpeg missing")
	}
	t.Log(strings.SplitN(string(version), "\n", 2)[0])
	var samples uint64
	frames := 0
	registry := pixeldata.NewMemoryEncoderRegistry()
	if err := registry.RegisterEncoder(transfer.JPEG2000LosslessOnly.UID, encoder); err != nil {
		t.Fatal(err)
	}
	for _, tc := range codecfixture.JPEG2000LosslessEncoderCases() {
		t.Run(tc.Name, func(t *testing.T) {
			source := tc.Object()
			sourceBefore, err := pixeldata.ExtractNativeFrames(source)
			if err != nil {
				t.Fatal(err)
			}
			uid, _ := source.GetUID(core.NewTag(8, 0x18))
			converted, report, err := pixeldata.TranscodeDataSet(ctx, source, tc.Syntax, transfer.JPEG2000LosslessOnly, pixeldata.TranscodeOptions{EncoderRegistry: registry})
			if err != nil || report.Lossy || report.Frames != 2 {
				t.Fatalf("transcode: %+v %v", report, err)
			}
			defer converted.Close()
			meta, err := pixeldata.ExtractMetadata(converted)
			gotUID, _ := converted.GetUID(core.NewTag(8, 0x18))
			if err != nil || meta.BitsAllocated != tc.Metadata.BitsAllocated || meta.BitsStored != tc.Metadata.BitsStored || meta.HighBit != tc.Metadata.HighBit || meta.PhotometricInterpretation != tc.Metadata.PhotometricInterpretation || meta.NumberOfFrames != 2 || meta.PlanarConfiguration != 0 || gotUID != uid {
				t.Fatal("metadata changed unexpectedly")
			}
			pixel, err := pixeldata.Extract(converted)
			if err != nil {
				t.Fatal(err)
			}
			payloads, err := framePayloads(pixel, 2)
			if err != nil || len(payloads) != 2 {
				t.Fatalf("encapsulation: %v", err)
			}
			sourceAfter, err := pixeldata.ExtractNativeFrames(source)
			if err != nil {
				t.Fatal(err)
			}
			for i := range sourceBefore.Data {
				if !bytes.Equal(sourceBefore.Data[i], sourceAfter.Data[i]) {
					t.Fatal("source changed")
				}
			}
			for frameIndex, native := range tc.ExpectedFrames {
				dir := t.TempDir()
				path := filepath.Join(dir, "frame.j2k")
				if err := os.WriteFile(path, payloads[frameIndex], 0600); err != nil {
					t.Fatal(err)
				}
				// Explicitly select the native decoder, never libopenjpeg. Its raw
				// display output is left-aligned in 8/16-bit allocation; normalize
				// that declared layout through the shared full-sample comparator.
				format := "gray"
				allocation := 8
				if tc.Metadata.BitsStored > 8 {
					format = "gray16le"
					allocation = 16
				}
				if tc.Metadata.SamplesPerPixel == 3 {
					format = "rgb24"
					if allocation == 16 {
						format = "rgb48le"
					}
				}
				cmd := command(t, "-v", "error", "-threads", "1", "-c:v", "jpeg2000", "-i", path, "-frames:v", "1", "-pix_fmt", format, "-f", "rawvideo", "pipe:1")
				var diag bytes.Buffer
				cmd.Stderr = &diag
				decoded, err := cmd.Output()
				if err != nil {
					t.Fatalf("FFmpeg decode: %v %s", err, diag.Bytes())
				}
				actual := codecfixture.SampleLayout{Rows: int(tc.Metadata.Rows), Columns: int(tc.Metadata.Columns), Components: int(tc.Metadata.SamplesPerPixel), BitsAllocated: allocation, BitsStored: int(tc.Metadata.BitsStored), HighBit: allocation - 1}
				reference := actual
				reference.BitsAllocated = int(tc.Metadata.BitsAllocated)
				reference.HighBit = int(tc.Metadata.HighBit)
				report, err := codecfixture.CompareSamples([][]byte{decoded}, actual, [][]byte{native}, reference, codecfixture.SamplePolicy{})
				if err != nil || !report.Qualified || report.Mismatches != 0 {
					t.Fatalf("full sample comparison: %+v %v (first bytes %x)", report, err, decoded[:min(len(decoded), 16)])
				}
				samples += report.Samples
				frames++
			}
		})
	}
	if frames != 42 || samples != 141974 {
		t.Fatalf("incomplete qualification: %d frames %d samples", frames, samples)
	}
	t.Logf("independent native FFmpeg verified %d frames, %d exact samples", frames, samples)
}
