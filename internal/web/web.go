// Package web serves MRTG-style pages of per-band RRD graphs.
package web

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jbrahy/rfmon/internal/bands"
	"github.com/jbrahy/rfmon/internal/rrd"
	"github.com/jbrahy/rfmon/internal/store"
)

type Grapher interface {
	Graph(slug, label, period string) ([]byte, error)
}

// Reports serves the WiFi and Bluetooth report pages.
type Reports interface {
	WifiAPsSince(t time.Time, limit int) ([]store.WifiAP, error)
	WifiClientsSince(t time.Time, limit int) ([]store.WifiClient, error)
	TopProbedSSIDs(t time.Time, limit int) ([]store.ProbedSSID, error)
	BleDevicesSince(t time.Time, limit int) ([]store.BleDeviceRow, error)
}

// CountsGrapher renders RRD graphs for the WiFi/BLE counts RRDs (as opposed
// to the per-band spectrum RRDs served by Grapher).
type CountsGrapher interface {
	GraphCounts(name, title string, dsNames []string, period string) ([]byte, error)
}

type Server struct {
	g       Grapher
	bands   []bands.Band
	reports Reports
	counts  CountsGrapher
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	png []byte
	at  time.Time
}

func New(g Grapher, bs []bands.Band) *Server {
	return &Server{g: g, bands: bs, ttl: 60 * time.Second, now: time.Now, cache: map[string]cached{}}
}

// WithReports wires the WiFi/Bluetooth report data source. It returns s so
// it can be chained onto New.
func (s *Server) WithReports(r Reports) *Server {
	s.reports = r
	return s
}

// WithCounts wires the WiFi/Bluetooth counts grapher. It returns s so it can
// be chained onto New.
func (s *Server) WithCounts(c CountsGrapher) *Server {
	s.counts = c
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /band/{slug}", s.band)
	mux.HandleFunc("GET /wifi", s.wifi)
	mux.HandleFunc("GET /wifi/graphs", s.wifiGraphs)
	mux.HandleFunc("GET /bluetooth", s.bluetooth)
	mux.HandleFunc("GET /bluetooth/graphs", s.bluetoothGraphs)
	mux.HandleFunc("GET /graph/{file}", s.graph)
	return mux
}

func (s *Server) find(slug string) (bands.Band, bool) {
	for _, b := range s.bands {
		if b.Slug == slug {
			return b, true
		}
	}
	return bands.Band{}, false
}

const navHTML = `<nav><a href="/">bands</a> | <a href="/wifi">wifi</a> | <a href="/bluetooth">bluetooth</a></nav>`

// timeFormat is used for all first/last seen columns in the report tables.
const timeFormat = "2006-01-02 15:04:05"

var pages = template.Must(template.New("index").Parse(`<!doctype html>
<title>rfmon</title>
` + navHTML + `
<h1>rfmon</h1>
{{range .}}<h2><a href="/band/{{.Slug}}">{{.Label}}</a> {{.LowMHz}} to {{.HighMHz}} MHz</h2>
<img src="/graph/{{.Slug}}-day.png" loading="lazy">
{{end}}`))

var bandPage = template.Must(template.New("band").Parse(`<!doctype html>
<title>{{.Band.Label}} - rfmon</title>
` + navHTML + `
<p><a href="/">all bands</a></p>
<h1>{{.Band.Label}} {{.Band.LowMHz}} to {{.Band.HighMHz}} MHz</h1>
{{range .Periods}}<h2>{{.Name}}</h2>
<img src="/graph/{{$.Band.Slug}}-{{.Name}}.png">
{{end}}`))

var noReportsPage = template.Must(template.New("noReports").Parse(`<!doctype html>
<title>{{.}} - rfmon</title>
` + navHTML + `
<h1>{{.}}</h1>
<p>No data yet.</p>`))

var wifiPage = template.Must(template.New("wifi").Parse(`<!doctype html>
<title>WiFi - rfmon</title>
` + navHTML + `
<h1>WiFi</h1>
<img src="/graph/wifi-day.png" loading="lazy">
<p><a href="/wifi/graphs">more graphs</a></p>
<h2>Access points</h2>
<table>
<tr><th>MAC</th><th>SSID</th><th>Channel</th><th>Security</th><th>Randomized</th><th>First seen</th><th>Last seen</th><th>Sightings</th><th>Best SNR</th></tr>
{{range .APs}}<tr><td>{{.MAC}}</td><td>{{.SSID}}</td><td>{{.Channel}}</td><td>{{.Security}}</td><td>{{.Randomized}}</td><td>{{.FirstSeen}}</td><td>{{.LastSeen}}</td><td>{{.Sightings}}</td><td>{{.BestSNR}}</td></tr>
{{end}}</table>
<h2>Clients</h2>
<table>
<tr><th>MAC</th><th>Randomized</th><th>First seen</th><th>Last seen</th><th>Sightings</th><th>Probed SSIDs</th></tr>
{{range .Clients}}<tr><td>{{.MAC}}</td><td>{{.Randomized}}</td><td>{{.FirstSeen}}</td><td>{{.LastSeen}}</td><td>{{.Sightings}}</td><td>{{.ProbedSSIDs}}</td></tr>
{{end}}</table>
<h2>Top probed SSIDs</h2>
<table>
<tr><th>SSID</th><th>Clients</th></tr>
{{range .Probed}}<tr><td>{{.SSID}}</td><td>{{.Count}}</td></tr>
{{end}}</table>`))

