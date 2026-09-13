package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jbrahy/rfmon/internal/store"
)

func noopWifi(context.Context, Step) error                             { return nil }
func noopDrain(Window) []store.WifiSighting                            { return nil }
func noopBle(context.Context, Step) ([]store.BleSighting, error)       { return nil, nil }
func noopSweep(context.Context) error                                  { return nil }
func noopRecord(store.Dwell, []store.WifiSighting, []store.BleSighting) error {
	return nil
}
func noopSleep(context.Context, time.Duration) error { return nil }
func noopUpdateCounts(time.Time) error               { return nil }

func TestRun_OneCycleWifiAndBle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var wifiCalls []Step
	var bleCalls int
	var recorded []store.Dwell
	var sweepCalls int

	d := Deps{
		WifiDwell: func(ctx context.Context, step Step) error {
			wifiCalls = append(wifiCalls, step)
			return nil
		},
		DrainWifi: noopDrain,
		BleDwell: func(ctx context.Context, step Step) ([]store.BleSighting, error) {
			bleCalls++
			// Stop before step 5 runs, so this test only asserts on the
			// four wifi/ble dwells of a single cycle.
			cancel()
			return nil, nil
		},
		Sweep: func(ctx context.Context) error {
			sweepCalls++
			return nil
		},
		RecordDwell: func(dw store.Dwell, w []store.WifiSighting, b []store.BleSighting) error {
			recorded = append(recorded, dw)
			return nil
		},
		UpdateCounts: noopUpdateCounts,
		Now:          func() time.Time { return clock },
		Sleep:        noopSleep,
		Dwell:        3 * time.Second,
		Pause:        5 * time.Second,
		SweepEvery:   time.Hour,
	}

	Run(ctx, d)

	if len(wifiCalls) != 3 {
		t.Fatalf("wifi calls = %d, want 3", len(wifiCalls))
	}
	wantChannels := []int{1, 6, 11}
	wantCenters := []int{2412, 2437, 2462}
	for i := range wantChannels {
		if wifiCalls[i].Channel != wantChannels[i] || wifiCalls[i].CenterMHz != wantCenters[i] {
			t.Errorf("wifi call %d = %+v, want channel %d center %d", i, wifiCalls[i], wantChannels[i], wantCenters[i])
		}
		if wifiCalls[i].Kind != "wifi" {
			t.Errorf("wifi call %d kind = %q, want %q", i, wifiCalls[i].Kind, "wifi")
		}
	}
	if bleCalls != 1 {
		t.Fatalf("ble calls = %d, want 1", bleCalls)
	}
	if len(recorded) != 4 {
		t.Fatalf("recorded dwells = %d, want 4", len(recorded))
	}
	if sweepCalls != 0 {
		t.Errorf("sweep calls = %d, want 0 (cancelled before step 5)", sweepCalls)
	}
	for i, dw := range recorded[:3] {
		if dw.Status != "ok" {
			t.Errorf("recorded[%d].Status = %q, want ok", i, dw.Status)
		}
	}
	if recorded[3].Kind != "ble" || recorded[3].CenterMHz != 2427 || recorded[3].Channel != 16 {
		t.Errorf("recorded[3] = %+v, want ble dwell centered 2427 over 16 channels", recorded[3])
	}
}

