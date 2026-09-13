package store

import (
	"testing"
	"time"
)

// day1/day2 are two distinct UTC calendar days.
var (
	day1 = time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 9, 13, 20, 0, 0, 0, time.UTC)
)

func recordBleAt(t *testing.T, db *DB, seen time.Time, addr string) {
	t.Helper()
	id, err := db.InsertDwell(Dwell{Kind: "ble", CenterMHz: 2427, StartedAt: seen, EndedAt: seen.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell: %v", err)
	}
	if err := db.RecordBle(id, seen, BleSighting{Address: addr, AddressType: "public", PDUTypes: "ADV_IND", PacketCount: 1}); err != nil {
		t.Fatalf("RecordBle: %v", err)
	}
}

func TestDailyBleCountsDistinctAddressesAndNew(t *testing.T) {
	db := newTestDB(t)

	// Day 1: addr A and B first seen.
	recordBleAt(t, db, day1, "aa:aa:aa:aa:aa:aa")
	recordBleAt(t, db, day1.Add(time.Minute), "bb:bb:bb:bb:bb:bb")
	// Day 2: A seen again (not new), C new.
	recordBleAt(t, db, day2, "aa:aa:aa:aa:aa:aa")
	recordBleAt(t, db, day2.Add(time.Minute), "cc:cc:cc:cc:cc:cc")

	rows, err := db.DailyBle(day1.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DailyBle: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d daily rows, want 2: %+v", len(rows), rows)
	}
	// Newest first.
	if rows[0].Date != "2026-09-13" || rows[1].Date != "2026-09-12" {
		t.Fatalf("dates = %q, %q; want 2026-09-13, 2026-09-12", rows[0].Date, rows[1].Date)
	}
	if rows[1].Devices != 2 || rows[1].New != 2 {
		t.Errorf("day1 = %d seen / %d new, want 2 / 2", rows[1].Devices, rows[1].New)
	}
	if rows[0].Devices != 2 || rows[0].New != 1 {
		t.Errorf("day2 = %d seen / %d new, want 2 seen (A,C) / 1 new (C)", rows[0].Devices, rows[0].New)
	}
}

func TestDailyWifiCountsByDistinctMac(t *testing.T) {
	db := newTestDB(t)

	// One MAC seen as both an AP and a client on the same day must count once.
	id, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 6, CenterMHz: 2437, StartedAt: day1, EndedAt: day1.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	mac := "de:ad:be:ef:00:01"
	if err := db.RecordWifi(id, day1, WifiSighting{MAC: mac, DeviceKind: "ap", SSID: strPtr("Net"), Channel: 6, FrameType: "beacon", FrameCount: 1, Decoder: "ofdm"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordWifi(id, day1, WifiSighting{MAC: mac, DeviceKind: "client", Channel: 6, FrameType: "probe_request", FrameCount: 1, Decoder: "ofdm"}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.DailyWifi(day1.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DailyWifi: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Devices != 1 || rows[0].New != 1 {
		t.Errorf("day1 = %d seen / %d new, want 1 / 1 (one distinct MAC)", rows[0].Devices, rows[0].New)
	}
}

func TestBleDevicesSinceGroupsByAddress(t *testing.T) {
	db := newTestDB(t)

	// Same address under two address types, plus a distinct address.
	id, err := db.InsertDwell(Dwell{Kind: "ble", CenterMHz: 2427, StartedAt: day1, EndedAt: day1.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	addr := "11:22:33:44:55:66"
	if err := db.RecordBle(id, day1, BleSighting{Address: addr, AddressType: "public", Name: strPtr("Widget"), PDUTypes: "ADV_IND", PacketCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordBle(id, day1.Add(time.Minute), BleSighting{Address: addr, AddressType: "random_static", PDUTypes: "ADV_IND", PacketCount: 3}); err != nil {
		t.Fatal(err)
	}
	recordBleAt(t, db, day1.Add(2*time.Minute), "99:99:99:99:99:99")

	rows, err := db.BleDevicesSince(day1.Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatalf("BleDevicesSince: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (address collapsed across type): %+v", len(rows), rows)
	}
	var collapsed *BleDeviceRow
	for i := range rows {
		if rows[i].Address == addr {
			collapsed = &rows[i]
		}
	}
	if collapsed == nil {
		t.Fatalf("address %s not in results", addr)
	}
	if collapsed.Sightings != 2 {
		t.Errorf("collapsed Sightings = %d, want 2 (one per address_type)", collapsed.Sightings)
	}
	// AddressType now carries both types.
	if collapsed.AddressType != "public,random_static" && collapsed.AddressType != "random_static,public" {
		t.Errorf("AddressType = %q, want both public and random_static", collapsed.AddressType)
	}
	if collapsed.Name == nil || *collapsed.Name != "Widget" {
		t.Errorf("Name = %v, want Widget kept across the merge", collapsed.Name)
	}
}
