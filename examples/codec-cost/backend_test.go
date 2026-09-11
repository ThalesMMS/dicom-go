package codeccost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestCLIDecodeSplitsLaunchFromExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake djxl script is POSIX-specific")
	}
	executable := writeFakeDjxl(t, `#!/bin/sh
out="$2"
sleep 0.05
printf 'P5\n2 1\n255\n\x00\xff' > "$out"
`)
	backend := CLIBackend{
		Name:       "jpegxl-cli",
		Executable: executable,
		Timeout:    8 * time.Second,
		Args: func(inputPath, outputPath string, meta pixeldata.Metadata) []string {
			return []string{inputPath, outputPath, "--quiet"}
		},
		OutputExt: djxlOutputExtension,
	}
	result, err := backend.Decode(context.Background(), DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeCold,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Launches != 1 {
		t.Fatalf("launches = %d, want 1", result.Launches)
	}
	if result.Stage(StageLaunch).Duration <= 0 {
		t.Fatal("launch duration was not measured separately from execute")
	}
	if result.Stage(StageExecute).Duration < 40*time.Millisecond {
		t.Fatalf("execute = %s, want sleep to land in execute not launch", result.Stage(StageExecute).Duration)
	}
	if result.Stage(StageExecute).Note != WaitIncludesChildInitNote() {
		t.Fatalf("execute note = %q", result.Stage(StageExecute).Note)
	}
	if got := hex.EncodeToString(sha256Bytes(result.Pixels)); got == "" {
		t.Fatal("missing pixel hash")
	}
	if result.IOBytes <= 0 || result.IOOps < 2 {
		t.Fatalf("I/O accounting missing: bytes=%d ops=%d", result.IOBytes, result.IOOps)
	}
	if result.TempFilesCreated != 2 {
		t.Fatalf("temp files = %d, want 2 after frame.bin and frame.pgm", result.TempFilesCreated)
	}
	if _, err := peekProcessRSS(os.Getpid()); err == nil && result.SubprocessRSSBytes == 0 {
		t.Fatal("subprocess RSS was not sampled while the child was alive")
	}
}

