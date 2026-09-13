package wifi

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jbrahy/rfmon/internal/store"
)

func TestSupervisorStartReadsFrame(t *testing.T) {
	d := Decoder{Python: "/bin/sh", Script: "testdata/fake_decoder.sh"}
	s := NewSupervisor(d)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	select {
	case tf := <-s.Frames():
		if tf.FrameType != "beacon" {
			t.Errorf("FrameType = %q, want beacon", tf.FrameType)
		}
		since := time.Since(tf.At)
		if since < 0 || since > time.Second {
			t.Errorf("At = %v (%v ago), want within a second of now", tf.At, since)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a frame")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSupervisorEnsureRestartsAfterExit(t *testing.T) {
	dir := t.TempDir()
	d := Decoder{
		Python:     "/bin/sh",
		Script:     "testdata/fake_decoder_once.sh",
		ModulePath: dir,
	}
	s := NewSupervisor(d)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	// The first invocation of fake_decoder_once.sh exits immediately
	// without emitting anything. Poll Ensure until the supervisor notices
	// the exit and restarts, at which point the second invocation
	// behaves like fake_decoder.sh and emits a beacon frame.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := s.Ensure(); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		select {
		case tf := <-s.Frames():
			if tf.FrameType != "beacon" {
				t.Fatalf("FrameType = %q, want beacon", tf.FrameType)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}

		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Ensure to restart the decoder")
		}
	}
}

func TestTransferRunnerDwellWritesExpectedBytes(t *testing.T) {
	r := TransferRunner{Bin: "testdata/fake_transfer.sh"}
	var buf bytes.Buffer

	dur := 1 * time.Second
	wantN := int64(dur.Seconds()) * dwellSampleRate

	if err := r.Dwell(context.Background(), &buf, 2_412_000_000, dur); err != nil {
		t.Fatalf("Dwell: %v", err)
	}
	if int64(buf.Len()) != wantN {
		t.Errorf("wrote %d bytes, want %d", buf.Len(), wantN)
	}
}

func TestTransferRunnerDwellCanceledContextReturnsPromptly(t *testing.T) {
	r := TransferRunner{Bin: "testdata/fake_transfer.sh"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	start := time.Now()
	err := r.Dwell(ctx, &buf, 2_412_000_000, 1*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Dwell with an already-canceled context returned nil error, want an error")
	}
	if elapsed > time.Second {
		t.Errorf("Dwell with a canceled context took %v, want a prompt return", elapsed)
	}
}

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }

func TestAggregatorGroupsByMACFrameTypeAndSSID(t *testing.T) {
	a := NewAggregator()

	base := Frame{
		Kind:      "ap",
		MAC:       "aa:bb:cc:dd:ee:ff",
		SSID:      strPtr("fixture-net"),
		Channel:   6,
		FrameType: "beacon",
	}

	f1, f2, f3 := base, base, base
	f1.SNR = intPtr(10)
	f2.SNR = intPtr(30)
	f3.SNR = intPtr(20)
	a.Add(f1)
	a.Add(f2)
	a.Add(f3)

	other := base
	other.SSID = strPtr("other-fixture-net")
	other.SNR = intPtr(5)
	a.Add(other)

	sightings := a.Drain()
	if len(sightings) != 2 {
		t.Fatalf("len(sightings) = %d, want 2", len(sightings))
	}

	var got *store.WifiSighting
	for i := range sightings {
		if sightings[i].SSID != nil && *sightings[i].SSID == "fixture-net" {
			got = &sightings[i]
		}
	}
	if got == nil {
		t.Fatal("no sighting found for fixture-net")
	}
	if got.FrameCount != 3 {
		t.Errorf("FrameCount = %d, want 3", got.FrameCount)
	}
	if got.BestSNR == nil || *got.BestSNR != 30 {
		t.Errorf("BestSNR = %v, want 30", got.BestSNR)
	}
	if got.MAC != base.MAC || got.DeviceKind != "ap" || got.FrameType != "beacon" || got.Decoder != "ofdm" {
		t.Errorf("sighting = %+v, unexpected field values", got)
	}

	// Drain resets the aggregator.
	if remaining := a.Drain(); len(remaining) != 0 {
		t.Errorf("Drain after Drain returned %d sightings, want 0", len(remaining))
	}
}
