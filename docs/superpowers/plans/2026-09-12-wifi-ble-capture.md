# WiFi and Bluetooth LE Capture (sub-project A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend rfmon so its single HackRF time-slices between spectrum sweeps, WiFi capture (channels 1/6/11), and Bluetooth LE capture, recording networks, WiFi clients, and BLE devices to SQLite plus per-minute counts to RRD, with `/wifi` and `/bluetooth` web reports.

**Architecture:** A new `internal/scheduler` runs one cycle of dwell steps. WiFi dwells run `hackrf_transfer` and pipe IQ to one long-running GNU Radio OFDM decoder process (`decoders/wifi_ofdm_rx.py`) whose radiotap pcap stdout is parsed by `internal/wifi`. BLE dwells run `ice9-bluetooth` to a pcap file parsed by `internal/ble`. Both share `internal/pcap`. Devices and sightings go to SQLite via `internal/store` (modernc.org/sqlite, pure Go). Counts go to new RRD files. `internal/web` gains report routes. The existing spectrum `poll` becomes the scheduler's sweep step.

**Tech Stack:** Go 1.26, modernc.org/sqlite (pure Go, cgo-free), hackrf tools 2026.01.3, rrdtool 1.11.0, GNU Radio 3.10.12 with gr-foo + gr-ieee802-11 (maint-3.10) and ice9-bluetooth (CPU/fftw build).

**Spec:** `docs/superpowers/specs/2026-09-12-wifi-ble-capture-design.md`

## Global Constraints

- Module path: `github.com/jbrahy/rfmon`. Go version line in go.mod: `go 1.26`.
- Only third-party Go dependency allowed: `modernc.org/sqlite` (and its transitive deps). No other new modules. No cgo in rfmon's own code.
- No em dashes and no emojis in code, comments, docs, scripts, or commit messages.
- Every commit message ends with these two lines:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu
  ```
- Repo root: `/Users/jbrahy/OtherProjects/HackRfOne`. Branch: `wifi-ble` (created off `main` before Task 1).
- Timestamps stored and compared as UTC RFC3339 text (`time.RFC3339`), computed from `time.Now().UTC()`.
- SQLite DSN: `file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)`, driver name `sqlite`.
- SQL: parameterized only (`?` placeholders), full table and column names, no `SELECT *`, no aliases.
- Do not touch the HackRF except in the Task 12 live check. Before that task, confirm `pgrep -fl hackrf_` and `pgrep -fl rfmon` print nothing.
- Decoder tool locations resolve under a prefix (default `$HOME/.local/share/rfmon/decoders`): `<prefix>/bin/ice9-bluetooth`, GNU Radio Python modules under `<prefix>/lib/python3.14/site-packages`.
- Pinned decoder commits: gr-foo `4c2a471`, gr-ieee802-11 `ad0598e`, ice9-bluetooth-sniffer `788c5bc`.

## Existing interfaces (already committed, do not change signatures)

- `sweep.Runner{Bin string; Passes int; Timeout time.Duration}`, `(Runner).Run(ctx) (sweep.Spectrum, error)`, `sweep.DropSpurs(Spectrum)`, `sweep.ExpectedBins` (25200), `sweep.Spectrum{Hz []int64; DB []float64}`.
- `bands.All []bands.Band`, `bands.Compute(Spectrum, Band) (Metrics, bool)`.
- `rrd.Store{Dir, Bin string}`, `(Store).Update(slug string, t time.Time, m bands.Metrics) error`, `(Store).Graph(slug, label, period string) ([]byte, error)`, `rrd.Periods`.
- `web.New(g web.Grapher, bs []bands.Band) *web.Server`, `(Server).Handler() http.Handler`, `web.Grapher` = `interface{ Graph(slug, label, period string) ([]byte, error) }`.
- `cmd/rfmon/main.go` has `poll(ctx, runner, store, specDir)` and startup tool checks.

## File Map

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | add modernc.org/sqlite |
| `internal/pcap/pcap.go` + test | streaming pcap reader (global header + records) |
| `internal/store/store.go` + test | SQLite open, schema, upsert/insert, report + counts queries |
| `internal/store/schema.sql` | embedded schema |
| `internal/wifi/parse.go` + test | radiotap + 802.11 management frame parsing |
| `internal/wifi/dwell.go` + test | OFDM decoder supervisor, hackrf_transfer dwell runner, aggregation |
| `internal/ble/parse.go` + test | LE pseudo-header + advertising PDU parsing |
| `internal/ble/dwell.go` + test | ice9 dwell runner, aggregation |
| `internal/rrd/rrd.go` | add counts RRD create/update/graph |
| `internal/rrd/counts_test.go` | counts RRD integration test |
| `internal/scheduler/scheduler.go` + test | cycle order, timing, sweep substitution, failure handling |
| `internal/web/web.go` + test | `/wifi`, `/bluetooth`, graph routes, navigation |
| `cmd/rfmon/main.go` | flags, startup checks, wiring, scheduler |
| `decoders/wifi_ofdm_rx.py` | GNU Radio OFDM chain, fd0 IQ to fd1 pcap |
| `scripts/install-decoders.sh` | build/install decoders |
| `README.md` | WiFi/BLE section, privacy note |

---

### Task 1: Add SQLite dependency and the store package

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/store/store.go`, `internal/store/schema.sql`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing from other new tasks.
- Produces:
  - `type DB struct { ... }`; `func Open(path string) (*DB, error)` (applies schema); `func (*DB) Close() error`.
  - `type Dwell struct { Kind string; Channel, CenterMHz int; StartedAt, EndedAt time.Time; Status, Error string; FrameCount int }` and `func (*DB) InsertDwell(Dwell) (int64, error)` returning dwell_id.
  - `type WifiSighting struct { MAC, DeviceKind string; SSID *string; Channel int; Security *string; Randomized bool; FrameType string; FrameCount int; BestSNR *int; Decoder string }`
  - `func (*DB) RecordWifi(dwellID int64, seen time.Time, s WifiSighting) error` (upserts wifi_devices, inserts wifi_sightings).
  - `type BleSighting struct { Address, AddressType string; Name *string; CompanyID *int; ManufacturerHex, ServiceUUIDs *string; TxPower *int; PDUTypes string; PacketCount int; BestRSSI *int }`
  - `func (*DB) RecordBle(dwellID int64, seen time.Time, s BleSighting) error`.
  - Report/counts queries (used by web + counts RRD):
    - `type WifiAP struct { MAC string; SSID *string; Channel int; Security *string; Randomized bool; FirstSeen, LastSeen time.Time; Sightings int; BestSNR *int }`; `func (*DB) WifiAPsSince(t time.Time, limit int) ([]WifiAP, error)`.
    - `type WifiClient struct { MAC string; Randomized bool; FirstSeen, LastSeen time.Time; Sightings int; ProbedSSIDs []string }`; `func (*DB) WifiClientsSince(t time.Time, limit int) ([]WifiClient, error)`.
    - `type ProbedSSID struct { SSID string; Count int }`; `func (*DB) TopProbedSSIDs(t time.Time, limit int) ([]ProbedSSID, error)`.
    - `type BleDeviceRow struct { Address, AddressType string; Name *string; CompanyID *int; FirstSeen, LastSeen time.Time; Sightings int; BestRSSI *int }`; `func (*DB) BleDevicesSince(t time.Time, limit int) ([]BleDeviceRow, error)`.
    - `type Counts struct { WifiAPs, WifiClients, WifiFrames, BleDevices, BlePackets int }`; `func (*DB) CountsSince(t time.Time) (Counts, error)` (distinct devices and summed frame/packet counts from dwells started at/after t).

