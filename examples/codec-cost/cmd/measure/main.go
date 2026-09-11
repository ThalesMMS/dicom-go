package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/examples/codec-cost"
)

var absPath = filepath.Abs

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("codec-frame-cost", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	out := flags.String("out", "", "JSON report path (stdout when empty)")
	corpus := flags.String("corpus", "", "codecfull corpus root (default: pixeldata/codecfixture/testdata/codecfull)")
	iterations := flags.Int("iterations", 8, "timed warm iterations per cohort/backend")
	warmup := flags.Int("warmup", 1, "discarded warmup iterations before warm samples")
	djxl := flags.String("djxl", os.Getenv("DICOM_GO_DJXL"), "djxl executable; empty uses PATH")
	cjxl := flags.String("cjxl", os.Getenv("DICOM_GO_CJXL"), "cjxl executable for the large synthetic cohort")
	helper := flags.String("helper", "", "prebuilt libjxl helper executable")
	compileHelper := flags.Bool("compile-helper", false, "compile helperc/jxl_helper.c with pkg-config libjxl")
	require := flags.String("require", "", "comma-separated backends/cohorts that must not be skipped")
	cpuProfile := flags.String("cpuprofile", "", "local CPU pprof file")
	memProfile := flags.String("memprofile", "", "local heap pprof file")
	tracePath := flags.String("trace", "", "local runtime/trace file")
	timeout := flags.Duration("timeout", 15*time.Minute, "campaign timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	var required []string
	if *require != "" {
		required = strings.Split(*require, ",")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, err := codeccost.RunCampaign(ctx, codeccost.CampaignOptions{
		CorpusRoot:    *corpus,
		Iterations:    *iterations,
		Warmup:        *warmup,
		Djxl:          *djxl,
		Cjxl:          *cjxl,
		HelperPath:    *helper,
		CompileHelper: *compileHelper,
		Require:       required,
		Profiles: codeccost.ProfileFiles{
			CPU:   *cpuProfile,
			Heap:  *memProfile,
			Trace: *tracePath,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := *out
	if path != "" {
		resolved, err := absPath(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		path = resolved
	}
	if err := codeccost.WriteReport(path, report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "decision=%s %s\nskips=%d frames=%d\n", report.Decision.Choice, report.Decision.Reason, len(report.Skips), len(report.Frames))
	return 0
}
