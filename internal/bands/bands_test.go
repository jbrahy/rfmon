package bands

import (
	"math"
	"regexp"
	"testing"

	"github.com/jbrahy/rfmon/internal/sweep"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func TestComputeMetrics(t *testing.T) {
	s := sweep.Spectrum{}
	for i := 0; i < 10; i++ {
		s.Hz = append(s.Hz, int64(100_000_000+i*1_000_000))
		s.DB = append(s.DB, -60)
	}
	s.DB[4] = -30

	m, ok := Compute(s, Band{Slug: "t", LowMHz: 100, HighMHz: 110})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if m.Bins != 10 {
		t.Errorf("Bins = %d, want 10", m.Bins)
	}
	if !near(m.Peak, -30) {
		t.Errorf("Peak = %v, want -30", m.Peak)
	}
	if !near(m.Floor, -60) {
		t.Errorf("Floor = %v, want -60", m.Floor)
	}
	// (9 * 1e-6 mW + 1e-3 mW) / 10 = 1.009e-4 mW = -39.961 dB
	if !near(m.Avg, -39.961) {
		t.Errorf("Avg = %v, want -39.961", m.Avg)
	}
	if !near(m.Occ, 10) {
		t.Errorf("Occ = %v, want 10", m.Occ)
	}
}

func TestAvgIsLinearNotArithmetic(t *testing.T) {
	s := sweep.Spectrum{Hz: []int64{1_000_000, 2_000_000}, DB: []float64{-10, -20}}
	m, _ := Compute(s, Band{LowMHz: 1, HighMHz: 3})
	// (0.1 + 0.01) / 2 = 0.055 mW = -12.596 dB; arithmetic mean would be -15
	if !near(m.Avg, -12.596) {
		t.Errorf("Avg = %v, want -12.596", m.Avg)
	}
}

func TestComputeSelectsBinsInRange(t *testing.T) {
	s := sweep.Spectrum{
		Hz: []int64{99_999_000, 100_000_000, 101_000_000, 102_000_000, 110_000_000},
		DB: []float64{0, -50, math.NaN(), -40, 0},
	}
	m, ok := Compute(s, Band{LowMHz: 100, HighMHz: 110})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if m.Bins != 2 {
		t.Errorf("Bins = %d, want 2 (low edge included, NaN and high edge excluded)", m.Bins)
	}
	if !near(m.Peak, -40) {
		t.Errorf("Peak = %v, want -40", m.Peak)
	}
}

func TestComputeEmptyBand(t *testing.T) {
	s := sweep.Spectrum{Hz: []int64{50_000_000}, DB: []float64{math.NaN()}}
	if _, ok := Compute(s, Band{LowMHz: 40, HighMHz: 60}); ok {
		t.Error("ok = true for band with only NaN bins, want false")
	}
	if _, ok := Compute(s, Band{LowMHz: 100, HighMHz: 200}); ok {
		t.Error("ok = true for band with no bins, want false")
	}
}

func TestTableSanity(t *testing.T) {
	slugRe := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	seen := map[string]bool{}
	if len(All) != 43 {
		t.Errorf("len(All) = %d, want 43", len(All))
	}
	for i, b := range All {
		if !slugRe.MatchString(b.Slug) {
			t.Errorf("bad slug %q", b.Slug)
		}
		if seen[b.Slug] {
			t.Errorf("duplicate slug %q", b.Slug)
		}
		seen[b.Slug] = true
		if b.Label == "" {
			t.Errorf("%s: empty label", b.Slug)
		}
		if b.LowMHz >= b.HighMHz {
			t.Errorf("%s: low %v >= high %v", b.Slug, b.LowMHz, b.HighMHz)
		}
		if i > 0 && b.LowMHz < All[i-1].HighMHz {
			t.Errorf("%s overlaps or is out of order with %s", b.Slug, All[i-1].Slug)
		}
	}
}
