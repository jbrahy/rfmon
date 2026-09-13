package ble

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const fakeIce9Path = "testdata/fake_ice9.sh"
const fakeIce9FailPath = "testdata/fake_ice9_fail.sh"

func TestDwellWithFake(t *testing.T) {
	pcapPath := filepath.Join(t.TempDir(), "capture.pcap")

	r := Runner{Bin: fakeIce9Path}
	ctx := context.Background()

	sightings, err := r.Dwell(ctx, 2440, 2, pcapPath, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Dwell returned error: %v", err)
	}

	if len(sightings) != 1 {
		t.Fatalf("len(sightings) = %d, want 1: %+v", len(sightings), sightings)
	}
	got := sightings[0]
	if got.PacketCount != 2 {
		t.Errorf("PacketCount = %d, want 2", got.PacketCount)
	}
	if got.Address == "" {
		t.Errorf("Address is empty")
	}

	if _, err := os.Stat(pcapPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pcap file still exists after Dwell (stat err = %v), want it deleted", err)
	}
}

func TestDwellContextCanceled(t *testing.T) {
	pcapPath := filepath.Join(t.TempDir(), "capture.pcap")

	r := Runner{Bin: fakeIce9Path}
	ctx, cancel := context.WithCancel(context.Background())

	start := time.Now()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	// dur is much longer than the cancel delay above, so a prompt return
	// proves the cancel (not the dur timeout) stopped the process.
	sightings, err := r.Dwell(ctx, 2440, 2, pcapPath, 10*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Dwell returned error: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Dwell took %v after ctx was canceled, want a prompt return", elapsed)
	}
	if len(sightings) != 1 {
		t.Fatalf("len(sightings) = %d, want 1: %+v", len(sightings), sightings)
	}
	if sightings[0].PacketCount != 2 {
		t.Errorf("PacketCount = %d, want 2", sightings[0].PacketCount)
	}

	if _, err := os.Stat(pcapPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pcap file still exists after Dwell (stat err = %v), want it deleted", err)
	}
}

func TestDwellDeletesPcapOnProcessError(t *testing.T) {
	pcapPath := filepath.Join(t.TempDir(), "capture.pcap")

	r := Runner{Bin: fakeIce9FailPath}
	ctx := context.Background()

	// dur is generous: the fake writes the pcap and exits non-zero on its
	// own almost immediately, well before dur or SIGINT would apply.
	_, err := r.Dwell(ctx, 2440, 2, pcapPath, 10*time.Second)
	if err == nil {
		t.Fatal("Dwell returned nil error for a process that exited with an error on its own")
	}

	if _, statErr := os.Stat(pcapPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("pcap file still exists after Dwell returned an error (stat err = %v), want it deleted", statErr)
	}
}

func strPtr(s string) *string { return &s }

func TestAggregateKeepsNameAndUnionsPDUTypes(t *testing.T) {
	advs := []Adv{
		{
			Address:     "aa:bb:cc:dd:ee:ff",
			AddressType: "public",
			PDUType:     "ADV_IND",
			RSSI:        intPtr(-70),
		},
		{
			Address:     "aa:bb:cc:dd:ee:ff",
			AddressType: "public",
			PDUType:     "SCAN_RSP",
			Name:        strPtr("widget"),
			RSSI:        intPtr(-50),
		},
	}

	got := aggregate(advs)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}

	s := got[0]
	if s.Name == nil || *s.Name != "widget" {
		t.Errorf("Name = %v, want \"widget\"", s.Name)
	}
	if s.PDUTypes != "ADV_IND,SCAN_RSP" {
		t.Errorf("PDUTypes = %q, want %q", s.PDUTypes, "ADV_IND,SCAN_RSP")
	}
	if s.PacketCount != 2 {
		t.Errorf("PacketCount = %d, want 2", s.PacketCount)
	}
	if s.BestRSSI == nil || *s.BestRSSI != -50 {
		t.Errorf("BestRSSI = %v, want -50", s.BestRSSI)
	}
}

func TestAggregateSeparatesDifferentAddresses(t *testing.T) {
	advs := []Adv{
		{Address: "aa:bb:cc:dd:ee:ff", AddressType: "public", PDUType: "ADV_IND"},
		{Address: "11:22:33:44:55:66", AddressType: "public", PDUType: "ADV_IND"},
	}

	got := aggregate(advs)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
}
