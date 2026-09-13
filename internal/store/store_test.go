package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rfmon.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func TestOpenTwiceIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rfmon.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.conn.QueryRow(`SELECT COUNT(*) FROM schema_versions`).Scan(&count); err != nil {
		t.Fatalf("count schema_versions: %v", err)
	}
	if count != 1 {
		t.Fatalf("schema_versions row count = %d, want 1", count)
	}

	var version int
	if err := db2.conn.QueryRow(`SELECT version FROM schema_versions`).Scan(&version); err != nil {
		t.Fatalf("select version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_versions.version = %d, want 1", version)
	}
}

func TestInsertDwell(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	dw1 := Dwell{
		Kind:       "wifi",
		Channel:    6,
		CenterMHz:  2437,
		StartedAt:  now,
		EndedAt:    now.Add(time.Second),
		Status:     "ok",
		FrameCount: 3,
	}
	id1, err := db.InsertDwell(dw1)
	if err != nil {
		t.Fatalf("InsertDwell 1: %v", err)
	}

	dw2 := dw1
	dw2.StartedAt = now.Add(time.Minute)
	dw2.EndedAt = now.Add(time.Minute + time.Second)
	id2, err := db.InsertDwell(dw2)
	if err != nil {
		t.Fatalf("InsertDwell 2: %v", err)
	}

	if id2 <= id1 {
		t.Fatalf("id2 (%d) should be greater than id1 (%d)", id2, id1)
	}
}

func TestRecordWifiApUpsert(t *testing.T) {
	db := newTestDB(t)
	t1 := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 12, 10, 5, 0, 0, time.UTC)

	dwellID1, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 6, CenterMHz: 2437, StartedAt: t1, EndedAt: t1.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell 1: %v", err)
	}
	dwellID2, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 6, CenterMHz: 2437, StartedAt: t2, EndedAt: t2.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell 2: %v", err)
	}

	err = db.RecordWifi(dwellID1, t1, WifiSighting{
		MAC:        "aa:bb:cc:dd:ee:ff",
		DeviceKind: "ap",
		SSID:       strPtr("home-network"),
		Channel:    6,
		Security:   strPtr("wpa2"),
		Randomized: false,
		FrameType:  "beacon",
		FrameCount: 10,
		BestSNR:    intPtr(20),
		Decoder:    "ofdm",
	})
	if err != nil {
		t.Fatalf("RecordWifi 1: %v", err)
	}

	var deviceCount int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM wifi_devices WHERE wifi_devices.mac = ? AND wifi_devices.device_kind = ?`, "aa:bb:cc:dd:ee:ff", "ap").Scan(&deviceCount); err != nil {
		t.Fatalf("count wifi_devices: %v", err)
	}
	if deviceCount != 1 {
		t.Fatalf("wifi_devices row count = %d, want 1", deviceCount)
	}

	var sightingCount int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM wifi_sightings`).Scan(&sightingCount); err != nil {
		t.Fatalf("count wifi_sightings: %v", err)
	}
	if sightingCount != 1 {
		t.Fatalf("wifi_sightings row count = %d, want 1", sightingCount)
	}

	err = db.RecordWifi(dwellID2, t2, WifiSighting{
		MAC:        "aa:bb:cc:dd:ee:ff",
		DeviceKind: "ap",
		SSID:       nil,
		Channel:    6,
		Security:   strPtr("wpa2"),
		Randomized: false,
		FrameType:  "beacon",
		FrameCount: 5,
		BestSNR:    intPtr(30),
		Decoder:    "ofdm",
	})
	if err != nil {
		t.Fatalf("RecordWifi 2: %v", err)
	}

	aps, err := db.WifiAPsSince(t1, 10)
	if err != nil {
		t.Fatalf("WifiAPsSince: %v", err)
	}
	if len(aps) != 1 {
		t.Fatalf("WifiAPsSince returned %d rows, want 1", len(aps))
	}
	ap := aps[0]
	if !ap.FirstSeen.Equal(t1) {
		t.Fatalf("FirstSeen = %v, want %v", ap.FirstSeen, t1)
	}
	if !ap.LastSeen.Equal(t2) {
		t.Fatalf("LastSeen = %v, want %v", ap.LastSeen, t2)
	}
	if ap.Sightings != 2 {
		t.Fatalf("Sightings = %d, want 2", ap.Sightings)
	}
	if ap.BestSNR == nil || *ap.BestSNR != 30 {
		t.Fatalf("BestSNR = %v, want 30", ap.BestSNR)
	}
	if ap.SSID == nil || *ap.SSID != "home-network" {
		t.Fatalf("SSID = %v, want home-network (kept from first sighting)", ap.SSID)
	}
}

