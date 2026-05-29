package dcmtime_test

import (
	"errors"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"testing"
	"time"
)

func TestParseDate(t *testing.T) {
	testCases := []struct {
		Name              string
		DAValue           string
		ExpectedString    string
		Expected          time.Time
		ExpectedPrecision dcmtime.PrecisionLevel
	}{
		{
			Name:              "PrecisionFull",
			DAValue:           "20200304",
			ExpectedString:    "2020-03-04",
			Expected:          time.Date(2020, 3, 4, 0, 0, 0, 0, time.UTC),
			ExpectedPrecision: dcmtime.PrecisionFull,
		},
		{
			Name:              "PrecisionMonth",
			DAValue:           "202003",
			ExpectedString:    "2020-03",
			Expected:          time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC),
			ExpectedPrecision: dcmtime.PrecisionMonth,
		},
		{
			Name:              "PrecisionYear",
			DAValue:           "2020",
			ExpectedString:    "2020",
			Expected:          time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
			ExpectedPrecision: dcmtime.PrecisionYear,
		},
		{
			Name:              "PrecisionFullNEMA",
			DAValue:           "2020.03.04",
			ExpectedString:    "2020-03-04",
			Expected:          time.Date(2020, 3, 4, 0, 0, 0, 0, time.UTC),
			ExpectedPrecision: dcmtime.PrecisionFull,
		},
		{
			Name:              "PrecisionMonthNEMA",
			DAValue:           "2020.03",
			ExpectedString:    "2020-03",
			Expected:          time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC),
			ExpectedPrecision: dcmtime.PrecisionMonth,
		},
		{
			Name:              "PrecisionYearNEMA",
			DAValue:           "2020",
			ExpectedString:    "2020",
			Expected:          time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
			ExpectedPrecision: dcmtime.PrecisionYear,
		},
	}

	for _, tc := range testCases {
		// Run a master test on the test case
		t.Run(tc.Name, func(t *testing.T) {

			// We'll store the parsed object here for subsequent subtests
			var parsed dcmtime.Date

			t.Run("ParseDate", func(t *testing.T) {
				var err error
				parsed, err = dcmtime.ParseDate(tc.DAValue)
				if err != nil {
					t.Fatal("parse err:", err)
				}

				if !tc.Expected.Equal(parsed.Time) {
					t.Errorf(
						"parsed time (%v) != expected (%v) from source DA '%v'",
						parsed.Time,
						tc.Expected,
						tc.DAValue,
					)

				}

				if parsed.Precision != tc.ExpectedPrecision {
					t.Errorf(
						"precision: expected %v, got %v from source DA '%v'",
						tc.ExpectedPrecision.String(),
						parsed.Precision.String(),
						tc.DAValue,
					)
				}
			})

			t.Run("String()", func(t *testing.T) {
				stringVal := parsed.String()
				if stringVal != tc.ExpectedString {
					t.Fatalf(
						"got String() value '%v', expected '%v'",
						stringVal,
						tc.ExpectedString,
					)
				}
			})

			t.Run("DCM()", func(t *testing.T) {
				dcmVal := parsed.DCM()
				if dcmVal != tc.DAValue {
					t.Fatalf(
						"got DCM() value '%v', expected '%v'", dcmVal, tc.DAValue,
					)
				}
			})
		})

	}
}

func TestParseDateErr(t *testing.T) {
	testCases := []struct {
		Name     string
		BadValue string
	}{
		{
			Name:     "TooManyDigits",
			BadValue: "101002034",
		},
		{
			Name:     "MissingDigit_Days",
			BadValue: "1010023",
		},
		{
			Name:     "MissingDigit_Months",
			BadValue: "10102",
		},
		{
			Name:     "MissingDigit_Year",
			BadValue: "101",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := dcmtime.ParseDate(tc.BadValue)
			if !errors.Is(err, dcmtime.ErrParseDA) {
				t.Errorf("expected ErrParseDA, got %v", err)
			}
		})
	}
}

func TestParseDAAndTMExposeValidatedComponents(t *testing.T) {
	testCases := []struct {
		name          string
		input         string
		parse         func(string) (time.Time, error)
		wantError     bool
		wantErrorText string
		assert        func(*testing.T, time.Time)
	}{
		{
			name:  "valid DA",
			input: " 20260712\x00 ",
			parse: dcmtime.ParseDA,
			assert: func(t *testing.T, got time.Time) {
				if got.Year() != 2026 || got.Month() != time.July || got.Day() != 12 {
					t.Fatalf("ParseDA = %v", got)
				}
			},
		},
		{
			name:      "partial DA",
			input:     "202607",
			parse:     dcmtime.ParseDA,
			wantError: true,
		},
		{
			name:          "legacy DA",
			input:         "2020.12.10",
			parse:         dcmtime.ParseDA,
			wantError:     true,
			wantErrorText: "date must be a full-precision DICOM DA value",
		},
		{
			name:  "fractional TM",
			input: "153045.25",
			parse: dcmtime.ParseTM,
			assert: func(t *testing.T, got time.Time) {
				if got.Hour() != 15 || got.Minute() != 30 || got.Second() != 45 || got.Nanosecond() != 250000000 {
					t.Fatalf("ParseTM = %v", got)
				}
			},
		},
		{
			name:      "invalid TM",
			input:     "later",
			parse:     dcmtime.ParseTM,
			wantError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.parse(tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parser accepted %q", tc.input)
				}
				if tc.wantErrorText != "" && err.Error() != tc.wantErrorText {
					t.Fatalf("error = %q, want %q", err, tc.wantErrorText)
				}
				return
			}
			if err != nil {
				t.Fatalf("parser returned error: %v", err)
			}
			if tc.assert != nil {
				tc.assert(t, got)
			}
		})
	}
}

