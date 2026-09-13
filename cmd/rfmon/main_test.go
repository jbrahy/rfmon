package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jbrahy/rfmon/internal/rrd"
	"github.com/jbrahy/rfmon/internal/sweep"
)

// TestPollRejectsPartialSweep covers F1: a sweep that returns fewer bins
// than a full 1:6000 / 250000 grid must not be written to RRD or spectrum.
// testdata/fake_sweep.sh (borrowed from the sweep package) always emits the
// 168-bin fixture, far short of sweep.ExpectedBins, exercising the same
// partial-output path a mid-sweep USB error would take.
func TestPollRejectsPartialSweep(t *testing.T) {
	dir := t.TempDir()
	rrdDir := filepath.Join(dir, "rrd")
	specDir := filepath.Join(dir, "spectrum")
	if err := os.MkdirAll(rrdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := sweep.Runner{
		Bin:     "../../internal/sweep/testdata/fake_sweep.sh",
		Passes:  1,
		Timeout: 5 * time.Second,
	}
	// store.Bin is never expected to run; poll must return before touching
	// rrdtool once the bin count check fails.
	store := rrd.Store{Dir: rrdDir, Bin: "does-not-exist-should-not-run"}

	poll(context.Background(), runner, store, specDir)

	rrdEntries, err := os.ReadDir(rrdDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rrdEntries) != 0 {
		t.Errorf("rrd dir has %d entries, want 0 for a partial sweep", len(rrdEntries))
	}

	specEntries, err := os.ReadDir(specDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(specEntries) != 0 {
		t.Errorf("spectrum dir has %d entries, want 0 for a partial sweep", len(specEntries))
	}
}