func TestRecordWifiNilSSIDStaysNull(t *testing.T) {
	db := newTestDB(t)
	t1 := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)

	dwellID, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 1, CenterMHz: 2412, StartedAt: t1, EndedAt: t1.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell: %v", err)
	}

	err = db.RecordWifi(dwellID, t1, WifiSighting{
		MAC:        "11:22:33:44:55:66",
		DeviceKind: "ap",
		SSID:       nil,
		Channel:    1,
		Randomized: false,
		FrameType:  "beacon",
		FrameCount: 1,
		Decoder:    "ofdm",
	})
	if err != nil {
		t.Fatalf("RecordWifi: %v", err)
	}

	aps, err := db.WifiAPsSince(t1, 10)
	if err != nil {
		t.Fatalf("WifiAPsSince: %v", err)
	}
	if len(aps) != 1 {
		t.Fatalf("WifiAPsSince returned %d rows, want 1", len(aps))
	}
	if aps[0].SSID != nil {
		t.Fatalf("SSID = %v, want nil", *aps[0].SSID)
	}
}

func TestRecordWifiClientFeedsReports(t *testing.T) {
	db := newTestDB(t)
	t1 := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 12, 10, 1, 0, 0, time.UTC)

	dwellID1, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 1, CenterMHz: 2412, StartedAt: t1, EndedAt: t1.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell 1: %v", err)
	}
	dwellID2, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 1, CenterMHz: 2412, StartedAt: t2, EndedAt: t2.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell 2: %v", err)
	}

	err = db.RecordWifi(dwellID1, t1, WifiSighting{
		MAC:        "de:ad:be:ef:00:01",
		DeviceKind: "client",
		Randomized: true,
		FrameType:  "probe_request",
		SSID:       strPtr("coffee-shop-wifi"),
		Channel:    1,
		FrameCount: 2,
		Decoder:    "ofdm",
	})
	if err != nil {
		t.Fatalf("RecordWifi 1: %v", err)
	}

	err = db.RecordWifi(dwellID2, t2, WifiSighting{
		MAC:        "de:ad:be:ef:00:02",
		DeviceKind: "client",
		Randomized: true,
		FrameType:  "probe_request",
		SSID:       strPtr("coffee-shop-wifi"),
		Channel:    1,
		FrameCount: 1,
		Decoder:    "ofdm",
	})
	if err != nil {
		t.Fatalf("RecordWifi 2: %v", err)
	}

	clients, err := db.WifiClientsSince(t1, 10)
	if err != nil {
		t.Fatalf("WifiClientsSince: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("WifiClientsSince returned %d rows, want 2", len(clients))
	}
	found := false
	for _, c := range clients {
		if c.MAC == "de:ad:be:ef:00:01" {
			found = true
			if !c.Randomized {
				t.Fatalf("Randomized = false, want true")
			}
			if len(c.ProbedSSIDs) != 1 || c.ProbedSSIDs[0] != "coffee-shop-wifi" {
				t.Fatalf("ProbedSSIDs = %v, want [coffee-shop-wifi]", c.ProbedSSIDs)
			}
		}
	}
	if !found {
		t.Fatalf("client de:ad:be:ef:00:01 not found in %+v", clients)
	}

	top, err := db.TopProbedSSIDs(t1, 10)
	if err != nil {
		t.Fatalf("TopProbedSSIDs: %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("TopProbedSSIDs returned %d rows, want 1", len(top))
	}
	if top[0].SSID != "coffee-shop-wifi" || top[0].Count != 2 {
		t.Fatalf("top[0] = %+v, want {coffee-shop-wifi 2}", top[0])
	}
}

func TestRecordBleUpsert(t *testing.T) {
	db := newTestDB(t)
	t1 := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 12, 10, 2, 0, 0, time.UTC)

	dwellID1, err := db.InsertDwell(Dwell{Kind: "ble", StartedAt: t1, EndedAt: t1.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell 1: %v", err)
	}
	dwellID2, err := db.InsertDwell(Dwell{Kind: "ble", StartedAt: t2, EndedAt: t2.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell 2: %v", err)
	}

	err = db.RecordBle(dwellID1, t1, BleSighting{
		Address:     "aa:aa:aa:aa:aa:aa",
		AddressType: "random",
		Name:        strPtr("Old Name"),
		CompanyID:   intPtr(76),
		PDUTypes:    "ADV_IND",
		PacketCount: 3,
		BestRSSI:    intPtr(-70),
	})
	if err != nil {
		t.Fatalf("RecordBle 1: %v", err)
	}

	err = db.RecordBle(dwellID2, t2, BleSighting{
		Address:     "aa:aa:aa:aa:aa:aa",
		AddressType: "random",
		Name:        strPtr("New Name"),
		CompanyID:   intPtr(89),
		PDUTypes:    "ADV_IND,SCAN_RSP",
		PacketCount: 2,
		BestRSSI:    intPtr(-50),
	})
	if err != nil {
		t.Fatalf("RecordBle 2: %v", err)
	}

	devices, err := db.BleDevicesSince(t1, 10)
	if err != nil {
		t.Fatalf("BleDevicesSince: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("BleDevicesSince returned %d rows, want 1", len(devices))
	}
	dev := devices[0]
	if !dev.FirstSeen.Equal(t1) {
		t.Fatalf("FirstSeen = %v, want %v", dev.FirstSeen, t1)
	}
	if !dev.LastSeen.Equal(t2) {
		t.Fatalf("LastSeen = %v, want %v", dev.LastSeen, t2)
	}
	if dev.Sightings != 2 {
		t.Fatalf("Sightings = %d, want 2", dev.Sightings)
	}
	if dev.Name == nil || *dev.Name != "New Name" {
		t.Fatalf("Name = %v, want New Name", dev.Name)
	}
	if dev.CompanyID == nil || *dev.CompanyID != 89 {
		t.Fatalf("CompanyID = %v, want 89", dev.CompanyID)
	}
	if dev.BestRSSI == nil || *dev.BestRSSI != -50 {
		t.Fatalf("BestRSSI = %v, want -50 (max of -70 and -50)", dev.BestRSSI)
	}
}

func TestWifiAPsSinceAndBleDevicesSinceRespectCutoffAndLimit(t *testing.T) {
	db := newTestDB(t)
	base := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)

	macs := []string{"00:00:00:00:00:01", "00:00:00:00:00:02", "00:00:00:00:00:03"}
	for i, mac := range macs {
		seen := base.Add(time.Duration(i) * time.Hour)
		dwellID, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 1, CenterMHz: 2412, StartedAt: seen, EndedAt: seen.Add(time.Second), Status: "ok"})
		if err != nil {
			t.Fatalf("InsertDwell %d: %v", i, err)
		}
		if err := db.RecordWifi(dwellID, seen, WifiSighting{MAC: mac, DeviceKind: "ap", Channel: 1, FrameType: "beacon", FrameCount: 1, Decoder: "ofdm"}); err != nil {
			t.Fatalf("RecordWifi %d: %v", i, err)
		}

		addr := mac
		if err := db.RecordBle(dwellID, seen, BleSighting{Address: addr, AddressType: "public", PDUTypes: "ADV_IND", PacketCount: 1}); err != nil {
			t.Fatalf("RecordBle %d: %v", i, err)
		}
	}

	// Cutoff excludes the first device (seen at base, before base+30m).
	cutoff := base.Add(30 * time.Minute)

	aps, err := db.WifiAPsSince(cutoff, 10)
	if err != nil {
		t.Fatalf("WifiAPsSince: %v", err)
	}
	if len(aps) != 2 {
		t.Fatalf("WifiAPsSince returned %d rows, want 2", len(aps))
	}
	if aps[0].MAC != "00:00:00:00:00:03" || aps[1].MAC != "00:00:00:00:00:02" {
		t.Fatalf("WifiAPsSince order = [%s, %s], want most-recent-first [00:00:00:00:00:03, 00:00:00:00:00:02]", aps[0].MAC, aps[1].MAC)
	}

	apsLimited, err := db.WifiAPsSince(cutoff, 1)
	if err != nil {
		t.Fatalf("WifiAPsSince limited: %v", err)
	}
	if len(apsLimited) != 1 {
		t.Fatalf("WifiAPsSince with limit 1 returned %d rows, want 1", len(apsLimited))
	}
	if apsLimited[0].MAC != "00:00:00:00:00:03" {
		t.Fatalf("WifiAPsSince limited MAC = %s, want 00:00:00:00:00:03", apsLimited[0].MAC)
	}

	bleDevices, err := db.BleDevicesSince(cutoff, 10)
	if err != nil {
		t.Fatalf("BleDevicesSince: %v", err)
	}
	if len(bleDevices) != 2 {
		t.Fatalf("BleDevicesSince returned %d rows, want 2", len(bleDevices))
	}
	if bleDevices[0].Address != "00:00:00:00:00:03" || bleDevices[1].Address != "00:00:00:00:00:02" {
		t.Fatalf("BleDevicesSince order = [%s, %s], want most-recent-first [00:00:00:00:00:03, 00:00:00:00:00:02]", bleDevices[0].Address, bleDevices[1].Address)
	}
}