// TestDate_DCMTrimming tests that dates are trimmed to the correct precision on DCM
// out
func TestDate_DCMTrimming(t *testing.T) {
	testCases := []struct {
		Name      string
		Time      time.Time
		Precision dcmtime.PrecisionLevel
		Expected  string
	}{
		{
			Name:      "PrecisionFull",
			Time:      time.Date(1010, 2, 3, 5, 6, 7, 8, time.UTC),
			Precision: dcmtime.PrecisionFull,
			Expected:  "10100203",
		},
		{
			Name:      "PrecisionDay",
			Time:      time.Date(1010, 2, 3, 5, 6, 7, 8, time.UTC),
			Precision: dcmtime.PrecisionDay,
			Expected:  "10100203",
		},
		{
			Name:      "PrecisionMonth",
			Time:      time.Date(1010, 2, 3, 5, 6, 7, 8, time.UTC),
			Precision: dcmtime.PrecisionMonth,
			Expected:  "101002",
		},
		{
			Name:      "PrecisionYear",
			Time:      time.Date(1010, 2, 3, 5, 6, 7, 8, time.UTC),
			Precision: dcmtime.PrecisionYear,
			Expected:  "1010",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			da := dcmtime.Date{
				Time:      tc.Time,
				Precision: tc.Precision,
			}

			if da.DCM() != tc.Expected {
				t.Errorf(
					"Date.DCM(): expected '%v', got '%v'", tc.Expected, da.DCM(),
				)
			}
		})
	}
}

// TestDate_SaneDefaults tests that instantiating a new Date object with just the Time
// field specified yields a reasonable result.
func TestDate_SaneDefaults(t *testing.T) {
	newValue := dcmtime.Date{
		Time: time.Date(2021, 03, 16, 0, 0, 0, 0, time.FixedZone("", 0)),
	}

	dcmVal := newValue.DCM()
	expected := "20210316"
	if dcmVal != expected {
		t.Errorf("DCM(): expected '%v', but got '%v'", expected, dcmVal)
	}
}

func TestDateFormattingUsesSingleAllocation(t *testing.T) {
	date := time.Date(2026, 6, 12, 13, 14, 15, 0, time.UTC)
	cases := []struct {
		name string
		date dcmtime.Date
		dcm  string
		text string
	}{
		{
			name: "year only",
			date: dcmtime.Date{Time: date, Precision: dcmtime.PrecisionYear},
			dcm:  "2026",
			text: "2026",
		},
		{
			name: "full date",
			date: dcmtime.Date{Time: date, Precision: dcmtime.PrecisionFull},
			dcm:  "20260612",
			text: "2026-06-12",
		},
		{
			name: "nema full date",
			date: dcmtime.Date{Time: date, Precision: dcmtime.PrecisionFull, IsNEMA: true},
			dcm:  "2026.06.12",
			text: "2026-06-12",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/DCM", func(t *testing.T) {
			assertFormatAllocs(t, 1, tc.date.DCM, tc.dcm)
		})
		t.Run(tc.name+"/String", func(t *testing.T) {
			assertFormatAllocs(t, 1, tc.date.String, tc.text)
		})
	}
}

func BenchmarkDateFormatting(b *testing.B) {
	date := time.Date(2026, 6, 12, 13, 14, 15, 0, time.UTC)
	cases := []struct {
		name string
		date dcmtime.Date
		fn   func(dcmtime.Date) string
	}{
		{"DCM/year", dcmtime.Date{Time: date, Precision: dcmtime.PrecisionYear}, dcmtime.Date.DCM},
		{"DCM/full", dcmtime.Date{Time: date, Precision: dcmtime.PrecisionFull}, dcmtime.Date.DCM},
		{"DCM/nema", dcmtime.Date{Time: date, Precision: dcmtime.PrecisionFull, IsNEMA: true}, dcmtime.Date.DCM},
		{"String/full", dcmtime.Date{Time: date, Precision: dcmtime.PrecisionFull}, dcmtime.Date.String},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = tc.fn(tc.date)
			}
		})
	}
}

func assertFormatAllocs(t *testing.T, maxAllocs float64, format func() string, want string) {
	t.Helper()
	allocs := testing.AllocsPerRun(1000, func() {
		if got := format(); got != want {
			panic(got)
		}
	})
	if allocs > maxAllocs {
		t.Fatalf("allocations/run = %.1f, want <= %.1f", allocs, maxAllocs)
	}
}
