// Package sweep runs hackrf_sweep and turns its CSV output into one
// spectrum: the median power per frequency bin across all passes.
package sweep

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Spectrum holds bins in ascending frequency order. DB is NaN for bins with
// no usable data.
type Spectrum struct {
	Hz []int64
	DB []float64
}

// Parse reads hackrf_sweep CSV rows
// (date, time, hz_low, hz_high, bin_width, num_samples, dB...) and returns
// the median dB per bin. Bins are keyed by center frequency rounded to 1 kHz.
func Parse(r io.Reader) (Spectrum, error) {
	readings := map[int64][]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		fields := strings.Split(text, ",")
		if len(fields) < 7 {
			return Spectrum{}, fmt.Errorf("line %d: %d fields, want at least 7", line, len(fields))
		}
		hzLow, err := strconv.ParseFloat(strings.TrimSpace(fields[2]), 64)
		if err != nil {
			return Spectrum{}, fmt.Errorf("line %d: hz_low: %w", line, err)
		}
		width, err := strconv.ParseFloat(strings.TrimSpace(fields[4]), 64)
		if err != nil {
			return Spectrum{}, fmt.Errorf("line %d: bin_width: %w", line, err)
		}
		for i, field := range fields[6:] {
			v, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
			if err != nil {
				return Spectrum{}, fmt.Errorf("line %d: bin %d: %w", line, i, err)
			}
			center := hzLow + (float64(i)+0.5)*width
			key := int64(math.Round(center/1000)) * 1000
			vals := readings[key]
			if !math.IsInf(v, 0) && !math.IsNaN(v) {
				vals = append(vals, v)
			}
			readings[key] = vals
		}
	}
	if err := sc.Err(); err != nil {
		return Spectrum{}, err
	}
	if len(readings) == 0 {
		return Spectrum{}, errors.New("no sweep rows")
	}

	s := Spectrum{Hz: make([]int64, 0, len(readings))}
	for hz := range readings {
		s.Hz = append(s.Hz, hz)
	}
	slices.Sort(s.Hz)
	s.DB = make([]float64, len(s.Hz))
	for i, hz := range s.Hz {
		s.DB[i] = median(readings[hz])
	}
	return s, nil
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

const (
	SpurStepHz   = 20_000_000
	SpurRadiusHz = 250_000
)

// DropSpurs marks bins within SpurRadiusHz of a multiple of SpurStepHz as
// no-data. Those spikes are HackRF / USB adapter self-interference, confirmed
// on 2026-09-12 by two sweeps whose tuning grids were offset by 10 MHz.
func DropSpurs(s Spectrum) {
	for i, hz := range s.Hz {
		off := hz % SpurStepHz
		if off < SpurRadiusHz || SpurStepHz-off < SpurRadiusHz {
			s.DB[i] = math.NaN()
		}
	}
}

// Runner executes hackrf_sweep over 1 MHz to 6 GHz.
type Runner struct {
	Bin     string
	Passes  int
	Timeout time.Duration
}

// Run performs one poll and returns the parsed spectrum.
func (r Runner) Run(ctx context.Context) (Spectrum, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Bin,
		"-f", "1:6000", "-w", "250000", "-l", "32", "-g", "20",
		"-N", strconv.Itoa(r.Passes))
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Spectrum{}, fmt.Errorf("hackrf_sweep: %w: %s", err, lastLine(stderr.String()))
	}
	return Parse(bytes.NewReader(out))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
