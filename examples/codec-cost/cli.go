package codeccost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// CLIBackend mirrors the production temp-file CLI path with Start/Wait split.
type CLIBackend struct {
	Name       string
	Executable string
	Timeout    time.Duration
	Args       func(inputPath, outputPath string, metadata pixeldata.Metadata) []string
	OutputExt  func(pixeldata.Metadata) string
}

// Decode writes a temporary codestream, launches the decoder, and converts PNM.
func (b CLIBackend) Decode(ctx context.Context, req DecodeRequest) (FrameResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := FrameResult{Codec: req.Codec, Backend: b.Name, Cohort: req.Cohort, Mode: req.Mode}
	clock := NewClock(nil)
	clock.Start()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	if err := ctx.Err(); err != nil {
		clock.Step(StagePreflight)
		clock.Step(StageAdmission)
		result.Cancelled = true
		result.StageRecord = clock.Record()
		return result, err
	}

	executable := b.Executable
	if executable == "" {
		return result, fmt.Errorf("codeccost: CLI executable is required")
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	clock.Step(StagePreflight)

	if req.WaitAdmission != nil {
		if err := req.WaitAdmission(ctx); err != nil {
			clock.Step(StageAdmission)
			result.Cancelled = true
			result.StageRecord = clock.Record()
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		clock.Step(StageAdmission)
		result.Cancelled = true
		result.StageRecord = clock.Record()
		return result, err
	}
	clock.Step(StageAdmission)

	dir, err := os.MkdirTemp("", "dicom-go-codec-cost-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)

	ext := ".pgm"
	if b.OutputExt != nil {
		ext = b.OutputExt(req.Metadata)
	}
	inputPath := filepath.Join(dir, "frame.bin")
	outputPath := filepath.Join(dir, "frame"+ext)
	if err := os.WriteFile(inputPath, req.Fragment, 0o600); err != nil {
		clock.Step(StagePrepare)
		return result, err
	}
	result.TempFilesCreated++
	result.IOBytes += int64(len(req.Fragment))
	result.IOOps++
	clock.Step(StagePrepare)

	if err := ctx.Err(); err != nil {
		result.Cancelled = true
		result.StageRecord = clock.Record()
		return result, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	args := []string{inputPath, outputPath, "--quiet"}
	if b.Args != nil {
		args = b.Args(inputPath, outputPath, req.Metadata)
	}
	cmd := exec.CommandContext(runCtx, executable, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		clock.Step(StageLaunch)
		return result, err
	}
	result.Launches = 1
	clock.Step(StageLaunch)

	rss, waitErr := waitAndSampleRSS(cmd)
	clock.Step(StageExecute)
	clock.Annotate(WaitIncludesChildInitNote())
	result.SubprocessRSSBytes = rss
	if waitErr != nil {
		if runCtx.Err() != nil {
			result.Cancelled = true
			result.StageRecord = clock.Record()
			return result, runCtx.Err()
		}
		return result, waitErr
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		clock.Step(StageConvert)
		return result, err
	}
	result.TempFilesCreated++
	if info.Size() > ppmOutputSizeLimit(req.Metadata) {
		clock.Step(StageConvert)
		return result, fmt.Errorf("codeccost: output bytes=%d exceed limit", info.Size())
	}
	pnm, err := os.ReadFile(outputPath)
	if err != nil {
		clock.Step(StageConvert)
		return result, err
	}
	result.IOBytes += int64(len(pnm))
	result.IOOps++
	pixels, err := ppmToFrameBytes(pnm, req.Metadata)
	if err != nil {
		clock.Step(StageConvert)
		return result, err
	}
	clock.Step(StageConvert)

	result.Pixels = pixels
	sum := sha256.Sum256(pixels)
	result.PixelSHA256 = hex.EncodeToString(sum[:])
	clock.Step(StageDeliver)
	result.StageRecord = clock.Record()
	result.Elapsed = result.StageRecord.FirstFrame
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	if memAfter.TotalAlloc > memBefore.TotalAlloc {
		result.AllocBytes = memAfter.TotalAlloc - memBefore.TotalAlloc
	}
	result.HeapInuseBytes = memAfter.HeapInuse
	result.ProcessRSSBytes, _ = peekProcessRSS(os.Getpid())
	return result, nil
}
