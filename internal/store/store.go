// Package store persists WiFi and Bluetooth LE sightings, grouped by dwell,
// in a SQLite database. Callers open one DB per data file; the schema is
// created automatically on first use.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// DB is a handle to the sighting database. It is safe for concurrent use;
// the underlying connection pool is limited to one connection so that
// WAL writers never contend with each other.
type DB struct {
	conn *sql.DB
}

// Open creates or opens the SQLite database at path and applies the schema.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Close releases the underlying database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// Dwell is one tuning window during which frames or packets were collected.
type Dwell struct {
	Kind       string
	Channel    int
	CenterMHz  int
	StartedAt  time.Time
	EndedAt    time.Time
	Status     string
	Error      string
	FrameCount int
}

// InsertDwell records a completed dwell and returns its dwell_id.
func (d *DB) InsertDwell(dw Dwell) (int64, error) {
	var errArg any
	if dw.Error != "" {
		errArg = dw.Error
	}
	var dwellID int64
	err := d.conn.QueryRow(
		`INSERT INTO dwells
		   (dwell_kind, channel, center_mhz, started_at, ended_at, status, error, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING dwell_id`,
		dw.Kind, dw.Channel, dw.CenterMHz, formatTime(dw.StartedAt), formatTime(dw.EndedAt), dw.Status, errArg, dw.FrameCount,
	).Scan(&dwellID)
	if err != nil {
		return 0, err
	}
	return dwellID, nil
}

// WifiSighting is one aggregated group of WiFi frames from a single dwell:
// one device, one frame type, one SSID.
type WifiSighting struct {
	MAC        string
	DeviceKind string
	SSID       *string
	Channel    int
	Security   *string
	Randomized bool
	FrameType  string
	FrameCount int
	BestSNR    *int
	Decoder    string
}

