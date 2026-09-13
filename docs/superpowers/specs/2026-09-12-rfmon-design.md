# rfmon: HackRF spectrum monitor with MRTG-style statistics

Date: 2026-09-12
Status: approved in chat, pending spec review

## Purpose

Continuously record what RF energy is present from 1 MHz to 6 GHz, using the
HackRF One attached to a Mac, and keep long-term statistics that can be
browsed like MRTG graphs.

## Where it runs

On the Mac. The HackRF is a USB device attached to the laptop; a cloud server
cannot host USB hardware. The web server binds to 127.0.0.1 only.

## Poll loop

1. Run `hackrf_sweep -f 1:6000 -w 250000 -l 32 -g 20 -N 5` and capture stdout.
2. Parse rows (`date, time, hz_low, hz_high, bin_width, num_samples, dB...`).
   Bin center = `hz_low + (i + 0.5) * bin_width`. Key each bin by center
   rounded to the nearest kHz.
3. Per bin, take the median dB across the 5 passes.
4. Drop bins within 250 kHz of an exact multiple of 20 MHz (HackRF / USB
   adapter self-interference observed on 2026-09-12 in two grid-shifted sweeps).
5. Compute band metrics and write them to RRD files.
6. Append a spectrum snapshot.
7. Sleep 60 seconds, then repeat.

A failed poll (hackrf_sweep exits non-zero, device missing, parse error) is
logged to stderr and the loop continues after the pause. RRD records the gap
as unknown once the heartbeat expires.

The poller holds the HackRF exclusively while running.

## Bands

The band table from the 2026-09-12 survey (US allocations), about 40 bands,
each with a slug name (e.g. `fm-broadcast`, `lte-700`), low MHz, high MHz, and
a display label. Bands are defined in code.

Per band, per poll:

| Metric | Definition |
|---|---|
| `avg` | Mean of bin powers in linear (mW) terms, converted back to dB |
| `peak` | Max bin dB |
| `floor` | 10th percentile of bin dB |
| `occ` | Percent of bins with dB >= floor + 10 |

A band with zero bins after spur removal is skipped for that poll.

## RRD storage

One file per band: `data/rrd/<slug>.rrd`, created on first use.

- Step: 60 s. Heartbeat: 180 s.
- Data sources: `avg`, `peak`, `floor` (GAUGE, -150..50), `occ` (GAUGE, 0..100).
- RRAs, each for AVERAGE and MAX:
  - 1 step x 2880 rows (1-minute, 2 days)
  - 5 steps x 4032 rows (5-minute, 14 days)
  - 30 steps x 2976 rows (30-minute, 62 days)
  - 120 steps x 8760 rows (2-hour, 2 years)

All RRD operations shell out to the `rrdtool` CLI (Homebrew). No cgo.

## Spectrum snapshots

- File per UTC day: `data/spectrum/YYYY-MM-DD.bin.gz`. Each poll appends one
  gzip member (concatenated gzip members form a valid stream).
- Record: `int64` Unix seconds (little endian), `uint32` bin count, then one
  `uint8` per bin: `clamp(round((dB + 120) * 2), 0, 254)`; 255 = no data
  (spur-dropped bin). Bin order is ascending frequency.
- `data/spectrum/bins.json` stores the bin center frequencies (Hz) for the
  record layout; rewritten if the layout changes.
- Files older than 7 days are deleted after each poll.

## Web server

`127.0.0.1:8080` (flag `-listen`).

- `GET /` lists all bands with the daily graph for each.
- `GET /band/<slug>` shows day, week, month, and year graphs.
- `GET /graph/<slug>-<period>.png` renders via `rrdtool graph -` to stdout.
  Periods: `day` (-1d), `week` (-1w), `month` (-1m), `year` (-1y). Each graph
  plots `avg` and `peak`, with `floor` as a line. Rendered PNGs are cached in
  memory for 60 s.

## Flags

| Flag | Default |
|---|---|
| `-data` | `./data` |
| `-listen` | `127.0.0.1:8080` |
| `-pause` | `60s` |
| `-passes` | `5` |

## Code layout

| Path | Responsibility |
|---|---|
| `cmd/rfmon/main.go` | Flags, poll loop, wiring |
| `internal/sweep` | Run hackrf_sweep, parse output, median per bin, spur filter |
| `internal/bands` | Band table and metric computation |
| `internal/rrd` | rrdtool create / update / graph |
| `internal/spectrum` | Snapshot encode, append, prune |
| `internal/web` | HTTP handlers and graph cache |

## Testing

- `sweep`: parser test against a captured real hackrf_sweep fixture; median
  and spur-filter tests.
- `bands`: metric math on hand-built bin sets (linear averaging, floor,
  occupancy, empty band).
- `spectrum`: encode/decode round trip, multi-member gzip append, prune by date.
- `rrd`: integration test against real `rrdtool` in a temp dir (create, update,
  fetch returns the written value).
- `web`: handler tests with a stub graph renderer.
- Live check: one poll against the HackRF, then `rrdtool fetch` shows values
  and `/` renders.

## Out of scope

Waterfall/heatmap views of spectrum snapshots, launchd autostart, alerting,
signal demodulation.
