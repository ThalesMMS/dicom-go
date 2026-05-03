package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSeriesExampleZipWhenAvailable(t *testing.T) {
	path := filepath.Join("..", "..", "dicom_series_example.zip")
	if _, err := os.Stat(path); err != nil {
		t.Skip("dicom_series_example.zip not available")
	}

	series, err := loadSeries(path)
	if err != nil {
		t.Fatalf("loadSeries() error = %v", err)
	}
	if got, want := len(series.Frames), 174; got != want {
		t.Fatalf("len(series.Frames) = %d, want %d", got, want)
	}
	first := series.Frames[0]
	if first.Metadata.Rows != 512 || first.Metadata.Columns != 512 {
		t.Fatalf("first frame size = %dx%d, want 512x512", first.Metadata.Columns, first.Metadata.Rows)
	}
	if first.DecodeErr != nil {
		t.Fatalf("first frame decode error = %v", first.DecodeErr)
	}
	if _, err := renderFrame(first, first.DefaultWindow); err != nil {
		t.Fatalf("render first frame: %v", err)
	}
}
