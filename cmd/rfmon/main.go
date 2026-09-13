// Command rfmon sweeps 1 MHz to 6 GHz with a HackRF, pauses, and repeats.
// Each poll writes per-band statistics to RRD files and a full-spectrum
// snapshot, and a local web server shows MRTG-style graphs.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"hackrfone/internal/bands"
	"hackrfone/internal/rrd"
	"hackrfone/internal/spectrum"
	"hackrfone/internal/sweep"
	"hackrfone/internal/web"
)

const keepSpectrumDays = 7

func main() {
	dataDir := flag.String("data", "./data", "directory for rrd and spectrum files")
	listen := flag.String("listen", "127.0.0.1:8080", "web server address")
	pause := flag.Duration("pause", 60*time.Second, "pause between polls")
	passes := flag.Int("passes", 5, "hackrf_sweep passes per poll")
	flag.Parse()

	rrdDir := filepath.Join(*dataDir, "rrd")
	specDir := filepath.Join(*dataDir, "spectrum")
	for _, d := range []string{rrdDir, specDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	store := rrd.Store{Dir: rrdDir, Bin: "rrdtool"}
	runner := sweep.Runner{Bin: "hackrf_sweep", Passes: *passes, Timeout: 2 * time.Minute}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: *listen, Handler: web.New(store, bands.All).Handler()}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("rfmon: graphs at http://%s, data in %s", *listen, *dataDir)

	for {
		poll(ctx, runner, store, specDir)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				log.Printf("web shutdown: %v", err)
			}
			log.Print("rfmon: stopped")
			return
		case <-time.After(*pause):
		}
	}
}

func poll(ctx context.Context, runner sweep.Runner, store rrd.Store, specDir string) {
	start := time.Now()
	s, err := runner.Run(ctx)
	if err != nil {
		log.Printf("poll failed: %v", err)
		return
	}
	sweep.DropSpurs(s)

	updated := 0
	for _, b := range bands.All {
		m, ok := bands.Compute(s, b)
		if !ok {
			continue
		}
		if err := store.Update(b.Slug, start, m); err != nil {
			log.Printf("rrd %s: %v", b.Slug, err)
			continue
		}
		updated++
	}
	if err := spectrum.Append(specDir, start, s); err != nil {
		log.Printf("spectrum: %v", err)
	}
	if err := spectrum.Prune(specDir, start, keepSpectrumDays); err != nil {
		log.Printf("spectrum prune: %v", err)
	}
	log.Printf("poll ok: %d bins, %d bands, %s", len(s.Hz), updated, time.Since(start).Round(time.Millisecond))
}
