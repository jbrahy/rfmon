# Architecture

rfmon is one Go program with no cgo. It drives two external command-line
tools: `hackrf_sweep` to capture spectrum and `rrdtool` to store and graph
statistics. Everything else (parsing, statistics, snapshot files, HTTP) is
plain Go standard library.

```
                 +--------------+
                 | hackrf_sweep |
                 +------+-------+
                        | CSV on stdout
                        v
cmd/rfmon  --->  sweep.Runner.Run  --->  sweep.Spectrum (median per bin)
   |                                           |
   |                               bin count check, sweep.DropSpurs
   |                                           |
   |                   +-----------------------+----------------------+
   |                   v                                              v
   |           bands.Compute (x43)                           spectrum.Append
   |                   |                                     spectrum.Prune
   |                   v                                              |
   |           rrd.Store.Update  ---> rrdtool update         data/spectrum/
   |                                  data/rrd/<slug>.rrd
   |
   +--> web.Server  --->  rrd.Store.Graph  ---> rrdtool graph -  ---> PNG
```

## Package map

### `internal/sweep`

Runs `hackrf_sweep` and turns its output into a single spectrum.

| Identifier | Description |
|---|---|
| `type Spectrum struct { Hz []int64; DB []float64 }` | Bins in ascending frequency order. `Hz` is the bin center rounded to 1 kHz. `DB` is NaN for bins with no usable data. |
| `func Parse(r io.Reader) (Spectrum, error)` | Reads `hackrf_sweep` CSV rows (`date, time, hz_low, hz_high, bin_width, num_samples, dB...`) and returns the median dB per bin across all rows that cover it. Infinite and NaN readings are ignored. |
| `func DropSpurs(s Spectrum)` | Sets `DB` to NaN for bins within `SpurRadiusHz` of a multiple of `SpurStepHz`. Modifies `s.DB` in place. |
| `const SpurStepHz = 20_000_000`, `const SpurRadiusHz = 250_000` | Spur filter parameters. |
| `const ExpectedBins = 25200` | Bins in one complete sweep with the fixed arguments (1200 rows of 21 bins). |
| `type Runner struct { Bin string; Passes int; Timeout time.Duration }` | How to run the sweep tool. |
| `func (r Runner) Run(ctx context.Context) (Spectrum, error)` | Runs `<Bin> -f 1:6000 -w 250000 -l 32 -g 20 -N <Passes>` with a timeout, then parses stdout. |

### `internal/bands`

The band table and the per-band statistics.

| Identifier | Description |
|---|---|
| `type Band struct { Slug, Label string; LowMHz, HighMHz float64 }` | One frequency range. The range is `[LowMHz, HighMHz)`. |
| `var All []Band` | The 43 US bands, in frequency order. See [bands.md](bands.md). |
| `type Metrics struct { Avg, Peak, Floor, Occ float64; Bins int }` | One poll's statistics for one band. |
| `const OccupiedAboveFloorDB = 10` | Occupancy threshold above the floor, in dB. |
| `func Compute(s sweep.Spectrum, b Band) (Metrics, bool)` | Computes metrics over the non-NaN bins in the band. Returns `false` when the band has none. |

### `internal/rrd`

Stores metrics in RRD files and renders graphs, by running the `rrdtool`
command.

| Identifier | Description |
|---|---|
| `type Store struct { Dir, Bin string }` | One RRD file per band slug in `Dir`, using the `rrdtool` binary named by `Bin`. |
| `type Period struct { Name, Start string }` | A graph time span. |
| `var Periods []Period` | `day` (`-1d`), `week` (`-1w`), `month` (`-1m`), `year` (`-1y`). |
| `func (s Store) Update(slug string, t time.Time, m bands.Metrics) error` | Creates `<Dir>/<slug>.rrd` on first use, then runs `rrdtool update` with `t` and the four metrics. |
| `func (s Store) Graph(slug, label, period string) ([]byte, error)` | Runs `rrdtool graph -` and returns a 600x200 PNG of avg, peak, and floor. |

