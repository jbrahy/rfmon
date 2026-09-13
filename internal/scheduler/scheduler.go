// Package scheduler runs one repeating cycle of dwell steps that share the
// single HackRF: three WiFi channels, one BLE sweep across all
// advertising/data channels, and a periodic spectrum sweep.
package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/jbrahy/rfmon/internal/store"
)

// wifiGrace is how long the scheduler waits after a WiFi dwell ends before
// draining the aggregator, so frames decoded just after hackrf_transfer
// stops are still attributed to the dwell that captured them.
const wifiGrace = 1 * time.Second

// Step describes one tuning of the radio: a WiFi channel and its center
// frequency, or the BLE sweep (Channel holds the number of channels
// covered, not a single channel number).
type Step struct {
	Kind      string
	Channel   int
	CenterMHz int
}

// Window is the time span whose frames are attributed to a dwell.
type Window struct {
	Start time.Time
	End   time.Time
}

var wifiSteps = []Step{
	{Kind: "wifi", Channel: 1, CenterMHz: 2412},
	{Kind: "wifi", Channel: 6, CenterMHz: 2437},
	{Kind: "wifi", Channel: 11, CenterMHz: 2462},
}

var bleStep = Step{Kind: "ble", Channel: 16, CenterMHz: 2427}

// Deps are the scheduler's function-typed dependencies. They are the only
// way the scheduler touches the radio, the aggregators, or the store, so
// Run is fully testable with fakes and an injected clock.
type Deps struct {
	// WifiDwell runs one hackrf_transfer dwell into the OFDM supervisor and
	// returns when it ends.
	WifiDwell func(ctx context.Context, step Step) error
	// DrainWifi returns the frames whose read time falls in window.
	DrainWifi func(window Window) []store.WifiSighting
	// BleDwell runs one ice9 dwell and returns its aggregated sightings.
	BleDwell func(ctx context.Context, step Step) ([]store.BleSighting, error)
	// Sweep runs the existing spectrum poll.
	Sweep func(ctx context.Context) error
	// RecordDwell writes one dwells row plus its sightings.
	RecordDwell func(store.Dwell, []store.WifiSighting, []store.BleSighting) error
	// UpdateCounts recomputes the counts RRDs as of now.
	UpdateCounts func(now time.Time) error
	// Now returns the current time.
	Now func() time.Time
	// Sleep waits for d, returning early with ctx.Err() if ctx is done
	// first. Injected so tests never sleep real time.
	Sleep func(ctx context.Context, d time.Duration) error

	Dwell      time.Duration
	Pause      time.Duration
	SweepEvery time.Duration
}

