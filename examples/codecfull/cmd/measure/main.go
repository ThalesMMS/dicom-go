//go:build codecfull

// Command measure records reproducible codecfull decode latency percentiles and
// sampled peak process-tree working set for representative non-PHI studies.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	jpeg2000 "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpeg2000"
	jpegls "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegls"
	jpegxl "github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegxl"
	codecfull "github.com/ThalesMMS/dicom-go/examples/codecfull"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const heapSampleInterval = 100 * time.Microsecond

type report struct {
	SchemaVersion int               `json:"schemaVersion"`
	Profile       string            `json:"profile"`
	RecordedAt    string            `json:"recordedAt"`
	Environment   environment       `json:"environment"`
	Dependencies  map[string]string `json:"dependencies"`
	Memory        memoryMeasurement `json:"memoryMeasurement"`
	Studies       []studyResult     `json:"studies"`
}

type environment struct {
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GoVersion  string `json:"goVersion"`
	LogicalCPU int    `json:"logicalCPU"`
}

type memoryMeasurement struct {
	Metric                       string `json:"metric"`
	Scope                        string `json:"scope"`
	SamplingIntervalMicroseconds int64  `json:"samplingIntervalMicroseconds"`
}

type studyResult struct {
	ID                  string  `json:"id"`
	Fixture             string  `json:"fixture"`
	SHA256              string  `json:"sha256"`
	Frames              int     `json:"frames"`
	Iterations          int     `json:"iterations"`
	DecodesPerIteration int     `json:"decodesPerIteration"`
	P50Microseconds     float64 `json:"p50Microseconds"`
	P95Microseconds     float64 `json:"p95Microseconds"`
	P99Microseconds     float64 `json:"p99Microseconds"`
	PeakMemoryBytes     uint64  `json:"peakMemoryBytes"`
	PeakHeapBytes       uint64  `json:"peakHeapBytes"`
	PeakTotalAllocBytes uint64  `json:"peakTotalAllocBytes"`
}

