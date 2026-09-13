# rfmon sub-project A: shared HackRF scheduler, WiFi and Bluetooth LE capture

Date: 2026-09-12
Status: approved in chat, pending spec review
Follows: `docs/superpowers/specs/2026-09-12-rfmon-design.md` (spectrum monitor, built)
Later: sub-project B (802.11b DSSS decoder in Go), sub-project C (baseline, pass-by, vehicle classifier)

## Purpose

Log the WiFi networks, WiFi clients, and Bluetooth LE devices that can be
received at the monitoring location, using the same single HackRF One that
already runs the spectrum monitor, so that later analysis can study passing
vehicles. rfmon grows from one report (spectrum) to three: spectrum, WiFi,
Bluetooth.

## Decisions carried in from brainstorming

- One HackRF, time-sliced. No second radio.
- Capture tools are executed per dwell (no cgo): `hackrf_transfer` for WiFi,
  `ice9-bluetooth` live capture for BLE, `hackrf_sweep` for spectrum.
- WiFi 802.11a/g/n OFDM frames are decoded by gr-ieee802-11 in one
  long-running GNU Radio process fed through a Go-owned pipe. The pipe is the
  seam where sub-project B's Go DSSS decoder will also read the samples.
- 2.4 GHz only (channels 1, 6, 11). 5 GHz is out of scope.
- SQLite via `modernc.org/sqlite` (pure Go, first third-party dependency) for
  devices and sightings; RRD for per-minute counts graphs.
- Sightings are kept forever (no pruning).
- Facts verified on 2026-09-12: Soapy HackRF source in GNU Radio fails to
  stream on this Mac; piping `hackrf_transfer -r -` into GNU Radio works at a
  steady 40 MB/s. ice9 decoded 854 BLE packets from 44 advertisers in 70 s at
  2427 MHz; 20 channels ran at 98 to 101 percent of real time, so use 16.
  gr-ieee802-11 needed the amplifier on (`-a 1 -l 40 -g 30`) to decode 5 GHz
  and decoded 3x more beacons on channel 1 with it.

## Cycle

One cycle, repeated forever:

| Step | Kind | Radio setting | Duration |
|---|---|---|---|
| 1 | wifi | channel 1, 2412 MHz | dwell |
| 2 | wifi | channel 6, 2437 MHz | dwell |
| 3 | wifi | channel 11, 2462 MHz | dwell |
| 4 | ble | center 2427 MHz, 16 channels | dwell |
| 5 | pause or sweep | none, or `hackrf_sweep` spectrum poll | pause, or one poll |

- dwell defaults to 3 s, pause to 5 s.
- Step 5 runs the existing spectrum poll instead of the pause when at least
  `-sweep-every` (default 60 s) has passed since the last poll started, and
  otherwise sleeps the pause.
- Each dwell and poll is a separate process run under a context deadline of
  dwell + 10 s. Cancellation sends SIGINT first, SIGKILL after 3 s (same as
  the spectrum runner).
- A failed step is logged, recorded in `dwells` with status `failed`, and the
  cycle continues with the next step.
- Expected cycle length is about 17 s plus device open/close overhead per
  step (measured during implementation and recorded in the README).

## WiFi dwell

1. Start `hackrf_transfer -r - -f <freq> -s 20000000 -l 40 -g 30 -a 1 -n <dwell_seconds * 20000000>`.
2. Copy its stdout into the OFDM decoder's stdin (`io.Copy`). Nothing else
   writes to that stdin.
3. When hackrf_transfer exits, the dwell ends. Its exit status and the last
   `MB/second` line from its stderr are logged.

### OFDM decoder process

- Script committed at `decoders/wifi_ofdm_rx.py`: the chain from
  `examples/wifi_rx.grc` in gr-ieee802-11 maint-3.10 with
  `blocks.file_descriptor_source` on fd 0, `interleaved_char_to_complex`
  (scale 128), `ieee802_11.frame_equalizer(Equalizer(0), 2437e6, 20e6)`,
  `decode_mac`, `foo.wireshark_connector(127)`, and a file sink on fd 1.
  It runs until stdin closes.
