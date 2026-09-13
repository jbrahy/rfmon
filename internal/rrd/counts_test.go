package rrd

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateCountsCreatesFileAndStoresValues(t *testing.T) {
	s := newStore(t)
	at := time.Unix(1_789_000_020, 0)
	ds := map[string]float64{"aps": 3, "clients": 5, "frames": 40}
	if err := s.UpdateCounts("wifi", at, ds); err != nil {
		t.Fatal(err)
	}
	ts, vals := lastUpdate(t, filepath.Join(s.Dir, "wifi.rrd"))
	if ts != at.Unix() {
		t.Errorf("last update time = %d, want %d", ts, at.Unix())
	}
	// sorted DS-name order: aps, clients, frames
	want := []float64{3, 5, 40}
	if len(vals) != 3 {
		t.Fatalf("got %d values, want 3", len(vals))
	}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("value %d = %v, want %v", i, vals[i], want[i])
		}
	}
}

func TestUpdateCountsAppendsToExistingFile(t *testing.T) {
	s := newStore(t)
	at := time.Unix(1_789_000_020, 0)
	ds := map[string]float64{"aps": 3, "clients": 5, "frames": 40}
	if err := s.UpdateCounts("wifi", at, ds); err != nil {
		t.Fatal(err)
	}
	next := map[string]float64{"aps": 4, "clients": 6, "frames": 41}
	if err := s.UpdateCounts("wifi", at.Add(64*time.Second), next); err != nil {
		t.Fatal(err)
	}
	ts, vals := lastUpdate(t, filepath.Join(s.Dir, "wifi.rrd"))
	if ts != at.Unix()+64 {
		t.Errorf("last update time = %d, want %d", ts, at.Unix()+64)
	}
	want := []float64{4, 6, 41}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("value %d = %v, want %v", i, vals[i], want[i])
		}
	}
}

func TestGraphCountsReturnsPNG(t *testing.T) {
	s := newStore(t)
	at := time.Now().Add(-2 * time.Minute)
	ds := map[string]float64{"aps": 3, "clients": 5, "frames": 40}
	if err := s.UpdateCounts("wifi", at, ds); err != nil {
		t.Fatal(err)
	}
	png, err := s.GraphCounts("wifi", "WiFi", []string{"aps", "clients", "frames"}, "day")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Errorf("output is not a PNG (%d bytes)", len(png))
	}
}

func TestGraphCountsErrors(t *testing.T) {
	s := newStore(t)
	if _, err := s.GraphCounts("wifi", "WiFi", []string{"aps"}, "hour"); err == nil {
		t.Error("unknown period: want error")
	}
	if _, err := s.GraphCounts("missing", "X", []string{"aps"}, "day"); err == nil {
		t.Error("missing rrd file: want error")
	}
}
