package dcmtime_test

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/dcmtime"
)

func TestParseDateRejectsOutOfRangeComponents(t *testing.T) {
	for _, value := range []string{
		"20210001", "20211301", "20210100", "20210132", "20210431",
		"20230229", "2021.00.01", "2021.04.31", "20211385",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := dcmtime.ParseDate(value); !errors.Is(err, dcmtime.ErrParseDA) {
				t.Fatalf("ParseDate(%q) error = %v, want ErrParseDA", value, err)
			}
		})
	}
	for _, value := range []string{"2024", "202402", "20240229", "2024.02.29"} {
		if _, err := dcmtime.ParseDate(value); err != nil {
			t.Fatalf("ParseDate(%q) error = %v", value, err)
		}
	}
}

func TestParseTimeRejectsOutOfRangeComponents(t *testing.T) {
	for _, value := range []string{
		"24", "2400", "1260", "125961", "256990", "1200.1",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := dcmtime.ParseTime(value); !errors.Is(err, dcmtime.ErrParseTM) {
				t.Fatalf("ParseTime(%q) error = %v, want ErrParseTM", value, err)
			}
		})
	}
	for _, value := range []string{"00", "2359", "235959.999999", "235960"} {
		if _, err := dcmtime.ParseTime(value); err != nil {
			t.Fatalf("ParseTime(%q) error = %v", value, err)
		}
	}
}

func TestParseDatetimeRejectsOutOfRangeComponentsAndOffsets(t *testing.T) {
	for _, value := range []string{
		"20211301", "20210431", "2024010124", "202401011260", "20240101125961",
		"202401011200.1", "20240101120000+1401", "20240101120000+1460",
		"20240101120000-1201", "20240101120000-1300", "20240101120000-0000",
		"20211301-9959",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := dcmtime.ParseDatetime(value); !errors.Is(err, dcmtime.ErrParseDT) {
				t.Fatalf("ParseDatetime(%q) error = %v, want ErrParseDT", value, err)
			}
		})
	}
	for _, value := range []string{
		"20240229", "20240101235960", "20240101120000+1400",
		"20240101120000-1200", "20240101120000+1359", "20240101120000-1159", "2007-0500",
	} {
		if _, err := dcmtime.ParseDatetime(value); err != nil {
			t.Fatalf("ParseDatetime(%q) error = %v", value, err)
		}
	}
}