- Started once by rfmon at startup with the GNU Radio Python interpreter
  (`-gr-python`, default `/opt/homebrew/opt/gnuradio/libexec/venv/bin/python`),
  `PYTHONPATH=<decoders>/lib/python3.14/site-packages`, and
  `DYLD_LIBRARY_PATH=<decoders>/lib`. Its stderr goes to
  `data/logs/wifi-ofdm.log`.
- If it exits, rfmon logs it and restarts it before the next WiFi dwell.
- rfmon reads its stdout as a pcap stream (global header, then records) in
  one goroutine for the life of the process.

### Frame attribution and parsing

- Link type 127 (radiotap). The radiotap header written by gr-foo is 17 bytes,
  packed: version u16, length u16, present u32, flags u8, rate u8, channel
  u32, signal u8, noise u8, antenna u8. `signal` holds the decoder's SNR in dB.
  Parsing must use the length field, not a constant.
- Each frame read is attributed to the WiFi dwell that is active when it is
  read, or to the most recent WiFi dwell if read within 1 s after that dwell
  ended. Other frames are counted as unattributed and logged once per minute.
- Only management frames are recorded:
  - beacon (type 0, subtype 8) and probe response (0, 5): device kind `ap`,
    device address = BSSID (addr3), SSID from element 0 (empty or all-zero
    SSID stored as NULL and flagged hidden), channel from element 3 (DS
    Parameter Set) when present, otherwise the dwell channel.
  - probe request (0, 4): device kind `client`, device address = transmitter
    (addr2), probed SSID from element 0 (empty = wildcard, stored as NULL).
- Security for ap frames: RSN element (48) present -> `wpa3` if any AKM suite
  is 00-0F-AC:8 (SAE), else `wpa2`; else vendor element 221 with OUI
  00-50-F2 type 1 -> `wpa`; else capability privacy bit set -> `wep`; else
  `open`.
- `randomized` = locally administered bit (0x02) of the address's first octet.
- Frames shorter than 24 bytes or with truncated elements are skipped, never
  fatal.

## BLE dwell

1. Run `<decoders>/bin/ice9-bluetooth -l -c 2427 -C 16 -w <data>/tmp/ble-dwell.pcap`
   for the dwell, then stop it with SIGINT.
2. Parse the pcap after the process exits; delete the file.

### Packet parsing

- Link type 256 (BLUETOOTH_LE_LL_WITH_PHDR). 10-byte pseudo-header: rf_channel
  u8, signal_power i8 (dBm), noise_power i8, aa_offenses u8, ref_aa u32,
  flags u16. Then access address u32 and the PDU.
- Only packets with access address 0x8E89BED6 (advertising) are recorded.
- PDU types recorded with AdvA and advertising data: ADV_IND (0),
  ADV_NONCONN_IND (2), SCAN_RSP (4), ADV_SCAN_IND (6). ADV_EXT_IND (7) is
  recorded only when its extended header carries AdvA, with no AD parsing.
  SCAN_REQ, CONNECT_IND, ADV_DIRECT_IND, and data-channel packets are counted
  in the dwell's packet total but not recorded as sightings.
- Address type: TxAdd bit 0 -> `public`; TxAdd 1 with top two bits of the most
  significant octet 11 -> `random_static`, 01 -> `random_resolvable`,
  00 -> `random_nonresolvable`.
- AD fields kept: local name (0x08, 0x09), manufacturer specific data (0xFF:
  company id u16 plus the payload as hex), 16-bit service UUIDs (0x02, 0x03),
  TX power level (0x0A). Malformed AD lengths end parsing of that packet.

## Storage