Every `rrdtool` invocation has a 10 second timeout. The layout of the files
is in [data-formats.md](data-formats.md#rrd-files).

### `internal/spectrum`

Writes and reads full-spectrum snapshots.

| Identifier | Description |
|---|---|
| `type Record struct { Time time.Time; DB []float64 }` | One decoded snapshot. `DB` is NaN for no-data bins. |
| `func Encode(t time.Time, s sweep.Spectrum) []byte` | Returns one uncompressed record. |
| `func Append(dir string, t time.Time, s sweep.Spectrum) error` | Writes `bins.json` if it changed, then appends one gzip member to `<dir>/<UTC date of t>.bin.gz`. |
| `func ReadDay(path string) ([]Record, error)` | Decodes every record in one day file. |
| `func Prune(dir string, now time.Time, keepDays int) error` | Deletes day files dated before `now`'s UTC day minus `keepDays`. |

The byte layout is in [data-formats.md](data-formats.md#spectrum-snapshots).

### `internal/web`

Serves the HTML pages and graph PNGs.

| Identifier | Description |
|---|---|
| `type Grapher interface { Graph(slug, label, period string) ([]byte, error) }` | Anything that renders a graph. `rrd.Store` satisfies it; tests use a stub. |
| `type Server struct` | Holds the grapher, the band list, and a graph cache. |
| `func New(g Grapher, bs []bands.Band) *Server` | Creates a server with a 60 second graph cache. |
| `func (s *Server) Handler() http.Handler` | Routes `GET /`, `GET /band/{slug}`, and `GET /graph/{file}`. Unknown bands and periods return 404. A graph render error returns 500. |

### `cmd/rfmon`

The command. It parses flags, checks that `hackrf_sweep` and `rrdtool` are
in `PATH`, creates `<data>/rrd` and `<data>/spectrum`, opens the web
listener, starts serving in a goroutine, and runs the poll loop until it
receives SIGINT or SIGTERM. Snapshot retention is the constant
`keepSpectrumDays = 7`. The `hackrf_sweep` timeout is 2 minutes.

## One poll

1. Record the start time. This one timestamp is used for the RRD updates, the
   snapshot record, and the choice of day file.
2. Run `hackrf_sweep -f 1:6000 -w 250000 -l 32 -g 20 -N <passes>` and capture
   stdout and stderr.
   - `-f 1:6000` sweeps 1 MHz to 6000 MHz.
   - `-w 250000` requests 250 kHz bins. `hackrf_sweep` rounds this to
     238095.24 Hz (20 MHz sample rate / 84 FFT bins), and each 5 MHz row
     carries 21 bins.
   - `-l 32` and `-g 20` set the LNA (IF) gain to 32 dB and the VGA
     (baseband) gain to 20 dB.
   - `-N` is the number of sweep passes.
3. Parse the CSV. For each value in a row, the bin center is
   `hz_low + (i + 0.5) * bin_width`, rounded to the nearest kHz. Readings are
   grouped by bin across passes and reduced to their median. A bin with no
   finite reading is NaN.
4. Reject the poll unless it has exactly 25200 bins.
5. Mark the 600 bins within 250 kHz of a multiple of 20 MHz as NaN.
6. For each of the 43 bands, compute metrics over the band's non-NaN bins.
   Skip a band that has none. Otherwise call `rrd.Store.Update`, which
   creates the RRD file if it does not exist.
7. Append the spectrum to `<data>/spectrum/YYYY-MM-DD.bin.gz` for the start
   time's UTC date, and write `bins.json` if the bin layout changed.
8. Delete snapshot files older than the retention window.
9. Log `poll ok: <bins> bins, <bands updated> bands, <duration>`.

The loop then waits `-pause` (or until shutdown) and starts the next poll.
The pause is measured from the end of a poll, so the cycle is poll time plus
pause.

## Failure handling

rfmon exits for problems at startup, and if the HTTP server stops with an
error. Once polling, every other failure is logged and the loop continues.

| Failure | What happens |
|---|---|
| `hackrf_sweep` or `rrdtool` not in `PATH` | Exits at startup: `<tool> not found in PATH: brew install hackrf rrdtool`. |
| Cannot create the data directories | Exits at startup with the OS error. |
| Web listen address unavailable | Exits at startup with the listen error, before any poll runs. |
| `hackrf_sweep` exits non-zero, cannot start, or exceeds 2 minutes | Logs `poll failed: hackrf_sweep: <error>: <last line of its stderr>`. Nothing is written for that poll. |
| CSV cannot be parsed | Logs `poll failed: line <n>: ...` (or `no sweep rows`). Nothing is written. |
| Sweep is incomplete | Logs `poll failed: got <n> bins, want 25200`. Nothing is written. |
| A band has no usable bins | That band is skipped for the poll. It is not counted in the `poll ok` band total. |
| `rrdtool update` fails for a band | Logs `rrd <slug>: rrdtool update: <error>: <rrdtool stderr>`. Other bands and the snapshot are still written. |
| Snapshot append fails | Logs `spectrum: <error>`. RRD updates for that poll are already done. |
| Snapshot pruning fails | Logs `spectrum prune: <error>`. |
| Graph render fails | Logs `web: graph <file>: <error>` and returns HTTP 500 `graph failed`. |

When polls fail or rfmon is not running, the RRD files record nothing. Once
more than 180 seconds (the heartbeat) pass without an update, rrdtool treats
that interval as unknown, and graphs show a gap.

`hackrf_sweep` stderr is only shown for failed polls. Warnings it prints
during a successful poll are discarded.

## Shutdown

rfmon listens for SIGINT (Ctrl-C) and SIGTERM.

- During the pause, it stops waiting immediately.
- During a sweep, the context is cancelled and `hackrf_sweep` is sent
  SIGINT, which lets it run its own teardown and release the HackRF. If it
  has not exited 3 seconds later, it is killed. The interrupted poll is
  logged as `poll failed` and writes nothing.
- RRD updates and the snapshot write for a poll that has already finished
  sweeping are not cancelled. Each `rrdtool` call is bounded by its 10 second
  timeout.

After the poll loop exits, the web server is shut down with a 5 second
limit for in-flight requests, rfmon logs `rfmon: stopped`, and the process
exits with status 0.

## Design decisions

### rrdtool CLI instead of cgo bindings

Calling the `rrdtool` binary keeps rfmon a pure Go build: `go install` works
without a C compiler or librrd headers, and cross-compiling is unaffected.
`rrdtool` has to be installed anyway, since it also renders the graphs. The
cost is one process per update, 43 per poll. The measured 4.9 s poll time
includes those updates. The 10 second timeout keeps a hung `rrdtool` from
stalling the poll loop or a web request.

### Medians across passes

A single `hackrf_sweep` pass is noisy, and bursty transmitters such as WiFi,
Bluetooth, and radar land in some passes and not others. The median of 5
passes rejects values that appear in only one or two passes, and unlike a
mean it is always a value that was actually observed. The trade-off is that
`peak` is the highest median bin, not the highest single reading, so a burst
that shows up in fewer than half the passes does not move it.

### Spur filter

On the development HackRF, sweeps showed narrow spikes at exact multiples of
20 MHz (360, 400, 1000, 1020 MHz, and so on). Two sweeps whose tuning grids
were offset by 10 MHz from each other both showed them at the same
frequencies. A comb that regular, present across the whole range, is
attributed to self-interference from the HackRF's own clocking or its USB
connection rather than to transmitters.
Left in, they would set `peak` and inflate `occ` in every band that contains
a multiple of 20 MHz. Dropping bins within 250 kHz (two bins per spur, 600 of
25200 in total, 2.4 percent) removes them. The bins are stored as no-data in
the snapshot so the removal is visible.

### Linear averaging

dB values are logarithmic. The arithmetic mean of dB values understates power
whenever a band holds a few strong carriers over a quiet floor. rfmon
converts each bin to linear power, averages, and converts back. For two bins
at -40 dB and -80 dB, the linear mean is -43.0 dB, while the arithmetic mean
of the dB values would be -60 dB.

### Listening on 127.0.0.1

The web server has no authentication and no TLS, and each uncached graph
request starts an `rrdtool` process. A spectrum record of a fixed location
can also say something about that location. Binding to loopback keeps it
private by default. To view it from another machine, use an SSH tunnel such
as `ssh -L 8080:127.0.0.1:8080 <host>`, or set `-listen` deliberately.

### Opening the listener before polling

The listener is opened synchronously before the first poll, so a port
conflict stops rfmon at startup with a clear error instead of failing later in
a background goroutine after the HackRF is already in use.

### Rejecting partial sweeps

If the HackRF is unplugged or the USB link fails mid-sweep, `hackrf_sweep`
can return only some of the rows. Writing that poll would give some bands
metrics from a fraction of their bins and write a short snapshot record, so
any poll without exactly 25200 bins is discarded.