type preparedStudy struct {
	id       string
	path     string
	sha256   string
	file     *object.File
	pixel    pixeldata.PixelData
	metadata pixeldata.Metadata
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("codecfull-measure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	iterations := flags.Int("iterations", 25, "timed decode iterations per study")
	outputPath := flags.String("out", "", "optional JSON output path")
	corpusRoot := flags.String("corpus", filepath.Join("..", "..", "pixeldata", "codecfixture", "testdata", "codecfull"), "codecfull corpus root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *iterations < 5 {
		fmt.Fprintln(stderr, "iterations must be at least 5")
		return 2
	}
	if err := validateProcessTreeMemoryMeasurement(); err != nil {
		fmt.Fprintf(stderr, "codecfull peak-memory measurement unavailable: %v\n", err)
		return 1
	}

	registry, err := codecfull.NewRegistry()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	studies := []struct {
		id   string
		path string
	}{
		{"jpeg2000-mr-10-frame", filepath.Join(*corpusRoot, "pydicom", "emri_small_jpeg_2k_lossless.dcm")},
		{"jpegls-mr-10-frame", filepath.Join(*corpusRoot, "pydicom", "emri_small_jpeg_ls_lossless.dcm")},
		{"rle-mr-10-frame", filepath.Join(*corpusRoot, "pydicom", "emri_small_RLE.dcm")},
		{"htj2k-rgb-ultrasound", filepath.Join(*corpusRoot, "pydicom", "HTJ2KLossless_08_RGB.dcm")},
	}
	results := make([]studyResult, 0, len(studies))
	for _, study := range studies {
		prepared, err := prepareStudy(study.id, study.path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		result, err := measureStudy(registry, prepared, *iterations)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		results = append(results, result)
	}

	value := report{
		SchemaVersion: 1,
		Profile:       codecfull.BuildTag,
		RecordedAt:    time.Now().UTC().Format(time.RFC3339),
		Environment: environment{
			GOOS:       runtime.GOOS,
			GOARCH:     runtime.GOARCH,
			GoVersion:  runtime.Version(),
			LogicalCPU: runtime.NumCPU(),
		},
		Dependencies: map[string]string{
			"CharLS":   jpegls.QualifiedCharLSVersion,
			"libjxl":   jpegxl.QualifiedDjxlVersion,
			"OpenJPEG": jpeg2000.QualifiedOpenJPEGVersion,
			"OpenJPH":  jpeg2000.QualifiedOpenJPHVersion,
		},
		Memory: memoryMeasurement{
			Metric:                       "peak-process-tree-working-set-bytes",
			Scope:                        "measurement-process-and-descendants",
			SamplingIntervalMicroseconds: processTreeSampleInterval.Microseconds(),
		},
		Studies: results,
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encoded = append(encoded, '\n')
	if *outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := os.WriteFile(*outputPath, encoded, 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func prepareStudy(id, path string) (preparedStudy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return preparedStudy{}, fmt.Errorf("%s: %w", id, err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		return preparedStudy{}, fmt.Errorf("%s: read DICOM: %w", id, err)
	}
	pixel, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		return preparedStudy{}, fmt.Errorf("%s: extract pixels: %w", id, err)
	}
	metadata, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		return preparedStudy{}, fmt.Errorf("%s: extract metadata: %w", id, err)
	}
	sum := sha256.Sum256(data)
	return preparedStudy{
		id:       id,
		path:     path,
		sha256:   hex.EncodeToString(sum[:]),
		file:     file,
		pixel:    pixel,
		metadata: metadata,
	}, nil
}

func measureStudy(registry pixeldata.Registry, study preparedStudy, iterations int) (studyResult, error) {
	decodesPerIteration := 1
	switch study.id {
	case "jpegls-mr-10-frame":
		decodesPerIteration = 10
	case "rle-mr-10-frame":
		decodesPerIteration = 100
	}
	durations := make([]time.Duration, 0, iterations)
	for range iterations {
		started := time.Now()
		for range decodesPerIteration {
			if _, err := registry.DecodeFrames(study.file.TransferSyntax.UID, study.pixel, study.file.Dataset); err != nil {
				return studyResult{}, fmt.Errorf("%s timed decode: %w", study.id, err)
			}
		}
		durations = append(durations, time.Since(started)/time.Duration(decodesPerIteration))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	var peakMemory, peakHeap, peakTotalAlloc uint64
	for range 3 {
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		processTree, err := startProcessTreeSampler(os.Getpid(), processTreeSampleInterval)
		if err != nil {
			return studyResult{}, fmt.Errorf("%s memory sampler: %w", study.id, err)
		}
		var sampledPeak atomic.Uint64
		done := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(heapSampleInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					var current runtime.MemStats
					runtime.ReadMemStats(&current)
					if current.HeapInuse > before.HeapInuse {
						updatePeak(&sampledPeak, current.HeapInuse-before.HeapInuse)
					}
				case <-done:
					return
				}
			}
		}()
		_, decodeErr := registry.DecodeFrames(study.file.TransferSyntax.UID, study.pixel, study.file.Dataset)
		processTreePeak, sampleErr := processTree.Stop()
		if decodeErr != nil {
			close(done)
			<-stopped
			return studyResult{}, fmt.Errorf("%s memory decode: %w", study.id, decodeErr)
		}
		if sampleErr != nil {
			close(done)
			<-stopped
			return studyResult{}, fmt.Errorf("%s memory sampler: %w", study.id, sampleErr)
		}
		close(done)
		<-stopped
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		peakHeap = max(peakHeap, sampledPeak.Load())
		peakMemory = max(peakMemory, processTreePeak)
		if after.TotalAlloc > before.TotalAlloc {
			peakTotalAlloc = max(peakTotalAlloc, after.TotalAlloc-before.TotalAlloc)
		}
	}

	return studyResult{
		ID:                  study.id,
		Fixture:             filepath.ToSlash(filepath.Join("pydicom", filepath.Base(study.path))),
		SHA256:              study.sha256,
		Frames:              study.metadata.NumberOfFrames,
		Iterations:          iterations,
		DecodesPerIteration: decodesPerIteration,
		P50Microseconds:     durationMicroseconds(percentile(durations, 0.50)),
		P95Microseconds:     durationMicroseconds(percentile(durations, 0.95)),
		P99Microseconds:     durationMicroseconds(percentile(durations, 0.99)),
		PeakMemoryBytes:     peakMemory,
		PeakHeapBytes:       peakHeap,
		PeakTotalAllocBytes: peakTotalAlloc,
	}, nil
}

func durationMicroseconds(value time.Duration) float64 {
	return float64(value.Nanoseconds()) / 1000
}

func percentile(sorted []time.Duration, percentile float64) time.Duration {
	index := int(float64(len(sorted)-1)*percentile + 0.5)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func updatePeak(target *atomic.Uint64, value uint64) {
	for {
		current := target.Load()
		if value <= current || target.CompareAndSwap(current, value) {
			return
		}
	}
}