func TestCountsSince(t *testing.T) {
	db := newTestDB(t)
	before := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	after := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)

	// A dwell that started before the cutoff: must not be counted.
	dwellBefore, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 1, CenterMHz: 2412, StartedAt: before, EndedAt: before.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell before: %v", err)
	}
	if err := db.RecordWifi(dwellBefore, before, WifiSighting{MAC: "ff:ff:ff:ff:ff:ff", DeviceKind: "ap", Channel: 1, FrameType: "beacon", FrameCount: 100, Decoder: "ofdm"}); err != nil {
		t.Fatalf("RecordWifi before: %v", err)
	}

	// A dwell at/after the cutoff with one AP and one client sighting.
	dwellAfter, err := db.InsertDwell(Dwell{Kind: "wifi", Channel: 1, CenterMHz: 2412, StartedAt: after, EndedAt: after.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell after: %v", err)
	}
	if err := db.RecordWifi(dwellAfter, after, WifiSighting{MAC: "aa:aa:aa:aa:aa:aa", DeviceKind: "ap", Channel: 1, FrameType: "beacon", FrameCount: 7, Decoder: "ofdm"}); err != nil {
		t.Fatalf("RecordWifi ap after: %v", err)
	}
	if err := db.RecordWifi(dwellAfter, after, WifiSighting{MAC: "bb:bb:bb:bb:bb:bb", DeviceKind: "client", Channel: 1, FrameType: "probe_request", FrameCount: 3, Decoder: "ofdm"}); err != nil {
		t.Fatalf("RecordWifi client after: %v", err)
	}

	dwellBle, err := db.InsertDwell(Dwell{Kind: "ble", StartedAt: after, EndedAt: after.Add(time.Second), Status: "ok"})
	if err != nil {
		t.Fatalf("InsertDwell ble: %v", err)
	}
	if err := db.RecordBle(dwellBle, after, BleSighting{Address: "cc:cc:cc:cc:cc:cc", AddressType: "public", PDUTypes: "ADV_IND", PacketCount: 5}); err != nil {
		t.Fatalf("RecordBle after: %v", err)
	}

	counts, err := db.CountsSince(cutoff)
	if err != nil {
		t.Fatalf("CountsSince: %v", err)
	}
	want := Counts{WifiAPs: 1, WifiClients: 1, WifiFrames: 10, BleDevices: 1, BlePackets: 5}
	if counts != want {
		t.Fatalf("CountsSince(cutoff) = %+v, want %+v", counts, want)
	}
}