func TestCLICancelBeforeLaunchDoesNotStartProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake djxl script is POSIX-specific")
	}
	executable := writeFakeDjxl(t, `#!/bin/sh
printf 'started' > "$(dirname "$2")/launched"
printf 'P5\n2 1\n255\n\x00\xff' > "$2"
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := CLIBackend{
		Name:       "jpegxl-cli",
		Executable: executable,
		Timeout:    time.Second,
		Args: func(inputPath, outputPath string, meta pixeldata.Metadata) []string {
			return []string{inputPath, outputPath, "--quiet"}
		},
		OutputExt: djxlOutputExtension,
	}
	_, err := backend.Decode(ctx, DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeCancel,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode() error = %v, want context.Canceled", err)
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "dicom-go-codec-cost-*", "launched"))
	if len(matches) != 0 {
		t.Fatalf("process launched after cancel: %v", matches)
	}
}

func TestCLIDecodeCountsInputFileWhenDecoderOmitsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake djxl script is POSIX-specific")
	}
	executable := writeFakeDjxl(t, "#!/bin/sh\nexit 0\n")
	backend := CLIBackend{
		Name:       "jpegxl-cli",
		Executable: executable,
		Timeout:    time.Second,
		Args: func(inputPath, outputPath string, meta pixeldata.Metadata) []string {
			return []string{inputPath, outputPath, "--quiet"}
		},
		OutputExt: djxlOutputExtension,
	}
	result, err := backend.Decode(context.Background(), DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeCold,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	})
	if err == nil {
		t.Fatal("missing decoder output succeeded")
	}
	if result.TempFilesCreated != 1 {
		t.Fatalf("temp files = %d, want 1 after frame.bin only", result.TempFilesCreated)
	}
}

func TestPipeDecodeAvoidsTemporaryFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake djxl script is POSIX-specific")
	}
	executable := writeFakeDjxl(t, `#!/bin/sh
if [ "$1" != "-" ] || [ "$2" != "-" ]; then
  echo "expected stdin/stdout" >&2
  exit 2
fi
cat >/dev/null
printf 'P5\n2 1\n255\n\x00\xff'
`)
	backend := PipeBackend{
		Name:       "jpegxl-pipe",
		Executable: executable,
		Timeout:    8 * time.Second,
		OutputFormat: func(meta pixeldata.Metadata) string {
			if meta.SamplesPerPixel == 3 {
				return "ppm"
			}
			return "pgm"
		},
	}
	result, err := backend.Decode(context.Background(), DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeWarm,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TempFilesCreated != 0 {
		t.Fatalf("temp files = %d, want 0 for pipe prototype", result.TempFilesCreated)
	}
	if result.Launches != 1 {
		t.Fatalf("launches = %d, want 1", result.Launches)
	}
	if !bytes.Equal(result.Pixels, []byte{0, 255}) {
		t.Fatalf("pixels = %v, want [0 255]", result.Pixels)
	}
}

func TestPipeDecodeRejectsOversizedStdoutAndKillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake djxl script is POSIX-specific")
	}
	executable := writeFakeDjxl(t, `#!/bin/sh
cat >/dev/null
# Keep writing after the bound so Decode must kill the child.
dd if=/dev/zero bs=1024 count=64
sleep 30
`)
	backend := PipeBackend{
		Name:       "jpegxl-pipe",
		Executable: executable,
		Timeout:    8 * time.Second,
		OutputFormat: func(pixeldata.Metadata) string {
			return "pgm"
		},
	}
	started := time.Now()
	_, err := backend.Decode(context.Background(), DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeWarm,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Decode took %s; oversized stdout was buffered instead of killing the child", elapsed)
	}
	if err == nil {
		t.Fatal("oversized stdout accepted")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v, want output limit", err)
	}
}

func TestHelperPrototypeReusesProcessAcrossFrames(t *testing.T) {
	helper := StartTestHelper(t, func(fragment []byte, meta pixeldata.Metadata) ([]byte, error) {
		return []byte{0, 255}, nil
	})
	defer helper.Close()
	req := DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeWarm,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	}
	first, err := helper.Decode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := helper.Decode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.HelperPID == 0 || first.HelperPID != second.HelperPID {
		t.Fatalf("helper pid first=%d second=%d, want reused process", first.HelperPID, second.HelperPID)
	}
	if first.Launches != 0 || second.Launches != 0 {
		t.Fatalf("helper launches first=%d second=%d, want 0 per-frame process launches", first.Launches, second.Launches)
	}
}

func TestCachedBackendRelabelsCacheHitRequestOnMiss(t *testing.T) {
	inner := &countingBackend{pixels: []byte{1, 2, 3, 4}}
	cached := NewCachedBackend(inner)
	got, err := cached.Decode(context.Background(), DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray-hit",
		Mode:     ModeCacheHit,
		Fragment: []byte("uncached"),
		Metadata: gray8x2Metadata(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeCacheMiss {
		t.Fatalf("mode = %q, want cache-miss when a cache-hit request misses", got.Mode)
	}
	if got.CacheHit {
		t.Fatal("CacheHit true after a miss")
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
}

func TestCacheHitDoesNotLaunchDecoder(t *testing.T) {
	inner := &countingBackend{pixels: []byte{1, 2, 3, 4}}
	cached := NewCachedBackend(inner)
	req := DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeCacheMiss,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	}
	miss, err := cached.Decode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Mode = ModeCacheHit
	req.Cohort = "tiny-gray-hit"
	hit, err := cached.Decode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.CacheHit || miss.CacheHit {
		t.Fatalf("cache flags miss=%t hit=%t", miss.CacheHit, hit.CacheHit)
	}
	if hit.Launches != 0 {
		t.Fatalf("cache-hit launches = %d, want 0", hit.Launches)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 even when hit cohort name differs", inner.calls)
	}
}

func TestCachedBackendSeparatesSameFragmentDifferentMetadata(t *testing.T) {
	inner := &metadataPixelsBackend{}
	cached := NewCachedBackend(inner)
	fragment := []byte("shared-codestream")
	metaA := gray8x2Metadata()
	metaB := gray8x2Metadata()
	metaB.Rows = 3
	metaB.BitsAllocated = 16
	metaB.BitsStored = 16
	decode := func(mode string, meta pixeldata.Metadata) FrameResult {
		t.Helper()
		got, err := cached.Decode(context.Background(), DecodeRequest{
			Codec:    "jpegxl",
			Cohort:   "tiny-gray",
			Mode:     mode,
			Fragment: fragment,
			Metadata: meta,
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	missA := decode(ModeCacheMiss, metaA)
	missB := decode(ModeCacheMiss, metaB)
	hitA := decode(ModeCacheHit, metaA)
	if inner.calls != 2 {
		t.Fatalf("inner calls = %d, want 2 for distinct metadata", inner.calls)
	}
	if !hitA.CacheHit {
		t.Fatal("second request with metaA was not a cache hit")
	}
	if !bytes.Equal(hitA.Pixels, missA.Pixels) {
		t.Fatalf("cache hit pixels = %v, want metaA %v", hitA.Pixels, missA.Pixels)
	}
	if bytes.Equal(hitA.Pixels, missB.Pixels) {
		t.Fatal("cache reused pixels from a different geometry")
	}
}

func gray8x2Metadata() pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows:                      1,
		Columns:                   2,
		SamplesPerPixel:           1,
		BitsAllocated:             8,
		BitsStored:                8,
		HighBit:                   7,
		NumberOfFrames:            1,
		PhotometricInterpretation: "MONOCHROME2",
	}
}

func writeFakeDjxl(t testing.TB, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "djxl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

type countingBackend struct {
	pixels []byte
	calls  int
}

func (b *countingBackend) Decode(ctx context.Context, req DecodeRequest) (FrameResult, error) {
	b.calls++
	return FrameResult{Mode: req.Mode, Pixels: append([]byte(nil), b.pixels...), Launches: 1}, nil
}

type metadataPixelsBackend struct {
	calls int
}

func (b *metadataPixelsBackend) Decode(ctx context.Context, req DecodeRequest) (FrameResult, error) {
	b.calls++
	return FrameResult{
		Mode:     req.Mode,
		Pixels:   []byte{byte(req.Metadata.Rows), byte(req.Metadata.Columns), byte(req.Metadata.SamplesPerPixel), byte(req.Metadata.BitsAllocated)},
		Launches: 1,
	}, nil
}
