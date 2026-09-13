package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hackrfone/internal/bands"
)

type stubGrapher struct {
	calls []string
	err   error
}

func (g *stubGrapher) Graph(slug, label, period string) ([]byte, error) {
	g.calls = append(g.calls, slug+"|"+label+"|"+period)
	if g.err != nil {
		return nil, g.err
	}
	return []byte("\x89PNG " + slug + " " + period), nil
}

var testBands = []bands.Band{
	{Slug: "fm-broadcast", Label: "FM broadcast", LowMHz: 88, HighMHz: 108},
	{Slug: "cbrs", Label: "CBRS", LowMHz: 3550, HighMHz: 3700},
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestIndexListsBands(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"FM broadcast", "CBRS", `href="/band/fm-broadcast"`,
		`src="/graph/fm-broadcast-day.png"`, `src="/graph/cbrs-day.png"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
}

func TestBandPageShowsAllPeriods(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	rec := get(t, h, "/band/cbrs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, p := range []string{"day", "week", "month", "year"} {
		if !strings.Contains(rec.Body.String(), `src="/graph/cbrs-`+p+`.png"`) {
			t.Errorf("band page missing %s graph", p)
		}
	}
	if rec := get(t, h, "/band/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown band status = %d, want 404", rec.Code)
	}
}

func TestGraphServesAndCaches(t *testing.T) {
	g := &stubGrapher{}
	s := New(g, testBands)
	now := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	h := s.Handler()

	rec := get(t, h, "/graph/fm-broadcast-week.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Body.String() != "\x89PNG fm-broadcast week" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if len(g.calls) != 1 || g.calls[0] != "fm-broadcast|FM broadcast|week" {
		t.Errorf("calls = %v", g.calls)
	}

	now = now.Add(59 * time.Second)
	get(t, h, "/graph/fm-broadcast-week.png")
	if len(g.calls) != 1 {
		t.Errorf("cached graph re-rendered: %d calls, want 1", len(g.calls))
	}

	now = now.Add(2 * time.Second)
	get(t, h, "/graph/fm-broadcast-week.png")
	if len(g.calls) != 2 {
		t.Errorf("stale graph not re-rendered: %d calls, want 2", len(g.calls))
	}
}

func TestGraphRejectsBadNames(t *testing.T) {
	g := &stubGrapher{}
	h := New(g, testBands).Handler()
	for _, path := range []string{
		"/graph/nope-day.png",
		"/graph/fm-broadcast-hour.png",
		"/graph/fm-broadcast-day.gif",
		"/graph/day.png",
	} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
	if len(g.calls) != 0 {
		t.Errorf("grapher called for bad names: %v", g.calls)
	}
}

func TestGraphErrorIs500(t *testing.T) {
	h := New(&stubGrapher{err: errors.New("boom")}, testBands).Handler()
	if rec := get(t, h, "/graph/cbrs-day.png"); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
