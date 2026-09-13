# HackRfOne

Tools for the HackRF One attached to this Mac.

## rfmon

Sweeps 1 MHz to 6 GHz every poll, pauses (default 60 s), and repeats. Each
poll records per-band statistics (avg, peak, noise floor, occupancy) in RRD
files with MRTG-style rollups, and a full-spectrum snapshot kept for 7 days.
Graphs are served at http://127.0.0.1:8080.

Requirements: `brew install hackrf rrdtool`, Go 1.26.

```
go build -o rfmon ./cmd/rfmon
./rfmon
```

Flags: `-data ./data`, `-listen 127.0.0.1:8080`, `-pause 60s`, `-passes 5`.

rfmon holds the HackRF while it runs. Stop it (Ctrl-C) before using
`hackrf_info`, `hackrf_transfer`, or other SDR software.

Data layout:

- `data/rrd/<band>.rrd`: 1-minute for 2 days, 5-minute for 14 days,
  30-minute for 62 days, 2-hour for 2 years (AVERAGE and MAX).
- `data/spectrum/YYYY-MM-DD.bin.gz`: one record per poll, format documented
  in `internal/spectrum/spectrum.go`. Bin frequencies in `bins.json`.

Design: `docs/superpowers/specs/2026-09-12-rfmon-design.md`.
