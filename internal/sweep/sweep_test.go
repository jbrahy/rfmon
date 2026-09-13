package sweep

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestParseFixtureMediansAcrossPasses(t *testing.T) {
	f, err := os.Open("testdata/sweep_88_128.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	s, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hz) != 168 || len(s.DB) != 168 {
		t.Fatalf("got %d Hz / %d DB bins, want 168", len(s.Hz), len(s.DB))
	}
	if s.Hz[0] != 88_119_000 {
		t.Errorf("first bin Hz = %d, want 88119000", s.Hz[0])
	}
	for i := 1; i < len(s.Hz); i++ {
		if s.Hz[i] <= s.Hz[i-1] {
			t.Fatalf("Hz not strictly ascending at %d: %d <= %d", i, s.Hz[i], s.Hz[i-1])
		}
	}
	if !near(s.DB[0], -7.97) {
		t.Errorf("bin 0 median = %v, want -7.97", s.DB[0])
	}
	if !near(s.DB[1], -15.705) {
		t.Errorf("bin 1 median = %v, want -15.705", s.DB[1])
	}
}

func TestParseOddCountMedianAndNonFinite(t *testing.T) {
	in := strings.Join([]string{
		"d, t, 1000000, 2000000, 1000000.00, 10, -10.0",
		"d, t, 1000000, 2000000, 1000000.00, 10, -30.0",
		"d, t, 1000000, 2000000, 1000000.00, 10, -20.0",
		"d, t, 3000000, 4000000, 1000000.00, 10, -inf",
	}, "\n")
	s, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hz) != 2 {
		t.Fatalf("got %d bins, want 2", len(s.Hz))
	}
	if s.Hz[0] != 1_500_000 || !near(s.DB[0], -20) {
		t.Errorf("bin 0 = %d Hz %v dB, want 1500000 Hz -20 dB", s.Hz[0], s.DB[0])
	}
	if s.Hz[1] != 3_500_000 || !math.IsNaN(s.DB[1]) {
		t.Errorf("bin 1 = %d Hz %v dB, want 3500000 Hz NaN", s.Hz[1], s.DB[1])
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"short row":   "d, t, 1000000, 2000000",
		"bad hz_low":  "d, t, x, 2000000, 1000000.00, 10, -10.0",
		"bad width":   "d, t, 1000000, 2000000, x, 10, -10.0",
		"bad reading": "d, t, 1000000, 2000000, 1000000.00, 10, abc",
	}
	for name, in := range cases {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func TestDropSpurs(t *testing.T) {
	s := Spectrum{
		Hz: []int64{19_999_000, 20_000_000, 40_249_000, 40_251_000, 99_548_000, 99_786_000, 100_024_000},
		DB: []float64{-1, -2, -3, -4, -5, -6, -7},
	}
	DropSpurs(s)
	wantNaN := []bool{true, true, true, false, false, true, true}
	for i, w := range wantNaN {
		if math.IsNaN(s.DB[i]) != w {
			t.Errorf("Hz %d: NaN = %v, want %v", s.Hz[i], math.IsNaN(s.DB[i]), w)
		}
	}
}

func TestRunnerParsesToolOutput(t *testing.T) {
	r := Runner{Bin: "testdata/fake_sweep.sh", Passes: 2, Timeout: 5 * time.Second}
	s, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hz) != 168 {
		t.Errorf("got %d bins, want 168", len(s.Hz))
	}
}

func TestRunnerReportsStderrOnFailure(t *testing.T) {
	r := Runner{Bin: "testdata/fail_sweep.sh", Passes: 2, Timeout: 5 * time.Second}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HACKRF_ERROR_NOT_FOUND") {
		t.Fatalf("err = %v, want it to mention HACKRF_ERROR_NOT_FOUND", err)
	}
}

func TestRunnerTimeout(t *testing.T) {
	r := Runner{Bin: "testdata/slow_sweep.sh", Passes: 2, Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Run took %v, want it to stop near the 200ms timeout", time.Since(start))
	}
}