// Run loops cycles of wifi ch1, ch6, ch11, then a BLE dwell, then either a
// spectrum sweep (if SweepEvery has elapsed since the last one) or a pause,
// until ctx is done. The first cycle always sweeps, since lastSweep starts
// far in the past. A failed step is logged, recorded as a failed dwell, and
// the cycle continues; cancellation between or within steps returns.
func Run(ctx context.Context, d Deps) {
	var lastSweep time.Time // zero value: far in the past, so cycle 1 sweeps.

	for {
		for _, step := range wifiSteps {
			if !runWifiStep(ctx, d, step) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !runBleStep(ctx, d) {
			return
		}
		if ctx.Err() != nil {
			return
		}

		if d.Now().Sub(lastSweep) >= d.SweepEvery {
			if !runSweepStep(ctx, d) {
				return
			}
			lastSweep = d.Now()
		} else if err := d.Sleep(ctx, d.Pause); err != nil {
			return
		}
	}
}

// runWifiStep runs one WiFi dwell step and records it. It returns false if
// the context is done and Run should stop.
func runWifiStep(ctx context.Context, d Deps, step Step) bool {
	if ctx.Err() != nil {
		return false
	}

	start := d.Now()
	err := d.WifiDwell(ctx, step)
	end := d.Now()
	if err != nil {
		log.Printf("scheduler: wifi ch%d dwell: %v", step.Channel, err)
	}

	// Frames may still be decoded just after hackrf_transfer stops, so
	// drain after the grace period regardless of whether the dwell itself
	// reported an error.
	cont := true
	var sightings []store.WifiSighting
	if slErr := d.Sleep(ctx, wifiGrace); slErr != nil {
		cont = false
	} else {
		sightings = d.DrainWifi(Window{Start: start, End: end.Add(wifiGrace)})
	}

	// The parser leaves Channel unset (0) when a frame has no DS
	// Parameter Set element (e.g. probe requests); fall back to the
	// dwell's own channel so wifi_sightings.channel, which is NOT NULL,
	// never stores a misleading 0.
	for i := range sightings {
		if sightings[i].Channel == 0 {
			sightings[i].Channel = step.Channel
		}
	}

	status, errMsg := dwellStatus(err)
	recordDwell(d, store.Dwell{
		Kind:       step.Kind,
		Channel:    step.Channel,
		CenterMHz:  step.CenterMHz,
		StartedAt:  start,
		EndedAt:    end,
		Status:     status,
		Error:      errMsg,
		FrameCount: sumWifiFrames(sightings),
	}, sightings, nil)

	return cont
}

// runBleStep runs the BLE dwell step and records it. It returns false if
// the context is done and Run should stop.
func runBleStep(ctx context.Context, d Deps) bool {
	if ctx.Err() != nil {
		return false
	}

	start := d.Now()
	sightings, err := d.BleDwell(ctx, bleStep)
	end := d.Now()
	if err != nil {
		log.Printf("scheduler: ble dwell: %v", err)
	}

	status, errMsg := dwellStatus(err)
	recordDwell(d, store.Dwell{
		Kind:       bleStep.Kind,
		Channel:    bleStep.Channel,
		CenterMHz:  bleStep.CenterMHz,
		StartedAt:  start,
		EndedAt:    end,
		Status:     status,
		Error:      errMsg,
		FrameCount: sumBlePackets(sightings),
	}, nil, sightings)

	return true
}

// runSweepStep runs the spectrum sweep step, records it, and updates the
// counts RRDs. It returns false if the context is done and Run should stop.
func runSweepStep(ctx context.Context, d Deps) bool {
	if ctx.Err() != nil {
		return false
	}

	start := d.Now()
	err := d.Sweep(ctx)
	end := d.Now()
	if err != nil {
		log.Printf("scheduler: sweep: %v", err)
	}

	status, errMsg := dwellStatus(err)
	recordDwell(d, store.Dwell{
		Kind:      "sweep",
		StartedAt: start,
		EndedAt:   end,
		Status:    status,
		Error:     errMsg,
	}, nil, nil)

	if uErr := d.UpdateCounts(d.Now()); uErr != nil {
		log.Printf("scheduler: update counts: %v", uErr)
	}

	return true
}

// recordDwell writes dw via d.RecordDwell, logging rather than failing the
// cycle if the write itself errors.
func recordDwell(d Deps, dw store.Dwell, wifi []store.WifiSighting, ble []store.BleSighting) {
	if err := d.RecordDwell(dw, wifi, ble); err != nil {
		log.Printf("scheduler: record %s dwell: %v", dw.Kind, err)
	}
}

// dwellStatus turns a step error into the status/error pair stored on a
// dwells row.
func dwellStatus(err error) (status, message string) {
	if err != nil {
		return "failed", err.Error()
	}
	return "ok", ""
}

func sumWifiFrames(sightings []store.WifiSighting) int {
	total := 0
	for _, s := range sightings {
		total += s.FrameCount
	}
	return total
}

func sumBlePackets(sightings []store.BleSighting) int {
	total := 0
	for _, s := range sightings {
		total += s.PacketCount
	}
	return total
}