var bluetoothPage = template.Must(template.New("bluetooth").Parse(`<!doctype html>
<title>Bluetooth - rfmon</title>
` + navHTML + `
<h1>Bluetooth</h1>
<img src="/graph/ble-day.png" loading="lazy">
<p><a href="/bluetooth/graphs">more graphs</a></p>
<h2>Devices</h2>
<table>
<tr><th>Address</th><th>Type</th><th>Name</th><th>Company ID</th><th>First seen</th><th>Last seen</th><th>Sightings</th><th>Best RSSI</th></tr>
{{range .Devices}}<tr><td>{{.Address}}</td><td>{{.AddressType}}</td><td>{{.Name}}</td><td>{{.CompanyID}}</td><td>{{.FirstSeen}}</td><td>{{.LastSeen}}</td><td>{{.Sightings}}</td><td>{{.BestRSSI}}</td></tr>
{{end}}</table>`))

var countsGraphsPage = template.Must(template.New("countsGraphs").Parse(`<!doctype html>
<title>{{.Title}} graphs - rfmon</title>
` + navHTML + `
<h1>{{.Title}} graphs</h1>
{{range .Periods}}<h2>{{.Name}}</h2>
<img src="/graph/{{$.Prefix}}-{{.Name}}.png">
{{end}}`))

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if err := pages.Execute(w, s.bands); err != nil {
		log.Printf("web: index: %v", err)
	}
}

func (s *Server) band(w http.ResponseWriter, r *http.Request) {
	b, ok := s.find(r.PathValue("slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := struct {
		Band    bands.Band
		Periods []rrd.Period
	}{b, rrd.Periods}
	if err := bandPage.Execute(w, data); err != nil {
		log.Printf("web: band %s: %v", b.Slug, err)
	}
}

// wifiAPView is the display form of a store.WifiAP row.
type wifiAPView struct {
	MAC        string
	SSID       string
	Channel    int
	Security   string
	Randomized bool
	FirstSeen  string
	LastSeen   string
	Sightings  int
	BestSNR    string
}

// wifiClientView is the display form of a store.WifiClient row.
type wifiClientView struct {
	MAC         string
	Randomized  bool
	FirstSeen   string
	LastSeen    string
	Sightings   int
	ProbedSSIDs string
}

// bleDeviceView is the display form of a store.BleDeviceRow row.
type bleDeviceView struct {
	Address     string
	AddressType string
	Name        string
	CompanyID   string
	FirstSeen   string
	LastSeen    string
	Sightings   int
	BestRSSI    string
}

func strOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

func intOr(p *int, fallback string) string {
	if p == nil {
		return fallback
	}
	return strconv.Itoa(*p)
}

func (s *Server) wifi(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		if err := noReportsPage.Execute(w, "WiFi"); err != nil {
			log.Printf("web: wifi: %v", err)
		}
		return
	}

	since := s.now().Add(-24 * time.Hour)
	aps, err := s.reports.WifiAPsSince(since, 500)
	if err != nil {
		log.Printf("web: wifi aps: %v", err)
		http.Error(w, "failed to load wifi report", http.StatusInternalServerError)
		return
	}
	clients, err := s.reports.WifiClientsSince(since, 500)
	if err != nil {
		log.Printf("web: wifi clients: %v", err)
		http.Error(w, "failed to load wifi report", http.StatusInternalServerError)
		return
	}
	probed, err := s.reports.TopProbedSSIDs(since, 50)
	if err != nil {
		log.Printf("web: wifi probed: %v", err)
		http.Error(w, "failed to load wifi report", http.StatusInternalServerError)
		return
	}

	apViews := make([]wifiAPView, len(aps))
	for i, a := range aps {
		ssid := "hidden"
		if a.SSID != nil {
			ssid = *a.SSID
		}
		apViews[i] = wifiAPView{
			MAC:        a.MAC,
			SSID:       ssid,
			Channel:    a.Channel,
			Security:   strOr(a.Security, "-"),
			Randomized: a.Randomized,
			FirstSeen:  a.FirstSeen.Format(timeFormat),
			LastSeen:   a.LastSeen.Format(timeFormat),
			Sightings:  a.Sightings,
			BestSNR:    intOr(a.BestSNR, "-"),
		}
	}

	clientViews := make([]wifiClientView, len(clients))
	for i, c := range clients {
		clientViews[i] = wifiClientView{
			MAC:         c.MAC,
			Randomized:  c.Randomized,
			FirstSeen:   c.FirstSeen.Format(timeFormat),
			LastSeen:    c.LastSeen.Format(timeFormat),
			Sightings:   c.Sightings,
			ProbedSSIDs: strings.Join(c.ProbedSSIDs, ", "),
		}
	}

	data := struct {
		APs     []wifiAPView
		Clients []wifiClientView
		Probed  []store.ProbedSSID
	}{apViews, clientViews, probed}
	if err := wifiPage.Execute(w, data); err != nil {
		log.Printf("web: wifi: %v", err)
	}
}

