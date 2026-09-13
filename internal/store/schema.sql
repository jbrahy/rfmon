CREATE TABLE IF NOT EXISTS dwells (
  dwell_id     INTEGER PRIMARY KEY,
  dwell_kind   TEXT    NOT NULL,          -- 'wifi', 'ble', 'sweep'
  channel      INTEGER,                   -- WiFi channel, NULL for ble/sweep
  center_mhz   INTEGER,
  started_at   TIMESTAMP NOT NULL,
  ended_at     TIMESTAMP NOT NULL,
  status       TEXT    NOT NULL,          -- 'ok', 'failed'
  error        TEXT,
  frame_count  INTEGER NOT NULL DEFAULT 0 -- frames or packets decoded
);
CREATE INDEX IF NOT EXISTS dwells_started_at ON dwells (started_at);

CREATE TABLE IF NOT EXISTS wifi_devices (
  wifi_device_id INTEGER PRIMARY KEY,
  mac            TEXT    NOT NULL,
  device_kind    TEXT    NOT NULL,        -- 'ap', 'client'
  ssid           TEXT,                    -- last non-hidden SSID (ap only)
  channel        INTEGER,
  security       TEXT,                    -- 'open','wep','wpa','wpa2','wpa3' (ap only)
  randomized     INTEGER NOT NULL,        -- 0 or 1
  first_seen     TIMESTAMP NOT NULL,
  last_seen      TIMESTAMP NOT NULL,
  sighting_count INTEGER NOT NULL,
  best_snr_db    INTEGER,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (mac, device_kind)
);
CREATE INDEX IF NOT EXISTS wifi_devices_last_seen ON wifi_devices (last_seen);

CREATE TABLE IF NOT EXISTS wifi_sightings (
  wifi_sighting_id INTEGER PRIMARY KEY,
  wifi_device_id   INTEGER NOT NULL REFERENCES wifi_devices (wifi_device_id),
  dwell_id         INTEGER NOT NULL REFERENCES dwells (dwell_id),
  seen_at          TIMESTAMP NOT NULL,    -- dwell started_at
  frame_type       TEXT    NOT NULL,      -- 'beacon','probe_response','probe_request'
  ssid             TEXT,
  channel          INTEGER NOT NULL,
  frame_count      INTEGER NOT NULL,
  best_snr_db      INTEGER,
  decoder          TEXT    NOT NULL       -- 'ofdm' (sub-project B adds 'dsss')
);
CREATE INDEX IF NOT EXISTS wifi_sightings_wifi_device_id ON wifi_sightings (wifi_device_id);
CREATE INDEX IF NOT EXISTS wifi_sightings_dwell_id ON wifi_sightings (dwell_id);
CREATE INDEX IF NOT EXISTS wifi_sightings_seen_at ON wifi_sightings (seen_at);

CREATE TABLE IF NOT EXISTS ble_devices (
  ble_device_id  INTEGER PRIMARY KEY,
  address        TEXT    NOT NULL,
  address_type   TEXT    NOT NULL,
  name           TEXT,                    -- last seen non-empty name
  company_id     INTEGER,                 -- last seen manufacturer company id
  first_seen     TIMESTAMP NOT NULL,
  last_seen      TIMESTAMP NOT NULL,
  sighting_count INTEGER NOT NULL,
  best_rssi_dbm  INTEGER,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (address, address_type)
);
CREATE INDEX IF NOT EXISTS ble_devices_last_seen ON ble_devices (last_seen);

CREATE TABLE IF NOT EXISTS ble_sightings (
  ble_sighting_id       INTEGER PRIMARY KEY,
  ble_device_id         INTEGER NOT NULL REFERENCES ble_devices (ble_device_id),
  dwell_id              INTEGER NOT NULL REFERENCES dwells (dwell_id),
  seen_at               TIMESTAMP NOT NULL,
  pdu_types             TEXT    NOT NULL, -- comma list, e.g. 'ADV_IND,SCAN_RSP'
  name                  TEXT,
  company_id            INTEGER,
  manufacturer_data_hex TEXT,
  service_uuids         TEXT,             -- comma list of 4-hex-digit UUIDs
  tx_power_dbm          INTEGER,
  packet_count          INTEGER NOT NULL,
  best_rssi_dbm         INTEGER
);
CREATE INDEX IF NOT EXISTS ble_sightings_ble_device_id ON ble_sightings (ble_device_id);
CREATE INDEX IF NOT EXISTS ble_sightings_dwell_id ON ble_sightings (dwell_id);
CREATE INDEX IF NOT EXISTS ble_sightings_seen_at ON ble_sightings (seen_at);

CREATE TABLE IF NOT EXISTS schema_versions (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL);
INSERT OR IGNORE INTO schema_versions (version, applied_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
