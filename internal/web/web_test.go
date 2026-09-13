package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jbrahy/rfmon/internal/bands"
	"github.com/jbrahy/rfmon/internal/store"
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

type stubReports struct {
	aps       []store.WifiAP
	clients   []store.WifiClient
	probed    []store.ProbedSSID
	ble       []store.BleDeviceRow
	dailyWifi []store.DailyRow
	dailyBle  []store.DailyRow
	err       error
}

func (r *stubReports) WifiAPsSince(t time.Time, limit int) ([]store.WifiAP, error) {
	return r.aps, r.err
}

func (r *stubReports) WifiClientsSince(t time.Time, limit int) ([]store.WifiClient, error) {
	return r.clients, r.err
}

func (r *stubReports) TopProbedSSIDs(t time.Time, limit int) ([]store.ProbedSSID, error) {
	return r.probed, r.err
}

func (r *stubReports) BleDevicesSince(t time.Time, limit int) ([]store.BleDeviceRow, error) {
	return r.ble, r.err
}

func (r *stubReports) DailyWifi(t time.Time) ([]store.DailyRow, error) {
	return r.dailyWifi, r.err
}

func (r *stubReports) DailyBle(t time.Time) ([]store.DailyRow, error) {
	return r.dailyBle, r.err
}

type stubCounts struct {
	calls []string
	err   error
}

func (c *stubCounts) GraphCounts(name, title string, dsNames []string, period string) ([]byte, error) {
	c.calls = append(c.calls, name+"|"+title+"|"+strings.Join(dsNames, ",")+"|"+period)
	if c.err != nil {
		return nil, c.err
	}
	return []byte("\x89PNG " + name + " " + period), nil
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

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

func TestWifiPageRendersReports(t *testing.T) {
	stubTime := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	ssid := "stub-network"
	security := "WPA2"
	snr := 20
	reports := &stubReports{
		aps: []store.WifiAP{
			{
				MAC: "aa:bb:cc:dd:ee:01", SSID: &ssid, Channel: 6, Security: &security,
				Randomized: false, FirstSeen: stubTime, LastSeen: stubTime, Sightings: 3, BestSNR: &snr,
			},
			{
				MAC: "aa:bb:cc:dd:ee:02", SSID: nil, Channel: 11,
				Randomized: true, FirstSeen: stubTime, LastSeen: stubTime, Sightings: 1,
			},
		},
		clients: []store.WifiClient{
			{
				MAC: "aa:bb:cc:dd:ee:03", Randomized: true, FirstSeen: stubTime, LastSeen: stubTime,
				Sightings: 2, ProbedSSIDs: []string{"stub-probe"},
			},
		},
		probed:    []store.ProbedSSID{{SSID: "stub-probe", Count: 5}},
		dailyWifi: []store.DailyRow{{Date: "2026-09-12", Devices: 7, New: 4}},
	}
	h := New(&stubGrapher{}, testBands).WithReports(reports).Handler()
	rec := get(t, h, "/wifi")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"aa:bb:cc:dd:ee:01", "stub-network", "hidden", "aa:bb:cc:dd:ee:03",
		"stub-probe", `src="/graph/wifi-day.png"`,
		"2026-09-12", // daily table row
		"randomized", // vendor of the randomized client
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wifi page missing %q", want)
		}
	}
}

func TestWifiWithoutReportsShowsNoDataPage(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	rec := get(t, h, "/wifi")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No data yet") {
		t.Errorf("wifi page without reports missing no-data message: %q", rec.Body.String())
	}
}