SQLite database `data/rfmon.db`, WAL mode, one writer (the scheduler
goroutine). Schema created on startup if missing (`CREATE TABLE IF NOT
EXISTS`), with a `schema_versions` table holding version 1.

Per dwell, sightings are aggregated in memory by device (and, for WiFi, by
frame type and SSID) and written in one transaction when the dwell's
attribution window closes: one sighting row per group per dwell, not per
frame.

```sql
CREATE TABLE dwells (
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
CREATE INDEX dwells_started_at ON dwells (started_at);

CREATE TABLE wifi_devices (
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
CREATE INDEX wifi_devices_last_seen ON wifi_devices (last_seen);

CREATE TABLE wifi_sightings (
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
CREATE INDEX wifi_sightings_wifi_device_id ON wifi_sightings (wifi_device_id);
CREATE INDEX wifi_sightings_dwell_id ON wifi_sightings (dwell_id);
CREATE INDEX wifi_sightings_seen_at ON wifi_sightings (seen_at);

CREATE TABLE ble_devices (
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
CREATE INDEX ble_devices_last_seen ON ble_devices (last_seen);

CREATE TABLE ble_sightings (
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
CREATE INDEX ble_sightings_ble_device_id ON ble_sightings (ble_device_id);
CREATE INDEX ble_sightings_dwell_id ON ble_sightings (dwell_id);
CREATE INDEX ble_sightings_seen_at ON ble_sightings (seen_at);
```

Timestamps are stored as UTC RFC 3339 text.

## Counts RRD

- `data/rrd/wifi.rrd`: DS `aps`, `clients`, `frames`.
- `data/rrd/ble.rrd`: DS `devices`, `packets`.
- Same step, heartbeat, and RRAs as the band RRDs; GAUGE, min 0, no max.
- Updated once per spectrum poll (about once a minute) with distinct devices
  and frame or packet totals from dwells that started in the preceding 60 s,
  queried from SQLite.

## Web reports

Existing server, new routes. All pages link to each other.

| Route | Content |
|---|---|
| `GET /` | spectrum index (unchanged content, adds navigation) |
| `GET /wifi` | counts day graph; APs seen in the last 24 h (mac, SSID or hidden, channel, security, randomized, first seen, last seen, sightings, best SNR), sorted by last seen, max 500 rows; clients seen in the last 24 h with their probed SSIDs; top 50 probed SSIDs in the last 24 h |
| `GET /wifi/graphs` | wifi counts day, week, month, year |
| `GET /bluetooth` | counts day graph; devices seen in the last 24 h (address, type, name, company id, first seen, last seen, sightings, best RSSI), sorted by last seen, max 500 rows |
| `GET /bluetooth/graphs` | ble counts day, week, month, year |
| `GET /graph/wifi-<period>.png`, `GET /graph/ble-<period>.png` | counts graphs, same 60 s cache |

Queries use parameterized SQL only and full table names, no aliases, no
`SELECT *`.

## Installation of decoders

`scripts/install-decoders.sh`:

- `brew install gnuradio liquid-dsp pybind11 cmake` (skips installed ones).
- Fetches pinned commits into a temporary build directory:
  gr-foo `4c2a471` (maint-3.10), gr-ieee802-11 `ad0598e` (maint-3.10),
  ice9-bluetooth-sniffer `788c5bc`.
- Builds gr-foo then gr-ieee802-11 with
  `CMAKE_INSTALL_PREFIX=$HOME/.local/share/rfmon/decoders`, and ice9 with
  `-DUSE_VKFFT=OFF` (the Metal build fails to link on macOS 26), installing
  `ice9-bluetooth` into `<prefix>/bin`.
- Verifies by importing `ieee802_11` and `foo` with the GNU Radio Python and
  running `ice9-bluetooth -h`. Prints the prefix at the end.
