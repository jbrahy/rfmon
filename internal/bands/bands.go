// Package bands defines the frequency ranges rfmon tracks (US allocations)
// and the statistics computed for each one per poll.
package bands

import (
	"math"
	"slices"

	"hackrfone/internal/sweep"
)

type Band struct {
	Slug    string
	Label   string
	LowMHz  float64
	HighMHz float64
}

var All = []Band{
	{"am-broadcast", "AM broadcast", 1, 1.7},
	{"hf", "HF shortwave / ham", 1.8, 30},
	{"tv-vhf-low", "VHF TV ch 2-6", 54, 88},
	{"fm-broadcast", "FM broadcast", 88, 108},
	{"aviation-vhf", "Aviation VHF", 108, 137},
	{"wx-satellite", "Weather satellites", 137, 138},
	{"ham-2m", "2m ham", 144, 148},
	{"vhf-land-mobile", "VHF land mobile / public safety", 148, 174},
	{"tv-vhf-high", "VHF TV ch 7-13", 174, 216},
	{"mil-aviation", "Military aviation / misc", 216, 400},
	{"federal-uhf", "Federal UHF", 400, 420},
	{"ham-70cm", "70cm ham", 420, 450},
	{"uhf-land-mobile", "UHF land mobile / GMRS", 450, 470},
	{"tv-uhf", "UHF TV ch 14-36", 470, 608},
	{"radio-astronomy", "Radio astronomy", 608, 614},
	{"lte-600", "600 MHz LTE/5G (B71)", 614, 698},
	{"lte-700", "700 MHz LTE (B12/13/14/17)", 698, 806},
	{"public-safety-800", "800 MHz public safety / SMR", 806, 824},
	{"cell-uplink", "Cellular uplink (B5)", 824, 849},
	{"misc-850", "800 MHz misc", 849, 869},
	{"cell-downlink", "Cellular downlink (B5)", 869, 894},
	{"narrowband-900", "900 MHz narrowband", 894, 902},
	{"ism-900", "900 ISM (LoRa, smart meters)", 902, 928},
	{"paging", "Paging / fixed links", 928, 960},
	{"aviation-dme", "Aviation (DME, ADS-B)", 960, 1215},
	{"radar-gnss", "Radar / GNSS L2", 1215, 1300},
	{"l-band-misc", "L-band misc", 1300, 1525},
	{"sat-l-band", "Satellite L-band / GPS L1", 1525, 1660},
	{"met-sat", "Met satellites", 1660, 1710},
	{"aws-uplink", "AWS uplink", 1710, 1780},
	{"pcs-uplink", "PCS uplink", 1850, 1915},
	{"pcs-downlink", "PCS downlink (B2/25)", 1930, 1995},
	{"aws-downlink", "AWS downlink (B4/66)", 2110, 2200},
	{"wcs-siriusxm", "WCS / SiriusXM", 2305, 2360},
	{"ism-2400", "2.4 GHz ISM (WiFi, Bluetooth)", 2400, 2483.5},
	{"lte-2500", "2.5 GHz 5G (B41/n41)", 2496, 2690},
	{"radar-s-band", "S-band radar", 2700, 3450},
	{"nr-3450", "3.45 GHz 5G", 3450, 3550},
	{"cbrs", "CBRS", 3550, 3700},
	{"c-band-5g", "C-band 5G", 3700, 3980},
	{"public-safety-4900", "4.9 GHz public safety", 4940, 4990},
	{"wifi-5g", "5 GHz WiFi", 5150, 5895},
	{"v2x", "V2X", 5895, 5925},
}

// Metrics are one poll's statistics for one band. All dB values are the
// hackrf_sweep power scale (uncalibrated dB).
type Metrics struct {
	Avg   float64 // mean of bin powers in linear terms, as dB
	Peak  float64 // highest bin
	Floor float64 // 10th percentile bin
	Occ   float64 // percent of bins at or above Floor + OccupiedAboveFloorDB
	Bins  int
}

const OccupiedAboveFloorDB = 10

// Compute returns metrics over the non-NaN bins in [LowMHz, HighMHz).
// It returns false when the band has no such bins.
func Compute(s sweep.Spectrum, b Band) (Metrics, bool) {
	lo := int64(math.Round(b.LowMHz * 1e6))
	hi := int64(math.Round(b.HighMHz * 1e6))
	var vals []float64
	for i, hz := range s.Hz {
		if hz >= lo && hz < hi && !math.IsNaN(s.DB[i]) {
			vals = append(vals, s.DB[i])
		}
	}
	if len(vals) == 0 {
		return Metrics{}, false
	}
	slices.Sort(vals)

	var sumMW float64
	for _, v := range vals {
		sumMW += math.Pow(10, v/10)
	}
	m := Metrics{
		Avg:   10 * math.Log10(sumMW/float64(len(vals))),
		Peak:  vals[len(vals)-1],
		Floor: vals[int(0.10*float64(len(vals)-1))],
		Bins:  len(vals),
	}
	occupied := 0
	for _, v := range vals {
		if v >= m.Floor+OccupiedAboveFloorDB {
			occupied++
		}
	}
	m.Occ = 100 * float64(occupied) / float64(len(vals))
	return m, true
}