- [ ] **Step 1: Add the dependency**

Run: `go get modernc.org/sqlite@latest` then `go mod tidy`. Confirm `go.mod` lists `modernc.org/sqlite` and the module still says `go 1.26`. Do not add any other direct dependency.

- [ ] **Step 2: Write the schema file**

Create `internal/store/schema.sql` with the exact DDL from the spec's "Storage" section (tables `dwells`, `wifi_devices`, `wifi_sightings`, `ble_devices`, `ble_sightings`, all listed indexes), plus:
```sql
CREATE TABLE IF NOT EXISTS schema_versions (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL);
INSERT OR IGNORE INTO schema_versions (version, applied_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
```
Every `CREATE TABLE` and `CREATE INDEX` uses `IF NOT EXISTS`. Embed it with `//go:embed schema.sql` in store.go.

- [ ] **Step 3: Write failing tests**

`internal/store/store_test.go` covering, against a `t.TempDir()` database:
- Open twice on the same path succeeds (schema idempotent), and `schema_versions` has exactly one row with version 1.
- `InsertDwell` returns an id; a second insert returns a larger id.
- `RecordWifi` for a new AP creates one `wifi_devices` row (kind `ap`) and one `wifi_sightings` row; a second `RecordWifi` for the same MAC+kind with a later time and higher SNR keeps `first_seen`, advances `last_seen`, sets `sighting_count`=2, and raises `best_snr_db`; a nil SSID stays NULL.
- `RecordWifi` with kind `client` and a probed SSID feeds `WifiClientsSince` and `TopProbedSSIDs`.
- `RecordBle` upsert parallels wifi (name/company_id updated, first_seen kept, best_rssi is the max i.e. closest to 0... use "greater dBm is stronger": keep the max).
- `WifiAPsSince` / `BleDevicesSince` respect the `since` cutoff and the row limit and sort by last_seen descending.
- `CountsSince` counts distinct devices and sums frame/packet counts across dwells at/after the cutoff.
Assert concrete values; do not just check "no error".

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL, `undefined: Open`.

- [ ] **Step 5: Implement store.go**