func (s *Server) bluetooth(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		if err := noReportsPage.Execute(w, "Bluetooth"); err != nil {
			log.Printf("web: bluetooth: %v", err)
		}
		return
	}

	since := s.now().Add(-24 * time.Hour)
	devices, err := s.reports.BleDevicesSince(since, 500)
	if err != nil {
		log.Printf("web: bluetooth devices: %v", err)
		http.Error(w, "failed to load bluetooth report", http.StatusInternalServerError)
		return
	}

	deviceViews := make([]bleDeviceView, len(devices))
	for i, d := range devices {
		deviceViews[i] = bleDeviceView{
			Address:     d.Address,
			AddressType: d.AddressType,
			Name:        strOr(d.Name, "-"),
			CompanyID:   intOr(d.CompanyID, "-"),
			FirstSeen:   d.FirstSeen.Format(timeFormat),
			LastSeen:    d.LastSeen.Format(timeFormat),
			Sightings:   d.Sightings,
			BestRSSI:    intOr(d.BestRSSI, "-"),
		}
	}

	data := struct {
		Devices []bleDeviceView
	}{deviceViews}
	if err := bluetoothPage.Execute(w, data); err != nil {
		log.Printf("web: bluetooth: %v", err)
	}
}

func (s *Server) wifiGraphs(w http.ResponseWriter, r *http.Request) {
	s.renderCountsGraphsPage(w, "wifi", "WiFi")
}

func (s *Server) bluetoothGraphs(w http.ResponseWriter, r *http.Request) {
	s.renderCountsGraphsPage(w, "ble", "Bluetooth")
}

func (s *Server) renderCountsGraphsPage(w http.ResponseWriter, prefix, title string) {
	data := struct {
		Prefix  string
		Title   string
		Periods []rrd.Period
	}{prefix, title, rrd.Periods}
	if err := countsGraphsPage.Execute(w, data); err != nil {
		log.Printf("web: %s graphs: %v", prefix, err)
	}
}

func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	name, ok := strings.CutSuffix(file, ".png")
	cut := strings.LastIndex(name, "-")
	if !ok || cut < 0 {
		http.NotFound(w, r)
		return
	}
	slug, period := name[:cut], name[cut+1:]

	// The stdlib ServeMux cannot express "wifi-{period}.png" as its own
	// pattern: wildcards must occupy a whole path segment, and here the
	// wildcard shares a segment with the "wifi-"/"ble-" prefix and ".png"
	// suffix. So the counts graph routes are handled by branching here,
	// inside the same "/graph/{file}" handler, rather than as separate
	// registrations.
	if slug == "wifi" || slug == "ble" {
		s.countsGraph(w, r, file, slug, period)
		return
	}

	b, ok := s.find(slug)
	if !ok || !validPeriod(period) {
		http.NotFound(w, r)
		return
	}

	s.mu.Lock()
	c, hit := s.cache[file]
	s.mu.Unlock()
	if !hit || s.now().Sub(c.at) >= s.ttl {
		png, err := s.g.Graph(b.Slug, b.Label, period)
		if err != nil {
			log.Printf("web: graph %s: %v", file, err)
			http.Error(w, "graph failed", http.StatusInternalServerError)
			return
		}
		c = cached{png: png, at: s.now()}
		s.mu.Lock()
		s.cache[file] = c
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(c.png)
}

// countsDataSources returns the RRD data source names and display title for
// a counts graph name ("wifi" or "ble"). Names are given pre-sorted, as
// GraphCounts requires.
func countsDataSources(name string) ([]string, string) {
	switch name {
	case "wifi":
		return []string{"aps", "clients", "frames"}, "WiFi"
	case "ble":
		return []string{"devices", "packets"}, "Bluetooth"
	default:
		return nil, name
	}
}

func (s *Server) countsGraph(w http.ResponseWriter, r *http.Request, file, name, period string) {
	if !validPeriod(period) {
		http.NotFound(w, r)
		return
	}
	if s.counts == nil {
		http.Error(w, "counts graphs not available", http.StatusServiceUnavailable)
		return
	}

	s.mu.Lock()
	c, hit := s.cache[file]
	s.mu.Unlock()
	if !hit || s.now().Sub(c.at) >= s.ttl {
		dsNames, title := countsDataSources(name)
		png, err := s.counts.GraphCounts(name, title, dsNames, period)
		if err != nil {
			log.Printf("web: counts graph %s: %v", file, err)
			http.Error(w, "graph failed", http.StatusInternalServerError)
			return
		}
		c = cached{png: png, at: s.now()}
		s.mu.Lock()
		s.cache[file] = c
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(c.png)
}

func validPeriod(name string) bool {
	for _, p := range rrd.Periods {
		if p.Name == name {
			return true
		}
	}
	return false
}