func TestBluetoothPageRendersReports(t *testing.T) {
	stubTime := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	name := "stub-device"
	reports := &stubReports{
		ble: []store.BleDeviceRow{
			{
				Address: "11:22:33:44:55:66", AddressType: "public", Name: &name,
				CompanyID: intPtr(76), FirstSeen: stubTime, LastSeen: stubTime,
				Sightings: 4, BestRSSI: intPtr(-60),
			},
		},
		dailyBle: []store.DailyRow{{Date: "2026-09-12", Devices: 82, New: 61}},
	}
	h := New(&stubGrapher{}, testBands).WithReports(reports).Handler()
	rec := get(t, h, "/bluetooth")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"11:22:33:44:55:66", "stub-device", `src="/graph/ble-day.png"`,
		"Apple, Inc.", // company_id 76 translated to name
		"2026-09-12",  // daily table row
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bluetooth page missing %q", want)
		}
	}
}

func TestBluetoothWithoutReportsShowsNoDataPage(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	rec := get(t, h, "/bluetooth")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No data yet") {
		t.Errorf("bluetooth page without reports missing no-data message: %q", rec.Body.String())
	}
}

func TestWifiGraphsPageListsAllPeriods(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	rec := get(t, h, "/wifi/graphs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, p := range []string{"day", "week", "month", "year"} {
		if !strings.Contains(rec.Body.String(), `src="/graph/wifi-`+p+`.png"`) {
			t.Errorf("wifi graphs page missing %s graph", p)
		}
	}
}

func TestBluetoothGraphsPageListsAllPeriods(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	rec := get(t, h, "/bluetooth/graphs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, p := range []string{"day", "week", "month", "year"} {
		if !strings.Contains(rec.Body.String(), `src="/graph/ble-`+p+`.png"`) {
			t.Errorf("bluetooth graphs page missing %s graph", p)
		}
	}
}

func TestCountsGraphServesAndCaches(t *testing.T) {
	c := &stubCounts{}
	s := New(&stubGrapher{}, testBands).WithCounts(c)
	now := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	h := s.Handler()

	rec := get(t, h, "/graph/wifi-week.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Body.String() != "\x89PNG wifi week" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if len(c.calls) != 1 || c.calls[0] != "wifi|WiFi|aps,clients,frames|week" {
		t.Errorf("calls = %v", c.calls)
	}

	now = now.Add(59 * time.Second)
	get(t, h, "/graph/wifi-week.png")
	if len(c.calls) != 1 {
		t.Errorf("cached counts graph re-rendered: %d calls, want 1", len(c.calls))
	}

	now = now.Add(2 * time.Second)
	get(t, h, "/graph/wifi-week.png")
	if len(c.calls) != 2 {
		t.Errorf("stale counts graph not re-rendered: %d calls, want 2", len(c.calls))
	}
}

func TestBleCountsGraphUsesSortedDataSourceNames(t *testing.T) {
	c := &stubCounts{}
	s := New(&stubGrapher{}, testBands).WithCounts(c)
	h := s.Handler()
	rec := get(t, h, "/graph/ble-day.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(c.calls) != 1 || c.calls[0] != "ble|Bluetooth|devices,packets|day" {
		t.Errorf("calls = %v", c.calls)
	}
}

func TestCountsGraphBadPeriod404sWithoutCallingGrapher(t *testing.T) {
	c := &stubCounts{}
	h := New(&stubGrapher{}, testBands).WithCounts(c).Handler()
	rec := get(t, h, "/graph/ble-hour.png")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if len(c.calls) != 0 {
		t.Errorf("grapher called for bad period: %v", c.calls)
	}
}

func TestCountsGraphWithoutGrapherIsUnavailable(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	rec := get(t, h, "/graph/wifi-day.png")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestNavHeaderOnAllPages(t *testing.T) {
	h := New(&stubGrapher{}, testBands).Handler()
	for _, path := range []string{"/", "/band/cbrs", "/wifi", "/bluetooth", "/wifi/graphs", "/bluetooth/graphs"} {
		rec := get(t, h, path)
		body := rec.Body.String()
		for _, want := range []string{`href="/"`, `href="/wifi"`, `href="/bluetooth"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing nav link %q", path, want)
			}
		}
	}
}
