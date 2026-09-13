// Command rfmon sweeps 1 MHz to 6 GHz with a HackRF, pauses, and repeats.
// Each poll writes per-band statistics to RRD files and a full-spectrum
// snapshot, and a local web server shows MRTG-style graphs.
//
// Unless started with -spectrum-only, rfmon also time-slices the same
// HackRF across WiFi channels 1, 6, and 11 and a Bluetooth LE sweep,
// decoding management frames and BLE advertisements into a SQLite
// database and reporting them at /wifi and /bluetooth.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jbrahy/rfmon/internal/bands"
	"github.com/jbrahy/rfmon/internal/ble"
	"github.com/jbrahy/rfmon/internal/rrd"
	"github.com/jbrahy/rfmon/internal/scheduler"
	"github.com/jbrahy/rfmon/internal/spectrum"
	"github.com/jbrahy/rfmon/internal/store"
	"github.com/jbrahy/rfmon/internal/sweep"
	"github.com/jbrahy/rfmon/internal/web"
	"github.com/jbrahy/rfmon/internal/wifi"
)

const keepSpectrumDays = 7

func main() {
	dataDir := flag.String("data", "./data", "directory for rrd and spectrum files")
	listen := flag.String("listen", "127.0.0.1:8080", "web server address")
	passes := flag.Int("passes", 5, "hackrf_sweep passes per poll")
	pause := flag.Duration("pause", 5*time.Second, "pause between cycles")
	dwell := flag.Duration("dwell", 3*time.Second, "wifi and ble dwell length")
	sweepEvery := flag.Duration("sweep-every", 60*time.Second, "minimum time between spectrum polls")
	decodersDir := flag.String("decoders", defaultDecodersDir(), "decoder install prefix")
	grPython := flag.String("gr-python", "/opt/homebrew/opt/gnuradio/libexec/venv/bin/python", "GNU Radio python interpreter")
	decoderScript := flag.String("decoder-script", "decoders/wifi_ofdm_rx.py", "OFDM decoder script path (run from the repo root)")
	spectrumOnly := flag.Bool("spectrum-only", false, "spectrum sweeps only, no wifi/ble decoders")
	flag.Parse()

	checkStartupTools(*spectrumOnly, *decodersDir, *grPython)

	rrdDir := filepath.Join(*dataDir, "rrd")
	specDir := filepath.Join(*dataDir, "spectrum")
	logsDir := filepath.Join(*dataDir, "logs")
	tmpDir := filepath.Join(*dataDir, "tmp")
	for _, d := range []string{rrdDir, specDir, logsDir, tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	db, err := store.Open(filepath.Join(*dataDir, "rfmon.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rrdStore := rrd.Store{Dir: rrdDir, Bin: "rrdtool"}
	runner := sweep.Runner{Bin: "hackrf_sweep", Passes: *passes, Timeout: 2 * time.Minute}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:    *listen,
		Handler: web.New(rrdStore, bands.All).WithReports(db).WithCounts(rrdStore).Handler(),
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("rfmon: graphs at http://%s, data in %s", *listen, *dataDir)

	if *spectrumOnly {
		runSpectrumOnly(ctx, runner, rrdStore, specDir, *pause, srv)
		return
	}

	runWithDecoders(ctx, decoderDeps{
		runner:        runner,
		rrdStore:      rrdStore,
		specDir:       specDir,
		db:            db,
		grPython:      *grPython,
		decoderScript: *decoderScript,
		decodersDir:   *decodersDir,
		logsDir:       logsDir,
		tmpDir:        tmpDir,
		dwell:         *dwell,
		pause:         *pause,
		sweepEvery:    *sweepEvery,
	}, srv)
}

// defaultDecodersDir returns $HOME/.local/share/rfmon/decoders, or "" if
// the home directory cannot be determined (the flag then requires -decoders
// to be passed explicitly).
func defaultDecodersDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local/share/rfmon/decoders")
}

// checkStartupTools verifies the external tools rfmon shells out to are
// present, fatally exiting with an install hint if not. hackrf_sweep and
// rrdtool are required in every mode; the wifi/ble decoder tools and the
// GNU Radio Python import check are skipped under -spectrum-only.
func checkStartupTools(spectrumOnly bool, decodersDir, grPython string) {
	for _, tool := range []string{"hackrf_sweep", "rrdtool"} {
		if _, err := exec.LookPath(tool); err != nil {
			log.Fatalf("%s not found in PATH: brew install hackrf rrdtool", tool)
		}
	}
	if spectrumOnly {
		return
	}

	icePath := filepath.Join(decodersDir, "bin", "ice9-bluetooth")
	for _, tool := range []string{"hackrf_transfer", icePath} {
		if _, err := exec.LookPath(tool); err != nil {
			log.Fatalf("%s not found: run scripts/install-decoders.sh, or pass -spectrum-only", tool)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, grPython, "-c", "import ieee802_11, foo")
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+filepath.Join(decodersDir, "lib/python3.14/site-packages"),
		"DYLD_LIBRARY_PATH="+filepath.Join(decodersDir, "lib"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("gr-python import check failed: %v: %s\nrun scripts/install-decoders.sh, or pass -spectrum-only", err, out)
	}
}

// runSpectrumOnly is the original poll-then-pause loop, unchanged in
// behavior from before this task (aside from -pause's new 5s default): it
// preserves today's spectrum-only path for users without the wifi/ble
// decoders installed.
func runSpectrumOnly(ctx context.Context, runner sweep.Runner, store rrd.Store, specDir string, pause time.Duration, srv *http.Server) {
	for {
		poll(ctx, runner, store, specDir)
		select {
		case <-ctx.Done():
			shutdownWeb(srv)
			log.Print("rfmon: stopped")
			return
		case <-time.After(pause):
		}
	}
}

// decoderDeps bundles what runWithDecoders needs to build the OFDM
// supervisor and the scheduler.Deps that drive it, so the function itself
// stays readable.
type decoderDeps struct {
	runner   sweep.Runner
	rrdStore rrd.Store
	specDir  string
	db       *store.DB

	grPython      string
	decoderScript string
	decodersDir   string
	logsDir       string
	tmpDir        string

	dwell      time.Duration
	pause      time.Duration
	sweepEvery time.Duration
}

// runWithDecoders starts the OFDM decoder supervisor, wires
// scheduler.Deps, and runs the scheduler until ctx is canceled, then shuts
// everything down in order: the scheduler has already returned (it only
// returns once ctx is done), so close the supervisor, then the store, then
// the web server.
func runWithDecoders(ctx context.Context, dd decoderDeps, srv *http.Server) {
	logPath := filepath.Join(dd.logsDir, "wifi-ofdm.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("wifi ofdm log %s: %v", logPath, err)
	}
	defer logFile.Close()

	supervisor := wifi.NewSupervisor(wifi.Decoder{
		Python:     dd.grPython,
		Script:     dd.decoderScript,
		ModulePath: filepath.Join(dd.decodersDir, "lib/python3.14/site-packages"),
		LibPath:    filepath.Join(dd.decodersDir, "lib"),
	})
	supervisor.Stderr = logFile
	if err := supervisor.Start(); err != nil {
		log.Fatalf("wifi ofdm decoder: %v", err)
	}

	// wifi.Aggregator is documented as not safe for concurrent use, but
	// here it is written by the frame-draining goroutine below and read by
	// DrainWifi from the scheduler's goroutine, so agg serializes access
	// with a mutex.
	agg := wifi.NewAggregator()
	var aggMu sync.Mutex
	go func() {
		frames := supervisor.Frames()
		for {
			select {
			case <-ctx.Done():
				// supervisor.Frames() is never closed by Supervisor.Close,
				// so without this the goroutine would block on the
				// receive below forever after shutdown.
				return
			case tf, ok := <-frames:
				if !ok {
					return
				}
				aggMu.Lock()
				agg.Add(tf.Frame)
				aggMu.Unlock()
			}
		}
	}()

	transfer := wifi.TransferRunner{Bin: "hackrf_transfer"}
	bleRunner := ble.Runner{Bin: "ice9-bluetooth"}
	blePcapPath := filepath.Join(dd.tmpDir, "ble-dwell.pcap")

	deps := scheduler.Deps{
		WifiDwell: func(ctx context.Context, step scheduler.Step) error {
			if err := supervisor.Ensure(); err != nil {
				return err
			}
			freqHz := int64(step.CenterMHz) * 1_000_000
			return transfer.Dwell(ctx, supervisor.Stdin(), freqHz, dd.dwell)
		},
		DrainWifi: func(window scheduler.Window) []store.WifiSighting {
			// The aggregator collects every frame since the last drain
			// rather than filtering by window; the scheduler already
			// drains once per wifi dwell (plus a short grace period), so
			// in practice each drain corresponds to one window. This is
			// a simplification: frames are not filtered by window.Start
			// / window.End.
			aggMu.Lock()
			defer aggMu.Unlock()
			return agg.Drain()
		},
		BleDwell: func(ctx context.Context, step scheduler.Step) ([]store.BleSighting, error) {
			return bleRunner.Dwell(ctx, step.CenterMHz, step.Channel, blePcapPath, dd.dwell)
		},
		Sweep: func(ctx context.Context) error {
			poll(ctx, dd.runner, dd.rrdStore, dd.specDir)
			return nil
		},
		RecordDwell: func(dw store.Dwell, wifiSightings []store.WifiSighting, bleSightings []store.BleSighting) error {
			dwellID, err := dd.db.InsertDwell(dw)
			if err != nil {
				return err
			}
			for _, s := range wifiSightings {
				if err := dd.db.RecordWifi(dwellID, dw.EndedAt, s); err != nil {
					return err
				}
			}
			for _, s := range bleSightings {
				if err := dd.db.RecordBle(dwellID, dw.EndedAt, s); err != nil {
					return err
				}
			}
			return nil
		},
		UpdateCounts: func(now time.Time) error {
			counts, err := dd.db.CountsSince(now.Add(-60 * time.Second))
			if err != nil {
				return err
			}
			// Always pass the same key set per RRD (wifi: aps, clients,
			// frames; ble: devices, packets): UpdateCounts fixes a file's
			// DS list at creation, so a differing key set on a later call
			// would land values in the wrong data source.
			if err := dd.rrdStore.UpdateCounts("wifi", now, map[string]float64{
				"aps":     float64(counts.WifiAPs),
				"clients": float64(counts.WifiClients),
				"frames":  float64(counts.WifiFrames),
			}); err != nil {
				return err
			}
			return dd.rrdStore.UpdateCounts("ble", now, map[string]float64{
				"devices": float64(counts.BleDevices),
				"packets": float64(counts.BlePackets),
			})
		},
		Now:        time.Now,
		Sleep:      ctxSleep,
		Dwell:      dd.dwell,
		Pause:      dd.pause,
		SweepEvery: dd.sweepEvery,
	}

	scheduler.Run(ctx, deps)

	// scheduler.Run only returns once ctx is done, so shutdown proceeds
	// straight from here: supervisor, then web server.
	if err := supervisor.Close(); err != nil {
		log.Printf("wifi ofdm decoder close: %v", err)
	}
	shutdownWeb(srv)
	log.Print("rfmon: stopped")
}

// ctxSleep waits for d, returning ctx.Err() early if ctx is done first.
func ctxSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func shutdownWeb(srv *http.Server) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("web shutdown: %v", err)
	}
}

func poll(ctx context.Context, runner sweep.Runner, store rrd.Store, specDir string) {
	start := time.Now()
	s, err := runner.Run(ctx)
	if err != nil {
		log.Printf("poll failed: %v", err)
		return
	}
	if len(s.Hz) != sweep.ExpectedBins {
		log.Printf("poll failed: got %d bins, want %d", len(s.Hz), sweep.ExpectedBins)
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