Implement the package. Key points:
- `Open`: `sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")`, `db.SetMaxOpenConns(1)` (one writer, avoids WAL writer contention), then `db.Exec(schema)`.
- Upserts: `INSERT ... ON CONFLICT(mac, device_kind) DO UPDATE SET last_seen=excluded.last_seen, sighting_count=wifi_devices.sighting_count+1, ssid=COALESCE(excluded.ssid, wifi_devices.ssid), best_snr_db=MAX(...)`. For MAX with NULLs use `MAX(COALESCE(wifi_devices.best_snr_db, excluded.best_snr_db), COALESCE(excluded.best_snr_db, wifi_devices.best_snr_db))` or handle in Go by reading then writing inside one transaction; either is fine, but do it in a transaction so device upsert and sighting insert are atomic, and return the device id for the sighting FK (`RETURNING wifi_device_id` is supported).
- Times: format with `t.UTC().Format(time.RFC3339)`; parse back with `time.Parse`.
- All queries parameterized, full names, no `SELECT *`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/store/ && go vet ./...`
Expected: `ok  github.com/jbrahy/rfmon/internal/store`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "Add store package: SQLite devices and sightings

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 2: pcap streaming reader

**Files:**
- Create: `internal/pcap/pcap.go`, `internal/pcap/pcap_test.go`

**Interfaces:**
- Produces:
  - `type Packet struct { Time time.Time; Data []byte }`
  - `type Reader struct { ... }`; `func NewReader(r io.Reader) (*Reader, uint32, error)` returns the reader and the link-layer type from the global header.
  - `func (*Reader) Next() (Packet, error)` returns one record; `io.EOF` at clean end. Little-endian and big-endian magic both handled; microsecond timestamps.

- [ ] **Step 1: Write failing tests**

`internal/pcap/pcap_test.go`:
- Build an in-memory little-endian pcap: global header (magic `0xa1b2c3d4`, version 2.4, snaplen 65535, network 127), then two records with known ts_sec/ts_usec and payloads. Assert `NewReader` returns network 127, `Next` returns both packets with correct times and bytes, and a third `Next` returns `io.EOF`.
- A truncated record header and a truncated payload each return a non-EOF error.
- Big-endian magic `0xd4c3b2a1` is accepted and fields byte-swapped.
- An unknown magic returns an error from `NewReader`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/pcap/`
Expected: FAIL, `undefined: NewReader`.

- [ ] **Step 3: Implement pcap.go**

Global header 24 bytes; record header 16 bytes (ts_sec u32, ts_usec u32, incl_len u32, orig_len u32). Pick byte order from magic. `Next` reads the 16-byte header with `io.ReadFull` (translate `io.EOF` on the very first byte to `io.EOF`; a partial header is `io.ErrUnexpectedEOF`), then reads `incl_len` bytes. Time = `time.Unix(int64(sec), int64(usec)*1000).UTC()`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/pcap/ && go vet ./...`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/pcap
git commit -m "Add pcap package: streaming reader for wifi and ble captures

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 3: WiFi frame parsing

**Files:**
- Create: `internal/wifi/parse.go`, `internal/wifi/parse_test.go`

**Interfaces:**
- Consumes: nothing (operates on a radiotap frame `[]byte`).
- Produces:
  - `type Frame struct { Kind string; MAC string; SSID *string; Hidden bool; Channel int; Security *string; Randomized bool; SNR *int; FrameType string }` where `Kind` is `ap` or `client`, `FrameType` is `beacon`/`probe_response`/`probe_request`.
  - `func Parse(radiotapFrame []byte) (Frame, bool)` returns false for frames that are not one of the three recorded management subtypes or are malformed.
  - Exposed helpers for tests: `func ParseRadiotap(b []byte) (snr int, hasSNR bool, dot11 []byte, ok bool)`.

Radiotap header written by gr-foo is fixed 17 bytes, packed little-endian: version u16, length u16, present u32 (=0x0000086e), flags u8, rate u8, channel u32, signal u8, noise u8, antenna u8. Use the length field (bytes 2-3) to find the 802.11 frame, not the constant, so a different length still works. `signal` is the SNR in dB (0..255, treat as unsigned).

- [ ] **Step 1: Write failing tests**

`internal/wifi/parse_test.go`. Build synthetic radiotap+802.11 frames in a helper (no real captures):
- Beacon (FC 0x80) with fixed params (12 bytes) then tagged elements: SSID element (0) "TestNet", DS element (3) channel 6, RSN element (48) with an SAE AKM -> expect Kind `ap`, FrameType `beacon`, SSID "TestNet", Channel 6, Security `wpa3`, not hidden.
- Beacon with a zero-length SSID element -> Hidden true, SSID nil.
- Beacon with RSN AKM PSK only -> `wpa2`; with no RSN but vendor WPA (221, 00-50-F2, type 1) -> `wpa`; with privacy capability bit set and no RSN/vendor -> `wep`; none of these -> `open`.
- Probe request (FC 0x40) transmitter addr with locally-administered bit set, SSID element "Home" -> Kind `client`, FrameType `probe_request`, SSID "Home", Randomized true.
- Probe request with wildcard (zero-length) SSID -> SSID nil.
- Probe response (FC 0x50) -> Kind `ap`, FrameType `probe_response`.
- A data frame (FC 0x08) -> Parse returns ok=false.
- A frame shorter than 24 bytes and one with a tag length running past the buffer -> ok=false, no panic.
- Radiotap `signal` value 42 -> Frame.SNR points to 42.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/wifi/`
Expected: FAIL, `undefined: Parse`.

- [ ] **Step 3: Implement parse.go**

