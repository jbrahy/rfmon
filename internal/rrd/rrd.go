// Package rrd stores band metrics in RRDtool files and renders graphs by
// running the rrdtool command.
package rrd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hackrfone/internal/bands"
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
	// 1-minute for 2 days, 5-minute for 14 days, 30-minute for 62 days,
	// 2-hour for 2 years.
	for _, rra := range []string{"1:2880", "5:4032", "30:2976", "120:8760"} {
		args = append(args, "RRA:AVERAGE:0.5:"+rra, "RRA:MAX:0.5:"+rra)
	}
	return s.run(nil, args...)
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

func (s Store) run(stdout io.Writer, args ...string) error {
	cmd := exec.Command(s.Bin, args...)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rrdtool %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