func TestRun_SweepEveryLarge_SleepsAndSkipsSweepAfterFirstCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pause := 5 * time.Second

	var sweepCalls, updateCountsCalls, pauseSleeps, step5Done int
	done := func() {
		step5Done++
		if step5Done >= 2 {
			cancel()
		}
	}

	d := Deps{
		WifiDwell: noopWifi,
		DrainWifi: noopDrain,
		BleDwell:  noopBle,
		Sweep: func(ctx context.Context) error {
			sweepCalls++
			done()
			return nil
		},
		RecordDwell: noopRecord,
		UpdateCounts: func(time.Time) error {
			updateCountsCalls++
			return nil
		},
		Now: func() time.Time { return clock },
		Sleep: func(ctx context.Context, dur time.Duration) error {
			if dur == pause {
				pauseSleeps++
				done()
			}
			return nil
		},
		Dwell:      3 * time.Second,
		Pause:      pause,
		SweepEvery: time.Hour,
	}

	Run(ctx, d)

	if sweepCalls != 1 {
		t.Errorf("sweep calls = %d, want 1 (only the first cycle sweeps)", sweepCalls)
	}
	if pauseSleeps != 1 {
		t.Errorf("pause sleeps = %d, want 1 (second cycle sleeps instead)", pauseSleeps)
	}
	if updateCountsCalls != 1 {
		t.Errorf("update counts calls = %d, want 1", updateCountsCalls)
	}
}

func TestRun_SweepEveryZero_SweepsEveryCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var sweepCalls, updateCountsCalls, pauseSleeps int

	d := Deps{
		WifiDwell: noopWifi,
		DrainWifi: noopDrain,
		BleDwell:  noopBle,
		Sweep: func(ctx context.Context) error {
			sweepCalls++
			if sweepCalls >= 2 {
				cancel()
			}
			return nil
		},
		RecordDwell: noopRecord,
		UpdateCounts: func(time.Time) error {
			updateCountsCalls++
			return nil
		},
		Now: func() time.Time { return clock },
		Sleep: func(ctx context.Context, dur time.Duration) error {
			if dur == 5*time.Second {
				pauseSleeps++
			}
			return nil
		},
		Dwell:      3 * time.Second,
		Pause:      5 * time.Second,
		SweepEvery: 0,
	}

	Run(ctx, d)

	if sweepCalls != 2 {
		t.Fatalf("sweep calls = %d, want 2", sweepCalls)
	}
	if updateCountsCalls != 2 {
		t.Errorf("update counts calls = %d, want 2", updateCountsCalls)
	}
	if pauseSleeps != 0 {
		t.Errorf("pause sleeps = %d, want 0 (sweep every cycle, never pauses)", pauseSleeps)
	}
}

func TestRun_WifiDwellErrorRecordsFailedAndContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	wantErr := errors.New("hackrf_transfer: exit status 1")

	var wifiCalls int
	var recorded []store.Dwell

	d := Deps{
		WifiDwell: func(ctx context.Context, step Step) error {
			wifiCalls++
			if step.Channel == 6 {
				return wantErr
			}
			return nil
		},
		DrainWifi: noopDrain,
		BleDwell: func(ctx context.Context, step Step) ([]store.BleSighting, error) {
			cancel()
			return nil, nil
		},
		Sweep: noopSweep,
		RecordDwell: func(dw store.Dwell, w []store.WifiSighting, b []store.BleSighting) error {
			recorded = append(recorded, dw)
			return nil
		},
		UpdateCounts: noopUpdateCounts,
		Now:          func() time.Time { return clock },
		Sleep:        noopSleep,
		Dwell:        3 * time.Second,
		Pause:        5 * time.Second,
		SweepEvery:   time.Hour,
	}

	Run(ctx, d)

	if wifiCalls != 3 {
		t.Fatalf("wifi dwell calls = %d, want 3 (cycle continued past the error)", wifiCalls)
	}
	if len(recorded) != 4 {
		t.Fatalf("recorded dwells = %d, want 4", len(recorded))
	}
	if recorded[0].Status != "ok" {
		t.Errorf("recorded[0] (ch1) status = %q, want ok", recorded[0].Status)
	}
	if recorded[1].Status != "failed" || recorded[1].Error != wantErr.Error() {
		t.Errorf("recorded[1] (ch6) = %+v, want failed with error %q", recorded[1], wantErr.Error())
	}
	if recorded[2].Status != "ok" {
		t.Errorf("recorded[2] (ch11) status = %q, want ok", recorded[2].Status)
	}
}