- MAC formatting: lowercase colon-separated. Beacon/probe-resp use addr3 (bytes 16..21 of the 802.11 header) as BSSID; probe-req uses addr2 (bytes 10..15).
- Randomized: `(firstOctet & 0x02) != 0`.
- Element walk starts after the MAC header (24 bytes) plus, for beacon/probe-resp, 12 bytes of fixed params; probe-req has no fixed params. Guard every element length against the remaining buffer.
- Security: check RSN (48) first (SAE AKM suite `00 0F AC 08` -> wpa3, else wpa2), then vendor 221 OUI `00 50 F2` type 1 -> wpa, then privacy bit (capability info, byte offset 34..35 of the frame for beacon/probe-resp) -> wep, else open. Security is nil for clients.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/wifi/ && go vet ./...`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/wifi/parse.go internal/wifi/parse_test.go
git commit -m "Add wifi frame parser: radiotap and 802.11 management frames

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 4: BLE packet parsing

**Files:**
- Create: `internal/ble/parse.go`, `internal/ble/parse_test.go`

**Interfaces:**
- Produces:
  - `type Adv struct { Address, AddressType string; PDUType string; Name *string; CompanyID *int; ManufacturerHex *string; ServiceUUIDs []string; TxPower *int; RSSI *int }`
  - `func Parse(lePhdrPacket []byte) (Adv, bool)` returns false for non-advertising or malformed packets.

LE pseudo-header 10 bytes packed little-endian: rf_channel u8, signal_power i8 (dBm), noise_power i8, aa_offenses u8, ref_aa u32, flags u16. Then access address u32 (advertising = 0x8E89BED6), then PDU: header byte (type = low 4 bits, TxAdd = bit 6), length byte, payload. For ADV_IND(0)/ADV_NONCONN_IND(2)/SCAN_RSP(4)/ADV_SCAN_IND(6): first 6 payload bytes are AdvA (little-endian), remainder is AD structures.

- [ ] **Step 1: Write failing tests**

`internal/ble/parse_test.go`, synthetic packets:
- ADV_IND, public address, AD: flags, complete local name "Sensor", manufacturer 0x004C + 2 bytes -> Adv Address formatted big-endian colon, AddressType `public`, PDUType `ADV_IND`, Name "Sensor", CompanyID 0x4C, ManufacturerHex the full hex, RSSI from signal_power.
- Random static address (top two bits 11) -> `random_static`; resolvable (01) -> `random_resolvable`; nonresolvable (00) -> `random_nonresolvable`.
- SCAN_RSP with 16-bit service UUIDs (0x03) -> ServiceUUIDs populated as 4-hex-digit strings.
- TX power AD (0x0A) -> TxPower set.
- Non-advertising access address (e.g. 0x11223344) -> ok=false.
- SCAN_REQ (type 3) -> ok=false.
- Truncated AD length running past the payload -> parse stops, no panic, still returns what was parsed with ok=true if AdvA was present.
- rf_channel/signal parsing: signal_power -60 -> RSSI -60.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ble/`
Expected: FAIL, `undefined: Parse`.

- [ ] **Step 3: Implement parse.go**