- No sudo. Idempotent.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-data` | `./data` | unchanged |
| `-listen` | `127.0.0.1:8080` | unchanged |
| `-passes` | `5` | unchanged |
| `-pause` | `5s` | pause between cycles (was 60 s between spectrum polls) |
| `-dwell` | `3s` | WiFi and BLE dwell length |
| `-sweep-every` | `60s` | minimum time between spectrum polls |
| `-decoders` | `$HOME/.local/share/rfmon/decoders` | decoder install prefix |
| `-gr-python` | `/opt/homebrew/opt/gnuradio/libexec/venv/bin/python` | GNU Radio Python |
| `-spectrum-only` | `false` | old behavior: spectrum polls with `-pause` between them, no decoders needed |

Startup checks (fatal with an install hint): `hackrf_sweep`, `rrdtool`, and
unless `-spectrum-only`: `hackrf_transfer`, `<decoders>/bin/ice9-bluetooth`,
and a successful `import ieee802_11, foo` with `-gr-python`.

## Code layout

| Path | Responsibility |
|---|---|
| `cmd/rfmon/main.go` | flags, startup checks, wiring; the existing spectrum `poll` function stays here and is passed to the scheduler as its sweep step |
| `internal/scheduler` | the cycle: step order, dwell timing, sweep substitution, failure recording |
| `internal/pcap` | streaming pcap reader (global header, records) shared by wifi and ble |
| `internal/wifi` | OFDM decoder process supervisor, hackrf_transfer dwell runner, radiotap and 802.11 management frame parsing, per-dwell aggregation |
| `internal/ble` | ice9 dwell runner, LE pseudo-header and advertising PDU parsing, per-dwell aggregation |
| `internal/store` | SQLite open, schema, dwell, device upsert, sighting insert, report queries, counts queries |
| `internal/rrd` | adds counts RRD create/update/graph |
| `internal/web` | adds `/wifi`, `/bluetooth`, their graph pages, navigation |
| `decoders/wifi_ofdm_rx.py` | GNU Radio OFDM receive chain, stdin IQ to stdout pcap |
| `scripts/install-decoders.sh` | decoder build and install |

## Privacy and fixtures

- rfmon stores identifiers of nearby devices. README gets a section stating
  that capture is passive receive only, data stays in `data/` on the local
  machine, and captured data should not be published.
- Test fixtures must not contain real device addresses or SSIDs. Parsers are
  tested with synthetic frames and packets built in test code. The captures
  taken during the 2026-09-12 test stay out of the repo.

## Testing

- `internal/pcap`: global header and record parsing, truncated stream,
  unsupported link type.
- `internal/wifi`: radiotap length handling; beacon, probe response, probe
  request parsing from synthetic frames; hidden SSID; DS channel element;
  each security classification; randomized bit; short and truncated frames;
  attribution window (active dwell, 1 s grace, unattributed); aggregation
  groups; supervisor restart using a fake decoder script that emits a
  synthetic pcap.
- `internal/ble`: pseudo-header; each recorded PDU type; address types; AD
  parsing (name, manufacturer, UUIDs, TX power, malformed length);
  non-advertising access address ignored; aggregation.
- `internal/store`: schema creation idempotent; upsert updates last seen,
  count, best signal, keeps first seen; sightings linked to dwells; report
  and counts queries against a temp database.
- `internal/scheduler`: with fake step runners and an injected clock: step
  order, sweep substitution after 60 s, failures recorded and cycle
  continues, cancellation stops within one step.
- `internal/web`: new routes render with a stub store and stub grapher; bad
  graph names 404.
- Live check (implementation's last task): run for 5 minutes on the HackRF;
  `dwells` has ok rows of all three kinds; at least one WiFi AP and at least
  one BLE device recorded; `/wifi` and `/bluetooth` return 200 with rows;
  measured cycle length logged; clean shutdown.

## Out of scope

802.11b DSSS decoding (sub-project B), vendor names from OUI or company id
tables, stationary baseline, pass-by detection, and vehicle classification
(sub-project C), 5 GHz, Bluetooth Classic, data retention limits.
