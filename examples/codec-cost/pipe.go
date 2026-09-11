package codeccost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// PipeBackend is the local-optimization prototype: djxl stdin/stdout, no temp files.
type PipeBackend struct {
	Name         string
	Executable   string
	Timeout      time.Duration
	OutputFormat func(pixeldata.Metadata) string
}

// Decode sends the codestream on stdin and reads PNM from stdout.
func (b PipeBackend) Decode(ctx context.Context, req DecodeRequest) (FrameResult, error) {
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
		result.Cancelled = true
		result.StageRecord = clock.Record()
		return result, err
	}
	if b.Executable == "" {
		return result, fmt.Errorf("codeccost: pipe executable is required")
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
	clock.Step(StageAdmission)

	format := "pgm"
	if b.OutputFormat != nil {
		format = b.OutputFormat(req.Metadata)
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, b.Executable, "-", "-", "--output_format", format, "--quiet")
	cmd.Stdin = bytes.NewReader(req.Fragment)
	stdout := &killingLimitWriter{limit: ppmOutputSizeLimit(req.Metadata), cmd: cmd}
	cmd.Stdout = stdout
	cmd.Stderr = nil
	result.IOBytes += int64(len(req.Fragment))
	result.IOOps++
	clock.Step(StagePrepare)

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
	if stdout.overLimit() {
		clock.Step(StageConvert)
		return result, fmt.Errorf("codeccost: output bytes exceed limit %d", stdout.limit)
	}
	if waitErr != nil {
		if runCtx.Err() != nil {
			result.Cancelled = true
			result.StageRecord = clock.Record()
			return result, runCtx.Err()
		}
		return result, waitErr
	}
	pixels, err := ppmToFrameBytes(stdout.bytes(), req.Metadata)
	if err != nil {
		clock.Step(StageConvert)
		return result, err
	}
	result.IOBytes += int64(stdout.len())
	result.IOOps++
	clock.Step(StageConvert)
	result.Pixels = pixels
	sum := sha256.Sum256(pixels)
	result.PixelSHA256 = hex.EncodeToString(sum[:])
	clock.Step(StageDeliver)
	result.StageRecord = clock.Record()
	result.TempFilesCreated = 0
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

type killingLimitWriter struct {
	buf      bytes.Buffer
	limit    int64
	cmd      *exec.Cmd
	mu       sync.Mutex
	exceeded bool
}

func (w *killingLimitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.exceeded {
		return 0, fmt.Errorf("codeccost: output bytes exceed limit %d", w.limit)
	}
	if int64(w.buf.Len())+int64(len(p)) > w.limit {
		w.exceeded = true
		if w.cmd != nil && w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
		}
		return 0, fmt.Errorf("codeccost: output bytes exceed limit %d", w.limit)
	}
	return w.buf.Write(p)
}

func (w *killingLimitWriter) overLimit() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exceeded
}

func (w *killingLimitWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Bytes()
}

func (w *killingLimitWriter) len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Len()
}
