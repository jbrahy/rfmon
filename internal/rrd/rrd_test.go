package rrd

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hackrfone/internal/bands"
)

func newStore(t *testing.T) Store {
	t.Helper()
	if _, err := exec.LookPath("rrdtool"); err != nil {
		t.Skip("rrdtool not installed")
	}
	return Store{Dir: t.TempDir(), Bin: "rrdtool"}
}

func lastUpdate(t *testing.T, path string) (int64, []float64) {
	t.Helper()
	out, err := exec.Command("rrdtool", "lastupdate", path).Output()
	if err != nil {
		t.Fatalf("lastupdate: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	ts, err := strconv.ParseInt(strings.TrimSuffix(fields[0], ":"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	var vals []float64
	for _, f := range fields[1:] {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			t.Fatal(err)
		}
		vals = append(vals, v)
	}
	return ts, vals
}

var sample = bands.Metrics{Avg: -45.5, Peak: -30, Floor: -60, Occ: 12.5, Bins: 84}

func TestUpdateCreatesFileAndStoresValues(t *testing.T) {
	s := newStore(t)
	at := time.Unix(1_789_000_020, 0)
	if err := s.Update("fm-broadcast", at, sample); err != nil {
		t.Fatal(err)
	}
	ts, vals := lastUpdate(t, filepath.Join(s.Dir, "fm-broadcast.rrd"))
	if ts != at.Unix() {
		t.Errorf("last update time = %d, want %d", ts, at.Unix())
	}
	want := []float64{-45.5, -30, -60, 12.5}
	if len(vals) != 4 {
		t.Fatalf("got %d values, want 4", len(vals))
	}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("value %d = %v, want %v", i, vals[i], want[i])
		}
	}
}

func TestUpdateAppendsToExistingFile(t *testing.T) {
	s := newStore(t)
	at := time.Unix(1_789_000_020, 0)
	if err := s.Update("x", at, sample); err != nil {
		t.Fatal(err)
	}
	next := sample
	next.Peak = -25
	if err := s.Update("x", at.Add(64*time.Second), next); err != nil {
		t.Fatal(err)
	}
	ts, vals := lastUpdate(t, filepath.Join(s.Dir, "x.rrd"))
	if ts != at.Unix()+64 || vals[1] != -25 {
		t.Errorf("last update = %d %v, want %d with peak -25", ts, vals, at.Unix()+64)
	}
}

func TestUpdateReportsRrdtoolError(t *testing.T) {
	s := newStore(t)
	at := time.Unix(1_789_000_020, 0)
	if err := s.Update("x", at, sample); err != nil {
		t.Fatal(err)
	}
	err := s.Update("x", at.Add(-30*time.Second), sample)
	if err == nil || !strings.Contains(err.Error(), "illegal attempt") {
		t.Fatalf("err = %v, want rrdtool's illegal attempt message", err)
	}
}

func TestGraphReturnsPNG(t *testing.T) {
	s := newStore(t)
	if err := s.Update("x", time.Now().Add(-2*time.Minute), sample); err != nil {
		t.Fatal(err)
	}
	for _, p := range Periods {
		png, err := s.Graph("x", "Test band", p.Name)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if !bytes.HasPrefix(png, []byte("\x89PNG")) {
			t.Errorf("%s: output is not a PNG (%d bytes)", p.Name, len(png))
		}
	}
}

func TestGraphErrors(t *testing.T) {
	s := newStore(t)
	if _, err := s.Graph("x", "X", "hour"); err == nil {
		t.Error("unknown period: want error")
	}
	if _, err := s.Graph("missing", "X", "day"); err == nil {
		t.Error("missing rrd file: want error")
	}
}
