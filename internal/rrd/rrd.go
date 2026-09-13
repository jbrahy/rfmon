// Package rrd stores band metrics in RRDtool files and renders graphs by
// running the rrdtool command.
package rrd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jbrahy/rfmon/internal/bands"
)

// Store keeps one RRD file per band slug in Dir. Dir must not contain ':'
// because rrdtool DEF arguments use ':' as a separator.
type Store struct {
	Dir string
	Bin string
}

type Period struct {
	Name  string
	Start string
}

// Periods are the graph time spans, in display order.
var Periods = []Period{{"day", "-1d"}, {"week", "-1w"}, {"month", "-1m"}, {"year", "-1y"}}

func (s Store) path(slug string) string {
	return filepath.Join(s.Dir, slug+".rrd")
}

// Update records one poll for a band, creating its RRD file on first use.
func (s Store) Update(slug string, t time.Time, m bands.Metrics) error {
	p := s.path(slug)
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		if err := s.create(p, t); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	value := fmt.Sprintf("%d:%.2f:%.2f:%.2f:%.2f", t.Unix(), m.Avg, m.Peak, m.Floor, m.Occ)
	return s.run(nil, "update", p, value)
}

func (s Store) create(p string, t time.Time) error {
	args := []string{
		"create", p,
		"--start", strconv.FormatInt(t.Unix()-60, 10),
		"--step", "60",
		"DS:avg:GAUGE:180:-150:50",
		"DS:peak:GAUGE:180:-150:50",
		"DS:floor:GAUGE:180:-150:50",
		"DS:occ:GAUGE:180:0:100",
	}
	args = append(args, rraArgs()...)
	return s.run(nil, args...)
}

// rraArgs is the RRA list shared by every RRD file this package creates:
// 1-minute resolution for 2 days, 5-minute for 14 days, 30-minute for 62
// days, 2-hour for 2 years, each kept as both AVERAGE and MAX.
func rraArgs() []string {
	var args []string
	for _, rra := range []string{"1:2880", "5:4032", "30:2976", "120:8760"} {
		args = append(args, "RRA:AVERAGE:0.5:"+rra, "RRA:MAX:0.5:"+rra)
	}
	return args
}

// Graph renders a PNG of avg, peak, and floor for one band over a period
// named in Periods.
func (s Store) Graph(slug, label, period string) ([]byte, error) {
	start := ""
	for _, p := range Periods {
		if p.Name == period {
			start = p.Start
		}
	}
	if start == "" {
		return nil, fmt.Errorf("unknown period %q", period)
	}
	p := s.path(slug)
	var out bytes.Buffer
	err := s.run(&out, "graph", "-",
		"--imgformat", "PNG",
		"--start", start,
		"--title", label+" ("+period+")",
		"--vertical-label", "dB",
		"--width", "600", "--height", "200",
		"DEF:avg="+p+":avg:AVERAGE",
		"DEF:peak="+p+":peak:MAX",
		"DEF:floor="+p+":floor:AVERAGE",
		"LINE1:peak#d62728:peak (max)",
		"LINE2:avg#1f77b4:avg",
		"LINE1:floor#7f7f7f:floor")
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// countsPalette gives GraphCounts a distinct color per DS, cycling if there
// are more DS names than colors.
var countsPalette = []string{"#1f77b4", "#d62728", "#2ca02c", "#9467bd", "#ff7f0e", "#17becf"}

// UpdateCounts records one poll of per-minute device counts for "wifi" or
// "ble", creating <Dir>/<name>.rrd on first use.
//
// RRD fixes a file's DS list at creation, so the first call for a given
// name determines it permanently: UpdateCounts sorts ds's keys and creates
// one DS per key, in that sorted order. Every later call for the same name
// must pass the same key set, since ds is again sorted to build the update
// value string, which keeps update order stable across calls regardless of
// map iteration order.
func (s Store) UpdateCounts(name string, t time.Time, ds map[string]float64) error {
	p := s.path(name)
	names := sortedKeys(ds)
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		if err := s.createCounts(p, t, names); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	value := strconv.FormatInt(t.Unix(), 10)
	for _, n := range names {
		value += fmt.Sprintf(":%.2f", ds[n])
	}
	return s.run(nil, "update", p, value)
}

func (s Store) createCounts(p string, t time.Time, names []string) error {
	args := []string{
		"create", p,
		"--start", strconv.FormatInt(t.Unix()-60, 10),
		"--step", "60",
	}
	for _, n := range names {
		args = append(args, "DS:"+n+":GAUGE:180:0:U")
	}
	args = append(args, rraArgs()...)
	return s.run(nil, args...)
}

func sortedKeys(m map[string]float64) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// GraphCounts renders a PNG with one line per DS in dsNames, drawn from the
// AVERAGE RRA, over a period named in Periods.
func (s Store) GraphCounts(name, title string, dsNames []string, period string) ([]byte, error) {
	start := ""
	for _, p := range Periods {
		if p.Name == period {
			start = p.Start
		}
	}
	if start == "" {
		return nil, fmt.Errorf("unknown period %q", period)
	}
	p := s.path(name)
	args := []string{
		"graph", "-",
		"--imgformat", "PNG",
		"--start", start,
		"--title", title + " (" + period + ")",
		"--width", "600", "--height", "200",
	}
	for _, ds := range dsNames {
		args = append(args, fmt.Sprintf("DEF:%s=%s:%s:AVERAGE", ds, p, ds))
	}
	for i, ds := range dsNames {
		args = append(args, fmt.Sprintf("LINE1:%s%s:%s", ds, countsPalette[i%len(countsPalette)], ds))
	}
	var out bytes.Buffer
	if err := s.run(&out, args...); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (s Store) run(stdout io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Bin, args...)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rrdtool %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