// RecordWifi upserts the wifi_devices row for s.MAC and s.DeviceKind, then
// inserts one wifi_sightings row for dwellID. The device's first_seen is
// kept on repeat sightings; last_seen advances; sighting_count increments;
// ssid and security keep their previous value when the new sighting has
// none; best_snr_db keeps the maximum of the old and new value.
func (d *DB) RecordWifi(dwellID int64, seen time.Time, s WifiSighting) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	seenAt := formatTime(seen)
	var wifiDeviceID int64
	err = tx.QueryRow(
		`INSERT INTO wifi_devices
		   (mac, device_kind, ssid, channel, security, randomized, first_seen, last_seen, sighting_count, best_snr_db)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		 ON CONFLICT (mac, device_kind) DO UPDATE SET
		   last_seen = excluded.last_seen,
		   sighting_count = wifi_devices.sighting_count + 1,
		   ssid = COALESCE(excluded.ssid, wifi_devices.ssid),
		   channel = excluded.channel,
		   security = COALESCE(excluded.security, wifi_devices.security),
		   randomized = excluded.randomized,
		   best_snr_db = MAX(COALESCE(wifi_devices.best_snr_db, excluded.best_snr_db), COALESCE(excluded.best_snr_db, wifi_devices.best_snr_db))
		 RETURNING wifi_device_id`,
		s.MAC, s.DeviceKind, stringPtrArg(s.SSID), s.Channel, stringPtrArg(s.Security), boolToInt(s.Randomized), seenAt, seenAt, intPtrArg(s.BestSNR),
	).Scan(&wifiDeviceID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		`INSERT INTO wifi_sightings
		   (wifi_device_id, dwell_id, seen_at, frame_type, ssid, channel, frame_count, best_snr_db, decoder)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wifiDeviceID, dwellID, seenAt, s.FrameType, stringPtrArg(s.SSID), s.Channel, s.FrameCount, intPtrArg(s.BestSNR), s.Decoder,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// BleSighting is one aggregated group of BLE advertisements from a single
// dwell for one device.
type BleSighting struct {
	Address         string
	AddressType     string
	Name            *string
	CompanyID       *int
	ManufacturerHex *string
	ServiceUUIDs    *string
	TxPower         *int
	PDUTypes        string
	PacketCount     int
	BestRSSI        *int
}

// RecordBle upserts the ble_devices row for s.Address and s.AddressType,
// then inserts one ble_sightings row for dwellID. Semantics mirror
// RecordWifi: first_seen kept, last_seen advanced, sighting_count
// incremented, name and company_id keep their previous value when the new
// sighting has none, and best_rssi_dbm keeps the maximum (least negative,
// i.e. strongest) of the old and new value.
func (d *DB) RecordBle(dwellID int64, seen time.Time, s BleSighting) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	seenAt := formatTime(seen)
	var bleDeviceID int64
	err = tx.QueryRow(
		`INSERT INTO ble_devices
		   (address, address_type, name, company_id, first_seen, last_seen, sighting_count, best_rssi_dbm)
		 VALUES (?, ?, ?, ?, ?, ?, 1, ?)
		 ON CONFLICT (address, address_type) DO UPDATE SET
		   last_seen = excluded.last_seen,
		   sighting_count = ble_devices.sighting_count + 1,
		   name = COALESCE(excluded.name, ble_devices.name),
		   company_id = COALESCE(excluded.company_id, ble_devices.company_id),
		   best_rssi_dbm = MAX(COALESCE(ble_devices.best_rssi_dbm, excluded.best_rssi_dbm), COALESCE(excluded.best_rssi_dbm, ble_devices.best_rssi_dbm))
		 RETURNING ble_device_id`,
		s.Address, s.AddressType, stringPtrArg(s.Name), intPtrArg(s.CompanyID), seenAt, seenAt, intPtrArg(s.BestRSSI),
	).Scan(&bleDeviceID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		`INSERT INTO ble_sightings
		   (ble_device_id, dwell_id, seen_at, pdu_types, name, company_id, manufacturer_data_hex, service_uuids, tx_power_dbm, packet_count, best_rssi_dbm)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		bleDeviceID, dwellID, seenAt, s.PDUTypes, stringPtrArg(s.Name), intPtrArg(s.CompanyID), stringPtrArg(s.ManufacturerHex), stringPtrArg(s.ServiceUUIDs), intPtrArg(s.TxPower), s.PacketCount, intPtrArg(s.BestRSSI),
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// WifiAP is one wifi_devices row of kind 'ap', for reporting.
type WifiAP struct {
	MAC        string
	SSID       *string
	Channel    int
	Security   *string
	Randomized bool
	FirstSeen  time.Time
	LastSeen   time.Time
	Sightings  int
	BestSNR    *int
}

// WifiAPsSince returns WiFi access points last seen at or after t, most
// recently seen first, limited to limit rows.
func (d *DB) WifiAPsSince(t time.Time, limit int) ([]WifiAP, error) {
	rows, err := d.conn.Query(
		`SELECT wifi_devices.mac, wifi_devices.ssid, wifi_devices.channel, wifi_devices.security, wifi_devices.randomized, wifi_devices.first_seen, wifi_devices.last_seen, wifi_devices.sighting_count, wifi_devices.best_snr_db
		 FROM wifi_devices
		 WHERE wifi_devices.device_kind = ? AND wifi_devices.last_seen >= ?
		 ORDER BY wifi_devices.last_seen DESC
		 LIMIT ?`,
		"ap", formatTime(t), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WifiAP
	for rows.Next() {
		var (
			mac           string
			ssid          sql.NullString
			channel       int
			security      sql.NullString
			randomized    int
			firstSeenText string
			lastSeenText  string
			sightingCount int
			bestSNR       sql.NullInt64
		)
		if err := rows.Scan(&mac, &ssid, &channel, &security, &randomized, &firstSeenText, &lastSeenText, &sightingCount, &bestSNR); err != nil {
			return nil, err
		}
		firstSeen, err := parseTime(firstSeenText)
		if err != nil {
			return nil, err
		}
		lastSeen, err := parseTime(lastSeenText)
		if err != nil {
			return nil, err
		}
		result = append(result, WifiAP{
			MAC:        mac,
			SSID:       nullStringPtr(ssid),
			Channel:    channel,
			Security:   nullStringPtr(security),
			Randomized: randomized != 0,
			FirstSeen:  firstSeen,
			LastSeen:   lastSeen,
			Sightings:  sightingCount,
			BestSNR:    nullInt64Ptr(bestSNR),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// WifiClient is one wifi_devices row of kind 'client', for reporting.
type WifiClient struct {
	MAC         string
	Randomized  bool
	FirstSeen   time.Time
	LastSeen    time.Time
	Sightings   int
	ProbedSSIDs []string
}

// WifiClientsSince returns WiFi clients last seen at or after t, most
// recently seen first, limited to limit rows. ProbedSSIDs lists the
// distinct non-hidden SSIDs each client has probed for, oldest first.
func (d *DB) WifiClientsSince(t time.Time, limit int) ([]WifiClient, error) {
	rows, err := d.conn.Query(
		`SELECT wifi_devices.wifi_device_id, wifi_devices.mac, wifi_devices.randomized, wifi_devices.first_seen, wifi_devices.last_seen, wifi_devices.sighting_count
		 FROM wifi_devices
		 WHERE wifi_devices.device_kind = ? AND wifi_devices.last_seen >= ?
		 ORDER BY wifi_devices.last_seen DESC
		 LIMIT ?`,
		"client", formatTime(t), limit,
	)
	if err != nil {
		return nil, err
	}

	type clientRow struct {
		deviceID      int64
		mac           string
		randomized    int
		firstSeenText string
		lastSeenText  string
		sightingCount int
	}
	var collected []clientRow
	for rows.Next() {
		var r clientRow
		if err := rows.Scan(&r.deviceID, &r.mac, &r.randomized, &r.firstSeenText, &r.lastSeenText, &r.sightingCount); err != nil {
			rows.Close()
			return nil, err
		}
		collected = append(collected, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	result := make([]WifiClient, 0, len(collected))
	for _, r := range collected {
		firstSeen, err := parseTime(r.firstSeenText)
		if err != nil {
			return nil, err
		}
		lastSeen, err := parseTime(r.lastSeenText)
		if err != nil {
			return nil, err
		}

		ssidRows, err := d.conn.Query(
			`SELECT wifi_sightings.ssid
			 FROM wifi_sightings
			 WHERE wifi_sightings.wifi_device_id = ? AND wifi_sightings.ssid IS NOT NULL
			 GROUP BY wifi_sightings.ssid
			 ORDER BY MIN(wifi_sightings.seen_at)`,
			r.deviceID,
		)
		if err != nil {
			return nil, err
		}
		var probed []string
		for ssidRows.Next() {
			var ssid string
			if err := ssidRows.Scan(&ssid); err != nil {
				ssidRows.Close()
				return nil, err
			}
			probed = append(probed, ssid)
		}
		if err := ssidRows.Err(); err != nil {
			ssidRows.Close()
			return nil, err
		}
		ssidRows.Close()

		result = append(result, WifiClient{
			MAC:         r.mac,
			Randomized:  r.randomized != 0,
			FirstSeen:   firstSeen,
			LastSeen:    lastSeen,
			Sightings:   r.sightingCount,
			ProbedSSIDs: probed,
		})
	}
	return result, nil
}

// ProbedSSID is one SSID ranked by how many distinct WiFi clients have
// probed for it.
type ProbedSSID struct {
	SSID  string
	Count int
}

// TopProbedSSIDs returns the SSIDs most probed for by distinct WiFi clients
// since t, ordered by distinct client count descending, limited to limit
// rows.
func (d *DB) TopProbedSSIDs(t time.Time, limit int) ([]ProbedSSID, error) {
	rows, err := d.conn.Query(
		`SELECT wifi_sightings.ssid, COUNT(DISTINCT wifi_sightings.wifi_device_id)
		 FROM wifi_sightings
		 WHERE wifi_sightings.frame_type = ? AND wifi_sightings.ssid IS NOT NULL AND wifi_sightings.seen_at >= ?
		 GROUP BY wifi_sightings.ssid
		 ORDER BY COUNT(DISTINCT wifi_sightings.wifi_device_id) DESC
		 LIMIT ?`,
		"probe_request", formatTime(t), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ProbedSSID
	for rows.Next() {
		var p ProbedSSID
		if err := rows.Scan(&p.SSID, &p.Count); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// BleDeviceRow is one ble_devices row, for reporting.
type BleDeviceRow struct {
	Address     string
	AddressType string
	Name        *string
	CompanyID   *int
	FirstSeen   time.Time
	LastSeen    time.Time
	Sightings   int
	BestRSSI    *int
}

// BleDevicesSince returns BLE devices last seen at or after t, most
// recently seen first, limited to limit rows. Rows are grouped by address:
// an address that was seen under more than one address type is collapsed into
// a single row whose AddressType lists the types (comma separated), summing
// its sighting counts and keeping the widest first/last seen span.
func (d *DB) BleDevicesSince(t time.Time, limit int) ([]BleDeviceRow, error) {
	rows, err := d.conn.Query(
		`SELECT ble_devices.address,
		        group_concat(DISTINCT ble_devices.address_type),
		        MAX(ble_devices.name),
		        MAX(ble_devices.company_id),
		        MIN(ble_devices.first_seen),
		        MAX(ble_devices.last_seen),
		        SUM(ble_devices.sighting_count),
		        MAX(ble_devices.best_rssi_dbm)
		 FROM ble_devices
		 WHERE ble_devices.last_seen >= ?
		 GROUP BY ble_devices.address
		 ORDER BY MAX(ble_devices.last_seen) DESC
		 LIMIT ?`,
		formatTime(t), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BleDeviceRow
	for rows.Next() {
		var (
			address       string
			addressType   string
			name          sql.NullString
			companyID     sql.NullInt64
			firstSeenText string
			lastSeenText  string
			sightingCount int
			bestRSSI      sql.NullInt64
		)
		if err := rows.Scan(&address, &addressType, &name, &companyID, &firstSeenText, &lastSeenText, &sightingCount, &bestRSSI); err != nil {
			return nil, err
		}
		firstSeen, err := parseTime(firstSeenText)
		if err != nil {
			return nil, err
		}
		lastSeen, err := parseTime(lastSeenText)
		if err != nil {
			return nil, err
		}
		result = append(result, BleDeviceRow{
			Address:     address,
			AddressType: addressType,
			Name:        nullStringPtr(name),
			CompanyID:   nullInt64Ptr(companyID),
			FirstSeen:   firstSeen,
			LastSeen:    lastSeen,
			Sightings:   sightingCount,
			BestRSSI:    nullInt64Ptr(bestRSSI),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Counts summarizes activity from dwells started at or after a cutoff time.
type Counts struct {
	WifiAPs     int
	WifiClients int
	WifiFrames  int
	BleDevices  int
	BlePackets  int
}

// CountsSince counts distinct WiFi access points and clients, distinct BLE
// devices, and sums WiFi frame_count and BLE packet_count, all restricted
// to sightings whose dwell started at or after t.
func (d *DB) CountsSince(t time.Time) (Counts, error) {
	since := formatTime(t)
	var counts Counts

	if err := d.conn.QueryRow(
		`SELECT COUNT(DISTINCT wifi_sightings.wifi_device_id)
		 FROM wifi_sightings
		 INNER JOIN dwells ON dwells.dwell_id = wifi_sightings.dwell_id
		 INNER JOIN wifi_devices ON wifi_devices.wifi_device_id = wifi_sightings.wifi_device_id
		 WHERE dwells.started_at >= ? AND wifi_devices.device_kind = ?`,
		since, "ap",
	).Scan(&counts.WifiAPs); err != nil {
		return Counts{}, err
	}

	if err := d.conn.QueryRow(
		`SELECT COUNT(DISTINCT wifi_sightings.wifi_device_id)
		 FROM wifi_sightings
		 INNER JOIN dwells ON dwells.dwell_id = wifi_sightings.dwell_id
		 INNER JOIN wifi_devices ON wifi_devices.wifi_device_id = wifi_sightings.wifi_device_id
		 WHERE dwells.started_at >= ? AND wifi_devices.device_kind = ?`,
		since, "client",
	).Scan(&counts.WifiClients); err != nil {
		return Counts{}, err
	}

	var wifiFrames sql.NullInt64
	if err := d.conn.QueryRow(
		`SELECT SUM(wifi_sightings.frame_count)
		 FROM wifi_sightings
		 INNER JOIN dwells ON dwells.dwell_id = wifi_sightings.dwell_id
		 WHERE dwells.started_at >= ?`,
		since,
	).Scan(&wifiFrames); err != nil {
		return Counts{}, err
	}
	counts.WifiFrames = int(wifiFrames.Int64)

	if err := d.conn.QueryRow(
		`SELECT COUNT(DISTINCT ble_sightings.ble_device_id)
		 FROM ble_sightings
		 INNER JOIN dwells ON dwells.dwell_id = ble_sightings.dwell_id
		 WHERE dwells.started_at >= ?`,
		since,
	).Scan(&counts.BleDevices); err != nil {
		return Counts{}, err
	}

	var blePackets sql.NullInt64
	if err := d.conn.QueryRow(
		`SELECT SUM(ble_sightings.packet_count)
		 FROM ble_sightings
		 INNER JOIN dwells ON dwells.dwell_id = ble_sightings.dwell_id
		 WHERE dwells.started_at >= ?`,
		since,
	).Scan(&blePackets); err != nil {
		return Counts{}, err
	}
	counts.BlePackets = int(blePackets.Int64)

	return counts, nil
}

// DailyRow is one calendar day (UTC) of activity for a report.
type DailyRow struct {
	Date    string // YYYY-MM-DD, UTC
	Devices int    // distinct devices seen that day
	New     int    // distinct devices first seen that day
}

// DailyWifi returns per-day WiFi activity for sightings at or after t, newest
// day first. Devices are counted by distinct MAC address (a MAC seen as both
// an AP and a client counts once).
func (d *DB) DailyWifi(t time.Time) ([]DailyRow, error) {
	return d.daily(t,
		`SELECT date(wifi_sightings.seen_at), COUNT(DISTINCT wifi_devices.mac)
		 FROM wifi_sightings
		 JOIN wifi_devices ON wifi_devices.wifi_device_id = wifi_sightings.wifi_device_id
		 WHERE wifi_sightings.seen_at >= ?
		 GROUP BY date(wifi_sightings.seen_at)`,
		`SELECT date(wifi_devices.first_seen), COUNT(DISTINCT wifi_devices.mac)
		 FROM wifi_devices
		 WHERE wifi_devices.first_seen >= ?
		 GROUP BY date(wifi_devices.first_seen)`)
}

// DailyBle returns per-day BLE activity for sightings at or after t, newest
// day first. Devices are counted by distinct address.
func (d *DB) DailyBle(t time.Time) ([]DailyRow, error) {
	return d.daily(t,
		`SELECT date(ble_sightings.seen_at), COUNT(DISTINCT ble_devices.address)
		 FROM ble_sightings
		 JOIN ble_devices ON ble_devices.ble_device_id = ble_sightings.ble_device_id
		 WHERE ble_sightings.seen_at >= ?
		 GROUP BY date(ble_sightings.seen_at)`,
		`SELECT date(ble_devices.first_seen), COUNT(DISTINCT ble_devices.address)
		 FROM ble_devices
		 WHERE ble_devices.first_seen >= ?
		 GROUP BY date(ble_devices.first_seen)`)
}

// daily runs a seen-per-day query and a new-per-day query and merges them into
// one row per day, newest first.
func (d *DB) daily(t time.Time, seenSQL, newSQL string) ([]DailyRow, error) {
	seen, err := d.countByDay(seenSQL, formatTime(t))
	if err != nil {
		return nil, err
	}
	fresh, err := d.countByDay(newSQL, formatTime(t))
	if err != nil {
		return nil, err
	}
	days := make([]string, 0, len(seen))
	for day := range seen {
		days = append(days, day)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	result := make([]DailyRow, 0, len(days))
	for _, day := range days {
		result = append(result, DailyRow{Date: day, Devices: seen[day], New: fresh[day]})
	}
	return result, nil
}

func (d *DB) countByDay(query, since string) (map[string]int, error) {
	rows, err := d.conn.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		counts[day] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func stringPtrArg(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func intPtrArg(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}

func nullStringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func nullInt64Ptr(ni sql.NullInt64) *int {
	if !ni.Valid {
		return nil
	}
	v := int(ni.Int64)
	return &v
}
