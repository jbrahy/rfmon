package spectrum

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hackrfone/internal/sweep"
)

func TestQuantize(t *testing.T) {
	cases := []struct {
		db   float64
		want byte
	}{
		{-120, 0}, {-119.5, 1}, {0, 240}, {7, 254}, {8, 254},
		{-130, 0}, {-45.26, 149}, {math.NaN(), 255},
	}
	for _, c := range cases {
		if got := quantize(c.db); got != c.want {
			t.Errorf("quantize(%v) = %d, want %d", c.db, got, c.want)
		}
	}
}

func TestEncodeLayout(t *testing.T) {
	s := sweep.Spectrum{Hz: []int64{1, 2, 3}, DB: []float64{-120, math.NaN(), 0}}
	b := Encode(time.Unix(0x0102030405, 0), s)
	want := []byte{0x05, 0x04, 0x03, 0x02, 0x01, 0, 0, 0, 3, 0, 0, 0, 0, 255, 240}
	if string(b) != string(want) {
		t.Errorf("Encode = % x, want % x", b, want)
	}
}

func TestAppendAndReadDayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1 := sweep.Spectrum{Hz: []int64{100, 200, 300}, DB: []float64{-45.26, math.NaN(), -10}}
	s2 := sweep.Spectrum{Hz: []int64{100, 200, 300}, DB: []float64{-50, -60, -70}}
	t1 := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	t2 := t1.Add(64 * time.Second)
	if err := Append(dir, t1, s1); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, t2, s2); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadDay(filepath.Join(dir, "2026-09-12.bin.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if !recs[0].Time.Equal(t1) || !recs[1].Time.Equal(t2) {
		t.Errorf("times = %v, %v; want %v, %v", recs[0].Time, recs[1].Time, t1, t2)
	}
	if math.Abs(recs[0].DB[0]-(-45.5)) > 1e-9 {
		t.Errorf("rec 0 bin 0 = %v, want -45.5", recs[0].DB[0])
	}
	if !math.IsNaN(recs[0].DB[1]) {
		t.Errorf("rec 0 bin 1 = %v, want NaN", recs[0].DB[1])
	}
	for i, want := range s2.DB {
		if math.Abs(recs[1].DB[i]-want) > 0.25 {
			t.Errorf("rec 1 bin %d = %v, want %v", i, recs[1].DB[i], want)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "bins.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hz []int64
	if err := json.Unmarshal(raw, &hz); err != nil {
		t.Fatal(err)
	}
	if len(hz) != 3 || hz[0] != 100 || hz[2] != 300 {
		t.Errorf("bins.json = %v, want [100 200 300]", hz)
	}
}

func TestAppendUsesUTCDay(t *testing.T) {
	dir := t.TempDir()
	pdt := time.FixedZone("PDT", -7*3600)
	at := time.Date(2026, 9, 12, 20, 0, 0, 0, pdt) // 2026-09-13 03:00 UTC
	s := sweep.Spectrum{Hz: []int64{1}, DB: []float64{-50}}
	if err := Append(dir, at, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-09-13.bin.gz")); err != nil {
		t.Errorf("want file named for UTC day 2026-09-13: %v", err)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	for d := 1; d <= 12; d++ {
		name := time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02") + ".bin.gz"
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, other := range []string{"bins.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, other), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	if err := Prune(dir, now, 7); err != nil {
		t.Fatal(err)
	}

	for d := 1; d <= 12; d++ {
		name := time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02") + ".bin.gz"
		_, err := os.Stat(filepath.Join(dir, name))
		exists := err == nil
		wantExists := d >= 5
		if exists != wantExists {
			t.Errorf("%s exists = %v, want %v", name, exists, wantExists)
		}
	}
	for _, other := range []string{"bins.json", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, other)); err != nil {
			t.Errorf("%s was removed: %v", other, err)
		}
	}
}
