// Package spectrum appends full-spectrum snapshots to one gzip file per UTC
// day and prunes old days.
//
// Each record is its own gzip member: int64 little-endian Unix seconds,
// uint32 little-endian bin count, then one byte per bin holding
// round((dB + 120) * 2) clamped to 0..254, or 255 for no data. Bin center
// frequencies are in bins.json in the same directory.
package spectrum

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hackrfone/internal/sweep"
)

const (
	offsetDB = 120
	noData   = 255
	suffix   = ".bin.gz"
)

type Record struct {
	Time time.Time
	DB   []float64
}

func quantize(db float64) byte {
	if math.IsNaN(db) {
		return noData
	}
	q := math.Round((db + offsetDB) * 2)
	return byte(max(0, min(254, q)))
}

// Encode returns one uncompressed record.
func Encode(t time.Time, s sweep.Spectrum) []byte {
	buf := make([]byte, 12+len(s.DB))
	binary.LittleEndian.PutUint64(buf[0:8], uint64(t.Unix()))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(len(s.DB)))
	for i, v := range s.DB {
		buf[12+i] = quantize(v)
	}
	return buf
}

// Append adds one record to the file for t's UTC day.
func Append(dir string, t time.Time, s sweep.Spectrum) error {
	if err := writeBins(dir, s.Hz); err != nil {
		return err
	}
	name := filepath.Join(dir, t.UTC().Format("2006-01-02")+suffix)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(Encode(t, s)); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeBins(dir string, hz []int64) error {
	want, err := json.Marshal(hz)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "bins.json")
	if have, err := os.ReadFile(p); err == nil && bytes.Equal(have, want) {
		return nil
	}
	return os.WriteFile(p, want, 0o644)
}

// ReadDay decodes every record in one day file.
func ReadDay(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var recs []Record
	header := make([]byte, 12)
	for {
		if _, err := io.ReadFull(zr, header); errors.Is(err, io.EOF) {
			return recs, nil
		} else if err != nil {
			return recs, err
		}
		body := make([]byte, binary.LittleEndian.Uint32(header[8:12]))
		if _, err := io.ReadFull(zr, body); err != nil {
			return recs, err
		}
		r := Record{
			Time: time.Unix(int64(binary.LittleEndian.Uint64(header[0:8])), 0).UTC(),
			DB:   make([]float64, len(body)),
		}
		for i, q := range body {
			if q == noData {
				r.DB[i] = math.NaN()
			} else {
				r.DB[i] = float64(q)/2 - offsetDB
			}
		}
		recs = append(recs, r)
	}
}

// Prune deletes day files dated before (now's UTC day - keepDays).
func Prune(dir string, now time.Time, keepDays int) error {
	cutoff := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -keepDays)
	matches, err := filepath.Glob(filepath.Join(dir, "*"+suffix))
	if err != nil {
		return err
	}
	for _, m := range matches {
		day, err := time.Parse("2006-01-02", strings.TrimSuffix(filepath.Base(m), suffix))
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			if err := os.Remove(m); err != nil {
				return err
			}
		}
	}
	return nil
}
