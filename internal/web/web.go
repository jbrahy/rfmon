// Package web serves MRTG-style pages of per-band RRD graphs.
package web

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jbrahy/rfmon/internal/bands"
	"github.com/jbrahy/rfmon/internal/rrd"
)

type Grapher interface {
	Graph(slug, label, period string) ([]byte, error)
}

type Server struct {
	g     Grapher
	bands []bands.Band
	ttl   time.Duration
	now   func() time.Time

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

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /band/{slug}", s.band)
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

var pages = template.Must(template.New("index").Parse(`<!doctype html>
<title>rfmon</title>
<h1>rfmon</h1>
{{range .}}<h2><a href="/band/{{.Slug}}">{{.Label}}</a> {{.LowMHz}} to {{.HighMHz}} MHz</h2>
<img src="/graph/{{.Slug}}-day.png" loading="lazy">
{{end}}`))

var bandPage = template.Must(template.New("band").Parse(`<!doctype html>
<title>{{.Band.Label}} - rfmon</title>
<p><a href="/">all bands</a></p>
<h1>{{.Band.Label}} {{.Band.LowMHz}} to {{.Band.HighMHz}} MHz</h1>
{{range .Periods}}<h2>{{.Name}}</h2>
<img src="/graph/{{$.Band.Slug}}-{{.Name}}.png">
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

func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	name, ok := strings.CutSuffix(file, ".png")
	cut := strings.LastIndex(name, "-")
	if !ok || cut < 0 {
		http.NotFound(w, r)
		return
	}
	slug, period := name[:cut], name[cut+1:]
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

func validPeriod(name string) bool {
	for _, p := range rrd.Periods {
		if p.Name == name {
			return true
		}
	}
	return false
}