Address formatted as the six AdvA bytes reversed, lowercase colon-separated. AddressType from TxAdd and the top bits of the most significant address octet. AD walk: `len, type, value[len-1]`; stop on a length that overflows. Manufacturer (0xFF): first two bytes little-endian company id, whole value hex-encoded. Names 0x08/0x09. Service UUID lists 0x02/0x03 as `%04x`. TX power 0x0A signed byte. ADV_EXT_IND (7) returns ok=false in this task (extended header parsing is out of scope; note it in a comment).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ble/ && go vet ./...`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/ble/parse.go internal/ble/parse_test.go
git commit -m "Add ble parser: LE pseudo-header and advertising PDUs

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 5: WiFi dwell runner and OFDM decoder supervisor

**Files:**
- Create: `internal/wifi/dwell.go`, `internal/wifi/dwell_test.go`
- Create: `internal/wifi/testdata/fake_decoder.sh`, `internal/wifi/testdata/fake_transfer.sh`

**Interfaces:**
- Consumes: `internal/pcap`, `internal/wifi` Parse (Task 3), `internal/store` types (Task 1).
- Produces:
  - `type Decoder struct { Python, Script, ModulePath, LibPath string }`; `type Supervisor struct { ... }`; `func NewSupervisor(d Decoder) *Supervisor`; `func (*Supervisor) Start() error` (starts the process, begins reading its stdout pcap in a goroutine); `func (*Supervisor) Stdin() io.Writer`; `func (*Supervisor) Frames() <-chan wifi.Frame` (parsed management frames as they arrive; each frame carries the read time); `func (*Supervisor) Ensure() error` (restart if the process died); `func (*Supervisor) Close() error`.
  - `type TransferRunner struct { Bin string }`; `func (TransferRunner) Dwell(ctx context.Context, w io.Writer, freqHz int64, dur time.Duration) error` runs `hackrf_transfer -r - -f <freq> -s 20000000 -l 40 -g 30 -a 1 -n <dur*20e6>` writing IQ to w, SIGINT cancel + 3s WaitDelay.
  - `type Aggregator struct{}` with `func NewAggregator() *Aggregator`, `func (*Aggregator) Add(wifi.Frame)`, `func (*Aggregator) Drain() []store.WifiSighting` grouping by (MAC, FrameType, SSID) counting frames and keeping best SNR.

Because Frame from Parse (Task 3) has no timestamp, the Supervisor wraps parsed frames with their read time; define `type TimedFrame struct { wifi.Frame; At time.Time }` and make `Frames()` a `<-chan TimedFrame`. Update the Produces list accordingly. The scheduler (Task 9) does attribution by comparing `At` to dwell windows.

- [ ] **Step 1: Write the fake scripts**

`internal/wifi/testdata/fake_decoder.sh`: reads and discards stdin in the background, and writes a valid radiotap pcap to stdout: the global header, then one synthetic beacon frame, then sleeps briefly and stays alive until stdin closes. Keep it a few lines of `printf`/`dd` producing bytes, or better, a tiny Go program is overkill; use `python3` here (allowed for a test fixture) to emit exact bytes.
`internal/wifi/testdata/fake_transfer.sh`: writes N zero bytes to stdout where N derives from the `-n` argument, then exits 0.
`chmod +x internal/wifi/testdata/*.sh`.

- [ ] **Step 2: Write failing tests**

- Supervisor with `fake_decoder.sh` as the Python (Decoder.Python=`/bin/sh`, Script=path to fake_decoder.sh): Start, read one `TimedFrame` from `Frames()` whose Frame.FrameType is `beacon`, then Close. `At` is within a second of now.
- Supervisor.Ensure restarts after the process exits (use a fake that exits immediately once, detect a second start).
- TransferRunner.Dwell with `fake_transfer.sh`: writes the expected number of bytes to a buffer and returns nil; a canceled context returns promptly.
- Aggregator: add three frames for the same MAC/type/SSID with SNR 10, 30, 20 -> one sighting, FrameCount 3, BestSNR 30; different SSIDs -> separate sightings.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/wifi/`
Expected: FAIL, `undefined: NewSupervisor`.

- [ ] **Step 4: Implement dwell.go**

- Supervisor runs `exec.Command(d.Python, d.Script)` with env `PYTHONPATH=d.ModulePath`, `DYLD_LIBRARY_PATH=d.LibPath`; stdin is a pipe (`Stdin()` returns it), stdout is a pipe read by a goroutine using `pcap.NewReader`; each packet goes through `wifi.Parse`, and successful frames are sent as `TimedFrame` on a buffered channel (drop-oldest if full so a stalled consumer cannot block the reader; log the drop count once per minute). Stderr to the process's configured log writer (default `os.Stderr`; scheduler will point it at a file).
- `Ensure` checks whether the process exited (non-nil from a stored `Wait` result) and restarts.
- `TransferRunner.Dwell`: `cmd.Cancel = SIGINT`, `WaitDelay=3s`, stdout to w, compute `-n` as `int64(dur.Seconds())*20_000_000`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/wifi/ && go vet ./...`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/wifi/dwell.go internal/wifi/dwell_test.go internal/wifi/testdata
git commit -m "Add wifi dwell runner and OFDM decoder supervisor

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 6: BLE dwell runner

**Files:**
- Create: `internal/ble/dwell.go`, `internal/ble/dwell_test.go`
- Create: `internal/ble/testdata/fake_ice9.sh`

**Interfaces:**
- Consumes: `internal/pcap`, `internal/ble` Parse (Task 4), `internal/store` types.
- Produces:
  - `type Runner struct { Bin string }`; `func (Runner) Dwell(ctx context.Context, center, channels int, pcapPath string, dur time.Duration) ([]store.BleSighting, error)` runs ice9 for `dur`, SIGINT to stop, then parses `pcapPath` (link type 256) and returns aggregated sightings; deletes the pcap before returning.
  - Aggregation groups by Address+AddressType, unions PDU types, keeps last non-nil name/company/etc., sums packet counts, keeps best (max) RSSI.

- [ ] **Step 1: Write the fake ice9 script**

`internal/ble/testdata/fake_ice9.sh`: ignores its flags, writes a link-type-256 pcap containing two synthetic ADV_IND packets for the same address to the `-w` path, then waits until signaled. `chmod +x`.

- [ ] **Step 2: Write failing tests**

- Runner.Dwell with the fake: returns one sighting for the repeated address with PacketCount 2, and the pcap file is deleted afterward.
- A canceled context returns promptly and still parses whatever the fake wrote.
- Aggregation: two advs same address, one with a name and one without -> Name retained; PDU types unioned.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/ble/`
Expected: FAIL, `undefined: Runner`.

- [ ] **Step 4: Implement dwell.go**

`cmd.Cancel` = SIGINT, `WaitDelay=3s`; run for `dur` by starting the process and stopping it after a timer, or by giving the context a `dur` deadline. After Wait, open `pcapPath` with `pcap.NewReader`, require link type 256, parse each packet with `ble.Parse`, aggregate, delete the file (defer), return.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/ble/ && go vet ./...`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/ble/dwell.go internal/ble/dwell_test.go internal/ble/testdata
git commit -m "Add ble dwell runner using ice9-bluetooth

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 7: Counts RRD

**Files:**
- Modify: `internal/rrd/rrd.go`
- Create: `internal/rrd/counts_test.go`

**Interfaces:**
- Produces (added to `rrd.Store`):
  - `func (s Store) UpdateCounts(name string, t time.Time, ds map[string]float64) error` creating `<Dir>/<name>.rrd` on first use with the given DS names (GAUGE:180:0:U) and the same four RRAs (AVERAGE and MAX). `name` is `wifi` or `ble`; DS order is the sorted keys, recorded so updates match.
  - `func (s Store) GraphCounts(name, title string, dsNames []string, period string) ([]byte, error)` drawing one LINE per DS.

Because RRD requires a fixed DS list at create time and a stable update order, `UpdateCounts` must sort the DS names and store all of them at create; callers always pass the same key set. Document this in the code.

- [ ] **Step 1: Write failing test**

`internal/rrd/counts_test.go` (skips if rrdtool absent, but must actually run): create a temp Store, `UpdateCounts("wifi", t, {"aps":3,"clients":5,"frames":40})`, then `rrdtool lastupdate` shows the three values in sorted DS order; `GraphCounts("wifi","WiFi",[]string{"aps","clients","frames"},"day")` returns PNG magic bytes; a second update at t+64 succeeds.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/rrd/ -run Counts`
Expected: FAIL, `undefined: ... UpdateCounts`.

- [ ] **Step 3: Implement**

Add the two methods reusing the existing `run` helper and RRA list. Create with `DS:<name>:GAUGE:180:0:U` for each sorted name. Update value string is `t.Unix():v1:v2:...` in sorted order. Graph: DEF per DS from the AVERAGE RRA, LINE1 with distinct colors (cycle a small fixed palette), title `title (period)`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/rrd/ && go vet ./...`
Expected: `ok`, and `go test -v ./internal/rrd/ -run Counts` does not print SKIP.

- [ ] **Step 5: Commit**

```bash
git add internal/rrd/rrd.go internal/rrd/counts_test.go
git commit -m "Add counts RRD for wifi and ble device rates

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 8: Scheduler

**Files:**
- Create: `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: everything above via small function-typed steps so it stays testable.
- Produces:
  - `type Step struct { Kind string; Channel, CenterMHz int }`
  - `type Deps struct {`
    - `WifiDwell func(ctx context.Context, step Step) error` (runs one hackrf_transfer dwell into the OFDM supervisor and returns when it ends),
    - `DrainWifi func(window Window) []store.WifiSighting` (frames whose read time is in the dwell window),
    - `BleDwell func(ctx context.Context, step Step) ([]store.BleSighting, error)`,
    - `Sweep func(ctx context.Context) error` (the existing spectrum poll),
    - `RecordDwell func(store.Dwell, []store.WifiSighting, []store.BleSighting) error`,
    - `UpdateCounts func(now time.Time) error`,
    - `Now func() time.Time`,
    - `Dwell, Pause, SweepEvery time.Duration` }
  - `type Window struct { Start, End time.Time }`
  - `func Run(ctx context.Context, d Deps)` loops cycles until ctx is done: steps wifi ch1, wifi ch6, wifi ch11, ble; then if `Now - lastSweep >= SweepEvery` run Sweep (recording a sweep dwell row and calling UpdateCounts after), else sleep Pause. Every step records a `dwells` row (ok/failed) via RecordDwell. Attribution window for a wifi step is [step start, step end + 1s]; DrainWifi is called after that grace period.

- [ ] **Step 1: Write failing tests**

`internal/scheduler/scheduler_test.go` with fake Deps and an injected clock (advance `Now` manually via a controllable func; use a context canceled after a set number of steps):
- One full cycle calls WifiDwell three times (channels 1/6/11 in order), BleDwell once, and records four dwell rows.
- With SweepEvery large, step 5 sleeps Pause and Sweep is not called; with SweepEvery 0, Sweep is called each cycle and UpdateCounts follows it.
- A WifiDwell error records a failed dwell and the cycle continues to the next step.
- Cancellation between steps stops within one step (Run returns).
Use channels or counters in the fakes; do not sleep real time (inject a `sleep func(context.Context, time.Duration)` into Deps if needed rather than `time.After`, so tests are fast; add that field).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/scheduler/`
Expected: FAIL, `undefined: Run`.

- [ ] **Step 3: Implement scheduler.go**

Implement `Run`. Keep `lastSweep` across cycles. Record each dwell with started/ended/status. Add a `Sleep func(context.Context, time.Duration) error` to Deps (default in main uses a `select` on ctx and `time.After`) so tests inject a fake. On ctx.Done at any point, return.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/scheduler/ && go vet ./...`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler
git commit -m "Add scheduler: cycle of wifi, ble, and periodic sweep dwells

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 9: Web reports

**Files:**
- Modify: `internal/web/web.go`, `internal/web/web_test.go`

**Interfaces:**
- New dependency injected into the server without breaking `web.New`: add `func (s *Server) WithReports(r Reports) *Server` returning s, where
  - `type Reports interface {`
    - `WifiAPsSince(t time.Time, limit int) ([]store.WifiAP, error)`
    - `WifiClientsSince(t time.Time, limit int) ([]store.WifiClient, error)`
    - `TopProbedSSIDs(t time.Time, limit int) ([]store.ProbedSSID, error)`
    - `BleDevicesSince(t time.Time, limit int) ([]store.BleDeviceRow, error)` }
  - and a counts grapher: extend the existing `Grapher` usage by adding `type CountsGrapher interface { GraphCounts(name, title string, dsNames []string, period string) ([]byte, error) }`, injected via `WithCounts(CountsGrapher)`.
- New routes on `Handler()`: `GET /wifi`, `GET /wifi/graphs`, `GET /bluetooth`, `GET /bluetooth/graphs`, `GET /graph/wifi-{period}.png`, `GET /graph/ble-{period}.png`. The existing `/graph/{file}` handler must not swallow `wifi-*`/`ble-*`; register the counts graph routes as distinct patterns (`GET /graph/wifi-{period}.png`) which take precedence over `{file}` in Go 1.22+ ServeMux, or branch inside the handler. Add a nav header linking `/`, `/wifi`, `/bluetooth` to all pages.

To avoid import issues, `store` types are referenced by `internal/web`; that is allowed (web already imports rrd and bands). If a cycle appears, define the report structs in `internal/store` (they already are) and import store.

- [ ] **Step 1: Write failing tests**

Extend `web_test.go` with stub Reports and stub CountsGrapher:
- `/wifi` returns 200 and contains a stubbed AP's MAC and SSID, a client's probed SSID, and `<img src="/graph/wifi-day.png"`.
- `/bluetooth` returns 200 and contains a stubbed device address and name.
- `/graph/wifi-week.png` calls CountsGrapher with name `wifi`, period `week`, returns the PNG and image/png, and caches for 60s (reuse the existing cache test pattern with the injected clock).
- `/graph/ble-hour.png` (bad period) 404s and does not call the grapher.
- The existing band routes still pass, and a server built without WithReports returns 503 or a friendly empty page for `/wifi` (choose 200 with an "no data yet" message; test for the status you pick).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/web/`
Expected: FAIL (undefined WithReports / new routes).

- [ ] **Step 3: Implement**

Add the interfaces, `WithReports`/`WithCounts`, HTML templates for the two report pages (tables per the spec, 24h window via `s.now().Add(-24h)`, limit 500 for device tables, 50 for probed SSIDs), the graphs pages (day/week/month/year images), and the counts graph handler with the same cache map (keyed by the file name). Keep all HTML minimal and consistent with the existing pages. Escaped output only (html/template).

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/web/ && go vet ./...`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "Add web reports for wifi and bluetooth with counts graphs

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 10: OFDM decoder script and install script

**Files:**
- Create: `decoders/wifi_ofdm_rx.py`
- Create: `scripts/install-decoders.sh`

**Interfaces:** none (Go). These are the artifacts main.go and the live check use.

- [ ] **Step 1: Write the decoder script**

`decoders/wifi_ofdm_rx.py`: the streaming version proven in the spike. It reads signed 8-bit interleaved IQ from fd 0 (`blocks.file_descriptor_source(gr.sizeof_char, 0, False)` then `interleaved_char_to_complex(False, 128.0)`), runs the gr-ieee802-11 OFDM receive chain (`sync_short(0.56,2,False,False)`, delay 320, `sync_long(320,False,False)`, stream_to_vector 64, `fft_vcc(64, True, window.rectangular(64), True, 1)`, `frame_equalizer(Equalizer(0), 2437e6, 20e6, False, False)`, `decode_mac(False,False)`), pipes decoded frames through `foo.wireshark_connector(127, False)` to `blocks.file_descriptor_sink(gr.sizeof_char, 1)`, and calls `tb.run()` so it runs until fd 0 closes. No GUI, no CLI args. Header comment marks it as run by rfmon, not by hand.

- [ ] **Step 2: Write the install script**

`scripts/install-decoders.sh` (bash, `set -euo pipefail`): implements the spec's Installation section. Prefix defaults to `${RFMON_DECODERS:-$HOME/.local/share/rfmon/decoders}`. `brew install` the deps (idempotent). Clone the three repos at the pinned commits into a temp dir (`git clone` then `git checkout <sha>`), build gr-foo then gr-ieee802-11 with `-DCMAKE_INSTALL_PREFIX=$PREFIX -DCMAKE_PREFIX_PATH="$PREFIX;/opt/homebrew" -DENABLE_DOXYGEN=OFF`, build ice9 with `-DUSE_VKFFT=OFF -DCMAKE_INSTALL_PREFIX=$PREFIX -DCMAKE_PREFIX_PATH=/opt/homebrew`. Verify by importing `ieee802_11, foo` with the GNU Radio Python and running `<PREFIX>/bin/ice9-bluetooth -h`. Print the prefix and the exact `-decoders`/`-gr-python` flags to pass rfmon. No sudo.

- [ ] **Step 3: Lint**

Run: `bash -n scripts/install-decoders.sh` and `python3 -c "import ast; ast.parse(open('decoders/wifi_ofdm_rx.py').read())"`.
Expected: no output.

Do not execute the install script or the decoder in this task (Task 12 exercises them live).

- [ ] **Step 4: Commit**

```bash
git add decoders scripts/install-decoders.sh
git commit -m "Add OFDM decoder script and decoder install script

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 11: Wire main.go

**Files:**
- Modify: `cmd/rfmon/main.go`
- Create: `cmd/rfmon/main_wiring_test.go` (optional; only if a unit is testable without hardware)

**Interfaces:** consumes all packages.

- [ ] **Step 1: Implement**

Rework `main`:
- Flags per the spec: keep `-data`, `-listen`, `-passes`; change `-pause` default to `5s`; add `-dwell 3s`, `-sweep-every 60s`, `-decoders <default>`, `-gr-python <default>`, `-spectrum-only`.
- Startup checks: always `hackrf_sweep`, `rrdtool`; unless `-spectrum-only`, also `hackrf_transfer`, `<decoders>/bin/ice9-bluetooth`, and a successful `import ieee802_11, foo` via `-gr-python` (run it with the right env, fatal with an install hint pointing at `scripts/install-decoders.sh`).
- Open the store at `<data>/rfmon.db`.
- Build the rrd Store (bands + counts), the web server with `WithReports(db)` and `WithCounts(store)`.
- If `-spectrum-only`: keep the existing spectrum-only loop (poll then pause) so nothing regresses for users without decoders.
- Else: start the OFDM supervisor (stderr to `<data>/logs/wifi-ofdm.log`), build `scheduler.Deps` (WifiDwell runs a TransferRunner dwell into the supervisor's Stdin and calls Ensure first; DrainWifi drains the aggregator for the window; BleDwell runs the ice9 Runner to `<data>/tmp/ble-dwell.pcap`; Sweep calls the existing `poll`; RecordDwell writes to the store; UpdateCounts queries `CountsSince(now-60s)` and updates both counts RRDs), and call `scheduler.Run`.
- Graceful shutdown: on signal, stop the scheduler (ctx), close the supervisor, close the store, shut down the web server, log `rfmon: stopped`.
- Create `<data>/logs` and `<data>/tmp`.

- [ ] **Step 2: Build and vet**

Run: `go vet ./... && go build -o rfmon ./cmd/rfmon && go test ./...`
Expected: no vet output; binary built; all packages `ok`.

- [ ] **Step 3: Commit**

```bash
git add cmd/rfmon
git commit -m "Wire rfmon: scheduler, store, wifi and ble dwells, reports

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

---

### Task 12: Install decoders, live check, and docs

**Files:**
- Modify: `README.md`

This task uses the real HackRF and installs the decoders. Confirm `pgrep -fl hackrf_` and `pgrep -fl rfmon` print nothing before starting.

- [ ] **Step 1: Install the decoders**

Run: `scripts/install-decoders.sh` (about 5 to 10 minutes). Confirm it prints the prefix and that the import check and `ice9-bluetooth -h` passed. If the build fails, stop and report BLOCKED with the failing output; do not hand-patch source.

- [ ] **Step 2: Live run**

Start rfmon in the background (nohup + PID file, as in the rfmon plan's ruling):
```bash
mkdir -p data
nohup ./rfmon -data ./data -dwell 3s -pause 5s > data/wifible-live.log 2>&1 &
echo $! > data/rfmon.pid
```
Wait with a bounded loop (up to ~6 minutes) until the log shows at least one wifi dwell, one ble dwell, and one sweep, using `perl -e 'select(undef,undef,undef,10)'` between checks. Then query the database:
```bash
sqlite3 data/rfmon.db "SELECT dwell_kind, status, count(*) FROM dwells GROUP BY dwell_kind, status;"
sqlite3 data/rfmon.db "SELECT count(*) FROM wifi_devices; SELECT count(*) FROM ble_devices;"
```
(If `sqlite3` is absent, `brew install sqlite` or query via a tiny Go snippet.) Expected: dwell rows of all three kinds with `ok` status; at least one ble_device; wifi_devices may be zero if no OFDM AP is nearby, which is acceptable, but at least one wifi dwell must be `ok`. Then check the web reports:
```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/wifi
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/bluetooth
curl -s http://127.0.0.1:8080/graph/ble-day.png | head -c4 | xxd | head -1
```
Expected: `200`, `200`, PNG magic `89504e47`. Record the measured cycle length from the log.

- [ ] **Step 3: Stop and verify clean shutdown**

`kill -INT $(cat data/rfmon.pid)`, bounded wait until the process exits, confirm the log ends with `rfmon: stopped` and `pgrep -fl rfmon` prints nothing. Remove `data/wifible-live.log` and `data/rfmon.pid`. Leave `data/rfmon.db` and the rest of `data/` in place (gitignored).

- [ ] **Step 4: Update the README**

Add a "WiFi and Bluetooth" section: what it captures, the cycle, the `scripts/install-decoders.sh` step and the `-decoders`/`-gr-python` flags, the new flags and `-spectrum-only`, the `/wifi` and `/bluetooth` pages, the SQLite database location and schema summary, and a privacy note (passive receive only; data stays local in `data/`; do not publish captured device identifiers; the 802.11b blind spot; measured cycle length). No em dashes, no emojis. Do not put any real captured SSID, MAC, or BLE address in the README.

- [ ] **Step 5: Verify and commit**

Run: `go vet ./... && go test ./...`
Expected: all `ok`.
```bash
git add README.md
git commit -m "Document WiFi and Bluetooth capture and verify live

Live check: dwell rows of all three kinds, ble devices recorded, /wifi and
/bluetooth return 200, counts graphs render. Measured cycle length recorded.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01AuxR3fPRUktrajRMfoZfqu"
```

## Self-review notes

- Spec coverage: cycle (Task 8), WiFi dwell + OFDM (Tasks 5, 10, 11), BLE dwell (Task 6), parsing (Tasks 3, 4), storage (Task 1), counts RRD (Task 7), web reports (Task 9), install (Task 10), privacy + fixtures (Tasks 3/4/6 synthetic data, Task 12 README), spectrum-only fallback (Task 11), live check (Task 12). Covered.
- The one spec point deferred to code judgment: BLE ADV_EXT_IND AdvA extraction is dropped in Task 4 (returns ok=false) rather than half-implemented; the spec allowed recording it "only when its extended header carries AdvA", which needs extended-header parsing not worth the risk now. Recorded as a known gap for sub-project C.
- 802.11b DSSS is explicitly out of scope (sub-project B); the OFDM chain is the only WiFi decoder here.