func TestRunWifiStep_ZeroChannelSightingGetsStepChannel(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var gotSightings []store.WifiSighting

	d := Deps{
		WifiDwell: noopWifi,
		DrainWifi: func(Window) []store.WifiSighting {
			return []store.WifiSighting{
				{MAC: "aa:bb:cc:dd:ee:ff", Channel: 0},
				{MAC: "11:22:33:44:55:66", Channel: 11},
			}
		},
		BleDwell: noopBle,
		Sweep:    noopSweep,
		RecordDwell: func(dw store.Dwell, w []store.WifiSighting, b []store.BleSighting) error {
			gotSightings = w
			return nil
		},
		UpdateCounts: noopUpdateCounts,
		Now:          func() time.Time { return clock },
		Sleep:        noopSleep,
		Dwell:        3 * time.Second,
		Pause:        5 * time.Second,
		SweepEvery:   time.Hour,
	}

	step := Step{Kind: "wifi", Channel: 6, CenterMHz: 2437}
	if !runWifiStep(ctx, d, step) {
		t.Fatal("runWifiStep returned false, want true")
	}

	if len(gotSightings) != 2 {
		t.Fatalf("recorded sightings = %d, want 2", len(gotSightings))
	}
	if gotSightings[0].Channel != step.Channel {
		t.Errorf("zero-channel sighting Channel = %d, want %d (step.Channel)", gotSightings[0].Channel, step.Channel)
	}
	if gotSightings[1].Channel != 11 {
		t.Errorf("nonzero-channel sighting Channel = %d, want unchanged 11", gotSightings[1].Channel)
	}
}

func TestRun_CancelBetweenStepsStopsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var wifiCalls, bleCalls, sweepCalls int

	d := Deps{
		WifiDwell: func(ctx context.Context, step Step) error {
			wifiCalls++
			if step.Channel == 6 {
				cancel()
			}
			return nil
		},
		DrainWifi: noopDrain,
		BleDwell: func(ctx context.Context, step Step) ([]store.BleSighting, error) {
			bleCalls++
			return nil, nil
		},
		Sweep: func(ctx context.Context) error {
			sweepCalls++
			return nil
		},
		RecordDwell:  noopRecord,
		UpdateCounts: noopUpdateCounts,
		Now:          func() time.Time { return clock },
		Sleep:        noopSleep,
		Dwell:        3 * time.Second,
		Pause:        5 * time.Second,
		SweepEvery:   time.Hour,
	}

	Run(ctx, d)

	if wifiCalls != 2 {
		t.Errorf("wifi calls = %d, want 2 (ch1 and ch6; stopped before ch11)", wifiCalls)
	}
	if bleCalls != 0 {
		t.Errorf("ble calls = %d, want 0", bleCalls)
	}
	if sweepCalls != 0 {
		t.Errorf("sweep calls = %d, want 0", sweepCalls)
	}
}

func TestRun_SleepCancellationStopsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var sweepCalls int

	d := Deps{
		WifiDwell: noopWifi,
		DrainWifi: noopDrain,
		BleDwell:  noopBle,
		Sweep: func(ctx context.Context) error {
			sweepCalls++
			return nil
		},
		RecordDwell:  noopRecord,
		UpdateCounts: noopUpdateCounts,
		Now:          func() time.Time { return clock },
		Sleep: func(ctx context.Context, dur time.Duration) error {
			if dur == 5*time.Second {
				// Simulate the pause sleep observing an already-cancelled
				// context, as the real ctx-aware sleep would.
				cancel()
				return context.Canceled
			}
			return nil
		},
		Dwell:      3 * time.Second,
		Pause:      5 * time.Second,
		SweepEvery: time.Hour,
	}

	Run(ctx, d)

	if sweepCalls != 1 {
		t.Errorf("sweep calls = %d, want 1 (first cycle forces a sweep before the pause sleep is ever hit)", sweepCalls)
	}
}
